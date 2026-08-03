// Copyright © rocksys 开发手册第 8 章 Admin API（管理接口服务器）。
//
// 职责：暴露回环地址的管理 HTTP API，支持组件热开关与配置热改。
// 通过 conf.Manager.Set 与 hotswap.Manager 的 Enable/Disable/List 与底座交互，
// 供 rockctl 与运维人员调用。
package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"github.com/iotames/easyserver"
	"github.com/iotames/easyserver/httpsvr"

	"rocksys/internal/conf"
	"rocksys/internal/hotswap"
)

// 内建端点路径（§8.1 表）。
const (
	PathSwitchOn  = "/admin/switch/on"
	PathSwitchOff = "/admin/switch/off"
	PathSwitchList = "/admin/switch/list"
	PathConfig     = "/admin/config"
)

const (
	// authorizationHeader 鉴权请求头名称。
	authorizationHeader = "Authorization"
	// bearerPrefix Bearer token 前缀。
	bearerPrefix = "Bearer "
	// envAdminToken 可选的鉴权 token 环境变量（§8.3），不注册进 easyconf。
	envAdminToken = "ROCKSYS_ADMIN_TOKEN"
)

var errNilHandler = errors.New("adminapi: nil path or handler")

// AdminServer 管理接口服务器（§8.1.0）。
type AdminServer struct {
	srv        *easyserver.Server // 独立 easyserver 实例（回环地址）
	confMgr    conf.Manager       // ★ 用于内建 PUT /admin/config（调用 conf.Manager.Set）
	hotswapMgr *hotswap.Manager   // ★ 用于内建 /admin/switch/on|off|list
}

// New 创建独立的管理接口服务器并注册 5 个内建端点（§8.1）。
func New(addr string, confMgr conf.Manager, hotswapMgr *hotswap.Manager) *AdminServer {
	s := &AdminServer{
		srv:        easyserver.NewServer(addr),
		confMgr:    confMgr,
		hotswapMgr: hotswapMgr,
	}
	s.registerBuiltin()
	return s
}

// ListenAndServe 启动监听（委托内部 *easyserver.Server）。
func (s *AdminServer) ListenAndServe() error {
	return s.srv.ListenAndServe()
}

// Shutdown 优雅停机：停止接收新连接，等待在途请求完成。
func (s *AdminServer) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// RegisterPlugin 注册挂件端点（§8.1）。
// 把 func(w, r) 包装为 func(ctx httpsvr.Context)，并为同一 path 注册 GET 与 POST 两个方法
// （挂件 handler 自行判断方法）。返回 error 表示注册失败。
func (s *AdminServer) RegisterPlugin(path string, h func(http.ResponseWriter, *http.Request)) error {
	if path == "" || h == nil {
		return errNilHandler
	}
	wrapped := func(ctx httpsvr.Context) { h(ctx.Writer, ctx.Request) }
	s.srv.AddHandler(http.MethodGet, path, wrapped)
	s.srv.AddHandler(http.MethodPost, path, wrapped)
	return nil
}

// registerBuiltin 注册 5 个内建端点，外层统一套一层鉴权检查（§8.3）。
func (s *AdminServer) registerBuiltin() {
	check := s.requireAuth()
	s.srv.AddHandler(http.MethodPost, PathSwitchOn, check(s.handleSwitchOn))
	s.srv.AddHandler(http.MethodPost, PathSwitchOff, check(s.handleSwitchOff))
	s.srv.AddHandler(http.MethodGet, PathSwitchList, check(s.handleSwitchList))
	s.srv.AddHandler(http.MethodGet, PathConfig, check(s.handleConfigGet))
	s.srv.AddHandler(http.MethodPut, PathConfig, check(s.handleConfigPut))
}

// requireAuth 返回构造时的鉴权包装器：可选 ROCKSYS_ADMIN_TOKEN 校验。
// 若设置了 token 且请求头 Authorization != "Bearer <token>" → 401；
// 未设置 token 时不校验（默认仅回环信任）。
func (s *AdminServer) requireAuth() func(func(httpsvr.Context)) func(httpsvr.Context) {
	token := os.Getenv(envAdminToken)
	return func(next func(httpsvr.Context)) func(httpsvr.Context) {
		return func(ctx httpsvr.Context) {
			if token != "" && ctx.Request.Header.Get(authorizationHeader) != bearerPrefix+token {
				_ = ctx.Text("unauthorized", http.StatusUnauthorized)
				return
			}
			next(ctx)
		}
	}
}

