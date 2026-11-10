# Agent Web 0.1.0 验证记录

验证日期：2026-11-10。全部网页任务使用合成本地站点、临时账号目录，没有使用用户真实网站账号。

## 交付物

- `dist/agent-web-0.1.0-linux-amd64.tar.gz`：Linux x86_64，内置 Chrome Headless Shell。
- `dist/agent-web-0.1.0-darwin-amd64.tar.gz`：macOS Intel，内置 Chrome Headless Shell。
- `dist/agent-web-0.1.0-darwin-arm64.tar.gz`：macOS Apple Silicon，已完成交叉构建和归档校验，未在本地执行。
- 每个包包含 `agent-web/bin/agent-web`、`SKILL.md`、构建清单和第三方许可。安装后的使用不依赖 Go、Python、Node.js 或另外安装 Chrome。Linux 仍需要浏览器的系统动态库。
- 三个平台使用 `release/browsers.json` 固定的 Chrome Headless Shell 157.0.8103.50，下载归档分别核对 SHA-256。

## 已验证

`go test ./...` 和 `go vet ./...` 通过。Skill Creator 的 `quick_validate.py` 通过。发行包中的二进制 SHA-256 与 `BUILD.json` 一致；归档内文档与源文档一致。

`tests/e2e.py` 在 macOS Intel 以及 Linux x86_64 容器中，分别对独立发行二进制通过以下检查。两次运行均未设置外部浏览器路径，使用二进制内置的浏览器。

1. 四个并发首调用只启动一个后台服务。
2. 页面语义引用、中文填表、点击和跨命令保持页面状态。
3. 同源与跨源 iframe 的读取和操作。
4. 导航后的旧引用拒绝；批量操作部分成功后准确报告已完成数量。
5. 弹窗归属、页面关闭和有效 PNG 截图。
6. 人工接管期间阻止 Agent 操作，校验链接凭证，用户交回后撤销链接。
7. 在第二个真实浏览器中打开接管网页，验证画面加载、缩放后鼠标坐标、文字输入和交回 Agent。
8. 不同账号隔离；同账号新任务复用主 PID；服务重启后恢复 HttpOnly 会话 Cookie、LocalStorage 和 IndexedDB。
9. 安装到新 Skill 目录，拒绝覆盖已有目录；安装后的二进制在新的数据目录中独立启动浏览器并完成页面观察。

单元测试另覆盖接管凭证不能跨任务使用、过期链接拒绝、任务超时撤销控制权、严格请求解析、账号路径约束、部分失败结果及安装目录保护。

## 发布流程验证

- `.github/workflows/release.yml` 通过 actionlint 1.7.12 检查。
- `tests/test_release.py` 的 8 项测试通过，覆盖三个系统分支、Codex / Claude / 自定义目录、`CODEX_HOME`、校验失败阻止安装、拒绝覆盖、版本不一致及平台包缺失。
- 对三个真实归档生成 `dist/install.sh`、`dist/SHA256SUMS` 和 `dist/RELEASE_NOTES.md`；逐包检查版本、浏览器锁定信息与内部二进制摘要。
- 生成的安装脚本通过 Shell 语法检查，并用本地下载替身安装真实 macOS Intel 发行包，确认安装后的二进制版本和嵌入技能。该检查没有使用尚未发布的 GitHub 下载链接。
- 更新后的固定浏览器版本再次通过 Linux 容器和 macOS Intel 的完整 `tests/e2e.py`。
- Apple Silicon 原生运行由流水线的 `macos-15` runner 验证。当前工作流尚未推送到 GitHub，也没有创建远端 Release；匿名 HTTP 下载需首次实际发布后验证。

## 验证边界

- Linux 容器使用已有 Ubuntu 24.04 浏览器测试镜像，禁用外部网络，只读挂载项目。该测试显式设置 `AGENT_WEB_NO_SANDBOX=1`；普通发行程序默认保留沙箱。本次没有验证所有 Linux 发行版或容器沙箱配置。
- 仓库 Dockerfile 的构建在拉取 `ubuntu:24.04` 元数据时超时，尚未完成该镜像的独立构建验收。Linux 二进制本身已在容器中通过真实浏览器测试。
- 用户登录闭环通过合成账号登录验证；没有宣称所有第三方 SSO、验证码或设备认证都已兼容。
- 未做与 ego 的同条件性能对比。共享浏览器主进程已验证，但具体 CPU、内存和任务速度收益需要业务负载测量。
- 尚未完成平台签名或公证；当前为本地构建的开发发行包。
