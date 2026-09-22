// Package dispatch 快照构建测试：(match_order,id) 稳定排序、domain 归一转小写、
// fail-closed（停用均衡器不吞规则）与参数注入辅助。
package dispatch

import "testing"

func TestSnapshot_BuildSortAndDomain(t *testing.T) {
	in := mkGraphInput([]RuleRow{
		{ID: 7, MatchOrder: 2, Domain: "B.EXAMPLE.COM", PathType: int(PathTypePrefix), PathValue: "/b", UpstreamID: 100, Enabled: true},
		{ID: 3, MatchOrder: 1, Domain: "a.example.com", PathType: int(PathTypePrefix), PathValue: "/a", UpstreamID: 100, Enabled: true},
		{ID: 5, MatchOrder: 2, Domain: "", PathType: int(PathTypeExact), PathValue: "/x", UpstreamID: 100, Enabled: true},
	})
	snap, err := BuildSnapshot(in)
	if err != nil {
		t.Fatalf("BuildSnapshot err: %v", err)
	}
	// (match_order, id) 稳定升序：order1 → order2 内 id 3<5<7。
	want := []int64{3, 5, 7}
	for i, id := range want {
		if snap.Rules[i].ID != id {
			t.Errorf("排序第 %d 位 want id=%d, got id=%d", i, id, snap.Rules[i].ID)
		}
	}
	// domain 构建期归一转小写（放行不拒绝）。
	if got := snap.Rules[2].Domain; got != "b.example.com" {
		t.Errorf("domain 归一错误: %q", got)
	}
	// 快照持均衡器对象图引用。
	if snap.Rules[0].Upstream == nil || snap.Rules[0].Upstream.ID != 100 {
		t.Error("规则应持均衡器引用")
	}
	if len(snap.Rules[0].Upstream.Nodes) != 1 || snap.Rules[0].Upstream.Nodes[0].URL != "http://n1:9001" {
		t.Error("均衡器节点展开错误")
	}
}

// TestSnapshot_FailClosed 停用均衡器/节点不吞规则：引用有效性不做构建期剔除。
func TestSnapshot_FailClosed(t *testing.T) {
	in := mkGraphInput([]RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 100, Enabled: true},
	})
	in.Upstreams[0].Enabled = false // 均衡器停用：规则仍留在快照（命中后由转发层 503）
	snap, err := BuildSnapshot(in)
	if err != nil {
		t.Fatalf("停用均衡器不应导致构建失败: %v", err)
	}
	if len(snap.Rules) != 1 || snap.Rules[0].ID != 1 {
		t.Fatalf("停用均衡器不应吞规则, got %v", snap.Rules)
	}
	in.Nodes[0].Enabled = false // 节点停用同理
	if _, err = BuildSnapshot(in); err != nil {
		t.Fatalf("停用节点不应导致构建失败: %v", err)
	}
}

// TestSnapshot_ReferIntegrity 引用不存在的均衡器仍为构建期校验错误（调用方保留旧快照）。
func TestSnapshot_ReferIntegrity(t *testing.T) {
	in := mkGraphInput([]RuleRow{
		{ID: 1, MatchOrder: 1, PathType: int(PathTypePrefix), PathValue: "/api", UpstreamID: 404, Enabled: true},
	})
	if _, err := BuildSnapshot(in); err == nil {
		t.Error("引用不存在的均衡器应返回校验错误")
	}
}

func TestSnapshot_RouteParamHeaders(t *testing.T) {
	got := RouteParamHeaders(map[string]string{"id": "42", "ver": "v2"})
	if got["X-Route-Param-id"] != "42" || got["X-Route-Param-ver"] != "v2" {
		t.Errorf("参数头键值对错误: %v", got)
	}
	if RouteParamHeaders(nil) != nil {
		t.Error("空参数应返回 nil")
	}
}
