// Package dispatch 单测：主件生命周期与 Handle 契约（ROUTE_DISPATCH STEP5）。
// 覆盖：DB 依赖注入（内存行集）、Start 构建/降级重试即停、Rebuild 串行与失败
// 保旧快照、Handle 匹配/选点/sticky/参数注入/503/续链契约。
package dispatch

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iotames/easyserver/httpsvr"

	"rocksys/internal/chain"
	"rocksys/internal/dataflow"
)

// stubSource 内存行集来源（测试注入；可注入失败模拟 DB 不可用）。
type stubSource struct {
	mu    sync.Mutex
	in    *GraphInput
	err   error        // 非 nil 时全部拉取失败（模拟 DB 不可用）
	calls atomic.Int64 // 拉取次数（Rebuild 调用计数，断言重试即停）
}

func (s *stubSource) fail(err error) { s.mu.Lock(); s.err = err; s.mu.Unlock() }
func (s *stubSource) ok()            { s.mu.Lock(); s.err = nil; s.mu.Unlock() }
func (s *stubSource) input() *GraphInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.in
}

func (s *stubSource) LoadRules() ([]RuleRow, error) {
	s.calls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.in.Rules, nil
}
func (s *stubSource) LoadUpstreams() ([]UpstreamRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.in.Upstreams, nil
}
func (s *stubSource) LoadNodes() ([]NodeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.in.Nodes, nil
}
func (s *stubSource) LoadRelations() ([]UpstreamNodeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.in.Relations, nil
}

// baseInput 最小合法对象图输入：up-100（rr、sticky 关）← node-200（w=1 高优）。
func baseInput(rules []RuleRow) *GraphInput {
	return &GraphInput{
		Rules: rules,
		Upstreams: []UpstreamRow{
			{ID: 100, Name: "up-a", Algo: int(AlgoRoundRobin), Enabled: true},
		},
		Nodes: []NodeRow{
			{ID: 200, URL: "http://n1:9001", Enabled: true},
		},
		Relations: []UpstreamNodeRow{
			{ID: 300, UpstreamID: 100, NodeID: 200, Weight: 1, Priority: int(PriorityPrimary), Enabled: true},
		},
	}
}

// newCtx 构造链上下文（httptest 请求 + DataFlow + Recorder）。
func newCtx(t *testing.T, method, target string) (*chain.Context, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	return &chain.Context{W: rec, R: req, DF: dataflow.New(httpsvr.NewDataFlow(), req)}, rec
}

// TestNewImplementsInterfaces 主件实现链中间件与生命周期接口。
func TestNewImplementsInterfaces(t *testing.T) {
	d := New(nil, nil, nil)
	var _ chain.Middleware = d
	if _, ok := chain.Middleware(d).(interface{ Slot() chain.Slot }); !ok {
		t.Fatal("dispatch 未实现 Slot()")
	}
	if d.Name() != "dispatch" {
		t.Errorf("Name=%q, want dispatch", d.Name())
	}
	// nil 来源：初始为空表快照，未命中直通。
	if d.Ready() {
		t.Error("初始不应 ready")
	}
	ctx, _ := newCtx(t, http.MethodGet, "/any")
	if !d.Handle(ctx) || ctx.DF.Target() != "" {
		t.Error("空表快照应未命中直通（true 且不写 Target）")
	}
}

// TestStart_BuildAndHandle 主流程：Start 同步构建成功 → Handle 命中写 Target。
func TestStart_BuildAndHandle(t *testing.T) {
	src := &stubSource{in: baseInput([]RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 100, Enabled: true},
	})}
	d := New(nil, src, nil)
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	defer func() { _ = d.Stop() }()
	if !d.Ready() {
		t.Fatal("Start 构建成功后应 ready")
	}
	ctx, _ := newCtx(t, http.MethodGet, "http://example.com/api/order/123")
	if !d.Handle(ctx) {
		t.Error("Handle 应返回 true")
	}
	if got := ctx.DF.Target(); got != "http://n1:9001" {
		t.Errorf("Target=%q, want http://n1:9001", got)
	}
	// DF 节点 id 已写（收尾件递减依据）；在途 +1 已计。
	var nodeID int64
	if v, ok := ctx.DF.Get(DFKeyDispatchNodeID); ok {
		nodeID, _ = v.(int64)
	}
	if nodeID != 200 {
		t.Errorf("DF 节点 id=%v, want 200", nodeID)
	}
	if got := d.reg.Inflight(200); got != 1 {
		t.Errorf("在途计数=%d, want 1", got)
	}
	// 收尾件递减闭环。
	tail := NewTailFin(d.reg)
	if err := tail.OnResponse(ctx); err != nil {
		t.Fatalf("OnResponse err: %v", err)
	}
	if got := d.reg.Inflight(200); got != 0 {
		t.Errorf("递减后在途=%d, want 0", got)
	}
}

