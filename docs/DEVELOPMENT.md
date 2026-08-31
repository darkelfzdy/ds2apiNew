# DS2API 开发者速查

语言 / Language: 中文

本文面向维护者和贡献者，用于快速判断“从哪里看、改哪里、跑什么”。架构细节仍以 [ARCHITECTURE.md](./ARCHITECTURE.md) 为准，接口行为以 [API.md](../API.md) 为准。

## 1. 本地入口

常用启动与检查：

```bash
# 后端
go run ./cmd/ds2api

# WebUI 开发服务器
npm run dev --prefix webui

# WebUI 生产构建
npm run build --prefix webui
```

PR 前固定门禁：

```bash
./scripts/lint.sh
./tests/scripts/check-refactor-line-gate.sh
./tests/scripts/run-unit-all.sh
npm run build --prefix webui
```

修改 Go 文件后先运行：

```bash
gofmt -w <changed-go-files>
```

## 2. 代码定位

优先从这些入口顺着调用链看：

| 目标 | 入口 |
| --- | --- |
| 总路由、CORS、健康检查 | `internal/server/router.go` |
| OpenAI Chat / Responses | `internal/httpapi/openai/chat`、`internal/httpapi/openai/responses` |
| Claude / Gemini 兼容入口 | `internal/httpapi/claude`、`internal/httpapi/gemini` |
| API 请求归一到网页纯文本上下文 | `internal/promptcompat`、`docs/prompt-compatibility.md` |
| 工具调用解析与流式防泄漏 | `internal/toolcall`、`internal/toolstream`、`docs/toolcall-semantics.md` |
| DeepSeek 上游调用、登录、PoW、代理 | `internal/deepseek/client`、`internal/deepseek/transport` |
| 账号池、并发槽位、等待队列 | `internal/account` |
| Admin API | `internal/httpapi/admin` |
| WebUI 页面 | `webui/src/layout/DashboardShell.jsx`、`webui/src/features/*` |
| 服务器端对话记录 | `internal/chathistory`、`internal/httpapi/admin/history` |

## 3. 常见改动建议

- 改接口行为时，同时检查 `API.md` / `API.en.md` 是否需要同步。
- 改 prompt 兼容链路时，必须同步 `docs/prompt-compatibility.md`。
- 改 tool call 语义时，同时检查 Go、Node sieve 和 `docs/toolcall-semantics.md`。
- 改 WebUI 配置项时，同时检查 `webui/src/features/settings`、语言包和 `config.example.json`。
- 拆分大文件时，保持对外函数签名稳定，并跑 `./tests/scripts/check-refactor-line-gate.sh`。

## 4. 故障定位

接口请求先看路由入口，再看协议适配层，最后看共享 runtime：

1. 路由是否命中：`internal/server/router.go` 和对应 `RegisterRoutes`。
2. 鉴权与账号选择：`internal/auth`、`internal/account`。
3. 请求归一化：`internal/promptcompat` 或协议转换包。
4. 上游请求：`internal/deepseek/client`。
5. 流式输出：`internal/stream`、`internal/sse`、`internal/toolstream`。
6. 响应格式：主路径看 `internal/assistantturn` 与 `internal/format/*`；`internal/translatorcliproxy` 只用于 Vercel/fallback/test 桥接。

对话记录页面问题优先检查：

- Admin API：`/admin/chat-history`、`/admin/chat-history/{id}`。
- 后端存储：`internal/chathistory/store.go`。
- 输出归档：`internal/responsehistory` 在协议回译/裁剪前记录 DeepSeek 上游 assistant text / thinking；即使工具调用已被对外响应转成结构化 `tool_calls` 并从可见正文剔除，后台历史仍应保留原始 EPSE / XML 片段，方便排查格式漂移。
- 前端轮询和 ETag：`webui/src/features/chatHistory/ChatHistoryContainer.jsx`。

Tool call 问题优先跑：

```bash
go test -v ./internal/toolcall ./internal/toolstream -count=1
./tests/scripts/run-unit-node.sh
```

## 5. 测试选择

小范围 Go 改动：

```bash
go test ./internal/<package> -count=1
```

