// select_test.go：选点引擎表驱动测试——平滑加权序列、least_conn 偏向、优先级回落、在途计数。
package dispatch

import (
	"reflect"
	"strconv"
	"testing"
)

// newTestUpstream 构建测试用均衡器：全部节点默认登记为健康。
// 节点 id 自 101 起按顺序递增；权重/优先级逐节点给定。
func newTestUpstream(t *testing.T, algo AlgoKind, weights []int, prios []Priority) (*UpstreamRT, *MemRegistry) {
	t.Helper()
	g := &GraphInput{
		Upstreams: []UpstreamRow{{ID: 7, Name: "测试池", Algo: int(algo), Enabled: true}},
	}
	ups := &g.Upstreams[0]
	_ = ups
	for i, w := range weights {
		id := int64(101 + i)
		p := PriorityPrimary
		if i < len(prios) {
			p = prios[i]
		}
		g.Nodes = append(g.Nodes, NodeRow{ID: id, URL: "http://n" + strconv.Itoa(i+1) + ":9000", Enabled: true})
		g.Relations = append(g.Relations, UpstreamNodeRow{ID: int64(i + 1), UpstreamID: 7, NodeID: id, Weight: w, Priority: int(p), Enabled: true})
	}
	snap, err := BuildGraph(g)
	if err != nil {
		t.Fatalf("构建测试均衡器失败: %v", err)
	}
	u := snap.Upstreams[7]
	reg := NewMemRegistry()
	for _, n := range u.Nodes {
		reg.SetHealth(n.ID, HealthOK)
	}
	return u, reg
}

// TestSelectRoundRobinWeightedSequence 平滑加权序列断言：权重 1:2 → [b,a,b] 循环。
func TestSelectRoundRobinWeightedSequence(t *testing.T) {
	u, reg := newTestUpstream(t, AlgoRoundRobin, []int{1, 2}, nil) // n1 w=1, n2 w=2
	var got []int64
	for i := 0; i < 6; i++ {
		n, ok := SelectNode(u, reg)
		if !ok {
			t.Fatalf("第 %d 次选点失败", i+1)
		}
		got = append(got, n.ID)
	}
	want := []int64{102, 101, 102, 102, 101, 102} // 权重 1:2 的平滑加权序列
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("平滑加权序列错误\n got: %v\nwant: %v", got, want)
	}
}

// TestSelectRoundRobinWeightDistribution 权重 5:1:1 分布断言：21 次选中 15/3/3。
func TestSelectRoundRobinWeightDistribution(t *testing.T) {
	u, reg := newTestUpstream(t, AlgoRoundRobin, []int{5, 1, 1}, nil)
	counts := map[int64]int{}
	for i := 0; i < 21; i++ {
		n, ok := SelectNode(u, reg)
		if !ok {
			t.Fatalf("第 %d 次选点失败", i+1)
		}
		counts[n.ID]++
	}
	want := map[int64]int{101: 15, 102: 3, 103: 3}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("权重分布错误\n got: %v\nwant: %v", counts, want)
	}
}

// TestSelectLeastConnBias 并发计数下偏向低在途节点；递减后回位。
func TestSelectLeastConnBias(t *testing.T) {
	u, reg := newTestUpstream(t, AlgoLeastConn, []int{1, 1, 1}, nil)
	// 预置在途：n1=5, n2=2, n3=0 → 必选 n3。
	reg.IncInflight(101)
	reg.IncInflight(101)
	reg.IncInflight(101)
	reg.IncInflight(101)
	reg.IncInflight(101)
	reg.IncInflight(102)
	reg.IncInflight(102)
	for i := 0; i < 2; i++ {
		n, ok := SelectNode(u, reg)
		if !ok || n.ID != 103 {
			t.Fatalf("应选最低在途节点 103, got %+v ok=%v", n, ok)
		}
	}
	// 两次选中后 n3=2，与 n2(2) 平局；再选一轮平局集回落游标，随后 n3=3 超过 n2 → 应选 n2。
	n, ok := SelectNode(u, reg)
	if !ok || n.ID != 102 {
		t.Fatalf("递增后应回落选 102, got %+v ok=%v", n, ok)
	}
}

// noopIncRegistry 屏蔽选中 +1 副作用的包装（专测平局回落游标的纯序列）。
type noopIncRegistry struct{ NodeRegistry }

func (noopIncRegistry) IncInflight(int64) {}

// TestSelectLeastConnTieFallbackRR 平局回落轮询游标：两节点同在途（屏蔽 +1 反馈）
// 时按平滑加权纯轮询交替 101,102,101,102。
func TestSelectLeastConnTieFallbackRR(t *testing.T) {
	u, reg := newTestUpstream(t, AlgoLeastConn, []int{1, 1}, nil)
	fixed := noopIncRegistry{reg}
	var got []int64
	for i := 0; i < 4; i++ {
		n, ok := SelectNode(u, fixed)
		if !ok {
			t.Fatalf("第 %d 次选点失败", i+1)
		}
		got = append(got, n.ID)
	}
	// 权重均 1 的纯轮询序列：交替 101,102,101,102。
	want := []int64{101, 102, 101, 102}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("平局应回落轮询交替\n got: %v\nwant: %v", got, want)
	}
}

