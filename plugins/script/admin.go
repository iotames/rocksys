// Admin Handler：脚本发布/回滚管理端点（DEV_HANDBOOK.md §8.1 插件端点机制 + §15）。
package script

import (
	"encoding/json"
	"net/http"

	"rocksys/internal/hotswap"
)

// 端点路径（由 cmd/rocksys 装配时经 adminapi.RegisterPlugin 注入）。
const (
	PathPublish  = "/admin/script/publish"
	PathRollback = "/admin/script/rollback"
)

// AdminHandler 脚本管理端点处理器。
// 通过 hotswap.Manager.GetMiddleware("script") 取回 *Engine 实例执行编译/回滚，
// 避免在装配代码中手动传递引用链条（§6.2 GetMiddleware）。
type AdminHandler struct {
	mgr *hotswap.Manager
}

// NewAdminHandler 创建脚本管理 handler。
func NewAdminHandler(mgr *hotswap.Manager) *AdminHandler {
	return &AdminHandler{mgr: mgr}
}

// engine 从 hotswap 管理器取回已注册的 *Engine 实例；未注册返回 nil。
func (h *AdminHandler) engine() *Engine {
	if h.mgr == nil {
		return nil
	}
	ml := h.mgr.GetMiddleware("script")
	if ml == nil {
		return nil
	}
	eng, ok := ml.(*Engine)
	if !ok {
		return nil
	}
	return eng
}

// Publish POST /admin/script/publish
//
//	{"name":"rule1","source":"if req.path() == \"/block\" ..."}
//	→ 200 {"ok":true,"version":1}
//
// 沙箱拒绝 / 编译失败 → {"ok":false,"error":"..."}（发布失败，§15）。
func (h *AdminHandler) Publish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid body, require {\"name\":\"...\",\"source\":\"...\"}"}, http.StatusBadRequest)
		return
	}
	eng := h.engine()
	if eng == nil {
		writeJSON(w, map[string]any{"ok": false, "error": "script engine not registered"}, http.StatusInternalServerError)
		return
	}
	ver, err := eng.Publish(body.Name, body.Source)
	if err != nil {
		// 沙箱拒绝 → 发布失败（§15 验收）。
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()}, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "version": ver}, http.StatusOK)
}

// Rollback POST /admin/script/rollback
//
//	{"name":"rule1","version":0}   → 移除该脚本 → 200 {"ok":true}
//	{"name":"rule1","version":2}  → 回滚到历史版本 2
func (h *AdminHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid body, require {\"name\":\"...\",\"version\":...}"}, http.StatusBadRequest)
		return
	}
	eng := h.engine()
	if eng == nil {
		writeJSON(w, map[string]any{"ok": false, "error": "script engine not registered"}, http.StatusInternalServerError)
		return
	}
	if err := eng.Rollback(body.Name, body.Version); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()}, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true}, http.StatusOK)
}

// writeJSON 以 JSON 写回客户端。
func writeJSON(w http.ResponseWriter, v any, code int) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}