// handleSwitchOn 开启组件：{"name":"shield"} → hotswapMgr.Enable。
func (s *AdminServer) handleSwitchOn(ctx httpsvr.Context) {
	var body struct {
		Name string `json:"name"`
	}
	if err := ctx.GetPostJson(&body); err != nil || body.Name == "" {
		_ = ctx.Json(map[string]any{"ok": false, "error": "invalid body, require {\"name\":\"...\"}"}, http.StatusBadRequest)
		return
	}
	if err := s.hotswapMgr.Enable(body.Name); err != nil {
		_ = ctx.Json(map[string]any{"ok": false, "error": err.Error()}, http.StatusInternalServerError)
		return
	}
	_ = ctx.Json(map[string]any{"ok": true}, http.StatusOK)
}

// handleSwitchOff 关闭组件：{"name":"shield"} → hotswapMgr.Disable。
func (s *AdminServer) handleSwitchOff(ctx httpsvr.Context) {
	var body struct {
		Name string `json:"name"`
	}
	if err := ctx.GetPostJson(&body); err != nil || body.Name == "" {
		_ = ctx.Json(map[string]any{"ok": false, "error": "invalid body, require {\"name\":\"...\"}"}, http.StatusBadRequest)
		return
	}
	if err := s.hotswapMgr.Disable(body.Name); err != nil {
		_ = ctx.Json(map[string]any{"ok": false, "error": err.Error()}, http.StatusInternalServerError)
		return
	}
	_ = ctx.Json(map[string]any{"ok": true}, http.StatusOK)
}

// handleSwitchList 列出所有实体状态（§8.1：name/kind/state/started_at/last_switch_at/message）。
func (s *AdminServer) handleSwitchList(ctx httpsvr.Context) {
	list := s.hotswapMgr.List()
	out := make([]map[string]any, 0, len(list))
	for _, st := range list {
		out = append(out, map[string]any{
			"name":           st.Name,
			"kind":           st.Kind,
			"state":          st.State.String(),
			"started_at":     st.StartedAt,
			"last_switch_at": st.LastSwitchAt,
			"message":        st.Message,
		})
	}
	_ = writeJSON(ctx.Writer, out, http.StatusOK)
}

// handleConfigGet 查看当前配置（listen/upstream/timeout(秒)/admin/config_file/log_level）。
func (s *AdminServer) handleConfigGet(ctx httpsvr.Context) {
	cfg := s.confMgr.Current()
	_ = writeJSON(ctx.Writer, map[string]any{
		"listen":      cfg.ListenAddr,
		"upstream":    cfg.DefaultUpstream,
		"timeout":     int(cfg.UpstreamTimeout / 1e9),
		"admin":       cfg.AdminAddr,
		"config_file": cfg.ConfigFile,
		"log_level":   cfg.LogLevel,
	}, http.StatusOK)
}

// handleConfigPut 热改配置：{"ROCKSYS_UPSTREAM":"http://..."} → 逐项 confMgr.Set。
// ★ key 必须为注册名全名（即环境变量名）；未注册 key 会被 easyconf 静默忽略。
func (s *AdminServer) handleConfigPut(ctx httpsvr.Context) {
	var body map[string]string
	if err := ctx.GetPostJson(&body); err != nil {
		_ = ctx.Json(map[string]any{"ok": false, "error": "invalid json body"}, http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		_ = ctx.Json(map[string]any{"ok": false, "error": "empty config body"}, http.StatusBadRequest)
		return
	}
	for k, v := range body {
		if err := s.confMgr.Set(k, v); err != nil {
			_ = ctx.Json(map[string]any{"ok": false, "error": "set " + k + ": " + err.Error()}, http.StatusInternalServerError)
			return
		}
	}
	_ = ctx.Json(map[string]any{"ok": true}, http.StatusOK)
}

// writeJSON 将任意值以 JSON 写回客户端（用于 map 与数组响应）。
func writeJSON(w http.ResponseWriter, v any, code int) error {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(b)
	return nil
}