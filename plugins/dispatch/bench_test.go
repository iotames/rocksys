// 匹配性能基准（STEP8）：100 与 1000 规则两档快照下单次 Match 耗时，
// 绝对门槛 ≤ 10µs（ROUTE_DISPATCH 设计 M 表性能验收）。
//
// 快照构成：域名规则（每 5 条带独立域名）与无域名规则混合，路径类型按
// 前缀 / 精确 / 模式轮转，覆盖命中（域名命中 / 纯路径命中）与全未命中场景。
// 执行：go test -bench . -benchtime=1x -run '^$' ./plugins/dispatch/
package dispatch

import (
	"fmt"
	"testing"
)

// benchSnapshot 构造 n 条规则的测试快照（单均衡器 + 单节点，规则条目差异仅在
// 匹配条件，均衡与转发不在本基准度量范围）。
// 规则形态按 i 轮转：
//   - i%3==0：前缀 /api<i>
//   - i%3==1：精确 /exact<i>
//   - i%3==2：模式 /mode<i>/:id
//
// 每 5 条（i%5==0）附加域名 d<i>.example.com；其余无域名（任意 Host）。
func benchSnapshot(b *testing.B, n int) *RouteSnapshot {
	b.Helper()
	in := &GraphInput{
		Upstreams: []UpstreamRow{{ID: 1, Name: "bench-up", Algo: int(AlgoRoundRobin), Enabled: true}},
		Nodes:     []NodeRow{{ID: 1, URL: "http://127.0.0.1:1", Enabled: true}},
		Relations: []UpstreamNodeRow{{UpstreamID: 1, NodeID: 1, Weight: 1}},
	}
	for i := 0; i < n; i++ {
		r := RuleRow{
			ID:         int64(i + 1),
			MatchOrder: i%999 + 1, // 1–999 循环取值（允许重复，排序按 order,id 稳定）
			PathType:   []int{int(PathTypePrefix), int(PathTypeExact), int(PathTypeMode)}[i%3],
			UpstreamID: 1,
			Enabled:    true,
		}
		switch r.PathType {
		case int(PathTypePrefix):
			r.PathValue = fmt.Sprintf("/api%d", i)
		case int(PathTypeExact):
			r.PathValue = fmt.Sprintf("/exact%d", i)
		default:
			r.PathValue = fmt.Sprintf("/mode%d/:id", i)
		}
		if i%5 == 0 {
			r.Domain = fmt.Sprintf("d%d.example.com", i)
		}
		in.Rules = append(in.Rules, r)
	}
	snap, err := BuildGraph(in)
	if err != nil {
		b.Fatalf("构建 %d 规则快照失败: %v", n, err)
	}
	return snap
}

// BenchmarkMatch 单次匹配耗时：两档规模 × 三场景（域名命中 / 纯路径命中 / 全未命中）。
func BenchmarkMatch(b *testing.B) {
	scenarios := []struct {
		name string
		host string
		path string
	}{
		// 域名命中：命中中间位置的域名规则（i=500/50，前缀型），需先扫描跳过
		// 更靠前的无域名前缀/精确/模式规则。
		{"domain_hit", "d500.example.com", "/api500/x"},
		// 纯路径命中：未知 Host 跳过全部域名规则后命中无域名精确规则。
		{"path_hit", "other.example.com", "/exact999"},
		// 全未命中：未知 Host + 未匹配路径，全表扫描后返回 nil（最坏情形）。
		{"miss", "other.example.com", "/nope/deep/path"},
	}
	for _, size := range []int{100, 1000} {
		for _, sc := range scenarios {
			host, path := sc.host, sc.path
			switch sc.name {
			case "domain_hit":
				// 取 i 为 15 的倍数（i%3==0 前缀型且 i%5==0 带域名），
				// 两档各取偏后位置的目标规则：100 档 i=90、1000 档 i=990。
				i := 90
				if size == 1000 {
					i = 990
				}
				host, path = fmt.Sprintf("d%d.example.com", i), fmt.Sprintf("/api%d/x", i)
			case "path_hit":
				// 精确规则按 i%3==1 生成，且排除 i%5==0 的带域名行，
				// 从后往前找第一个无域名精确规则（命中前需跳过其后全部规则）。
				i := size - 1
				for i%3 != 1 || i%5 == 0 {
					i--
				}
				path = fmt.Sprintf("/exact%d", i)
			}
			snap := benchSnapshot(b, size)
			b.Run(fmt.Sprintf("%d/%s", size, sc.name), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if rule, _ := Match(snap, host, path); rule == nil && sc.name != "miss" {
						b.Fatalf("预期命中未命中: %s %s", host, path)
					}
				}
			})
		}
	}
}
