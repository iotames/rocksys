// Package dispatch 单测：路由表解析与前缀匹配算法（DEV_HANDBOOK.md §10.4）。
package dispatch

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iotames/easyserver/httpsvr"

	"rocksys/internal/chain"
	"rocksys/internal/dataflow"
)

func TestNewImplementsInterfaces(t *testing.T) {
	var _ chain.Middleware = New(nil)
	if _, ok := chain.Middleware(New(nil)).(interface{ Slot() chain.Slot }); !ok {
		t.Fatal("dispatch 未实现 Slot()")
	}
}

func TestRouteTable_Match_newline(t *testing.T) {
	tests := []struct {
		name     string
		rules    *RouteTable
		path     string
		wantUp   string
		wantOK   bool
	}{
		{name: "命中前缀", rules: mustRT(t, "/api/order/=http://order-svc:9001"),
			path: "/api/order/123", wantUp: "http://order-svc:9001", wantOK: true},
		{name: "path 末尾自动补斜杠", rules: mustRT(t, "/api/order/=http://order-svc:9001"),
			path: "/api/order", wantUp: "http://order-svc:9001", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up, ok := tt.rules.Match(tt.path)
			if ok != tt.wantOK || up != tt.wantUp {
				t.Errorf("Match(%q) = (%q, %v), want (%q, %v)", tt.path, up, ok, tt.wantUp, tt.wantOK)
			}
		})
	}
}

func TestRouteMatch(t *testing.T) {
	rt := mustRT(t, "/api/order/=http://order-svc:9001")

	// 命中
	if up, ok := rt.Match("/api/order/123"); !ok || up != "http://order-svc:9001" {
		t.Errorf("Match /api/order/123 = (%q, %v)", up, ok)
	}
	// 前缀边界：/api/ordering 不匹配 /api/order/
	if up, ok := rt.Match("/api/ordering/list"); ok {
		t.Errorf("Match /api/ordering/list 不应命中，got (%q, %v)", up, ok)
	}
	// 其他路径不命中
	if _, ok := rt.Match("/other/path"); ok {
		t.Error("Match /other/path 不应命中")
	}
}

func TestRouteMatch_LongestPrefix(t *testing.T) {
	rt := mustRT(t, "/api/=http://api-svc:9000,/api/order/=http://order-svc:9001")
	// 最长前缀优先
	if up, ok := rt.Match("/api/order/123"); !ok || up != "http://order-svc:9001" {
		t.Errorf("Match /api/order/123 = (%q, %v), want order-svc", up, ok)
	}
	// 较短前缀仍命中其对应路由
	if up, ok := rt.Match("/api/user/1"); !ok || up != "http://api-svc:9000" {
		t.Errorf("Match /api/user/1 = (%q, %v), want api-svc", up, ok)
	}
}

func TestRouteMatch_CatchAll(t *testing.T) {
	rt := mustRT(t, "/api/order/=http://order-svc:9001,/=http://default-svc")
	// "/" 兜底匹配未命中其他路由的路径
	if up, ok := rt.Match("/zz/other"); !ok || up != "http://default-svc" {
		t.Errorf("Match /zz/other = (%q, %v), want default-svc", up, ok)
	}
	// 已命中的更长前缀优先于兜底 "/"
	if up, ok := rt.Match("/api/order/9"); !ok || up != "http://order-svc:9001" {
		t.Errorf("Match /api/order/9 = (%q, %v), want order-svc", up, ok)
	}
}

func TestRouteMatch_Root(t *testing.T) {
	rt := mustRT(t, "/=http://default-svc")
	// 根路径 "/" 命中兜底路由
	if up, ok := rt.Match("/"); !ok || up != "http://default-svc" {
		t.Errorf("Match / = (%q, %v), want default-svc", up, ok)
	}
}

func TestParseRules_Empty(t *testing.T) {
	rt, err := parseRules("")
	if err != nil {
		t.Fatalf("parseRules empty err: %v", err)
	}
	if len(rt.rules) != 0 {
		t.Errorf("empty 路由表应为空, got %d 条", len(rt.rules))
	}
}

func TestParseRules_ErrorKeepsOldSnapshot(t *testing.T) {
	// 格式错误：缺少 "="
	_, err := parseRules("/api/order")
	if err == nil {
		t.Fatal("格式错误应返回 error")
	}
	// Prefix 不以 "/" 开头
	_, err = parseRules("api/=http://x")
	if err == nil {
		t.Fatal("Prefix 不以 / 开头应返回 error")
	}
}

func TestStart_ErrorKeepsOldSnapshot(t *testing.T) {
	d := New(nil)
	d.rules = "/api/order/=http://order-svc:9001"
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	old := d.rt.Load().(*RouteTable)
	if old == nil || len(old.rules) == 0 {
		t.Fatal("初始 Start 后快照不应为空")
	}

	// 非法配置 → Start 应失败且保留旧快照。
	d.rules = "/api/no-equal-sign"
	if err := d.Start(nil); err == nil {
		t.Fatal("Start 非法配置应返回 error")
	}
	if got := d.rt.Load().(*RouteTable); got != old {
		t.Error("Start 失败后不应替换旧快照")
	}
}

func TestHandle_SetsTargetOnMatch(t *testing.T) {
	d := New(nil)
	d.rules = "/api/order/=http://order-svc:9001"
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	df := dataflow.New(httpsvr.NewDataFlow(), httptest.NewRequest(http.MethodGet, "/api/order/123", nil))
	ctx := &chain.Context{R: httptest.NewRequest(http.MethodGet, "/api/order/123", nil), DF: df}
	if !d.Handle(ctx) {
		t.Error("Handle 应返回 true（不中断链）")
	}
	if got := df.Target(); got != "http://order-svc:9001" {
		t.Errorf("Target()=%q, want order-svc", got)
	}
}

func TestHandle_NoTargetOnMiss(t *testing.T) {
	cfg := New(nil)
	cfg.rules = "/api/order/=http://order-svc:9001"
	if err := cfg.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	df := dataflow.New(httpsvr.NewDataFlow(), httptest.NewRequest(http.MethodGet, "/other/path", nil))
	ctx := &chain.Context{R: httptest.NewRequest(http.MethodGet, "/other/path", nil), DF: df}
	cfg.Handle(ctx)
	if got := df.Target(); got != "" {
		t.Errorf("未命中时 Target()=%q, want 空串（回退默认 upstream）", got)
	}
}

func mustRT(t *testing.T, s string) *RouteTable {
	t.Helper()
	rt, err := parseRules(s)
	if err != nil {
		t.Fatalf("parseRules(%q) err: %v", s, err)
	}
	return rt
}