前端改动：

```bash
npm run build --prefix webui
```

高风险协议或流式改动：

```bash
./tests/scripts/run-unit-all.sh
```

发布或真实账号链路验证：

```bash
./tests/scripts/run-live.sh
```

端到端测试产物默认写入 `artifacts/testsuite/`。分享日志前需要清理 token、密码、cookie 和原始请求响应内容。

## 6. DeepSeek 网页版指纹（Chrome 版本）怎么升

伪装参数只有一个权威来源：`internal/deepseek/protocol/constants_shared.json` 的 `chrome` 块。
Go 侧 `go:embed`、Node/Vercel 侧 `require` 读同一文件，**不要在 `chrome.go` 或
`deepseek-constants.js` 里另写版本号**（`tests/node/js_compat_test.js` 有守卫用例，会直接测挂）。

优先级：`DS2API_CHROME_MAJOR_VERSION` > JSON 的 `major_version` > 内置回退。
该变量可以写在 `.env`（v4.8.0 修过“包初始化早于 `LoadDotEnv` 导致 `.env` 不生效”的问题）；
非法值会被忽略并打 `[chrome] invalid DS2API_CHROME_MAJOR_VERSION ignored` 告警，
不会拼出 `Chrome/abc.0.0.0` 这种坏指纹。

**升版只需改一个数字**：把 `chrome.major_version` 改成目标大版本即可。`sec-ch-ua` 的
GREASE 品牌串是 Chrome 大版本的**确定性函数**，由代码按 Chromium 源码公式算出
（`protocol.ComputeChromeGreaseBrand` / JS `computeChromeGreaseBrand`）：

```
chars   = {" ", "(", ":", "-", ".", "/", ")", ";", "=", "?", "_"}   // 11 个
vers    = {"8", "99", "24"}                                            // 3 个
brand   = "Not" + chars[major % 11] + "A" + chars[(major + 1) % 11] + "Brand"
version = vers[major % 3]
```

JSON 里的 `grease_brands` 是**历史钉值兼逃生口**：命中则优先用它（万一 Chromium 轮换算法，
改 JSON 即可），未命中则自动计算。单测会交叉验证“算法输出 == 钉值”，两边不一致就报错。

TLS/H2 层自动跟随：`transport.ResolveChromePreset()` 取 **≤ 请求版本的最新可用预设**
（用 `fingerprint.GetStrict` 探测，不用会静默回退到旧预设的 `Get`）。所以目标版本尚无
httpcloak 预设时，要么先升级 httpcloak，要么直接用新大版本（TLS 会落在最近可用预设，
不会像旧实现那样“改了 UA 没改 TLS”）。

改完自检：

```bash
go run ./tests/wire-capture -ours     # 核对 UA / sec-ch-ua / TLS 预设三者版本一致
```

启动日志会打 `[chrome] web-client fingerprint major=<N> tls_preset=<name>`，部署后用它确认指纹真的切过去了。

## 7. 上游风控拦截与 token 刷新语义

- 拦截识别集中在 `protocol.ClassifyUpstreamBlock()`（Go）与
  `deepseek-constants.js` 的 `classifyUpstreamBlock()`（Node），两边共用同一张判定表；
  日志标签与含义见 `docs/MIHOMO_BRIDGE.md` 的「上游风控拦截分类」。
- 命中拦截归 `FailureUpstreamBlocked`，**不触发 token 刷新**，对调用方返回 502。
- `auth.RefreshToken()` **不再“先删再试”**：登录用的是账号密码，不依赖旧 token，
  提前清空对登录本身无帮助；而登录因出口被拦/网络抖动失败时，我们并没有从上游得
  到任何关于旧 token 的结论，因此保留它（避免无谓重登——登录是风控最敏感的一步）。
  只有登录确实被上游拒绝时才清 token（`MarkTokenInvalid`）。
- 注意 token **不写盘**（`writeConfigJSONLocked` 会 `ClearAccountTokens`），只存进程内存；
  token 为空时 `ensureManagedToken` 会在下一次请求自动重新登录，所以保留失败不会把账号打死。
