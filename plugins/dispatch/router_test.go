// Package dispatch Radix Tree 路由引擎专项测试：参数捕获、通配、优先级与 DataFlow 注入。
package dispatch

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iotames/easyserver/httpsvr"

	"rocksys/internal/chain"
	"rocksys/internal/dataflow"
)

func TestRouter_ParamCapture(t *testing.T) {
	rt := mustRT(t, "/api/order/:id=http://order-svc:9001")
	rule, params := rt.MatchParams("/api/order/123")
	if rule == nil {
		t.Fatal("应命中参数路由")
	}
	if rule.Prefix != "/api/order/:id" {
		t.Errorf("命中规则应为 :id 路由, got %q", rule.Prefix)
	}
	if params["id"] != "123" {
		t.Errorf("参数 id=%q, want 123", params["id"])
	}
}

func TestRouter_ParamCapture_Multi(t *testing.T) {
	rt := mustRT(t, "/api/:ver/users/:uid=http://user-svc:9001")
	rule, params := rt.MatchParams("/api/v2/users/42")
	if rule == nil {
		t.Fatal("应命中多参数路由")
	}
	if params["ver"] != "v2" || params["uid"] != "42" {
		t.Errorf("多参数捕获错误: ver=%q uid=%q", params["ver"], params["uid"])
	}
}

func TestRouter_Wildcard(t *testing.T) {
	rt := mustRT(t, "/api/*=http://api-svc:9000")
	if up, ok := matchUp(rt, "/api/anything/deep/nested"); !ok || up != "http://api-svc:9000" {
		t.Errorf("通配匹配失败, got (%q, %v)", up, ok)
	}
}

func TestRouter_ParamBeatsPrefix(t *testing.T) {
	// 参数路由比纯前缀更具体：/api/order/123 应命中 :id 而非纯前缀。
	rt := mustRT(t, "/api/order/=http://order-svc:9001,/api/order/:id=http://order-detail:9002")
	rule, params := rt.MatchParams("/api/order/123")
	if rule == nil {
		t.Fatal("应命中")
	}
	if rule.Prefix != "/api/order/:id" {
		t.Errorf("应命中参数路由, got %q", rule.Prefix)
	}
	if params["id"] != "123" {
		t.Errorf("参数 id=%q, want 123", params["id"])
	}
	// 统一前缀语义 + 最长匹配：多段路径命中更具体的 :id 规则（段数更多 = 更具体）。
	if up, ok := matchUp(rt, "/api/order/123/items"); !ok || up != "http://order-detail:9002" {
		t.Errorf("多段路径应命中更具体的 :id 规则, got (%q, %v)", up, ok)
	}
}

func TestRouter_LongestParam(t *testing.T) {
	// 更深路径的参数路由优先于更浅的。
	rt := mustRT(t, "/api/:ver=http://generic:9000,/api/:ver/users/:uid=http://user:9001")
	rule, params := rt.MatchParams("/api/v1/users/7")
	if rule == nil || rule.Prefix != "/api/:ver/users/:uid" {
		t.Fatalf("应命中更深参数路由, got %v", rule)
	}
	if params["ver"] != "v1" || params["uid"] != "7" {
		t.Errorf("参数错误: %v", params)
	}
	if up, ok := matchUp(rt, "/api/v1/orders"); !ok || up != "http://generic:9000" {
		t.Errorf("浅路径应命中通用路由, got (%q, %v)", up, ok)
	}
}

func TestRouter_Miss(t *testing.T) {
	rt := mustRT(t, "/api/order/:id=http://order-svc:9001,/api/*=http://api-svc:9000")
	// 不匹配任何规则：非 /api 前缀。
	rule, _ := rt.MatchParams("/other/path")
	if rule != nil {
		t.Errorf("未命中应返回 nil, got %v", rule.Prefix)
	}
}

func TestRouter_BackwardCompat_OldPrefix(t *testing.T) {
	// 旧格式纯前缀（无 :/*）经路由引擎匹配语义不变。
	rt := mustRT(t, "/api/order/=http://order-svc:9001,/api/=http://api-svc:9000")
	if up, ok := matchUp(rt, "/api/order/123"); !ok || up != "http://order-svc:9001" {
		t.Errorf("最长前缀优先失败, got (%q, %v)", up, ok)
	}
	if up, ok := matchUp(rt, "/api/user/1"); !ok || up != "http://api-svc:9000" {
		t.Errorf("短前缀命中失败, got (%q, %v)", up, ok)
	}
	// 前缀边界：/api/ordering 命中 /api/（旧前缀语义），但不命中更长的 /api/order/。
	if up, ok := matchUp(rt, "/api/ordering/list"); !ok || up != "http://api-svc:9000" {
		t.Errorf("前缀边界应命中 /api/, got (%q, %v)", up, ok)
	}
}

func TestRouter_CatchAllRoot(t *testing.T) {
	rt := mustRT(t, "/=http://default-svc,/api/order/:id=http://order-svc:9001")
	if up, ok := matchUp(rt, "/zz/other"); !ok || up != "http://default-svc" {
		t.Errorf("根兜底失败, got (%q, %v)", up, ok)
	}
	if up, ok := matchUp(rt, "/api/order/9"); !ok || up != "http://order-svc:9001" {
		t.Errorf("参数路由应优先于根兜底, got (%q, %v)", up, ok)
	}
}

func TestHandle_ParamInject(t *testing.T) {
	d := New(nil)
	d.rules = "/api/order/:id=http://order-svc:9001"
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/order/123", nil)
	df := dataflow.New(httpsvr.NewDataFlow(), req)
	ctx := &chain.Context{R: req, DF: df}
	if !d.Handle(ctx) {
		t.Error("Handle 应返回 true（不中断链）")
	}
	// 参数注入 DataFlow。
	v, ok := df.Get(keyPathParams)
	if !ok {
		t.Fatal("DataFlow 应存有 path_params")
	}
	if pm, ok := v.(map[string]string); !ok || pm["id"] != "123" {
		t.Errorf("DataFlow path_params 错误: %v", v)
	}
	// 参数注入请求头（透传上游）。
	if got := req.Header.Get("X-Route-Param-id"); got != "123" {
		t.Errorf("X-Route-Param-id=%q, want 123", got)
	}
	// Target 正确设置。
	if got := df.Target(); got != "http://order-svc:9001" {
		t.Errorf("Target()=%q, want order-svc", got)
	}
}
