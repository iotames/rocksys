package shield

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
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
func (f *fakeConfMgr) List() []conf.ConfigItem      { return nil }
func (f *fakeConfMgr) SyncDefaultFile() error       { return nil }

func newTestShield(t *testing.T) (*Shield, *fakeConfMgr) {
	t.Helper()
	f := newFakeConf()
	s, err := New(f)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, f
}

func newCtx(path, remoteAddr, ua string) (*chain.Context, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = remoteAddr
	if ua != "" {
		r.Header.Set("User-Agent", ua)
	}
	w := httptest.NewRecorder()
	return &chain.Context{W: w, R: r}, w
}

// New 应注册 §9.4 全部配置项；Name/Slot/Stop 符合接口约定。
func TestNewRegistersConfig(t *testing.T) {
	s, f := newTestShield(t)
	for _, n := range []string{
		"SHIELD_ENABLED", "SHIELD_IP_BLACKLIST", "SHIELD_IP_WHITELIST",
		"SHIELD_RATE_LIMIT_RPS", "SHIELD_RATE_LIMIT_BURST", "SHIELD_RATE_LIMIT_BY",
		"SHIELD_WAF_SQL_INJECTION", "SHIELD_WAF_XSS", "SHIELD_WAF_PATH_TRAVERSAL",
		"SHIELD_WAF_RISK_PATH", "SHIELD_WAF_CRAWLER_UA", "SHIELD_WAF_RISK_PATHS",
		"SHIELD_ALLOW_METHODS", "SHIELD_MAX_BODY_SIZE", "SHIELD_RULES_DIR",
	} {
		if _, ok := f.regs[n]; !ok {
			t.Errorf("应注册配置项 %s", n)
		}
	}
	if s.Name() != "shield" {
		t.Errorf("Name 应为 shield，实际 %q", s.Name())
	}
	if s.Slot() != chain.Head {
		t.Errorf("Slot 应为 Head，实际 %v", s.Slot())
	}
	if err := s.Stop(); err != nil {
		t.Errorf("Stop 应返回 nil，实际 %v", err)
	}
}

// 黑名单 IP（精确 + CIDR）→ 403，Handle 返回 false。
func TestBlacklistIP(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.ipBlacklist = "192.168.1.100,10.0.0.0/8"
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"192.168.1.100:1234", "10.1.2.3:80"} {
		ctx, w := newCtx("/api/test", ip, "")
		if next := s.Handle(ctx); next {
			t.Errorf("IP %s 应被拦截（Handle 返回 false），实际放行", ip)
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("IP %s 应返回 403，实际 %d", ip, w.Code)
		}
		if w.Body.Len() == 0 {
			t.Errorf("IP %s 被拦截时应已写入响应体", ip)
		}
	}
}

// 白名单 IP 放行，且放行时不写响应。
func TestWhitelistPasses(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.ipWhitelist = "127.0.0.1"
	s.ipBlacklist = "192.168.1.100"
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	ctx, w := newCtx("/api/test", "127.0.0.1:9999", "")
	if next := s.Handle(ctx); !next {
		t.Fatal("白名单 IP 应放行（Handle 返回 true）")
	}
	if w.Code != http.StatusOK {
		t.Errorf("放行时不应写响应，Code 应为默认 200，实际 %d", w.Code)
	}
	// 同 IP 同时命中白名单与黑名单时白名单优先；非白名单命中黑名单则拦截。
	ctx, w = newCtx("/api/test", "192.168.1.100:80", "")
	if next := s.Handle(ctx); next {
		t.Fatal("黑名单且非白名单 IP 应被拦截")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("应返回 403，实际 %d", w.Code)
	}
}

// 限流：rps=2 burst=2 时同一 key 前 2 个放行、第 3 个 429 + Retry-After。
func TestRateLimit(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.rps = 2
	s.burst = 2
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ctx, _ := newCtx("/api/test", "1.2.3.4:1000", "")
		if next := s.Handle(ctx); !next {
			t.Fatalf("第 %d 个请求应放行（burst=2）", i+1)
		}
	}
	ctx, w := newCtx("/api/test", "1.2.3.4:1000", "")
	if next := s.Handle(ctx); next {
		t.Fatal("第 3 个请求应被限流（Handle 返回 false）")
	}
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("限流应返回 429，实际 %d", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got == "" {
		t.Error("429 应携带 Retry-After 头")
	}
}

