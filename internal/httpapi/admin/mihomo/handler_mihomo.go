package mihomo

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// bridgeOrFail 统一处理 Bridge 未装配的场景（如部分测试构造），避免空指针。
func (h *Handler) bridgeOrFail(w http.ResponseWriter) Bridge {
	if h == nil || h.Bridge == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "Mihomo 代理桥未装配"})
		return nil
	}
	return h.Bridge
}

func (h *Handler) getStatus(w http.ResponseWriter, _ *http.Request) {
	b := h.bridgeOrFail(w)
	if b == nil {
		return
	}
	writeJSON(w, http.StatusOK, b.Status())
}

func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) {
	b := h.bridgeOrFail(w)
	if b == nil {
		return
	}
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	enabled := false
	if v, ok := req["enabled"].(bool); ok {
		enabled = v
	}
	err := b.UpdateSettings(r.Context(), enabled, fieldString(req, "binary_path"), fieldInt(req, "base_port"), fieldInt(req, "api_port"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": b.Status()})
}

func (h *Handler) applyNow(w http.ResponseWriter, r *http.Request) {
	b := h.bridgeOrFail(w)
	if b == nil {
		return
	}
	if err := b.Apply(r.Context()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": b.Status()})
}

func (h *Handler) listSubscriptions(w http.ResponseWriter, _ *http.Request) {
	snap := h.Store.Snapshot()
	items := make([]map[string]any, 0, len(snap.Mihomo.Subscriptions))
	for _, sub := range snap.Mihomo.Subscriptions {
		items = append(items, map[string]any{
			"id":         sub.ID,
			"name":       sub.Name,
			"url":        sub.URL,
			"updated_at": sub.UpdatedAt,
			"node_count": len(sub.Nodes),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (h *Handler) addSubscription(w http.ResponseWriter, r *http.Request) {
	b := h.bridgeOrFail(w)
	if b == nil {
		return
	}
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	sub, err := b.AddSubscription(r.Context(), fieldString(req, "name"), fieldString(req, "url"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"subscription": map[string]any{
			"id":         sub.ID,
			"name":       sub.Name,
			"url":        sub.URL,
			"updated_at": sub.UpdatedAt,
			"node_count": len(sub.Nodes),
		},
	})
}

func (h *Handler) refreshSubscription(w http.ResponseWriter, r *http.Request) {
	b := h.bridgeOrFail(w)
	if b == nil {
		return
	}
	subID := urlParam(r, "subID")
	count, err := b.RefreshSubscription(r.Context(), subID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "node_count": count})
}

func (h *Handler) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	b := h.bridgeOrFail(w)
	if b == nil {
		return
	}
	subID := urlParam(r, "subID")
	if err := b.DeleteSubscription(r.Context(), subID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *Handler) listNodes(w http.ResponseWriter, _ *http.Request) {
	b := h.bridgeOrFail(w)
	if b == nil {
		return
	}
	nodes := b.ListNodes()
	writeJSON(w, http.StatusOK, map[string]any{"items": nodes, "total": len(nodes)})
}

// bindAccount 处理 PUT /mihomo/bindings/{identifier}，body: {"node": "<nodeKey>"}。
// node 为空字符串表示解绑（账号回落到直连/手动代理）。
func (h *Handler) bindAccount(w http.ResponseWriter, r *http.Request) {
	b := h.bridgeOrFail(w)
	if b == nil {
		return
	}
	identifier := urlParam(r, "identifier")
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	nodeKey := strings.TrimSpace(fieldString(req, "node"))
	if err := b.BindAccount(r.Context(), identifier, nodeKey); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "node": nodeKey})
}

func urlParam(r *http.Request, name string) string {
	v := chi.URLParam(r, name)
	if decoded, err := url.PathUnescape(v); err == nil {
		return decoded
	}
	return v
}
