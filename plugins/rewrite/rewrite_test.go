// Package rewrite 单测：规则解析、URI/请求头改写、热更快照。
package rewrite

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"rocksys/internal/chain"
)

func TestNewImplementsInterfaces(t *testing.T) {
	var _ chain.Middleware = New(nil)
	if _, ok := chain.Middleware(New(nil)).(interface{ Slot() chain.Slot }); !ok {
		t.Fatal("rewrite 未实现 Slot()")
	}
}

func TestParseRules_Empty(t *testing.T) {
	rt, err := parseRules("")
	if err != nil {
		t.Fatalf("parseRules empty err: %v", err)
	}
	if len(rt.rules) != 0 {
		t.Errorf("空规则表应为空, got %d 条", len(rt.rules))
	}
}

func TestParseRules_Error(t *testing.T) {
	cases := []string{
		"/api/v1",                    // 缺少 =
		"api/v1/=uri|/api/",          // 前缀不以 / 开头
		"/api/v1/=",                  // 缺少 spec
		"/api/v1/=uri|api/",          // 改写前缀不以 / 开头
		"/api/v1/=header=no-colon",   // header 缺冒号
		"/api/v1/=unknown-action",    // 不支持的动作
	}
	for _, c := range cases {
		if _, err := parseRules(c); err == nil {
			t.Errorf("parseRules(%q) 应返回 error", c)
		}
	}
}

func TestParseRule_URIAndHeader(t *testing.T) {
	rt, err := parseRules("/api/v1/=uri|/api/;header=X-Proxy-Tag:rewrite")
	if err != nil {
		t.Fatalf("parseRules err: %v", err)
	}
	if len(rt.rules) != 1 {
		t.Fatalf("应解析 1 条规则, got %d", len(rt.rules))
	}
	r := rt.rules[0]
	if r.prefix != "/api/v1/" || r.rewriteURI != "/api/" {
		t.Errorf("规则解析错误: prefix=%q rewriteURI=%q", r.prefix, r.rewriteURI)
	}
	if r.headers["X-Proxy-Tag"] != "rewrite" {
		t.Errorf("header 解析错误: %v", r.headers)
	}
}

func TestHandle_RewriteURI(t *testing.T) {
	r := New(nil)
	r.rules = "/api/v1/=uri|/api/"
	if err := r.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/123", nil)
	ctx := &chain.Context{R: req}
	if !r.Handle(ctx) {
		t.Error("Handle 应返回 true（不中断链）")
	}
	if req.URL.Path != "/api/orders/123" {
		t.Errorf("URI 改写失败: %q, want /api/orders/123", req.URL.Path)
	}
}

func TestHandle_SetHeader(t *testing.T) {
	r := New(nil)
	r.rules = "/api/=uri|/api/[;header=X-Proxy-Tag:rewrite;header=X-Tenant:acme"
	if err := r.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	ctx := &chain.Context{R: req}
	r.Handle(ctx)
	if got := req.Header.Get("X-Proxy-Tag"); got != "rewrite" {
		t.Errorf("X-Proxy-Tag=%q, want rewrite", got)
	}
	if got := req.Header.Get("X-Tenant"); got != "acme" {
		t.Errorf("X-Tenant=%q, want acme", got)
	}
}

func TestHandle_NoMatch(t *testing.T) {
	r := New(nil)
	r.rules = "/api/v1/=uri|/api/"
	if err := r.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/other/path", nil)
	ctx := &chain.Context{R: req}
	r.Handle(ctx)
	if req.URL.Path != "/other/path" {
		t.Errorf("未命中不应改写, got %q", req.URL.Path)
	}
}

func TestStart_ErrorKeepsOldSnapshot(t *testing.T) {
	r := New(nil)
	r.rules = "/api/v1/=uri|/api/"
	if err := r.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	old := r.rt.Load().(*rewriteTable)
	if len(old.rules) == 0 {
		t.Fatal("初始 Start 后快照不应为空")
	}
	r.rules = "/bad-rule"
	if err := r.Start(nil); err == nil {
		t.Fatal("Start 非法配置应返回 error")
	}
	if got := r.rt.Load().(*rewriteTable); got != old {
		t.Error("Start 失败后不应替换旧快照")
	}
}