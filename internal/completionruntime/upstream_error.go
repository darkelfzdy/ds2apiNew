package completionruntime

import (
	"net/http"

	"ds2api/internal/assistantturn"
	dsclient "ds2api/internal/deepseek/client"
)

// upstreamBlockedMessage 是给调用方看的拦截说明：明确指向出口而非账号，
// 免得运维/客户端拿着它去重新登录或换 key。
const upstreamBlockedMessage = "Upstream risk control blocked this egress (WAF/Cloudflare challenge). Switch proxy node or exclude blocked regions."

// blockedUpstreamError 把「上游风控拦截」映射为面向调用方的 502；非拦截返回 nil。
//
// 为什么必须是 502 而不是既有的 401/500：AWS WAF / Cloudflare challenge 拦的是
// 出口 IP 与指纹，与账号状态和调用方 token 都无关。沿用 401（"invalid token"）
// 会诱导客户端去做一次毫无用处的重新登录，沿用 500 则把可定位的风控事件埋进
// 通用错误里。集中在这一个函数里判断，是为了让 OpenAI Chat / Responses / Claude /
// Gemini 四个入口拿到完全一致的语义（协议适配层不各自复制这套判断）。
func blockedUpstreamError(err error) *assistantturn.OutputError {
	if !dsclient.IsUpstreamBlockedError(err) {
		return nil
	}
	return &assistantturn.OutputError{
		Status:  http.StatusBadGateway,
		Message: upstreamBlockedMessage,
		Code:    "upstream_blocked",
	}
}