// TestSelectPriorityFallback 优先级回落：高优健康 → 备份 → 不可用。
func TestSelectPriorityFallback(t *testing.T) {
	u, reg := newTestUpstream(t, AlgoRoundRobin, []int{1, 1},
		[]Priority{PriorityPrimary, PriorityBackup})

	// 高优健康：永远选高优，不碰备份。
	for i := 0; i < 3; i++ {
		n, ok := SelectNode(u, reg)
		if !ok || n.ID != 101 || n.Priority != PriorityPrimary {
			t.Fatalf("高优健康时应选 101, got %+v ok=%v", n, ok)
		}
	}
	// 高优判死 → 回落备份。
	reg.SetHealth(101, HealthBad)
	n, ok := SelectNode(u, reg)
	if !ok || n.ID != 102 || n.Priority != PriorityBackup {
		t.Fatalf("高优全不健康应回落备份 102, got %+v ok=%v", n, ok)
	}
	// 备份也判死 → 不可用。
	reg.SetHealth(102, HealthBad)
	if n, ok := SelectNode(u, reg); ok {
		t.Fatalf("全部不可用应返回 ok=false, got %+v", n)
	}
	// 失效节点恢复 → 重新可用（高优优先）。
	reg.SetHealth(101, HealthOK)
	n, ok = SelectNode(u, reg)
	if !ok || n.ID != 101 {
		t.Fatalf("恢复后应重新选高优 101, got %+v ok=%v", n, ok)
	}
}

// TestSelectIncrementsInflight 选中即在途 +1（策略选点与 sticky 直路由同口径）。
func TestSelectIncrementsInflight(t *testing.T) {
	u, reg := newTestUpstream(t, AlgoRoundRobin, []int{1, 1}, nil)
	before := reg.Inflight(101)
	if _, ok := SelectNode(u, reg); !ok {
		t.Fatal("选点失败")
	}
	if got := reg.Inflight(101) - before; got != 1 {
		t.Fatalf("策略选点应在途 +1, 实际 +%d", got)
	}
	// sticky 直路由同样计入。
	before2 := reg.Inflight(102)
	if _, ok := SelectStickyNode(u, reg, 102); !ok {
		t.Fatal("sticky 直路由失败")
	}
	if got := reg.Inflight(102) - before2; got != 1 {
		t.Fatalf("sticky 直路由应在途 +1, 实际 +%d", got)
	}
}

// TestSelectUnavailableNoSideEffect 全部不可用时不得产生计数副作用。
func TestSelectUnavailableNoSideEffect(t *testing.T) {
	u, reg := newTestUpstream(t, AlgoLeastConn, []int{1, 1}, nil)
	for _, n := range u.Nodes {
		reg.SetHealth(n.ID, HealthBad)
	}
	if _, ok := SelectNode(u, reg); ok {
		t.Fatal("全部不可用应返回 ok=false")
	}
	for _, n := range u.Nodes {
		if got := reg.Inflight(n.ID); got != 0 {
			t.Fatalf("不可用选点不应产生计数, 节点 %d 在途 %d", n.ID, got)
		}
	}
}

// TestPickHealthyReadonlyNoSideEffect 只读选点（match-test 路径）：多次调用
// 不得推进共享轮询游标、不得产生在途计数。
func TestPickHealthyReadonlyNoSideEffect(t *testing.T) {
	u, reg := newTestUpstream(t, AlgoRoundRobin, []int{1, 2}, nil)
	before := append([]int(nil), u.rr.current...)
	for i := 0; i < 6; i++ {
		if n := pickHealthyRO(u, reg, PriorityPrimary); n == nil {
			t.Fatalf("第 %d 次只读选点失败", i+1)
		}
	}
	if !reflect.DeepEqual(before, u.rr.current) {
		t.Fatalf("只读选点不得推进游标\n before: %v\n after: %v", before, u.rr.current)
	}
	for _, n := range u.Nodes {
		if got := reg.Inflight(n.ID); got != 0 {
			t.Fatalf("只读选点不应计数, 节点 %d 在途 %d", n.ID, got)
		}
	}
}

// TestPickHealthyReadonlyLeastConnTie least_conn 平局回落也走只读试算：
// 屏蔽 +1 反馈后多次调用游标零推进（平局序列恒为首候选，不交替）。
func TestPickHealthyReadonlyLeastConnTie(t *testing.T) {
	u, reg := newTestUpstream(t, AlgoLeastConn, []int{1, 1}, nil)
	before := append([]int(nil), u.rr.current...)
	for i := 0; i < 4; i++ {
		n := pickHealthyRO(u, reg, PriorityPrimary)
		if n == nil || n.ID != 101 {
			t.Fatalf("零态平局只读试算应恒选首候选 101, got %+v", n)
		}
	}
	if !reflect.DeepEqual(before, u.rr.current) {
		t.Fatalf("least_conn 平局只读回落不得推进游标\n before: %v\n after: %v", before, u.rr.current)
	}
}
