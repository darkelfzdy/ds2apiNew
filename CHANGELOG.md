# Changelog

## 4.8.0 (2026-08-31)

### 新增：Chrome 指纹统一升级到 151（TLS 与 HTTP 层同源）+ 上游风控拦截识别

**背景/为什么做**：2026-08-31 对 chat.deepseek.com 官方前端 bundle 与真实浏览器重新取证发现，
上一轮（08-25）同步的模拟参数已经过时：真实 Chrome stable 已到 152（151 于 07-28、152 于 08-25
发布，且自 153 起改为两周一个版本），而我们仍自称 Chrome 150、`sec-ch-ua` 用着 150 时代的
GREASE 品牌串。同时 DeepSeek 风控已接入 AWS WAF + Cloudflare challenge，返回 405/202/403/429
带挑战响应头，而客户端完全不识别，导致“出口 IP 被拦”与“账号被封/token 失效”无法区分，
只能靠人肉看日志判断（即 08-26 那轮封号排查的痛点）。

**更新了什么**：

- **Chrome 指纹升级到 151，两层自洽**：`httpcloak` 升级 v1.6.8 → v1.6.11（新增 `chrome-151-*` 预设），
  TLS ClientHello 与 HTTP 头（`User-Agent` / `sec-ch-ua`）现在由**同一个版本源**驱动。
  默认 `Chrome/151.0.0.0` + `sec-ch-ua: "Not=A?Brand";v="99", "Chromium";v="151", "Google Chrome";v="151"`。
  选 151 而非 152：httpcloak 尚无 152 预设，全栈自洽比“UA 领先但 TLS 跟不上”更真实。
- **指纹参数改为跨语言单一来源**：`constants_shared.json` 新增 `chrome` 块
  （`major_version` + `grease_brands` 表 + `grease_fallback_major`），Go 侧 `go:embed` 与
  Node/Vercel 侧 `require` 读同一文件，消除“Go/JS 各写一份 UA 常量后悄悄错开”这类缺陷；
  `tests/node/js_compat_test.js` 新增守卫用例，只改一侧不改 JSON 会直接测失败。
- **TLS 预设自动“向下取最新可用”**：新增 `transport.ResolveChromePreset()`，用
  `fingerprint.GetStrict`（无回退）探测；设 `DS2API_CHROME_MAJOR_VERSION=152` 时 HTTP 层声称 152、
  TLS 自动用 `chrome-151-windows`，不再制造版本矛盾（旧实现把预设硬编码为
  `"chrome-150-windows"`，改环境变量只改 UA 不改 TLS）。
- **新增上游风控拦截识别与告警分类**：`protocol.ClassifyUpstreamBlock()` 按官方 bundle 同一规则
  识别三类挑战，并在登录/会话创建/POW/上传/流式 completion 五个出口打独立日志标签：
  `405`+`x-amzn-waf-action: captcha` → `[upstream_waf_captcha]`；
  `202`+`x-amzn-waf-action: challenge` → `[upstream_waf_challenge]`；
  `403`/`429`+`cf-mitigated: challenge` → `[upstream_cf_challenge]`。
  日志带 `kind`/`url`/`status`/`waf_action`/`cf_mitigated`/`account` 字段，可直接检索。
- **拦截与账号异常彻底分开**：新增失败类型 `FailureUpstreamBlocked`（`upstream_blocked`），
  命中挑战**不再**触发 token 刷新/切号（刷新解决不了出口 IP 被拦），也不会被误归为封号。
  对调用方统一返回 **502 Bad Gateway**（`code: upstream_blocked`）：此前会话创建/PoW 被拦时
  会落到 `401 Failed to get PoW (invalid token...)`，诱导客户端做一次毫无用处的重新登录；
  现由 `completionruntime.blockedUpstreamError()` 集中判断，覆盖 OpenAI Chat / Responses /
  Claude / Gemini 四个入口与流式/非流式/分段/空输出重试全部路径（协议适配层不复制该判断）。
  处置方向改为换出口节点/避开被拉黑地区段（配合 `mihomo.node_exclude`）。
- **流式 completion 不再把挑战页当 SSE 解析**：`CallCompletion` 遇到已识别的挑战响应直接
  返回 `FailureUpstreamBlocked`（其他非 200 行为完全不变）。
- **启动日志可观测**：新增 `[chrome] web-client fingerprint` 行，打印生效的 Chrome 大版本与
  实际 TLS 预设名，部署后一眼确认指纹是否真的切过去了。

### 修复：`DS2API_CHROME_MAJOR_VERSION` 两个真实缺陷

- **写在 `.env` 里静默失效**：该变量原本在包初始化阶段读取，而 `.env` 是 `main()` 里
  `config.LoadDotEnv()` 才加载的，导致按文档在 `.env` 配版本号根本不生效。现改为首次使用时
  惰性读取 + `.env` 加载完成后重同步。
- **非法值会拼出坏指纹**：`DS2API_CHROME_MAJOR_VERSION=abc` 原本直接产出
  `Chrome/abc.0.0.0` 这种没人用的 UA（比不改更危险）。现校验为纯数字且 ∈ [133,999]，
  非法则忽略并回退契约值，同时打 `[chrome] invalid DS2API_CHROME_MAJOR_VERSION ignored` 告警。
- 删除已失效的 `TLSChromeVersion = "133"` 写死常量（与实际生效预设早已不符、只误导阅读者），
  改为由解析结果推导的 `TLSChromeVersion()`；`tests/wire-capture` 输出同步修正。

**未变更（核对后确认无需动）**：`x-client-version` 仍为 `2.4.0`（官方 bundle 今日仍为
`appVersion:"2.4.0"`，Android App 的 2.4.3 与 web 头无关）、`x-client-*` 头集合无新增必需项、
PoW 字段与算法一致、继续不发设备级头 `x-hif-dliq`/`x-hif-leim`。

