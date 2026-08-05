// Copyright © 管理接口认证端点：状态/注册/登录/重置。
//
// 流程：
//   - 全新系统（无用户）→ /admin/auth/register 首次注册，成功后置 ADMIN_INITIALIZED=true；
//   - 正常使用 → /admin/auth/login 校验用户名+密码，签发登录 JWT；
//   - 忘记密码 → 运维把 .env 中 ADMIN_INITIALIZED 改为 false → 系统进入重置模式 →
//     /admin/auth/reset 重设用户名与密码 → 恢复 ADMIN_INITIALIZED=true。
//
// 安全：登录接口按 IP 限流（5 分钟窗口内失败 5 次锁定 5 分钟）；密码只存 PBKDF2 哈希。
package adminapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/iotames/easyserver/httpsvr"
)

// 登录限流参数。
const (
	loginMaxFailures = 5               // 窗口内最大失败次数
	loginWindow      = 5 * time.Minute // 失败计数窗口
	loginLockout     = 5 * time.Minute // 锁定时间
	minPasswordLen   = 8               // 密码最小长度
)

// loginLimiter 登录失败限流器（按 IP，进程内内存态）。
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string]*failState
}

// failState 单个 IP 的失败计数状态。
type failState struct {
	count    int
	windowAt time.Time
}

// newLoginLimiter 构造限流器。
func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: make(map[string]*failState)}
}

// allow 返回当前 IP 是否允许继续尝试登录。
func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.failures[ip]
	if !ok {
		return true
	}
	if time.Since(st.windowAt) > loginLockout {
		delete(l.failures, ip)
		return true
	}
	return st.count < loginMaxFailures
}

// recordFailure 记录一次登录失败。
func (l *loginLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.failures[ip]
	if !ok || time.Since(st.windowAt) > loginWindow {
		st = &failState{count: 0, windowAt: time.Now()}
		l.failures[ip] = st
	}
	st.count++
}

// reset 清除指定 IP 的失败计数（登录成功后调用）。
func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

// clientIP 从请求中提取客户端 IP（RemoteAddr 的 host 部分）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// handleAuthStatus 返回管理接口认证状态（WebUI 启动引导用）。
func (s *AdminServer) handleAuthStatus(ctx httpsvr.Context) {
	username := ""
	if u, _ := s.users.get(); u != nil {
		username = u.Username
	}
	_ = writeJSON(ctx.Writer, map[string]any{
		"auth_required": s.auth.authRequired(),
		"has_user":      s.auth.hasUser(),
		"username":      username,
		"setup_mode":    s.auth.setupMode(),
	}, http.StatusOK)
}

// handleRegister 首次注册超级管理员（仅未初始化时开放）。
func (s *AdminServer) handleRegister(ctx httpsvr.Context) {
	if s.auth.hasUser() {
		_ = ctx.Json(map[string]any{"ok": false, "error": "系统已初始化，禁止重复注册"}, http.StatusForbidden)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := ctx.GetPostJson(&body); err != nil {
		_ = ctx.Json(map[string]any{"ok": false, "error": "invalid body"}, http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(body.Username)
	if username == "" || len(body.Password) < minPasswordLen {
		_ = ctx.Json(map[string]any{"ok": false, "error": "需要用户名与至少 8 位密码"}, http.StatusBadRequest)
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		_ = ctx.Json(map[string]any{"ok": false, "error": "密码哈希失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}
	if err := s.users.save(username, hash); err != nil {
		_ = ctx.Json(map[string]any{"ok": false, "error": "保存用户失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}
	s.markInitialized(true)
	_ = ctx.Json(map[string]any{"ok": true}, http.StatusOK)
}

// handleLogin 登录：校验用户名+密码，成功签发登录 JWT。
func (s *AdminServer) handleLogin(ctx httpsvr.Context) {
	if !s.auth.hasUser() {
		_ = ctx.Json(map[string]any{"ok": false, "error": "系统尚未初始化，请先完成注册"}, http.StatusBadRequest)
		return
	}
	ip := clientIP(ctx.Request)
	if !s.loginLimiter.allow(ip) {
		_ = ctx.Json(map[string]any{"ok": false, "error": "登录尝试过于频繁，请稍后再试"}, http.StatusTooManyRequests)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := ctx.GetPostJson(&body); err != nil {
		_ = ctx.Json(map[string]any{"ok": false, "error": "invalid body"}, http.StatusBadRequest)
		return
	}
	u, err := s.users.getByUsername(strings.TrimSpace(body.Username))
	if err != nil || u == nil || !checkPassword(body.Password, u.PasswordHash) {
		s.loginLimiter.recordFailure(ip)
		_ = ctx.Json(map[string]any{"ok": false, "error": "用户名或密码错误"}, http.StatusUnauthorized)
		return
	}
	s.loginLimiter.reset(ip)
	token, err := s.auth.issueToken(u.Username)
	if err != nil {
		_ = ctx.Json(map[string]any{"ok": false, "error": "签发 token 失败"}, http.StatusInternalServerError)
		return
	}
	_ = ctx.Json(map[string]any{
		"ok":         true,
		"token":      token,
		"expires_in": int(jwtTTL.Seconds()),
	}, http.StatusOK)
}

// handleReset 重置管理员凭证（忘记密码：运维已将 ADMIN_INITIALIZED 改为 false）。
func (s *AdminServer) handleReset(ctx httpsvr.Context) {
	if !s.auth.setupMode() {
		_ = ctx.Json(map[string]any{"ok": false, "error": "未处于重置模式（需在 .env 中将 ADMIN_INITIALIZED 改为 false）"}, http.StatusForbidden)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := ctx.GetPostJson(&body); err != nil {
		_ = ctx.Json(map[string]any{"ok": false, "error": "invalid body"}, http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(body.Username)
	if username == "" || len(body.Password) < minPasswordLen {
		_ = ctx.Json(map[string]any{"ok": false, "error": "需要用户名与至少 8 位密码"}, http.StatusBadRequest)
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		_ = ctx.Json(map[string]any{"ok": false, "error": "密码哈希失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}
	if err := s.users.save(username, hash); err != nil {
		_ = ctx.Json(map[string]any{"ok": false, "error": "保存用户失败: " + err.Error()}, http.StatusInternalServerError)
		return
	}
	s.markInitialized(true)
	_ = ctx.Json(map[string]any{"ok": true}, http.StatusOK)
}

// markInitialized 更新 ADMIN_INITIALIZED 配置（注册/重置成功后置 true）。
// confMgr 为 nil（测试）或写入失败时仅记录，不阻断（热更已生效）。
func (s *AdminServer) markInitialized(v bool) {
	if s.confMgr == nil || s.initialized == nil {
		return
	}
	val := "false"
	if v {
		val = "true"
	}
	_ = s.confMgr.Set("ADMIN_INITIALIZED", val)
}
