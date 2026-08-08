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
	"io/fs"
	"net/http"
	"strings"

	"github.com/iotames/easydb"
	"github.com/iotames/easyserver"
	"github.com/iotames/easyserver/httpsvr"
	"github.com/iotames/easyserver/log"

	"rocksys/internal/conf"
	"rocksys/internal/db"
	"rocksys/internal/hotswap"
)

// 内建端点路径（§8.1 表）。
const (
	PathSwitchOn   = "/admin/switch/on"
	PathSwitchOff  = "/admin/switch/off"
	PathSwitchList = "/admin/switch/list"
	PathConfig     = "/admin/config"
	PathConfigList = "/admin/config/list"
)

// 认证端点路径（§8.4）：登录/注册/重置/状态，均免鉴权（前置条件由 handler 校验）。
const (
	PathAuthStatus = "/admin/auth/status"
	PathLogin      = "/admin/auth/login"
	PathRegister   = "/admin/auth/register"
	PathReset      = "/admin/auth/reset"
)

const (
	// authorizationHeader 鉴权请求头名称。
	authorizationHeader = "Authorization"
	// bearerPrefix Bearer token 前缀。
	bearerPrefix = "Bearer "
)

var errNilHandler = errors.New("adminapi: nil path or handler")

// AdminServer 管理接口服务器（§8.1.0）。
type AdminServer struct {
	srv          *easyserver.Server // 独立 easyserver 实例（回环地址）
	confMgr      conf.Manager       // ★ 用于内建 PUT /admin/config（调用 conf.Manager.Set）
	hotswapMgr   *hotswap.Manager   // ★ 用于内建 /admin/switch/on|off|list
	initialized  *bool              // ADMIN_INITIALIZED 配置指针（热更可读）
	jwtSecret    *string            // ADMIN_JWT_SECRET 配置指针（登录 JWT 签名密钥）
	adminToken   *string            // ROCKSYS_ADMIN_TOKEN 配置指针（静态预共享令牌）
	edb          *easydb.EasyDb     // 用户存储数据库连接（dataDB.EasyDB()，可 nil）
	sqls         db.SQLSource       // 用户存储 SQL 脚本源（dataDB，可 nil）
	users        *userStore         // 超级管理员用户存储（edb 与 sqls 均就绪时可用）
	auth         *adminAuth         // 管理接口鉴权器
	loginLimiter *loginLimiter      // 登录失败限流器（按 IP）
}

// New 创建独立的管理接口服务器并注册全部内建端点（§8.1/§8.4）。
// edb 为统一数据访问层底层连接（sqlite），用于用户存储；为 nil 时用户认证不可用，
// 管理接口降级为「静态 token / 回环信任」模式（既有行为）。
// 用户存储的 SQL 脚本源由 SetSQLSource 注入（装配时传入 dataDB）；在注入前
// 用户认证同样不可用（auth 自动降级）。
func New(addr string, confMgr conf.Manager, hotswapMgr *hotswap.Manager, edb *easydb.EasyDb) *AdminServer {
	s := &AdminServer{
		srv:          easyserver.NewServer(addr),
		confMgr:      confMgr,
		hotswapMgr:   hotswapMgr,
		edb:          edb,
		loginLimiter: newLoginLimiter(),
	}

	// 注册管理接口专属配置项：初始化标记 + 登录 JWT 签名密钥。
	if confMgr != nil {
		var initialized bool
		if err := confMgr.Register(&initialized, "ADMIN_INITIALIZED", "false", "是否已初始化超级管理员"); err != nil {
			panic(err)
		}
		s.initialized = &initialized
		var jwtSecret string
		if err := confMgr.Register(&jwtSecret, "ADMIN_JWT_SECRET", "", "管理接口登录 JWT 签名密钥（为空时进程内随机，重启后需重新登录）"); err != nil {
			panic(err)
		}
		s.jwtSecret = &jwtSecret
		var adminToken string
		if err := confMgr.Register(&adminToken, "ROCKSYS_ADMIN_TOKEN", "", "管理接口静态预共享令牌（供 rockctl/脚本使用；配置后即使回环地址也需 Bearer 鉴权）"); err != nil {
			panic(err)
		}
		s.adminToken = &adminToken
	}
	s.initUsers()
	s.auth = newAdminAuth(confMgr, s.initialized, s.jwtSecret, s.adminToken, s.users, addr)
	s.registerBuiltin()
	return s
}

// SetSQLSource 注入用户存储 SQL 脚本源（通常为 internal/db 数据访问层）。
// 需在 New 之后、认证功能使用前调用；edb 与 sqls 均就绪时创建用户存储。
// 无脚本源时用户认证不可用（auth 降级为静态 token / 回环信任），但管理接口其余功能不受影响。
func (s *AdminServer) SetSQLSource(src db.SQLSource) {
	s.sqls = src
	s.initUsers()
}

