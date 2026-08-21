package auth

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iotames/easyserver/log"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/hotswap"
	"rocksys/internal/jwtutil"
)

// JWTConfig JWT 认证配置（运行态快照内容，§16）。
type JWTConfig struct {
	Issuer string        // 签发方
	Secret string        // 签名密钥
	TTL    time.Duration // 令牌有效期
}

// Verifier JWT 验签器：密钥缓存 + 轮换（atomic.Value 原子替换，§16）。
type Verifier struct {
	issuer string
	key    atomic.Value // 持 []byte：当前生效密钥
}

// newVerifier 以指定 issuer/secret 构建验签器。
func newVerifier(issuer, secret string) *Verifier {
	v := &Verifier{issuer: issuer}
	v.key.Store([]byte(secret))
	return v
}

// Rotate 轮换签名密钥（原子替换，不影响在途请求校验）。
func (v *Verifier) Rotate(secret string) {
	v.key.Store([]byte(secret))
}

// Verify 解析并校验 JWT：验签（HS256）、issuer、过期时间。
// 成功返回载荷 map（含 tenant_id/user_id），供上层提取写入 DataFlow。
func (v *Verifier) Verify(token string) (map[string]interface{}, error) {
	secret, _ := v.key.Load().([]byte)
	return jwtutil.Verify(secret, token, v.issuer)
}

// authSnapshot 不可变运行态快照（整体重建后原子替换，§6.2/§6.3）。
type authSnapshot struct {
	verifier *Verifier
	ttl      time.Duration
}

// Auth JWT 认证/租户识别中间件（L1 阶段，挂 Head 槽位）。
//
// 运行态全部存于不可变快照（atomic.Value 承载），Start 整体重建后原子替换，
// 保证与在途请求的 Handle 并发安全。
type Auth struct {
	cfg *conf.Manager

	// 配置项挂件字段（构造时由 cfgMgr.Register 注册，Start 时读取重建快照）。
	// ★ AUTH_ENABLED 是挂载开关（配置中心唯一真源）：挂载即认证，内部不再读取本字段。
	enabled bool
	secret  string
	issuer  string
	ttlSec  int

	snapshot atomic.Value // *authSnapshot
}

// New 构造 Auth 并注册全部配置项（§16）。
func New(cfg *conf.Manager) *Auth {
	a := &Auth{cfg: cfg}
	if cfg == nil {
		return a
	}
	items := []struct {
		pval   any
		name   string
		defval string
		title  string
	}{
		{&a.enabled, "AUTH_ENABLED", "false", "是否启用 JWT 认证（false=不挂载；true=挂载并认证）"},
		{&a.secret, "AUTH_JWT_SECRET", "", "JWT 签名密钥"},
		{&a.issuer, "AUTH_JWT_ISSUER", "rocksys", "JWT 签发方"},
		{&a.ttlSec, "AUTH_JWT_TTL", "3600", "JWT 有效期(秒)"},
	}
	for _, it := range items {
		if err := (*cfg).Register(it.pval, it.name, it.defval, it.title); err != nil {
			log.Warn("auth: 注册配置项失败", "name", it.name, "err", err)
		}
	}
	return a
}

// Name 返回中间件名称。
func (a *Auth) Name() string { return "auth" }

// Slot 挂载位置：L1 认证在转发前最先执行。
func (a *Auth) Slot() chain.Slot { return chain.Head }

// Start 从配置项字段读取最新值，重建不可变快照并原子替换。
func (a *Auth) Start(_ any) error {
	snap := &authSnapshot{
		verifier: newVerifier(a.issuer, a.secret),
		ttl:      time.Duration(a.ttlSec) * time.Second,
	}
	a.snapshot.Store(snap)
	return nil
}

// Stop 清理资源（本挂件无特别资源）。
func (a *Auth) Stop() error { return nil }

func (a *Auth) current() *authSnapshot {
	if v := a.snapshot.Load(); v != nil {
		return v.(*authSnapshot)
	}
	return nil
}

// Handle 处理请求（§16 核心流程）：
// 校验 Authorization: Bearer <token> → 解析 tenant_id/user_id → 写入 DataFlow → 放行；
// 无 token 或校验失败返回 401。返回 true 表示继续转发链，false 表示已写响应并中断。
func (a *Auth) Handle(ctx *chain.Context) (next bool) {
	snap := a.current()
	if snap == nil {
		return true
	}
	token := bearerToken(ctx.R)
	if token == "" {
		http.Error(ctx.W, "unauthorized", http.StatusUnauthorized)
		return false
	}
	claims, err := snap.verifier.Verify(token)
	if err != nil {
		http.Error(ctx.W, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if tenant, _ := claims["tenant_id"].(string); tenant != "" {
		ctx.DF.SetTenantID(tenant)
	}
	return true
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

// 编译期断言：Auth 满足 hotswap.MiddlewareLifecycle。
var _ hotswap.MiddlewareLifecycle = (*Auth)(nil)
