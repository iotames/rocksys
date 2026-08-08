// Copyright © 管理接口鉴权器：回环信任 + 静态 token 双轨 + 登录 JWT。
//
// 鉴权策略（优先级从高到低）：
//  1. 回环信任：绑定 127.0.0.1/localhost 且未配置静态 token 时放行（本机免登录）；
//  2. 公开路径：/admin/auth/status|login|register|reset 免鉴权（handler 内部校验前置条件）；
//  3. 静态预共享 token（ROCKSYS_ADMIN_TOKEN，供 rockctl/脚本使用）；
//  4. 已初始化 → 校验登录 JWT；未初始化 → 拒绝（仅注册引导可用）。
package adminapi

import (
	"crypto/rand"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/iotames/easyserver/httpsvr"

	"rocksys/internal/conf"
	"rocksys/internal/jwtutil"
)

// jwtIssuer 管理接口登录 JWT 的签发方标识。
const jwtIssuer = "rocksys-admin"

// jwtTTL 管理接口登录 JWT 有效期。
const jwtTTL = 12 * time.Hour

// publicAdminPaths 免鉴权公开路径（均为 /admin/auth/*，前置条件由各 handler 校验）。
var publicAdminPaths = map[string]bool{
	PathAuthStatus: true,
	PathLogin:      true,
	PathRegister:   true,
	PathReset:      true,
}

// adminAuth 管理接口鉴权器。
type adminAuth struct {
	confMgr      conf.Manager // 读取 AdminAddr（动态判断是否回环）
	initialized  *bool        // ADMIN_INITIALIZED 配置指针（热更可读）
	jwtSecret    *string      // ADMIN_JWT_SECRET 配置指针
	token        *string      // ROCKSYS_ADMIN_TOKEN 配置指针（静态预共享令牌，可热更）
	users        *userStore   // 用户存储（判断是否已初始化）
	addrFallback string       // confMgr 为 nil 时的监听地址（New 传入，测试/降级场景）
	issuer       string
	ttl          time.Duration
	ephemeralKey []byte // 未配置 ADMIN_JWT_SECRET 时进程内随机密钥（重启后登录态失效）
}

// newAdminAuth 构造鉴权器。confMgr/users 可为 nil（单元测试场景），addr 为监听地址回退。
func newAdminAuth(confMgr conf.Manager, initialized *bool, jwtSecret, token *string, users *userStore, addr string) *adminAuth {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &adminAuth{
		confMgr:      confMgr,
		initialized:  initialized,
		jwtSecret:    jwtSecret,
		token:        token,
		users:        users,
		addrFallback: addr,
		issuer:       jwtIssuer,
		ttl:          jwtTTL,
		ephemeralKey: key,
	}
}

// check 校验请求是否通过鉴权。返回 false 表示已写 401 拒绝响应。
func (a *adminAuth) check(ctx httpsvr.Context) bool {
	// 回环信任：本机免登录（未配置静态 token 时）
	if !a.authRequired() {
		return true
	}
	// 公开路径：登录/注册/重置/状态
	if isPublicAdminPath(ctx.Request.URL.Path) {
		return true
	}
	// 静态预共享 token（ROCKSYS_ADMIN_TOKEN，rockctl/脚本用；经配置中心注册，可热更）
	if tok := a.staticToken(); tok != "" {
		if !secureCompare(bearerToken(ctx.Request), tok) {
			return a.writeUnauthorized(ctx)
		}
		return true
	}
	// 已初始化：校验登录 JWT
	if a.hasUser() {
		token := bearerToken(ctx.Request)
		if token == "" {
			return a.writeUnauthorized(ctx)
		}
		if _, err := jwtutil.Verify(a.secret(), token, a.issuer); err != nil {
			return a.writeUnauthorized(ctx)
		}
		return true
	}
	// 未初始化：拒绝（仅公开路径可用）
	return a.writeUnauthorized(ctx)
}

// authRequired 返回当前是否需要登录：绑定非回环地址 或 配置了静态 token。
func (a *adminAuth) authRequired() bool {
	if a.staticToken() != "" {
		return true
	}
	addr := a.addrFallback
	if a.confMgr != nil {
		addr = a.confMgr.Current().AdminAddr
	}
	return !isLoopbackAddr(addr)
}

// hasUser 返回是否已注册管理员（数据库存在用户即视为已初始化）。
func (a *adminAuth) hasUser() bool {
	if a.users == nil {
		return false
	}
	n, err := a.users.count()
	return err == nil && n > 0
}

// setupMode 返回是否处于重置/设置模式（配置 ADMIN_INITIALIZED=false 且已有用户）。
func (a *adminAuth) setupMode() bool {
	if a.initialized != nil && *a.initialized {
		return false
	}
	return a.hasUser()
}

// staticToken 返回静态预共享令牌（经配置中心注册的 ROCKSYS_ADMIN_TOKEN，可热更）。
// token 指针为 nil（confMgr 未就绪/测试场景）或值为空时视为未配置。
func (a *adminAuth) staticToken() string {
	if a.token != nil {
		return strings.TrimSpace(*a.token)
	}
	return ""
}

// secret 返回 JWT 签名密钥：优先配置项，否则进程内随机密钥。
func (a *adminAuth) secret() []byte {
	if a.jwtSecret != nil && strings.TrimSpace(*a.jwtSecret) != "" {
		return []byte(*a.jwtSecret)
	}
	return a.ephemeralKey
}

// issueToken 签发管理接口登录 JWT。
func (a *adminAuth) issueToken(username string) (string, error) {
	claims := map[string]interface{}{
		"iss":      a.issuer,
		"sub":      "admin",
		"username": username,
	}
	return jwtutil.Sign(a.secret(), claims, a.ttl)
}

// writeUnauthorized 写 401 响应。
func (a *adminAuth) writeUnauthorized(ctx httpsvr.Context) bool {
	_ = ctx.Text("unauthorized", 401)
	return false
}

// isPublicAdminPath 判断路径是否为免鉴权公开路径。
func isPublicAdminPath(path string) bool {
	return publicAdminPaths[path]
}

// secureCompare 常量时间比较两个字符串是否相等。
func secureCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// bearerToken 从 Authorization 头解析 "Bearer <token>"，非法格式返回空串。
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// isLoopbackAddr 判断监听地址是否仅回环（127.0.0.1/localhost/::1）。
// 空 host（如 ":19527"）表示监听所有网卡，视为非回环。
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}