// TestHandle_Miss_FallsThrough 未命中：true 且不写 Target（默认 upstream 兜底）。
func TestHandle_Miss_FallsThrough(t *testing.T) {
	src := &stubSource{in: baseInput([]RuleRow{
		{ID: 1, MatchOrder: 1, Domain: "a.com", PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 100, Enabled: true},
	})}
	d := New(nil, src, nil)
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	defer func() { _ = d.Stop() }()
	// 路径不匹配。
	ctx, _ := newCtx(t, http.MethodGet, "/other")
	if !d.Handle(ctx) || ctx.DF.Target() != "" {
		t.Error("未命中应 true 且不写 Target")
	}
	// domain 不匹配。
	ctx, _ = newCtx(t, http.MethodGet, "http://b.com/api/x")
	if !d.Handle(ctx) || ctx.DF.Target() != "" {
		t.Error("domain 不匹配应 true 且不写 Target")
	}
	// domain 匹配（大小写不敏感 + 剥端口）。
	ctx, _ = newCtx(t, http.MethodGet, "http://A.COM:8443/api/x")
	if !d.Handle(ctx) || ctx.DF.Target() != "http://n1:9001" {
		t.Error("归一 domain 命中应写 Target")
	}
}

// TestHandle_ParamInject 模式规则：参数注入 X-Route-Param-* 请求头。
func TestHandle_ParamInject(t *testing.T) {
	src := &stubSource{in: baseInput([]RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypeMode), PathValue: "/api/order/:id", UpstreamID: 100, Enabled: true},
	})}
	d := New(nil, src, nil)
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	defer func() { _ = d.Stop() }()
	ctx, _ := newCtx(t, http.MethodGet, "/api/order/123")
	if !d.Handle(ctx) {
		t.Error("Handle 应返回 true")
	}
	if got := ctx.R.Header.Get("X-Route-Param-id"); got != "123" {
		t.Errorf("X-Route-Param-id=%q, want 123", got)
	}
	if got := ctx.DF.Target(); got != "http://n1:9001" {
		t.Errorf("Target=%q", got)
	}
}

// TestHandle_DisabledUpstream_503 fail-closed：均衡器停用不剔除规则，命中 503 中断链。
func TestHandle_DisabledUpstream_503(t *testing.T) {
	in := baseInput(nil)
	in.Upstreams[0].Enabled = false
	src := &stubSource{in: baseInput([]RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 100, Enabled: true},
	})}
	src.in.Upstreams = in.Upstreams // 停用均衡器
	d := New(nil, src, nil)
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	defer func() { _ = d.Stop() }()
	ctx, rec := newCtx(t, http.MethodGet, "/api/x")
	if d.Handle(ctx) {
		t.Error("停用均衡器应 503 中断链（false）")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code=%d, want 503", rec.Code)
	}
	if ctx.DF.Target() != "" {
		t.Error("503 路径不应写 Target")
	}
}

// TestHandle_NoHealthyNode_503 全部节点无健康结论（未探活登记）→ 503 中断链。
func TestHandle_NoHealthyNode_503(t *testing.T) {
	src := &stubSource{in: baseInput([]RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 100, Enabled: true},
	})}
	d := New(nil, src, nil)
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	defer func() { _ = d.Stop() }()
	// 节点 hc_path 为空 → 免探活登记健康，先走通；再显式判死模拟探活失败。
	ctx, _ := newCtx(t, http.MethodGet, "/api/x")
	if !d.Handle(ctx) {
		t.Error("免探活登记健康节点应可选")
	}
	d.reg.SetHealth(200, HealthBad)
	ctx, rec := newCtx(t, http.MethodGet, "/api/x")
	if d.Handle(ctx) {
		t.Error("无健康节点应 503 中断链（false）")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code=%d, want 503", rec.Code)
	}
}

// TestHandle_StickyPlant 会话保持：开启 sticky 后策略选点种 Cookie（只设头，不写体）。
func TestHandle_StickyPlant(t *testing.T) {
	in := baseInput([]RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 100, Enabled: true},
	})
	in.Upstreams[0].StickyEnabled = true
	src := &stubSource{in: in}
	d := New(nil, src, nil)
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	defer func() { _ = d.Stop() }()
	ctx, rec := newCtx(t, http.MethodGet, "/api/x")
	if !d.Handle(ctx) {
		t.Fatal("Handle 应返回 true")
	}
	// 种值：Set-Cookie 经 Add 追加（续链契约：true 路径只设响应头，未 Write）。
	cookies := rec.Header().Values("Set-Cookie")
	if len(cookies) != 1 || !strings.HasPrefix(cookies[0], DefaultStickyCookie+"=200;") {
		t.Errorf("Set-Cookie=%v, want %s=200", cookies, DefaultStickyCookie)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("true 路径禁止写体，got %q", rec.Body.String())
	}
	// 二次请求携带 sticky Cookie → 直路由同一节点（不再重种）。
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(&http.Cookie{Name: DefaultStickyCookie, Value: "200"})
	df := dataflow.New(httpsvr.NewDataFlow(), req)
	rec2 := httptest.NewRecorder()
	ctx2 := &chain.Context{W: rec2, R: req, DF: df}
	if !d.Handle(ctx2) {
		t.Fatal("Handle 应返回 true")
	}
	if n := len(rec2.Header().Values("Set-Cookie")); n != 0 {
		t.Errorf("直路由不应重种 Cookie，got %d 个", n)
	}
}

