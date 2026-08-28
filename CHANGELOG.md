# Changelog

## 4.7.2 (2026-08-28)

### 变更：示例配置默认不再排除美/英节点，欧美节点恢复可选

4.7.0 引入 `mihomo.node_exclude` 时，`config.example.json` 预置了
`["🇺🇸", "🇬🇧"]`，照抄示例部署的实例会把美国/英国节点整体剔除，
表现为"代理选不了欧美节点"。本次把预置值改为空数组，默认不过滤，
全部订阅节点（含欧美）均可选择：

- `config.example.json`：`mihomo.node_exclude` 预置值改为 `[]`，
  注释同步说明"留空 = 不过滤"的默认行为。
- `docs/MIHOMO_BRIDGE.md`：配置参考明确默认行为与使用建议——
  仅当某地区段被 DeepSeek 风控拉黑时再按需填入关键字。
- 已部署实例不受示例配置影响：如需让欧美节点可选，需手工清空或删除
  各自 `config.json` 中 `mihomo.node_exclude` 的内容后重启。

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
