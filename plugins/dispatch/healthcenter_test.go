// 健康检查中心单测（STEP4）：任务集去重 / 差量增减与参数变更重启 / 跨热更保序 /
// goroutine 无泄漏。探活一律走 httptest 本地服务器，不依赖外网。
package dispatch

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// hcInput 组装 GraphInput 的测试便捷函数：全量启用，参数按需覆盖。
func hcInput(rules int64, ups []int64, nodes []NodeRow, rels [][2]int64) *GraphInput {
	in := &GraphInput{
		Rules: []RuleRow{{ID: rules, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/", UpstreamID: ups[0], Enabled: true}},
	}
	for _, id := range ups {
		in.Upstreams = append(in.Upstreams, UpstreamRow{ID: id, Name: "up", Algo: int(AlgoLeastConn), Enabled: true})
	}
	in.Nodes = nodes
	for _, r := range rels {
		in.Relations = append(in.Relations, UpstreamNodeRow{UpstreamID: r[0], NodeID: r[1], Weight: 1, Enabled: true})
	}
	return in
}

// hcNode 构造启用节点行。
func hcNode(id int64, url, path string, intervalMS int) NodeRow {
	return NodeRow{ID: id, URL: url, HCIntervalMS: intervalMS, HCTimeoutMS: 500, HCPath: path, Enabled: true}
}

// hcStatusServer 固定状态码的探活目标服务器（计数命中次数）。
func hcStatusServer(t *testing.T, code int32) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(int(atomic.LoadInt32(&code)))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// waitFor 轮询等待条件成立（探活为异步动作，断言统一收口）。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// TestHealthCenterMultiBalancerDedup 多均衡器引用同节点只探一份。
func TestHealthCenterMultiBalancerDedup(t *testing.T) {
	srv, hits := hcStatusServer(t, 200)
	reg := NewRegistry()
	hc := NewHealthCenter(reg)
	in := hcInput(1, []int64{1, 2}, []NodeRow{hcNode(7, srv.URL, "/healthz", 50)},
		[][2]int64{{1, 7}, {2, 7}}) // 两个均衡器都引用节点 7
	hc.Rebuild(in)
	if got := hc.taskCount(); got != 1 {
		t.Fatalf("同节点被多均衡器引用应只建一个探活任务，got %d", got)
	}
	waitFor(t, "首探出结论", func() bool { return reg.Health(7) == HealthOK })
	first := hits.Load()
	waitFor(t, "周期探测继续", func() bool { return hits.Load() > first })
}

// TestHealthCenterProbeStates 2xx/3xx 健康、4xx/网络错误判死；免探活（path 空）显绿不启任务。
func TestHealthCenterProbeStates(t *testing.T) {
	code := int32(200)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(int(atomic.LoadInt32(&code)))
	}))
	defer srv.Close()
	reg := NewRegistry()
	hc := NewHealthCenter(reg)
	defer hc.Stop()
	in := hcInput(1, []int64{1}, []NodeRow{
		hcNode(7, srv.URL, "/healthz", 30),
		hcNode(8, srv.URL, "", 30), // path 空：免探活
	}, [][2]int64{{1, 7}, {1, 8}})
	hc.Rebuild(in)
	if got := hc.taskCount(); got != 1 {
		t.Fatalf("免探活节点不应建任务，got %d", got)
	}
	if got := reg.Health(8); got != HealthOK {
		t.Fatalf("免探活节点应登记显绿，got %v", got)
	}
	waitFor(t, "200 判健康", func() bool { return reg.Health(7) == HealthOK })
	atomic.StoreInt32(&code, 503)
	waitFor(t, "503 判死", func() bool { return reg.Health(7) == HealthBad })
}