// TestStart_DegradedAndRetry recovers：DB 不可用 → 空表快照降级；恢复后后台重试
// 首次成功即停（拉取次数停止增长）。
func TestStart_DegradedAndRetry(t *testing.T) {
	src := &stubSource{in: baseInput([]RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 100, Enabled: true},
	})}
	src.fail(errors.New("db down"))
	d := New(nil, src, nil)
	d.retryEvery = 20 * time.Millisecond
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start 降级不应报错，got: %v", err)
	}
	if d.Ready() {
		t.Error("DB 不可用不应 ready")
	}
	// 降级期间：空表快照全走默认 upstream、无 panic。
	ctx, _ := newCtx(t, http.MethodGet, "/api/x")
	if !d.Handle(ctx) || ctx.DF.Target() != "" {
		t.Error("降级空表快照应未命中直通")
	}

	// DB 恢复 → 后台重试自动完成首次构建。
	src.ok()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !d.Ready() {
		time.Sleep(5 * time.Millisecond)
	}
	if !d.Ready() {
		t.Fatal("恢复后后台重试应完成首次构建")
	}
	ctx, _ = newCtx(t, http.MethodGet, "/api/x")
	if !d.Handle(ctx) || ctx.DF.Target() != "http://n1:9001" {
		t.Error("首次构建成功后规则应生效")
	}

	// 重试即停：ready 后拉取次数不再增长（非常驻轮询）。
	calls := src.calls.Load()
	time.Sleep(5 * d.retryEvery)
	if got := src.calls.Load(); got != calls {
		t.Errorf("首次成功后重试应停止，拉取次数 %d → %d", calls, got)
	}
	_ = d.Stop()

	// Stop 后重试 goroutine 不再拉取。
	after := src.calls.Load()
	time.Sleep(3 * d.retryEvery)
	if got := src.calls.Load(); got != after {
		t.Errorf("Stop 后不应再有拉取，%d → %d", after, got)
	}
}

// TestRebuild_ConcurrentSerial Rebuild 并发调用串行安全（-race 下验证）。
func TestRebuild_ConcurrentSerial(t *testing.T) {
	src := &stubSource{in: baseInput([]RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 100, Enabled: true},
	})}
	d := New(nil, src, nil)
	defer func() { _ = d.Stop() }()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.Rebuild(); err != nil {
				t.Errorf("并发 Rebuild err: %v", err)
			}
		}()
	}
	wg.Wait()
	if !d.Ready() {
		t.Error("并发 Rebuild 后应 ready")
	}
}

// TestRebuild_FailureKeepsOldSnapshot 构建失败保留旧快照（引用不变）。
func TestRebuild_FailureKeepsOldSnapshot(t *testing.T) {
	rules := []RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 100, Enabled: true},
	}
	src := &stubSource{in: baseInput(rules)}
	d := New(nil, src, nil)
	if err := d.Rebuild(); err != nil {
		t.Fatalf("首次 Rebuild err: %v", err)
	}
	old := d.snapshot()

	// 非法行集（规则引用不存在的均衡器）→ Rebuild 报错且快照指针不变。
	// 注意：规则行须独立构造（与合法输入不共享底层数组）。
	badRules := []RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 999, Enabled: true},
	}
	bad := baseInput(badRules)
	src.mu.Lock()
	src.in = bad
	src.mu.Unlock()
	if err := d.Rebuild(); err == nil {
		t.Fatal("非法对象图 Rebuild 应报错")
	}
	if d.snapshot() != old {
		t.Error("构建失败不应替换旧快照")
	}

	// 数据源失败同理。
	src.mu.Lock()
	src.in = baseInput(rules)
	src.mu.Unlock()
	src.fail(errors.New("db down"))
	if err := d.Rebuild(); err == nil {
		t.Fatal("数据源失败 Rebuild 应报错")
	}
	if d.snapshot() != old {
		t.Error("拉取失败不应替换旧快照")
	}

	// 恢复合法数据 → 重建成功替换快照。
	src.ok()
	if err := d.Rebuild(); err != nil {
		t.Fatalf("恢复后 Rebuild err: %v", err)
	}
	if d.snapshot() == old {
		t.Error("成功重建应替换为新快照")
	}
}

// TestStop_DrainsHealthCenter Stop 排空探活任务（带 hc_path 的被引用节点）。
func TestStop_DrainsHealthCenter(t *testing.T) {
	in := baseInput([]RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 100, Enabled: true},
	})
	in.Nodes[0].HCPath = "/healthz" // 触发探活任务
	src := &stubSource{in: in}
	d := New(nil, src, nil)
	if err := d.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	if d.hc.taskCount() != 1 {
		t.Fatalf("探活任务数=%d, want 1", d.hc.taskCount())
	}
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop err: %v", err)
	}
	if n := d.hc.taskCount(); n != 0 {
		t.Errorf("Stop 后探活任务应排空，got %d", n)
	}
	// 停止后拒绝 Rebuild（防探活任务泄漏）。
	if err := d.Rebuild(); err == nil {
		t.Error("停止后 Rebuild 应报错")
	}
}
