# Changelog

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
