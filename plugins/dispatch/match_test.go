// Package dispatch 匹配引擎测试：Host 归一化、三类型路径匹配与 Match 主流程
// （M 表「匹配语义正确性」口径全项，语义唯一权威见
// docs/plan/ROUTE_DISPATCH_DESIGN_PLAN.md）。
package dispatch

import (
	"testing"
)

// mkGraphInput 构造最小合法对象图输入：一个均衡器 + 一个节点 + 一条关系。
func mkGraphInput(rules []RuleRow) *GraphInput {
	return &GraphInput{
		Rules: rules,
		Upstreams: []UpstreamRow{
			{ID: 100, Name: "up-a", Algo: int(AlgoRoundRobin), Enabled: true},
		},
		Nodes: []NodeRow{
			{ID: 200, URL: "http://n1:9001", Enabled: true},
		},
		Relations: []UpstreamNodeRow{
			{ID: 300, UpstreamID: 100, NodeID: 200, Weight: 1, Enabled: true},
		},
	}
}

func mustSnap(t *testing.T, rules []RuleRow) *RouteSnapshot {
	t.Helper()
	snap, err := BuildSnapshot(mkGraphInput(rules))
	if err != nil {
		t.Fatalf("BuildSnapshot err: %v", err)
	}
	return snap
}

func TestNormalizeHost(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},                       // 空 Host 保持空
		{"Example.COM", "example.com"}, // 转小写
		{"api.example.com:8443", "api.example.com"}, // 剥端口
		{"[::1]:80", "[::1]"},                       // IPv6 方括号形态剥端口
		{"[::1]", "[::1]"},                          // 方括号无端口
		{"::1", "::1"},                              // 裸 IPv6 多冒号不误剥
		{"  Host.COM:80 ", "host.com"},              // 去空白 + 剥端口 + 小写
		{"localhost", "localhost"},                  // 无端口原样小写
	}
	for _, c := range cases {
		if got := normalizeHost(c.in); got != c.want {
			t.Errorf("normalizeHost(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

// TestMatchSegments_Pattern 段匹配公共函数（自 router_test 迁移的断言原样 + 通配细节）。
func TestMatchSegments_Pattern(t *testing.T) {
	// 参数捕获（原 TestRouter_ParamCapture 断言）。
	params, ok := matchSegments("/api/order/:id", "/api/order/123")
	if !ok || params["id"] != "123" {
		t.Errorf("参数捕获错误: ok=%v params=%v", ok, params)
	}
	// 多参数捕获（原 TestRouter_ParamCapture_Multi 断言）。
	params, ok = matchSegments("/api/:ver/users/:uid", "/api/v2/users/42")
	if !ok || params["ver"] != "v2" || params["uid"] != "42" {
		t.Errorf("多参数捕获错误: ok=%v params=%v", ok, params)
	}
	// 通配（原 TestRouter_Wildcard 断言）：剩余任意段命中。
	if _, ok = matchSegments("/api/*", "/api/anything/deep/nested"); !ok {
		t.Error("通配应命中多段剩余路径")
	}
	// 通配至少消费一个段：/api/* 不命中 /api。
	if _, ok = matchSegments("/api/*", "/api"); ok {
		t.Error("/api/* 不应命中 /api（通配至少消费一段）")
	}
	// 未命中：静态段不等。
	if _, ok = matchSegments("/api/order/:id", "/other/path"); ok {
		t.Error("静态段不等不应命中")
	}
}

func TestMatch_Semantics(t *testing.T) {
	// 规则集覆盖 M 表全项：同序号两条（id 决定先后）+ 各路径类型 + domain 条件。
	snap := mustSnap(t, []RuleRow{
		{ID: 2, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 100, Enabled: true},
		{ID: 1, MatchOrder: 1, Domain: "API.Example.COM", PathType: int(PathTypeExact), PathValue: "/ping", UpstreamID: 100, Enabled: true},
		{ID: 3, MatchOrder: 2, Domain: "admin.example.com", PathType: int(PathTypeMode), PathValue: "/admin/user/:id", UpstreamID: 100, Enabled: true},
		{ID: 4, MatchOrder: 3, Domain: "[::1]", PathType: int(PathTypePrefix), PathValue: "/v6", UpstreamID: 100, Enabled: true},
		{ID: 5, MatchOrder: 4, PathType: int(PathTypePrefix), PathValue: "/", UpstreamID: 100, Enabled: true},
	})

	cases := []struct {
		name     string
		host     string
		path     string
		wantID   int64 // 0 = 未命中
		wantPkey string
		wantPval string
	}{
		// 序号命中即停：order=1 先扫，domain 条件不满足的 id=2 先命中 /api 前缀。
		{"序号命中即停-前缀", "any.example.com", "/api/x", 2, "", ""},
		{"序号命中即停-精确", "api.example.com", "/ping", 1, "", ""},
		// domain 条件：空=任意（id=2 无 domain）；非空=归一精确相等。
		{"domain-精确归一小写", "API.EXAMPLE.COM", "/ping", 1, "", ""},
		{"domain-剥端口归一", "ADMIN.EXAMPLE.COM:9999", "/admin/user/7", 3, "id", "7"},
		{"domain-IPv6方括号", "[::1]:8080", "/v6/x", 4, "", ""},
		{"domain-不匹配则继续下条", "other.com", "/ping", 5, "", ""},
		// 前缀：段对齐且命中自身。
		{"前缀-命中自身", "h.com", "/api", 2, "", ""},
		{"前缀-段对齐子路径", "h.com", "/api/users", 2, "", ""},
		{"前缀-不匹配非段边界", "h.com", "/apix", 5, "", ""},
		// 精确：全等不做尾斜杠归一。
		{"精确-全等命中", "api.example.com", "/ping", 1, "", ""},
		{"精确-尾斜杠不归一", "api.example.com", "/ping/", 5, "", ""},
		// 模式：段匹配捕获。
		{"模式-捕获参数", "admin.example.com", "/admin/user/42", 3, "id", "42"},
		{"模式-段数不足不命中", "admin.example.com", "/admin/user", 5, "", ""},
		{"模式-域名不符跳过", "h.com", "/admin/user/42", 5, "", ""},
		// 路径大小写敏感：/API 不命中前缀 /api。
		{"路径大小写敏感", "h.com", "/API/x", 5, "", ""},
		// / 命中一切（含 / 本身，D13）。
		{"根兜底-任意路径", "h.com", "/zz/other", 5, "", ""},
		{"根兜底-根本身", "h.com", "/", 5, "", ""},
		// 空 Host：domain 空规则命中（域名不敏感）。
		{"空Host-任意domain空规则", "", "/api", 2, "", ""},
	}
	for _, c := range cases {
		rule, params := Match(snap, c.host, c.path)
		if c.wantID == 0 {
			if rule != nil {
				t.Errorf("%s: 不应命中, got id=%d", c.name, rule.ID)
			}
			continue
		}
		if rule == nil || rule.ID != c.wantID {
			t.Errorf("%s: want id=%d, got %v", c.name, c.wantID, rule)
			continue
		}
		if c.wantPkey != "" {
			if params == nil || params[c.wantPkey] != c.wantPval {
				t.Errorf("%s: 参数 %s=%v, want %q", c.name, c.wantPkey, params, c.wantPval)
			}
		}
	}

	// 全未命中：无兜底规则时返回 nil。
	empty := mustSnap(t, []RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypeExact), PathValue: "/only", UpstreamID: 100, Enabled: true},
	})
	if rule, _ := Match(empty, "h.com", "/nope"); rule != nil {
		t.Errorf("全未命中应返回 nil, got id=%d", rule.ID)
	}
	if rule, _ := Match(nil, "h.com", "/x"); rule != nil {
		t.Error("nil 快照应返回 nil")
	}
}