**验证**：gofmt / lint（0 issues）/ refactor-gate / Go 全量单测 + Node 161 项 / WebUI 构建全部通过；
`tests/wire-capture -ours` 核对 151/150/152/非法 四种取值的 UA、sec-ch-ua 与 TLS 预设均自洽。
**真实环环境实测**（非合成用例）：

- 阿里云国内出口：`GET /api/v0/client/settings` 用 Chrome 151 指纹 + `chrome-151-windows` TLS
  返回 **HTTP 200**，分类器正确返回 `none`（无误判）。
- 欧洲 VPS（193.123.167.208）：`GET https://chat.deepseek.com/` 返回 **202 + `x-amzn-waf-action: challenge`**，
  分类器正确识别为 **`waf_challenge`**——对着 DeepSeek 真实 AWS WAF 命中，证明识别规则有效；
  同一机器的 API 路径仍返回 200（说明新指纹能通过 CloudFront 层）。

文档同步：`docs/MIHOMO_BRIDGE.md` 新增「上游风控拦截分类（WAF / Cloudflare）」章节、
`.env.example` 更新默认值与预设解析说明。

## 4.7.2 (2026-08-28)

### 变更：node_exclude 接入管理台，欧美节点默认恢复可选

- **管理台接入**：WebUI「代理桥」页新增「节点排除（node_exclude）」编辑框
  （一行一个关键字），`PUT /admin/mihomo/settings` 同步支持该字段，
  `GET /admin/mihomo/status` 回显当前值。保存后立即生效并按新列表重滤
  订阅缓存节点、回收失效节点的端口映射与账号绑定；旧客户端请求不带
  该字段时保持原值不被清空。注意：被旧关键字过滤掉的节点已从缓存移除，
  放宽关键字后需刷新订阅才会恢复。
- **示例配置默认不再排除美/英节点**：4.7.0 预置的 `["🇺🇸", "🇬🇧"]` 会让
  照抄示例部署的实例整体剔除美国/英国节点，现改为空数组默认不过滤，
  全部订阅节点（含欧美）均可选择。
- 已部署实例不受示例配置影响：可在管理台「代理桥」页直接编辑，
  或手工清空各自 `config.json` 中 `mihomo.node_exclude` 后重启。

## 4.7.1 (2026-08-26)

### 修复：自定义部署镜像缺少 CA 根证书导致账号登录失败

v4.7.0 若使用自定义 Dockerfile 部署（非仓库自带多阶段构建），基于
`debian:bookworm-slim` 却遗漏 `ca-certificates` 时，容器内 `/etc/ssl/certs`
为空，Go 标准库 TLS 校验全部失败（`x509: certificate signed by unknown authority`），
账号无法登录刷新 token，表现为"账号登录不了"。

- 部署注意事项：自定义部署 Dockerfile 的 `FROM debian:*` 之后必须
  `RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates`。
- 验证方法：构建后 `docker run --rm <image> sh -c "ls /etc/ssl/certs/ca-certificates.crt"`。
- 生产环境 apt 源建议替换为 `mirrors.aliyun.com`，避免 `deb.debian.org` 下载卡死。

## 4.7.0 (2026-08-26)

### 新增：Mihomo 节点过滤（mihomo.node_exclude）

DeepSeek 网页端风控升级（AWS WAF + CloudFront 按 IP 信誉拦截）导致部分机场节点
（美国/英国数据中心段）被拉黑，账号被禁言。新增按节点名关键字排除节点的配置，
等效于在机场侧下掉风险节点，订阅刷新后依然生效：

- `mihomo.node_exclude`：字符串数组，节点名包含任一关键字即从节点池剔除。
  新增/刷新订阅时落库前自动过滤，启动加载旧缓存时同样过滤，无需手工清洗
  `mihomo_subscriptions.json`。
- 被排除节点不参与 mihomo 运行时 proxies 生成、健康检查与账号自动分配。
- 配置示例：`"node_exclude": ["🇺🇸", "🇬🇧"]`。

## 4.6.2 (2026-08-25)

### 修复：专家（PRO）模型丢失上下文

PRO 模型（`deepseek-v4-pro`）不支持文件上传，超长提示词依赖 `expert_prompt_segment`
分段发送：前 N-1 段用 `FireCompletionAndStop` 发送并中断，最后一段携带
`parent_message_id` 链从上游会话树取回前文。该链路依赖上游确认"被 stop 的消息
已提交到会话树"，一旦某段未落库，后续分段无法把前文并入上下文，表现为
"PRO 模型读不到上下文"。

本次改动：

- `FireCompletionAndStop` 在 `stop_stream` 后未收到提交确认（未等到 `event: close`
  且连接被超时强制关闭）时，返回 `ErrSegmentCommitUnconfirmed`，不再当作成功继续。
- 分段发送失败或提交未确认时，回退为单消息发送：把剩余分段按序拼接还原原文，
  以最后一个已确认提交的分段 id 作为 parent，保证最终请求携带尽可能完整的上下文
  而不是直接报错或静默丢上下文。回退路径记录 `[expert_segment_fallback]` 告警日志。
- 专家模型丢弃非文本附件（图片、PDF 等）时记录 `[expert_attachment_dropped]`
  告警日志（含文件名/MIME），便于线上确认"PRO 模型看不到附件"的原因；
  `expert_text_file_inline` 被关闭时同样会提示。
- 新增 `internal/completionruntime/segments_test.go` 覆盖分段链正常/失败/未确认三条路径。
- 同步更新 `docs/prompt-compatibility.md` 分段与文件内联章节。
