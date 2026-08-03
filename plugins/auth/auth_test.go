package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/iotames/easyserver/httpsvr"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/dataflow"
)

// fakeConfMgr 测试用假配置管理器：仅记录注册项，不触发真实重载。
type fakeConfMgr struct {
	regs map[string]any
}

func newFakeConf() *fakeConfMgr { return &fakeConfMgr{regs: make(map[string]any)} }

func (f *fakeConfMgr) Current() *conf.Config          { return nil }
func (f *fakeConfMgr) Watch(func(*conf.Config))       {}
func (f *fakeConfMgr) StartWatcher() error            { return nil }
func (f *fakeConfMgr) Shutdown(context.Context) error { return nil }
func (f *fakeConfMgr) Register(pval any, name, defval, title string, usage ...string) error {
	f.regs[name] = pval
	return nil
}
func (f *fakeConfMgr) Set(name, value string) error { return nil }

// signToken 生成合法 HS256 JWT（测试辅助）。
func signToken(t *testing.T, secret, issuer string, ttl time.Duration, tenantID, userID string) string {
	t.Helper()
	now := time.Now()
	claims := Claims{
		TenantID: tenantID,
		UserID:   userID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("签发 JWT 失败: %v", err)
	}
	return s
}

// newCtx 构造带 Authorization 头的请求上下文。
func newCtx(authz string) (*chain.Context, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	w := httptest.NewRecorder()
	df := dataflow.New(httpsvr.NewDataFlow(), r)
	return &chain.Context{W: w, R: r, DF: df}, w
}

// New 应注册 §16 全部配置项；Name/Slot/Stop 符合接口约定。
func TestNewRegistersConfig(t *testing.T) {
	f := newFakeConf()
	var cm conf.Manager = f
	a := New(&cm)
	for _, n := range []string{
		"AUTH_ENABLED", "AUTH_JWT_SECRET", "AUTH_JWT_ISSUER", "AUTH_JWT_TTL",
	} {
		if _, ok := f.regs[n]; !ok {
			t.Errorf("应注册配置项 %s", n)
		}
	}
	if a.Name() != "auth" {
		t.Errorf("Name 应为 auth，实际 %q", a.Name())
	}
	if a.Slot() != chain.Head {
		t.Errorf("Slot 应为 Head，实际 %v", a.Slot())
	}
	if err := a.Stop(); err != nil {
		t.Errorf("Stop 应返回 nil，实际 %v", err)
	}
}

// 合法令牌 → 放行，DataFlow.TenantID 被写入，响应不写（Code 保持 200）。
func TestValidTokenPasses(t *testing.T) {
	f := newFakeConf()
	var cm conf.Manager = f
	a := New(&cm)
	a.enabled = true
	a.secret = "test-secret"
	a.issuer = "rocksys"
	a.ttlSec = 3600
	if err := a.Start(nil); err != nil {
		t.Fatal(err)
	}

	tok := signToken(t, "test-secret", "rocksys", time.Hour, "tenant-1", "user-1")
	ctx, w := newCtx("Bearer " + tok)
	if next := a.Handle(ctx); !next {
		t.Fatal("合法令牌应放行（Handle 返回 true）")
	}
	if got := ctx.DF.TenantID(); got != "tenant-1" {
		t.Errorf("DataFlow.TenantID 应为 tenant-1，实际 %q", got)
	}
	if w.Code != http.StatusOK {
		t.Errorf("放行时不应写响应，Code 应为默认 200，实际 %d", w.Code)
	}
}

// 密钥错误/被篡改 → 401，Handle 返回 false。
func TestInvalidToken401(t *testing.T) {
	f := newFakeConf()
	var cm conf.Manager = f
	a := New(&cm)
	a.enabled = true
	a.secret = "real-secret"
	a.issuer = "rocksys"
	a.ttlSec = 3600
	if err := a.Start(nil); err != nil {
		t.Fatal(err)
	}

	tok := signToken(t, "wrong-secret", "rocksys", time.Hour, "tenant-1", "user-1")
	ctx, w := newCtx("Bearer " + tok)
	if next := a.Handle(ctx); next {
		t.Fatal("伪造签名的令牌应被拒绝（Handle 返回 false）")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("应返回 401，实际 %d", w.Code)
	}
}

