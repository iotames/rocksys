// Package dispatch 单测：路由表解析、前缀匹配、节点组负载均衡与健康检查。
// 覆盖 DEV_HANDBOOK.md §10.4 验收 + §10.5（批次10 增强）。
package dispatch

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// matchUp 便捷封装：Match 命中后返回 Select 选中的节点 URL。
func matchUp(rt *RouteTable, path string) (string, bool) {
	rule, ok := rt.Match(path)
	if !ok {
		return "", false
	}
	return rule.Select()
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
			up, ok := matchUp(tt.rules, tt.path)
			if ok != tt.wantOK || up != tt.wantUp {
				t.Errorf("Match(%q) = (%q, %v), want (%q, %v)", tt.path, up, ok, tt.wantUp, tt.wantOK)
			}
		})
	}
}

func TestRouteMatch(t *testing.T) {
	rt := mustRT(t, "/api/order/=http://order-svc:9001")

	// 命中
	if up, ok := matchUp(rt, "/api/order/123"); !ok || up != "http://order-svc:9001" {
		t.Errorf("Match /api/order/123 = (%q, %v)", up, ok)
	}
	// 前缀边界：/api/ordering 不匹配 /api/order/
	if up, ok := matchUp(rt, "/api/ordering/list"); ok {
		t.Errorf("Match /api/ordering/list 不应命中，got (%q, %v)", up, ok)
	}
	// 其他路径不命中
	if _, ok := matchUp(rt, "/other/path"); ok {
		t.Error("Match /other/path 不应命中")
	}
}

func TestRouteMatch_LongestPrefix(t *testing.T) {
	rt := mustRT(t, "/api/=http://api-svc:9000,/api/order/=http://order-svc:9001")
	// 最长前缀优先
	if up, ok := matchUp(rt, "/api/order/123"); !ok || up != "http://order-svc:9001" {
		t.Errorf("Match /api/order/123 = (%q, %v), want order-svc", up, ok)
	}
	// 较短前缀仍命中其对应路由
	if up, ok := matchUp(rt, "/api/user/1"); !ok || up != "http://api-svc:9000" {
		t.Errorf("Match /api/user/1 = (%q, %v), want api-svc", up, ok)
	}
}

func TestRouteMatch_CatchAll(t *testing.T) {
	rt := mustRT(t, "/api/order/=http://order-svc:9001,/=http://default-svc")
	// "/" 兜底匹配未命中其他路由的路径
	if up, ok := matchUp(rt, "/zz/other"); !ok || up != "http://default-svc" {
		t.Errorf("Match /zz/other = (%q, %v), want default-svc", up, ok)
	}
	// 已命中的更长前缀优先于兜底 "/"
	if up, ok := matchUp(rt, "/api/order/9"); !ok || up != "http://order-svc:9001" {
		t.Errorf("Match /api/order/9 = (%q, %v), want order-svc", up, ok)
	}
}

