// model_test.go：对象图构建与 ValidateGraph 校验矩阵（表驱动）。
package dispatch

import (
	"strings"
	"testing"
)

// validGraph 一份合法的四表行输入（各用例在此基础上覆写制造非法项）。
func validGraph() *GraphInput {
	return &GraphInput{
		Upstreams: []UpstreamRow{
			{ID: 7, Name: "订单池", Algo: 1, StickyEnabled: true, StickyCookie: "rocksys_node", Enabled: true},
		},
		Nodes: []NodeRow{
			{ID: 101, URL: "http://o1:9001", Enabled: true},
			{ID: 102, URL: "https://o2:9002", Enabled: true},
		},
		Relations: []UpstreamNodeRow{
			{ID: 1, UpstreamID: 7, NodeID: 101, Weight: 2, Priority: 0, Enabled: true},
			{ID: 2, UpstreamID: 7, NodeID: 102, Weight: 1, Priority: 1, Enabled: true},
		},
		Rules: []RuleRow{
			{ID: 31, MatchOrder: 10, Domain: "", PathType: 1, PathValue: "/api/", UpstreamID: 7, Enabled: true},
			{ID: 30, MatchOrder: 999, Domain: "A.COM", PathType: 3, PathValue: "/x/:id", UpstreamID: 7, Enabled: true},
		},
	}
}

// TestModelBuildGraph 对象图展开：关系展开、权重/优先级、规则排序、domain 归一。
func TestModelBuildGraph(t *testing.T) {
	snap, err := BuildGraph(validGraph())
	if err != nil {
		t.Fatalf("合法输入构建失败: %v", err)
	}
	u := snap.Upstreams[7]
	if u == nil || len(u.Nodes) != 2 {
		t.Fatalf("均衡器关系展开错误: %+v", u)
	}
	if u.Nodes[0].ID != 101 || u.Nodes[0].Weight != 2 || u.Nodes[0].Priority != PriorityPrimary {
		t.Errorf("节点 101 展开属性错误: %+v", u.Nodes[0])
	}
	if u.Nodes[1].ID != 102 || u.Nodes[1].Priority != PriorityBackup {
		t.Errorf("节点 102 展开属性错误: %+v", u.Nodes[1])
	}
	// 规则按 (match_order,id) 升序：999 的兜底规则排后。
	if len(snap.Rules) != 2 || snap.Rules[0].MatchOrder != 10 || snap.Rules[1].MatchOrder != 999 {
		t.Fatalf("规则排序错误: %+v", snap.Rules)
	}
	// domain 构建期归一：转小写放行，不拒绝。
	if got := snap.Rules[1].Domain; got != "a.com" {
		t.Errorf("domain 未归一小写: %q", got)
	}
	// 规则持均衡器引用。
	if snap.Rules[0].Upstream != u {
		t.Errorf("规则均衡器引用错误")
	}
}

// TestValidateGraph 合法基线：一份完整合法输入必须通过。
func TestValidateGraph(t *testing.T) {
	if err := ValidateGraph(validGraph()); err != nil {
		t.Fatalf("合法输入被误拒: %v", err)
	}
}

// TestValidateGraphInvalidMatrix 校验矩阵：逐项制造非法值，必须各产生对应报错。
func TestValidateGraphInvalidMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(g *GraphInput)
		want   string // 期望报错包含的片段
	}{
		{
			name:   "规则引用不存在的均衡器",
			mutate: func(g *GraphInput) { g.Rules[0].UpstreamID = 404 },
			want:   "均衡器不存在",
		},
		{
			name:   "关系引用不存在的节点",
			mutate: func(g *GraphInput) { g.Relations[0].NodeID = 404 },
			want:   "节点不存在",
		},
		{
			name:   "关系引用不存在的均衡器",
			mutate: func(g *GraphInput) { g.Relations[1].UpstreamID = 404 },
			want:   "均衡器不存在",
		},
		{
			name:   "节点 URL 非 http(s)",
			mutate: func(g *GraphInput) { g.Nodes[0].URL = "tcp://o1:9001" },
			want:   "http(s)://",
		},
		{
			name:   "权重非正整数",
			mutate: func(g *GraphInput) { g.Relations[0].Weight = 0 },
			want:   "weight 必须为正整数",
		},
		{
			name:   "权重负数",
			mutate: func(g *GraphInput) { g.Relations[0].Weight = -3 },
			want:   "weight 必须为正整数",
		},
		{
			name:   "priority 越界",
			mutate: func(g *GraphInput) { g.Relations[0].Priority = 2 },
			want:   "priority 非法",
		},
		{
			name:   "priority 负数",
			mutate: func(g *GraphInput) { g.Relations[0].Priority = -1 },
			want:   "priority 非法",
		},
		{
			name:   "match_order 低于下界",
			mutate: func(g *GraphInput) { g.Rules[0].MatchOrder = 0 },
			want:   "match_order",
		},
		{
			name:   "match_order 超上界",
			mutate: func(g *GraphInput) { g.Rules[0].MatchOrder = 1000 },
			want:   "match_order",
		},
		{
			name:   "path_value 不以 / 开头",
			mutate: func(g *GraphInput) { g.Rules[0].PathValue = "api/" },
			want:   "path_value",
		},
		{
			name:   "path_type 非法",
			mutate: func(g *GraphInput) { g.Rules[0].PathType = 4 },
			want:   "path_type 非法",
		},
		{
			name:   "algo 非法",
			mutate: func(g *GraphInput) { g.Upstreams[0].Algo = 3 },
			want:   "algo 非法",
		},
		{
			name: "均衡器无任何节点关系",
			mutate: func(g *GraphInput) {
				g.Relations = nil
				g.Rules = nil // 规则引用均衡器无碍，仅去关系
			},
			want: "无任何节点关系",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := validGraph()
			tc.mutate(g)
			err := ValidateGraph(g)
			if err == nil {
				t.Fatalf("期望报错（含 %q），实际通过", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("报错不含 %q: %v", tc.want, err)
			}
		})
	}
}

// TestValidateGraphDomainNormalize domain 归一放行：大写 domain 不拒绝、构建期静默转小写。
func TestValidateGraphDomainNormalize(t *testing.T) {
	g := validGraph()
	g.Rules[1].Domain = "MiXeD.Case.Example.COM"
	if err := ValidateGraph(g); err != nil {
		t.Fatalf("大写 domain 被误拒（应放行归一）: %v", err)
	}
	snap, err := BuildGraph(g)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if got := snap.Rules[1].Domain; got != "mixed.case.example.com" {
		t.Errorf("domain 归一结果错误: %q", got)
	}
}
