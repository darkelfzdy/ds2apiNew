package history

import (
	"net/http"

	dsclient "ds2api/internal/deepseek/client"
)

func MapError(err error) (int, string) {
	switch {
	case dsclient.IsManagedUnauthorizedError(err):
		return http.StatusUnauthorized, "Account token is invalid. Please re-login the account in admin."
	case dsclient.IsDirectUnauthorizedError(err):
		return http.StatusUnauthorized, "Invalid token. If this should be a DS2API key, add it to config.keys first."
	case dsclient.IsUpstreamBlockedError(err):
		// 上游风控层（AWS WAF / Cloudflare challenge）拦的是出口 IP/指纹，
		// 不是账号也不是调用方的凭据：语义上等价于上游网关拒绝，用 502，
		// 且不复用 401（避免客户端误判为 token 失效去重新登录）。
		return http.StatusBadGateway, err.Error()
	default:
		return http.StatusInternalServerError, err.Error()
	}
}
