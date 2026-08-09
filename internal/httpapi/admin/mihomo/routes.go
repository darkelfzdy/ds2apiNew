package mihomo

import (
	"github.com/go-chi/chi/v5"
)

func RegisterRoutes(r chi.Router, h *Handler) {
	r.Get("/mihomo/status", h.getStatus)
	r.Put("/mihomo/settings", h.updateSettings)
	r.Post("/mihomo/apply", h.applyNow)
	r.Get("/mihomo/subscriptions", h.listSubscriptions)
	r.Post("/mihomo/subscriptions", h.addSubscription)
	r.Post("/mihomo/subscriptions/{subID}/refresh", h.refreshSubscription)
	r.Delete("/mihomo/subscriptions/{subID}", h.deleteSubscription)
	r.Get("/mihomo/nodes", h.listNodes)
	r.Put("/mihomo/bindings/{identifier}", h.bindAccount)
}