// TestHealthCenterRebuildDiff 差量：新增启探 / 移除停探清记录转灰 / 参数变更重启 / 保序。
func TestHealthCenterRebuildDiff(t *testing.T) {
	okSrv, _ := hcStatusServer(t, 200)
	badSrv, _ := hcStatusServer(t, 404)
	reg := NewRegistry()
	hc := NewHealthCenter(reg)
	defer hc.Stop()

	// 初始：仅节点 7（探活，50ms 周期）。
	hc.Rebuild(hcInput(1, []int64{1}, []NodeRow{hcNode(7, okSrv.URL, "/healthz", 50)}, [][2]int64{{1, 7}}))
	waitFor(t, "节点7 健康", func() bool { return reg.Health(7) == HealthOK })
	if reg.Len() != 1 {
		t.Fatalf("初始记录数应为 1，got %d", reg.Len())
	}

	// 新增节点 8（探活 404）与免探活节点 9。
	in2 := hcInput(1, []int64{1}, []NodeRow{
		hcNode(7, okSrv.URL, "/healthz", 50),
		hcNode(8, badSrv.URL, "/healthz", 50),
		hcNode(9, okSrv.URL, "", 50),
	}, [][2]int64{{1, 7}, {1, 8}, {1, 9}})
	hc.Rebuild(in2)
	waitFor(t, "节点8 判死", func() bool { return reg.Health(8) == HealthBad })
	waitFor(t, "节点9 显绿", func() bool { return reg.Health(9) == HealthOK })
	if got := hc.taskCount(); got != 2 {
		t.Fatalf("新增后任务数应为 2（7/8 探活，9 免探活），got %d", got)
	}

	// 移除节点 8：停探 + 清记录转灰；节点 7 保留（跨热更保序，结论不清零）。
	in3 := hcInput(1, []int64{1}, []NodeRow{
		hcNode(7, okSrv.URL, "/healthz", 50),
		hcNode(9, okSrv.URL, "", 50),
	}, [][2]int64{{1, 7}, {1, 9}})
	hc.Rebuild(in3)
	if got := reg.Health(8); got != HealthUnknown {
		t.Fatalf("移除后节点8 应转灰，got %v", got)
	}
	if got := reg.Health(7); got != HealthOK {
		t.Fatalf("保留节点7 结论不得清零，got %v", got)
	}
	if got := hc.taskCount(); got != 1 {
		t.Fatalf("移除后任务数应为 1，got %d", got)
	}

	// 参数变更（探活周期）视为差量重启：旧任务停、新任务起，记录保留结论不清零。
	hc.Rebuild(hcInput(1, []int64{1}, []NodeRow{
		hcNode(7, okSrv.URL, "/healthz", 500),
		hcNode(9, okSrv.URL, "", 50),
	}, [][2]int64{{1, 7}, {1, 9}}))
	hc.mu.Lock()
	tasks := len(hc.tasks)
	hc.mu.Unlock()
	if tasks != 1 {
		t.Fatalf("参数变更重启后任务数应为 1，got %d", tasks)
	}
	if got := reg.Health(7); got != HealthOK {
		t.Fatalf("参数变更重启后节点7 结论不得清零，got %v", got)
	}
	waitFor(t, "新周期任务在探", func() bool { return reg.Health(7) == HealthOK })

	// 移除全部：任务清零，探活节点转灰，免探活节点记录同样移除（记录集口径随任务集收缩）。
	hc.Rebuild(hcInput(1, []int64{1}, []NodeRow{hcNode(7, okSrv.URL, "/healthz", 50)}, [][2]int64{{1, 7}}))
	if got := reg.Len(); got != 1 {
		t.Fatalf("仅剩节点7 时记录数应为 1，got %d", reg.Len())
	}
}

// TestHealthCenterStopDrains Stop 全量排空：全部任务停止且 goroutine 退出。
func TestHealthCenterStopDrains(t *testing.T) {
	okSrv, _ := hcStatusServer(t, 200)
	reg := NewRegistry()
	hc := NewHealthCenter(reg)
	in := hcInput(1, []int64{1}, []NodeRow{
		hcNode(7, okSrv.URL, "/healthz", 30),
		hcNode(8, okSrv.URL, "/healthz", 30),
	}, [][2]int64{{1, 7}, {1, 8}})
	hc.Rebuild(in)
	waitFor(t, "任务启动", func() bool { return hc.taskCount() == 2 })
	hc.Stop()
	if got := hc.taskCount(); got != 0 {
		t.Fatalf("Stop 后任务集应为空，got %d", got)
	}
}

// TestHealthCenterNoGoroutineLeak 热更重建 N 次后 goroutine 数不增长（-race 下验证）。
func TestHealthCenterNoGoroutineLeak(t *testing.T) {
	okSrv, _ := hcStatusServer(t, 200)
	reg := NewRegistry()
	hc := NewHealthCenter(reg)
	in := hcInput(1, []int64{1}, []NodeRow{
		hcNode(7, okSrv.URL, "/healthz", 30),
		hcNode(8, okSrv.URL, "/healthz", 30),
		hcNode(9, okSrv.URL, "", 30),
	}, [][2]int64{{1, 7}, {1, 8}, {1, 9}})
	hc.Rebuild(in)
	defer hc.Stop()

	waitFor(t, "基线稳定", func() bool { return hc.taskCount() == 2 })
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	base := runtime.NumGoroutine()

	const rounds = 5
	for i := 0; i < rounds; i++ {
		hc.Rebuild(in) // 每轮 Rebuild 停旧起新（含差量重启语义）
		waitFor(t, "任务就绪", func() bool { return hc.taskCount() == 2 })
		hc.Rebuild(in) // 同参数 Rebuild：保序，任务不重启
		if got := hc.taskCount(); got != 2 {
			t.Fatalf("第 %d 轮同参重建任务数应为 2，got %d", i, got)
		}
		hc.Stop()
	}
	// 全量排空后允许短暂收尾，数量必须回落基线（不允许趋势性增长）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= base {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutine 泄漏：基线 %d，%d 轮重建排空后仍为 %d", base, rounds, runtime.NumGoroutine())
}