// 路径 deny 规则 → 403；未匹配路径放行。
func TestPathRuleDeny(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.SetPathRules([]PathRule{{Pattern: "/admin/*", Action: "deny"}})
	ctx, w := newCtx("/admin/delete", "1.2.3.4:80", "")
	if next := s.Handle(ctx); next {
		t.Fatal("deny 规则应拦截")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("deny 应返回 403，实际 %d", w.Code)
	}
	ctx, _ = newCtx("/api/test", "1.2.3.4:80", "")
	if next := s.Handle(ctx); !next {
		t.Fatal("未匹配规则应放行")
	}
}

// 路径 allow 规则 → 跳过限流；普通路径仍受限流约束。
func TestPathRuleAllowSkipsLimit(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.rps = 1
	s.burst = 1
	s.SetPathRules([]PathRule{{Pattern: "/internal/*", Action: "allow"}})
	for i := 0; i < 3; i++ {
		ctx, _ := newCtx("/internal/heartbeat", "1.2.3.4:80", "")
		if next := s.Handle(ctx); !next {
			t.Fatalf("allow 规则路径第 %d 次应跳过限流", i+1)
		}
	}
	ctx, _ := newCtx("/api/test", "1.2.3.4:80", "")
	if next := s.Handle(ctx); !next {
		t.Fatal("普通路径第 1 次应放行")
	}
	ctx, w := newCtx("/api/test", "1.2.3.4:80", "")
	if next := s.Handle(ctx); next {
		t.Fatal("普通路径第 2 次应 429")
	}
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("应返回 429，实际 %d", w.Code)
	}
}

// UA deny 规则 → 403。
func TestUARuleDeny(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.SetPathRules([]PathRule{{Pattern: "*Bot*", Action: "deny"}})
	ctx, w := newCtx("/api/test", "1.2.3.4:80", "MyBot/2.0")
	if next := s.Handle(ctx); next {
		t.Fatal("UA deny 规则应拦截")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("应返回 403，实际 %d", w.Code)
	}
}

// disabled（SHIELD_ENABLED=false）→ 全部直通，防护不生效。
func TestDisabledPassThrough(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = false
	s.ipBlacklist = "192.168.1.100"
	s.rps = 1
	s.burst = 1
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		ctx, _ := newCtx("/api/test", "192.168.1.100:80", "")
		if next := s.Handle(ctx); !next {
			t.Fatal("disabled 时应全部放行")
		}
	}
}

// glob 匹配单测。
func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"/admin/*", "/admin/delete", true},
		{"/admin/*", "/api/test", false},
		{"*Bot*", "MyBot/2.0", true},
		{"*Bot*", "curl/7.0", false},
		{"*", "", true},
		{"/a/?c", "/a/bc", true},
		{"/a/?c", "/a/bbc", false},
		{"exact", "exact", true},
		{"exact", "exactX", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pat, c.s); got != c.want {
			t.Errorf("matchGlob(%q,%q)=%v，期望 %v", c.pat, c.s, got, c.want)
		}
	}
}

// RateLimiter：同 key 独立桶、令牌按 rps 补充。
func TestRateLimiterAllow(t *testing.T) {
	rl := newRateLimiter(2, 2)
	if !rl.Allow("a") {
		t.Fatal("第 1 个请求应放行")
	}
	if !rl.Allow("a") {
		t.Fatal("第 2 个请求应放行")
	}
	if rl.Allow("a") {
		t.Fatal("第 3 个请求应被拒绝")
	}
	if !rl.Allow("b") {
		t.Fatal("不同 key 应使用独立桶")
	}
	if rl := newRateLimiter(0, 0); !rl.Allow("x") {
		t.Fatal("rps=0 时应不限流")
	}
}

// 桶数量超上限时按 LRU 淘汰，数量回落到上限以内。
func TestRateLimiterLRUCap(t *testing.T) {
	rl := newRateLimiter(1, 1)
	for i := 0; i < bucketsMax+200; i++ {
		rl.Allow(fmt.Sprintf("k%d", i))
	}
	if got := rl.count.Load(); got > bucketsMax {
		t.Errorf("桶数应不超过 %d，实际 %d", bucketsMax, got)
	}
}