// TestMatch_SameOrderByIDAsc 同序号按 id 升序决定先后（S1 稳定排序口径）。
func TestMatch_SameOrderByIDAsc(t *testing.T) {
	snap := mustSnap(t, []RuleRow{
		{ID: 9, MatchOrder: 5, PathType: int(PathTypeExact), PathValue: "/dup", UpstreamID: 100, Enabled: true},
		{ID: 3, MatchOrder: 5, PathType: int(PathTypeExact), PathValue: "/dup", UpstreamID: 100, Enabled: true},
		{ID: 6, MatchOrder: 5, PathType: int(PathTypeExact), PathValue: "/dup", UpstreamID: 100, Enabled: true},
	})
	rule, _ := Match(snap, "h.com", "/dup")
	if rule == nil || rule.ID != 3 {
		t.Errorf("同序号应命中最小 id=3, got %v", rule)
	}
}

// TestMatch_HeaderParams 参数注入辅助：命中模式规则产出 X-Route-Param-* 键值对。
func TestMatch_HeaderParams(t *testing.T) {
	snap := mustSnap(t, []RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypeMode), PathValue: "/api/:ver/order/:id", UpstreamID: 100, Enabled: true},
	})
	rule, params := Match(snap, "h.com", "/api/v2/order/7")
	if rule == nil {
		t.Fatal("应命中模式规则")
	}
	headers := RouteParamHeaders(params)
	if headers["X-Route-Param-ver"] != "v2" || headers["X-Route-Param-id"] != "7" {
		t.Errorf("X-Route-Param-* 头错误: %v", headers)
	}
	// 非模式命中（无参数）与空参数均产出 nil。
	if got := RouteParamHeaders(nil); got != nil {
		t.Errorf("空参数应返回 nil, got %v", got)
	}
	if got := RouteParamHeaders(map[string]string{}); got != nil {
		t.Errorf("空参数应返回 nil, got %v", got)
	}
}
