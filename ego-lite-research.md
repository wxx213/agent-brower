# ego lite 浏览器实现与同类产品对比

2026 年 11 月 10 日 · 本机验证环境为 Intel macOS 15.7.9

ego lite 把三件事组合进同一个桌面浏览器：复用已登录的网站、给 Agent 分配独立任务空间、让用户在任务进行中查看或接管。Agent 的规划由 Codex、Claude Code 等外部工具负责，ego lite 提供可执行的浏览器接口和任务生命周期。**最接近的竞品是 BrowserOS neo**，两者都支持导入登录态、外部 Agent 接入和并行工作。公开资料显示，主要差异在于 ego lite 的任务 Space 和 JavaScript 接口，以及 BrowserOS neo 的标签页分组、MCP 接入和会话回放。 [ego lite Space 文档](https://lite.ego.app/document/en/docs/space) · [BrowserOS neo 产品与 FAQ](https://browseros.com/) · [BrowserOS neo MCP 文档](https://github.com/browseros-ai/BrowserOS/blob/main/docs/neo/mcp/index.mdx)

## 1. 实现范围与调用链

本机 `ego-browser --version` 输出应用版本 `0.6.2.5`、Chromium `156.0.8078.74`、Node.js `24.22.1`，随附 Skill 指南版本为 `2.2.0`。公开源码固定在 `bacaece43900e8ca68eea3b6f1663835ea38df9e`。MIT 仓库公开了 Skill、TypeScript SDK 和测试，Chromium 原生修改随浏览器安装包分发。下文把 SDK 源码可确认的机制、厂商描述的原生行为和本机观察分开。 [固定提交的仓库 README](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/README.md#license) · [SDK README](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/README.md)

```mermaid
flowchart LR
    A[外部 Agent 读取 Skill] --> B[ego-browser CLI 提交 JavaScript]
    B --> C[Node Helper 中的 TaskSpace 和 Page SDK]
    C --> D[原生桥接 globalThis.ego]
    D -->|任务控制与原生快照| E[ego lite 浏览器]
    D -->|CDP 命令与事件| E
    E --> F[Agent Space 与任务页面]
    E --> G[用户日常浏览界面]
    G -->|查看或接管| F
```

1. **接入与执行。** Skill 说明 API 和执行规则，Agent 通过 `ego-browser nodejs -e` 或 stdin 提交脚本。本机 `~/.local/bin/ego-browser` 是指向当前应用版本中原生 CLI 的符号链接，脚本在 `ego Helper (Node)` 中运行。SDK 使用 esbuild 打包成单个 ESM 文件，执行入口把 `taskSpace()` 等 helper 注入 `AsyncFunction`，支持顶层 `await` 和一轮执行多步操作。本机连续两次调用观察到相同的 Node PID，SDK 入口为每次提交的代码建立新的函数执行范围。 [SDK 构建脚本](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/scripts/build.mjs) · [脚本执行入口](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/run.ts#L68)
2. **原生桥接。** SDK 将 CDP 命令封装为带请求 ID 和可选 `sessionId` 的消息，经 `globalThis.ego.sendCDPMessage()` 发送，使用原生回调分发响应与事件。它用 `Target.attachToTarget` 建立页面会话，并跟踪跨进程 iframe 的子 target。任务空间选择、控制权和快照也有专用原生接口。公开代码能追到这个边界，底层 IPC 和 C++ 调度细节仍待原生源码或更细的观测确认。 [CDP 桥接](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/browser-runtime.ts#L506) · [TaskSpace 原生适配](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/page-model.ts#L899)
3. **跨轮状态。** `spaceId` 标识任务，`p1` 等 Page 标签标识该任务内的受管标签页。SDK 用本地账本保存标签与 target 的映射、页面归属和 ref 状态。恢复时会与浏览器标签列表对账。`NativeOperationGate` 在单个 SDK 实例内排队选择 Space、绑定 target 和 session，避免当前 Space 在原生调用进行中被该实例的另一操作切换。这个队列的作用域与浏览器可同时运行多个 Space 是两个层次。 [Page 账本](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/page-ledger.ts) · [NativeOperationGate](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/native-gate.ts#L39)

## 2. Space、登录态与控制权

**登录态迁移与任务运行是两个阶段。** 首次设置时，用户可以把 Chrome 等浏览器里的会话、书签、密码和扩展导入 ego lite，macOS 迁移可能需要钥匙串授权。运行时，多个任务在同一个浏览器中管理各自页面，并可跨命令恢复 Space。复核当前官方 Space 文档后，应撤回“每个 Space 必然拥有独立 Cookie、存储环境”的表述：官方明确区分页面分离与账号分离，并展示了两个 Space 共享一个本地 HttpOnly 会话 Cookie 的受控案例。该案例不代表所有 Profile 或 SSO 的行为，也不足以推断底层 BrowserContext 的具体映射。可继承认证意味着 Agent 可以使用该账户的网站权限，任务标签分离本身无法限制这些权限。 [快速入门与迁移](https://lite.ego.app/document/en/docs/quick-start) · [Space 文档](https://lite.ego.app/document/en/docs/space) · [迁移安全说明](https://lite.ego.app/blog/safety-and-privacy-in-ego-lite-browser-migration)

**控制权与任务生命周期绑定。** 用户可以在浏览器界面切入任务。SDK 的 `handOff()` 记录交接状态并调用原生交接接口。用户完成登录或验证并交回控制后，Agent 可通过 `takeOverTaskSpace(id)` 恢复同一个 Space。用户接管、任务不活跃或取消分配时，SDK 有对应的停止错误。`finish({ keep: [] })` 可以关闭 Agent 创建的页面，带有用户或未知来源页面的 Space 会保留这些页面。 [交接与结束源码](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/page-model.ts#L1216) · [恢复入口](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/helpers.ts#L492) · [停止错误](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/ego-errors.ts#L208)

## 3. Agent 怎样读页面和操作页面

| 环节 | 实现与边界 |
| --- | --- |
| 语义观察 | `page.snapshot()` 调用浏览器的 `ego.snapshot()`。官方说明快照基于无障碍树，输出角色、可见文本和可操作元素。默认看当前视口，也支持 `full_page` 和 iframe 子树。SDK 补充 frame 信息、整理 locator 并压缩结果。 [快照文档](https://lite.ego.app/document/en/docs/snapshot) · [快照处理源码](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/snapshot-result.ts#L290) |
| 元素定位与失效 | `@N` 关联 `backendNodeId`、frame 和文档身份。操作前会核对 frame 的 `loaderId` 等信息，拒绝已经失效的引用。CSS、文本、角色和 XPath 也是定位入口，解析器可搜索开放的 Shadow DOM，并为 iframe 匹配合适的会话。 [文档身份与引用校验](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/page-model.ts#L3501) · [元素解析器](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/element-resolver.ts) |
| 执行动作与校验结果 | 点击检查目标与命中位置，必要时先发送滚轮输入，滚不动时可回退到 `DOM.scrollIntoViewIfNeeded`，最后通过 `Input.dispatchMouseEvent` 派发。操作回执可带弹窗或对话框信息，业务成功由后续页面状态验证。截图走 `Page.captureScreenshot`，供画布、图片和复杂控件等场景使用。 [动作实现](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/driver/page-actions.ts) · [截图实现](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/driver/observe.ts#L203) |
| 底层能力 | `page.evaluate()` 在页面上下文执行代码，`page.fetch()` 使用页面的 `window.fetch()`，受浏览器 CORS 和会话约束。`page.cdp()` 可用于诊断或补齐 SDK 动作，并会使已有 ref 失效。Node 脚本同时能使用本地 Node API，因此本地执行环境与浏览器 Space 的权限边界需要分别评估。 [Page API 源码](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/page-model.ts#L2386) · [页面 fetch 实现](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/page-model.ts#L4143) |

### 本机验证

使用一个专用 Space，先打开 `example.com`，再在同一个 `p1` 中加载含表单和 `srcdoc` iframe 的测试页。页面交互仅写入测试页内存。

| 验证项 | 观察结果 |
| --- | --- |
| 跨轮恢复与操作 | 后续命令用同一 `spaceId` 和 `p1` 读回页面标题，用上一轮快照的 ref 填表、点击，再从页面读到 `local-check`。 |
| iframe、CDP 与截图 | 全页快照包含 iframe 内按钮，刷新快照后可读取 iframe 子树。`Page.getFrameTree` 返回一个子 frame，`page.screenshot()` 生成有效的 1862 × 859 PNG。 |
| 旧引用保护 | 导航到新文档后尝试点击旧按钮，SDK 返回 `Stale ref`，新快照为替换页按钮分配了新的 ref。 |
| 生命周期与启动条件 | `finish({ keep: [] })` 返回 `closedSpace: true`。结束后，在同一沙箱内再次运行无页面操作的 Node 命令，CLI 尝试唤起应用并返回 `Failed to start ego lite`。本机成功验证的是连接已运行应用的路径。 |

测试页只覆盖同页 `srcdoc` iframe。跨进程 iframe 能力有公开 E2E 用例支撑，本次没有复跑。真实登录态迁移、多 Space 存储隔离、用户接管后的续跑和竞品行为均按公开资料分析。 [跨进程 iframe 用例](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/scripts/real-browser-e2e/cases/page-oopif.mjs)

## 4. 同类产品对比

按外部 Agent 能否接入、从哪里取得登录态、任务隔离到哪一层，以及用户如何介入比较。竞品功能以调研日期的官方文档和公开源码为准。

| 产品 | 形态与接入 | 登录态与任务隔离 | 人工介入与适用场景 |
| --- | --- | --- | --- |
| **ego lite** | 独立 Chromium 桌面浏览器。Skill + CLI 执行多步 JavaScript，底层有 CDP。 | 导入后使用本机登录环境。Space 内管理任务标签；不同 Space 不保证独立登录态。 | 浏览器原生 Space、可见的 Agent 光标和显式交接。适合外部 Agent 使用登录态完成多步网页任务。 [Space 文档](https://lite.ego.app/document/en/docs/space) · [SDK README](https://github.com/citrolabs/ego-lite/tree/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser) |
| **BrowserOS neo** | 独立 Chromium 浏览器，通过本地 MCP 连接 Codex、Claude Code 等。仓库公开 Chromium 补丁与构建流程。 | 导入 Chrome 登录态，每个 Agent 使用独立标签集合。官网将每 Agent 的 Profile 隔离列为路线图功能。 | Cockpit 里看实时任务、审批和回放。适合需要跨 Agent 接入、可审查的原生实现和执行记录的桌面工作流。 [neo 官网与 FAQ](https://browseros.com/) · [MCP 接入](https://github.com/browseros-ai/BrowserOS/blob/main/docs/neo/mcp/index.mdx) · [浏览器构建](https://github.com/browseros-ai/BrowserOS/blob/main/packages/browseros/README.md) |
| **Claude in Chrome** | Chrome 扩展，可从侧栏、Claude Code 或 Cowork 操作。 | 直接使用当前 Chrome 的登录态。扩展可将新开的任务标签分组，标签组属于现有 Profile。 | 有站点授权、敏感操作确认和组织级名单。适合沿用现有 Chrome 与 Claude 工作流。 [扩展说明](https://support.claude.com/en/articles/12012173-get-started-with-claude-in-chrome) · [权限指南](https://support.claude.com/en/articles/12902446-claude-in-chrome-permissions-guide) · [Claude Code 集成](https://code.claude.com/docs/en/chrome) |
| **Perplexity Comet** | 独立 AI 浏览器，从地址栏或内置 Assistant 发起任务。 | Assistant 在用户的 Comet 浏览环境中读取当前标签，并行操作多个标签。底层任务存储隔离细节未披露。 | 在浏览器内授予代操作权限，可选择单次或自动授权。适合日常浏览、研究和跨站消费任务。 [Comet Assistant 说明](https://www.perplexity.ai/en-GB/hub/blog/the-new-comet-assistant) · [浏览器操作与审批](https://www.perplexity.ai/en-GB/hub/blog/comet-assistant-puts-you-in-control) |
| **Chrome DevTools MCP** | MCP 服务驱动 Chrome，提供页面操作、控制台、网络和性能追踪。 | 默认启动独立实例。Chrome 144+ 可经 `--autoConnect` 连接当前已登录 Chrome，需要启用远程调试并由用户允许。 | 原浏览器可人工观察，支持从 DevTools 调试现场接着查。适合已登录页面的开发调试。 [连接配置](https://developer.chrome.com/docs/devtools/agents/get-started/configuration) · [Chrome 团队的调试示例](https://developer.chrome.com/blog/chrome-devtools-mcp-debug-your-browser-session) |
| **Vercel agent-browser** | Rust CLI，逐条命令使用语义快照和元素 ref，后台进程维持浏览器会话。 | 可启动隔离命名会话、加载持久 Profile，或通过 CDP 连接已有 Chrome。官方域名白名单与现有会话附加、Profile 等模式互斥。 | 可有头运行，并配置动作策略或确认。适合脚本、CI 和可重复的自动化任务。 [开源仓库](https://github.com/vercel-labs/agent-browser) · [会话文档](https://agent-browser.dev/sessions) · [安全控制](https://agent-browser.dev/security) |
| **Browserbase** | 云托管浏览器。通过 Sessions API 创建会话，使用 CDP 或 Stagehand 等客户端驱动。 | 使用云端 Context 跨会话保存 Cookie、LocalStorage 和 IndexedDB。可以先在云会话登录，再由后续会话复用。 | Live View 提供实时观看和人工操作。适合服务端任务、远程接管及托管浏览器基础设施。 [Contexts](https://docs.browserbase.com/platform/browser/core-features/contexts) · [Session Live View](https://docs.browserbase.com/platform/browser/observability/session-live-view) |

ChatGPT Atlas 可作为产品演进案例。OpenAI 公告计划于 2026 年 8 月 9 日停用 Atlas，并将相关能力迁入 ChatGPT 和 Codex。后续更新也把已打开标签引用和浏览器操作带到了桌面端连接的浏览器扩展。 [OpenAI 发行说明](https://help.openai.com/en/articles/6825453-chatgpt-release-notes)

## 5. 关键取舍与性能证据

- **会话连续性与任务独立性。** 扩展和 DevTools 直连保留用户正在使用的 Chrome 环境，迁移成本低。ego lite 和 BrowserOS neo 都需要独立安装和迁移，换来可管理的 Agent 工作区。Browserbase 将登录会话放在云端，服务部署与人工远程介入更容易，登录态的保存位置和访问边界也随之改变。 [ego lite 快速入门](https://lite.ego.app/document/en/docs/quick-start) · [Chrome DevTools 配置](https://developer.chrome.com/docs/devtools/agents/get-started/configuration) · [Browserbase Contexts](https://docs.browserbase.com/platform/browser/core-features/contexts)
- **脚本表达力与权限治理。** ego lite 的一轮 JavaScript 可以把观察、条件、动作和结果校验组合起来，减少反复调用。浏览器 Space 的分离主要解决任务归属和界面干扰。登录会话能访问哪些站点、动作能否自动提交、快照会送到哪个模型，分别取决于浏览器和外部 Agent 的实际约束。Claude in Chrome 的站点与动作审批提供了可比较的治理入口，BrowserOS neo 的回放则侧重执行可见性与事后审查。 [ego lite 隐私政策](https://lite.ego.app/privacy) · [Claude 权限指南](https://support.claude.com/en/articles/12902446-claude-in-chrome-permissions-guide) · [BrowserOS neo Cockpit](https://github.com/browseros-ai/BrowserOS/blob/main/docs/neo/cockpit.mdx)
- **厂商跑分衡量的是整条工作流。** Citro Labs 的 Real-World Bench 在同一 Agent、模型和评审下比较 31 个真实网站任务。公开结果里，ego-browser 的全项通过率为 **93.5%**，agent-browser 为 **62.9%**，playwright-cli 为 **71.0%**。成绩受任务集、各工具版本、Skill、登录状态和判分方式影响。本次未独立复跑。此前引用的六路 `about:blank` 实验在当前 Space 页面中未找到可复核内容，不再作为内存优势的依据；端到端任务成绩也不能证明同一网页在 ego 中占用更少内存。 [厂商结果与口径](https://lite.ego.app/article/agent-harness) · [公开基准框架](https://github.com/citrolabs/ego-browser-benchmark-framework) · [当前 Space 文档](https://lite.ego.app/document/en/docs/space)


## 6. 为什么可能更快，以及是否需要修改内核

公开实现支持的优化方向包括：复用浏览器与任务页面、通过 JavaScript 一次提交多步操作、用语义快照和定向提取减少模型输入、通过原生桥接执行 CDP 和专用快照操作。它们分别可能减少启动、模型往返、观察数据和控制链路的成本，具体贡献没有独立分项实测。原生集成的存在不代表其通信一定快于本机 Playwright，也不能据此判断扩展普遍慢。 [脚本接口](https://lite.ego.app/document/en/docs/ego-browser) · [快照](https://lite.ego.app/document/en/docs/snapshot) · [原生桥接源码](https://github.com/citrolabs/ego-lite/blob/bacaece43900e8ca68eea3b6f1663835ea38df9e/package/ego-browser/src/browser-runtime.ts)

官方 README 将部分快照能力归因于浏览器引擎内定制；公开仓库没有提供足以复现这些原生修改的实现。因此不能保证普通 Chromium 的封装能完全达到相同的快照质量，但也不能把整条任务流程的加速都归因于内核修改。任务完成时间、模型 Token 消耗、浏览器 RAM 和 CPU 是不同指标。 [官方说明与基准](https://github.com/citrolabs/ego-lite#benchmarks)

对当前服务端需求，先在原版 Chromium 上实现会话复用、多步脚本和精简观察，再按相同网站、模型、登录态与成功标准对比 ego。Playwright 已支持同一浏览器内创建轻量 Context；只有确认差距来自标准接口无法满足的快照或引擎能力时，再评估定向原生修改。此前“不需要开发新浏览器”的建议是当前阶段的实施选择，不是对性能必然等同的承诺。 [Playwright Context](https://playwright.dev/docs/browser-contexts)