func TestRouteMatch_Root(t *testing.T) {
	rt := mustRT(t, "/=http://default-svc")
	// 根路径 "/" 命中兜底路由
	if up, ok := matchUp(rt, "/"); !ok || up != "http://default-svc" {
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
	// 节点不以 http(s):// 开头
	_, err = parseRules("/api/=order-svc:9001")
	if err == nil {
		t.Fatal("节点不以 http(s):// 开头应返回 error")
	}
	// 节点组为空
	_, err = parseRules("/api/=")
	if err == nil {
		t.Fatal("节点组为空应返回 error")
	}
	// 健康检查参数段数错误
	_, err = parseRules("/api/=http://a:1@10s@2s")
	if err == nil {
		t.Fatal("健康检查参数段数错误应返回 error")
	}
	// 权重非法
	_, err = parseRules("/api/=http://a:1|w=0")
	if err == nil {
		t.Fatal("权重为 0 应返回 error")
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

// ---------------------------------------------------------------------------
// 批次10 新增：节点组 / 负载均衡 / 健康检查
// ---------------------------------------------------------------------------

func TestParseRules_NodeGroup(t *testing.T) {
	rt := mustRT(t, "/api/order/=http://o1:9001;http://o2:9001|w=2;http://o3:9001|w=1|p=1@10s@2s@/healthz")
	if len(rt.rules) != 1 {
		t.Fatalf("应解析 1 条规则, got %d", len(rt.rules))
	}
	rule := rt.rules[0]
	if len(rule.Nodes) != 3 {
		t.Fatalf("节点组应为 3 个节点, got %d", len(rule.Nodes))
	}
	if rule.Nodes[0].Weight != 1 || rule.Nodes[0].Priority != 0 {
		t.Errorf("node0 应为默认权重1/高优, got w=%d p=%d", rule.Nodes[0].Weight, rule.Nodes[0].Priority)
	}
	if rule.Nodes[1].Weight != 2 || rule.Nodes[1].Priority != 0 {
		t.Errorf("node1 应为权重2/高优, got w=%d p=%d", rule.Nodes[1].Weight, rule.Nodes[1].Priority)
	}
	if rule.Nodes[2].Weight != 1 || rule.Nodes[2].Priority != 1 {
		t.Errorf("node2 应为权重1/备份, got w=%d p=%d", rule.Nodes[2].Weight, rule.Nodes[2].Priority)
	}
	if rule.HealthCheck == nil {
		t.Fatal("应解析出健康检查")
	}
	if rule.HealthCheck.Interval != 10*time.Second || rule.HealthCheck.Timeout != 2*time.Second || rule.HealthCheck.Path != "/healthz" {
		t.Errorf("健康检查解析错误: %+v", rule.HealthCheck)
	}
	// 配置了健康检查：节点初始为不健康（fail-closed），直到探活确认。
	if rule.Nodes[0].healthy.Load() {
		t.Error("配置健康检查的节点初始不应为健康")
	}
}

func TestRule_Select_WeightedRoundRobin(t *testing.T) {
	rt := mustRT(t, "/api/=http://a:1|w=2;http://b:1|w=1")
	rule := rt.rules[0]
	counts := map[string]int{}
	const n = 300
	for i := 0; i < n; i++ {
		up, ok := rule.Select()
		if !ok {
			t.Fatal("Select 应返回节点")
		}
		counts[up]++
	}
	// 权重 2:1 → 约 2:1 分配
	ratio := float64(counts["http://a:1"]) / float64(counts["http://b:1"])
	if ratio < 1.5 || ratio > 2.5 {
		t.Errorf("加权轮询比例应约 2:1, got %v (a=%d b=%d)", ratio, counts["http://a:1"], counts["http://b:1"])
	}
}

func TestRule_Select_PriorityBackup(t *testing.T) {
	// 高优节点 b 不健康（p=0），备份节点 c 健康（p=1）→ 应选备份。
	rt := mustRT(t, "/api/=http://a:1|p=0;http://b:1|p=0;http://c:1|p=1")
	rule := rt.rules[0]
	rule.Nodes[0].healthy.Store(false)
	rule.Nodes[1].healthy.Store(false)
	rule.Nodes[2].healthy.Store(true)

	for i := 0; i < 5; i++ {
		up, ok := rule.Select()
		if !ok || up != "http://c:1" {
			t.Errorf("高优全挂应选备份节点, got (%q, %v)", up, ok)
		}
	}
}

func TestRule_Select_AllDown(t *testing.T) {
	rt := mustRT(t, "/api/=http://a:1;http://b:1")
	rule := rt.rules[0]
	rule.Nodes[0].healthy.Store(false)
	rule.Nodes[1].healthy.Store(false)
	if _, ok := rule.Select(); ok {
		t.Error("全部节点不健康时应返回 ok=false")
	}
}

func TestHealthCheck_Probe_PicksHealthy(t *testing.T) {
	healthySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthySrv.Close()
	downSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer downSrv.Close()

	d := New(nil)
	d.rules = "/api/=" + healthySrv.URL + ";" + downSrv.URL + "@30ms@200ms@/healthz"
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	rule, ok := d.rt.Load().(*RouteTable).Match("/api/x")
	if !ok {
		t.Fatal("Match 应命中")
	}
	// 等待首次探活完成
	time.Sleep(250 * time.Millisecond)
	for i := 0; i < 5; i++ {
		up, ok := rule.Select()
		if !ok || up != healthySrv.URL {
			t.Errorf("Select 应只选健康节点 %q, got (%q, %v)", healthySrv.URL, up, ok)
		}
	}
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop err: %v", err)
	}
}

func TestHandle_NoHealthyNode_Writes503(t *testing.T) {
	downSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer downSrv.Close()

	d := New(nil)
	d.rules = "/api/=" + downSrv.URL + "@30ms@100ms@/healthz"
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	// 等首次探活标记不健康
	time.Sleep(200 * time.Millisecond)

	rec := httptest.NewRecorder()
	df := dataflow.New(httpsvr.NewDataFlow(), httptest.NewRequest(http.MethodGet, "/api/1", nil))
	ctx := &chain.Context{W: rec, R: httptest.NewRequest(http.MethodGet, "/api/1", nil), DF: df}
	if d.Handle(ctx) {
		t.Error("Handle 应返回 false（中断链）")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code=%d, want 503", rec.Code)
	}
	if got := df.Target(); got != "" {
		t.Errorf("Target()=%q, want 空串（不错误转发）", got)
	}
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop err: %v", err)
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
