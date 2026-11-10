# Agent Web

可直接安装到 Agent 的无 UI 浏览器 Skill。发行包包含 `SKILL.md` 和 Go 原生可执行文件，执行文件内置 Chrome Headless Shell。Agent 通过命令行发送 JSON，浏览器后台保持任务和登录态，无需 Node.js 或浏览器扩展。

## 安装与使用

在首个 Release 发布后，可直接安装最新版本：

```sh
curl -fsSL https://github.com/wxx213/agent-brower/releases/latest/download/install.sh | sh -s -- --agent codex
# Claude Code：将 --agent codex 改为 --agent claude
# 其他 Agent：将上述参数改为 --dir "$HOME/my-agent/skills/agent-web"
```

安装脚本自动识别 Linux x86_64、macOS Intel / Apple Silicon，并校验下载包的 SHA-256。每个 [Release 页面](https://github.com/wxx213/agent-brower/releases)也提供固定版本的安装命令。仓库需要公开，安装脚本与二进制才能无需登录直接下载。已有安装目录不会被覆盖，升级前先执行旧二进制的 `stop`，再备份旧技能目录。用户登录数据保存在独立的数据目录中。

解压对应操作系统的发行包，将其中 `agent-web` 文件夹放到 Agent 的技能目录；或者运行包内命令安装到一个新目录：

```sh
./agent-web/bin/agent-web install --dir "$HOME/.codex/skills/agent-web"
# Claude Code 可选择 ~/.claude/skills/agent-web；其他 Agent 使用自己的技能目录。
```

重新加载 Agent 的技能列表，即可要求它使用 `agent-web` 浏览网页。安装程序只写指定目录，不改 Agent 配置，不覆盖现有 Skill。完整操作说明在包内 `SKILL.md`，也可运行 `agent-web skill` 查看。

手工验证：

```sh
./agent-web/bin/agent-web call <<'JSON'
{"op":"start","account":"default","url":"https://example.com"}
JSON
# 用返回的 task ID 调用 observe、act、eval、handoff、finish。
```

本地首次调用自动启动后台进程，并将内置 Chromium 解压到私有运行目录。后续调用复用进程。`finish` 关闭任务页面；`stop` 关闭整个服务并保存状态。

## 运行模型

- 一个账号对应一个持久浏览器 Profile。同账号的连续任务复用同一个浏览器，任务内可多次调用、点击和填表。第一版同账号同时只运行一个任务。
- 不同账号使用独立浏览器实例和 Profile；默认最多 4 个实例，空闲 10 分钟回收。此选择优先保留完整站点存储，不依赖不完整的 IndexedDB 导入导出。
- Cookies、LocalStorage、IndexedDB 存在账号目录；会话 Cookie 在动作完成、用户交回、任务结束和正常退出时额外保存。强制杀死进程可能丢失最后一次检查点之后的更新。
- DOM、内存变量和 SessionStorage 属于页面。关闭页面后不保证保留；任务 ID 不跨服务重启恢复。
- 页面观察用 Chromium 无障碍树生成精简语义节点，操作引用会校验文档身份。支持 CSS、iframe、页面脚本、截图、多标签和批量动作。
- 登录时生成临时接管链接，网页显示服务器会话画面并转发用户输入。Agent 暂停操作；用户交回后再验证登录并续跑。平时没有桌面 UI。

实现采用 Go + Rod/CDP，避免为独立发行文件携带 Node.js 运行时。此前调研中推荐的 Playwright 能力在这里由 Rod 和 CDP 实现；无需修改 Chromium 内核。

## Linux 服务器

Linux amd64 发行包包含浏览器及其随附资源，但仍需系统动态库。在 Ubuntu 24.04 上可使用仓库提供的 Dockerfile，或安装浏览器所需系统包。默认保留 Chromium 沙箱；`--no-sandbox` / `AGENT_WEB_NO_SANDBOX=1` 仅用于外层已经隔离的受信测试容器。

```sh
export AGENT_WEB_HOME=/var/lib/agent-web
export AGENT_WEB_TOKEN='替换为随机长密钥'
./agent-web/bin/agent-web serve --listen 0.0.0.0:9228 \
  --public-url https://browser.example.com
```

在前面配置 HTTPS 反向代理，保留 `/rpc`、`/handoff/`、`/human/` 路径。Agent 所在机器设置同一密钥及 `AGENT_WEB_URL=https://browser.example.com`，命令行就会连接远程服务；不会在 Agent 所在机器启动浏览器。用户链接必须可达服务器，不能把远程服务器上的 localhost 链接直接发给用户。

API 密钥可控制该服务全部账号，适合自己的 Agent 使用，不提供多租户隔离。不同用户应使用不同服务和数据目录。接管链接只授权一个任务，15 分钟到期，用户交回或任务结束后撤销。网页登录流不保证支持系统原生对话框、Passkey 或硬件认证器。

## 构建

需要 Go 1.27 或兼容版本、Python 3。依赖在 `go.mod` 和 `go.sum` 中固定。

```sh
go build -o dist/agent-web-dev ./cmd/agent-web
# 开发构建不包含浏览器，设置现有可执行文件即可：
export AGENT_WEB_BROWSER=/absolute/path/to/chrome-headless-shell
```

制作包含浏览器的单可执行文件：

```sh
python3 scripts/build.py --target linux-amd64 \
  --browser-dir /path/to/chrome-headless-shell-linux64
# 或从 Chrome for Testing 下载指定版本并核对 SHA-256：
python3 scripts/build.py --target linux-amd64 \
  --chrome-version VERSION --sha256 EXPECTED_ARCHIVE_SHA256
# 正式发行：使用仓库固定的浏览器版本和 SHA-256，并注入应用版本：
python3 scripts/build.py --target linux-amd64 --version 0.1.0 --locked-browser
```

支持构建目标 `linux-amd64`、`darwin-amd64`、`darwin-arm64`。目标浏览器架构必须匹配。不同平台构建应顺序执行，因为构建脚本临时嵌入平台浏览器文件。输出在 `dist/`，包含归档、构建清单与第三方许可。二进制体积包含 Chromium，不应将“单文件安装”误解为浏览器内核变小。

## GitHub Release 发布

流水线位于 `.github/workflows/release.yml`，推送 `vX.Y.Z` 标签后触发。它在三个原生平台分别执行 Go 测试、静态检查、安装器测试、打包与真实浏览器验收，全部成功后生成安装脚本和 `SHA256SUMS`，上传附件并将 Release 草稿发布。发布说明自动包含 Codex、Claude Code 和自定义目录的一键安装命令。

代码提交并推送到 GitHub 后，发布示例：

```sh
git tag v0.1.0
git push origin v0.1.0
```

也可在 Actions → Release → Run workflow 中填写已存在的版本标签。手动入口要求工作流已进入默认分支；标签指向的提交也必须包含发布脚本。版本以标签为准，自动写入二进制、安装包名称和构建清单，仅支持正式版本 `vX.Y.Z`。

无需额外 Secret：发布任务使用 GitHub 自动提供的 `GITHUB_TOKEN`，仅该任务拥有 `contents: write` 权限。仓库设置需允许 GitHub Actions 运行；组织策略若禁止写权限，需要管理员调整策略。发布前会检查仓库已经公开，避免生成需要登录才能使用的下载链接。浏览器下载版本与摘要固定在 `release/browsers.json`；升级时需重新核对官方归档摘要并通过各平台测试。

已有同名 Release 不会被覆盖。上传失败会留下草稿，不对用户发布不完整版本；确认失败原因并删除该未发布草稿后，可重跑失败任务。已正式发布的版本应使用新标签修复。

附件包括三个 `.tar.gz`、`install.sh` 和 `SHA256SUMS`。Linux 仍需系统动态库，macOS 产物未签名、公证。首次推送前的本地验证与 GitHub 上实际运行成功是两回事，远端执行结果以 Actions 为准。

## 验证

```sh
go test ./...
python3 tests/e2e.py --binary dist/agent-web-dev --browser /path/to/chrome-headless-shell
# 独立包不传 --browser，验证内置引擎：
python3 tests/e2e.py --binary dist/linux-amd64/agent-web/bin/agent-web
```

真实浏览器验收全部使用合成本地网页和临时 Profile，覆盖跨轮操作、同进程复用、语义引用、iframe、弹窗、用户接管、账号隔离以及重启后的 Cookie / LocalStorage / IndexedDB 恢复。性能相对 ego 的优势需要另外用同一业务任务集测量，不能从功能测试推断。