// 过期令牌 → 401。
func TestExpiredToken401(t *testing.T) {
	f := newFakeConf()
	var cm conf.Manager = f
	a := New(&cm)
	a.enabled = true
	a.secret = "test-secret"
	a.issuer = "rocksys"
	a.ttlSec = 3600
	if err := a.Start(nil); err != nil {
		t.Fatal(err)
	}

	tok := signToken(t, "test-secret", "rocksys", -time.Hour, "tenant-1", "user-1")
	ctx, w := newCtx("Bearer " + tok)
	if next := a.Handle(ctx); next {
		t.Fatal("过期令牌应被拒绝（Handle 返回 false）")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("应返回 401，实际 %d", w.Code)
	}
}

// issuer 不匹配 → 401。
func TestWrongIssuer401(t *testing.T) {
	f := newFakeConf()
	var cm conf.Manager = f
	a := New(&cm)
	a.enabled = true
	a.secret = "test-secret"
	a.issuer = "rocksys"
	a.ttlSec = 3600
	if err := a.Start(nil); err != nil {
		t.Fatal(err)
	}

	tok := signToken(t, "test-secret", "evil-issuer", time.Hour, "tenant-1", "user-1")
	ctx, w := newCtx("Bearer " + tok)
	if next := a.Handle(ctx); next {
		t.Fatal("issuer 不匹配的令牌应被拒绝")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("应返回 401，实际 %d", w.Code)
	}
}

// 无 token / 非法 Bearer 格式 → 401。
func TestNoToken401(t *testing.T) {
	f := newFakeConf()
	var cm conf.Manager = f
	a := New(&cm)
	a.enabled = true
	a.secret = "test-secret"
	a.issuer = "rocksys"
	a.ttlSec = 3600
	if err := a.Start(nil); err != nil {
		t.Fatal(err)
	}

	for _, authz := range []string{"", "Basic dXNlcjpwYXNz", "Bearer "} {
		ctx, w := newCtx(authz)
		if next := a.Handle(ctx); next {
			t.Fatalf("Authorization=%q 应被拒绝（Handle 返回 false）", authz)
		}
		if w.Code != http.StatusUnauthorized {
			t.Errorf("Authorization=%q 应返回 401，实际 %d", authz, w.Code)
		}
	}
}

// AUTH_ENABLED=false → 无 token 也直通，不写 tenant。
func TestDisabledPassThrough(t *testing.T) {
	f := newFakeConf()
	var cm conf.Manager = f
	a := New(&cm)
	a.enabled = false
	a.secret = "test-secret"
	a.issuer = "rocksys"
	a.ttlSec = 3600
	if err := a.Start(nil); err != nil {
		t.Fatal(err)
	}

	ctx, w := newCtx("")
	if next := a.Handle(ctx); !next {
		t.Fatal("disabled 时应直通（Handle 返回 true）")
	}
	if got := ctx.DF.TenantID(); got != "" {
		t.Errorf("disabled 时不应写入 TenantID，实际 %q", got)
	}
	if w.Code != http.StatusOK {
		t.Errorf("直通时不应写响应，Code 应为默认 200，实际 %d", w.Code)
	}
}

// Verifier 密钥轮换：旧密钥令牌失效、新密钥令牌有效。
func TestVerifierRotation(t *testing.T) {
	v := newVerifier("rocksys", "old-secret")
	oldTok := signToken(t, "old-secret", "rocksys", time.Hour, "tenant-1", "user-1")
	if _, err := v.Verify(oldTok); err != nil {
		t.Fatalf("轮换前旧密钥应校验通过: %v", err)
	}

	v.Rotate("new-secret")
	if _, err := v.Verify(oldTok); err == nil {
		t.Fatal("轮换后旧密钥令牌应校验失败")
	}
	newTok := signToken(t, "new-secret", "rocksys", time.Hour, "tenant-2", "user-2")
	c, err := v.Verify(newTok)
	if err != nil {
		t.Fatalf("轮换后新密钥令牌应校验通过: %v", err)
	}
	if c.TenantID != "tenant-2" {
		t.Errorf("新令牌 TenantID 应为 tenant-2，实际 %q", c.TenantID)
	}
}
