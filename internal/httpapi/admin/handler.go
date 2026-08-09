package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"ds2api/internal/chathistory"
	adminaccounts "ds2api/internal/httpapi/admin/accounts"
	adminauth "ds2api/internal/httpapi/admin/auth"
	adminconfig "ds2api/internal/httpapi/admin/configmgmt"
	admindevcapture "ds2api/internal/httpapi/admin/devcapture"
	adminhistory "ds2api/internal/httpapi/admin/history"
	adminmihomo "ds2api/internal/httpapi/admin/mihomo"
	adminproxies "ds2api/internal/httpapi/admin/proxies"
	adminrawsamples "ds2api/internal/httpapi/admin/rawsamples"
	adminsettings "ds2api/internal/httpapi/admin/settings"
	adminshared "ds2api/internal/httpapi/admin/shared"
	adminvercel "ds2api/internal/httpapi/admin/vercel"
	adminversion "ds2api/internal/httpapi/admin/version"
)

type Handler struct {
	Store       adminshared.ConfigStore
	Pool        adminshared.PoolController
	DS          adminshared.DeepSeekCaller
	OpenAI      adminshared.OpenAIChatCaller
	ChatHistory *chathistory.Store
	// Mihomo 是 Mihomo 代理桥控制器；nil 时 /admin/mihomo/* 返回 503。
	Mihomo adminmihomo.Bridge
	// WebUIFallback is forwarded to the auth sub-handler so its RequireAdmin
	// middleware can serve the SPA index.html when a browser navigation request
	// (GET, no Authorization, Accept: text/html) hits a protected admin API
	// endpoint that collides with an SPA route (e.g. /admin/settings).
	WebUIFallback func(http.ResponseWriter, *http.Request) bool
}

func RegisterRoutes(r chi.Router, h *Handler) {
	deps := adminsharedDeps(h)
	authHandler := &adminauth.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory, WebUIFallback: h.WebUIFallback}
	accountsHandler := &adminaccounts.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	configHandler := &adminconfig.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	settingsHandler := &adminsettings.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	proxiesHandler := &adminproxies.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	mihomoHandler := &adminmihomo.Handler{Store: deps.Store, Pool: deps.Pool, Bridge: h.Mihomo}
	rawSamplesHandler := &adminrawsamples.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	vercelHandler := &adminvercel.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	historyHandler := &adminhistory.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	devCaptureHandler := &admindevcapture.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}
	versionHandler := &adminversion.Handler{Store: deps.Store, Pool: deps.Pool, DS: deps.DS, OpenAI: deps.OpenAI, ChatHistory: deps.ChatHistory}

	adminauth.RegisterPublicRoutes(r, authHandler)
	r.Group(func(pr chi.Router) {
		pr.Use(authHandler.RequireAdmin)
		adminauth.RegisterProtectedRoutes(pr, authHandler)
		adminconfig.RegisterRoutes(pr, configHandler)
		adminsettings.RegisterRoutes(pr, settingsHandler)
		adminproxies.RegisterRoutes(pr, proxiesHandler)
		adminmihomo.RegisterRoutes(pr, mihomoHandler)
		adminaccounts.RegisterRoutes(pr, accountsHandler)
		adminrawsamples.RegisterRoutes(pr, rawSamplesHandler)
		adminvercel.RegisterRoutes(pr, vercelHandler)
		admindevcapture.RegisterRoutes(pr, devCaptureHandler)
		adminhistory.RegisterRoutes(pr, historyHandler)
		adminversion.RegisterRoutes(pr, versionHandler)
	})
}

func adminsharedDeps(h *Handler) adminsharedDepsValue {
	if h == nil {
		return adminsharedDepsValue{}
	}
	return adminsharedDepsValue{Store: h.Store, Pool: h.Pool, DS: h.DS, OpenAI: h.OpenAI, ChatHistory: h.ChatHistory}
}

type adminsharedDepsValue struct {
	Store       adminshared.ConfigStore
	Pool        adminshared.PoolController
	DS          adminshared.DeepSeekCaller
	OpenAI      adminshared.OpenAIChatCaller
	ChatHistory *chathistory.Store
}