// initUsers 在 edb 与 sqls 均就绪时创建用户存储（幂等，失败仅记录）。
// 注意：auth 在 New 时已用当时的 users（可能为 nil）构造，创建成功须同步回填 auth.users。
func (s *AdminServer) initUsers() {
	if s.users == nil && s.edb != nil && s.sqls != nil {
		if users, err := newUserStore(s.edb, s.sqls); err == nil {
			s.users = users
			if s.auth != nil {
				s.auth.users = users
			}
		} else {
			// 建表/读脚本失败：认证降级为静态 token / 回环信任，但需留痕便于运维排查。
			log.Warn("adminapi: 用户存储初始化失败，认证降级为静态 token / 回环信任", "err", err.Error())
		}
	}
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
// （挂件 handler 自行判断方法）。外层统一套鉴权检查。返回 error 表示注册失败。
func (s *AdminServer) RegisterPlugin(path string, h func(http.ResponseWriter, *http.Request)) error {
	if path == "" || h == nil {
		return errNilHandler
	}
	wrapped := func(ctx httpsvr.Context) {
		if !s.auth.check(ctx) {
			return
		}
		h(ctx.Writer, ctx.Request)
	}
	s.srv.AddHandler(http.MethodGet, path, wrapped)
	s.srv.AddHandler(http.MethodPost, path, wrapped)
	return nil
}

// registerBuiltin 注册内建端点，外层统一套一层鉴权检查（§8.3/§8.4）。
// 认证端点（auth/status|login|register|reset）在 auth.check 中豁免鉴权。
func (s *AdminServer) registerBuiltin() {
	check := s.requireAuth()
	s.srv.AddHandler(http.MethodPost, PathSwitchOn, check(s.handleSwitchOn))
	s.srv.AddHandler(http.MethodPost, PathSwitchOff, check(s.handleSwitchOff))
	s.srv.AddHandler(http.MethodGet, PathSwitchList, check(s.handleSwitchList))
	s.srv.AddHandler(http.MethodGet, PathConfig, check(s.handleConfigGet))
	s.srv.AddHandler(http.MethodPut, PathConfig, check(s.handleConfigPut))
	s.srv.AddHandler(http.MethodGet, PathConfigList, check(s.handleConfigList))
	s.srv.AddHandler(http.MethodGet, PathAuthStatus, check(s.handleAuthStatus))
	s.srv.AddHandler(http.MethodPost, PathLogin, check(s.handleLogin))
	s.srv.AddHandler(http.MethodPost, PathRegister, check(s.handleRegister))
	s.srv.AddHandler(http.MethodPost, PathReset, check(s.handleReset))
	// 进程日志管理端点（§3.1 表）：info/level/output/tail/stream，均走 requireAuth 鉴权。
	// 命名区分：obs 插件已有 /admin/logs（复数，业务访问日志）；本组为 /admin/log/*（单数，进程日志）。
	s.srv.AddHandler(http.MethodGet, "/admin/log/info", check(s.handleLogInfo))
	s.srv.AddHandler(http.MethodPost, "/admin/log/level", check(s.handleLogLevel))
	s.srv.AddHandler(http.MethodPost, "/admin/log/output", check(s.handleLogOutput))
	s.srv.AddHandler(http.MethodGet, "/admin/log/tail", check(s.handleLogTail))
	s.srv.AddHandler(http.MethodGet, "/admin/log/stream", check(s.handleLogStream))
}

// RegisterWebUI 注册内嵌 WebUI 静态资源（管理控制台）。
// fsys 为嵌入的静态资源（含 index.html 与 assets/ 等），为每个文件注册一个精确 GET 路由：
// embed 中 "index.html" → GET "/"；其余文件 → "/<相对路径>"（如 "/assets/js/main.js"）。
func (s *AdminServer) RegisterWebUI(fsys fs.FS) error {
	return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		urlPath := "/" + path
		if path == "index.html" {
			urlPath = "/"
		}
		contentType := contentTypeByExt(path)
		s.srv.AddHandler(http.MethodGet, urlPath, func(ctx httpsvr.Context) {
			ctx.Writer.Header().Set("Content-Type", contentType)
			ctx.Writer.WriteHeader(http.StatusOK)
			_, _ = ctx.Writer.Write(data)
		})
		return nil
	})
}

// contentTypeByExt 根据文件扩展名返回 Content-Type（用于内嵌 WebUI 静态资源）。
func contentTypeByExt(path string) string {
	switch {
	case strings.HasSuffix(path, ".html"), strings.HasSuffix(path, ".htm"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(path, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(path, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(path, ".json"):
		return "application/json"
	case strings.HasSuffix(path, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(path, ".png"):
		return "image/png"
	case strings.HasSuffix(path, ".jpg"), strings.HasSuffix(path, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(path, ".gif"):
		return "image/gif"
	case strings.HasSuffix(path, ".webp"):
		return "image/webp"
	default:
		return "text/plain; charset=utf-8"
	}
}

// requireAuth 返回构造时的鉴权包装器（§8.3/§8.4）。
// 委托 adminAuth.check 完成：回环信任 → 公开路径豁免 → 静态 token → 登录 JWT。
func (s *AdminServer) requireAuth() func(func(httpsvr.Context)) func(httpsvr.Context) {
	return func(next func(httpsvr.Context)) func(httpsvr.Context) {
		return func(ctx httpsvr.Context) {
			if !s.auth.check(ctx) {
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

// handleConfigList 查看全部已注册配置项元数据（底座 + 各挂件，供 WebUI 分组展示）。
func (s *AdminServer) handleConfigList(ctx httpsvr.Context) {
	_ = writeJSON(ctx.Writer, s.confMgr.List(), http.StatusOK)
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
