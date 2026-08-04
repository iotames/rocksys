// 负载均衡：上游节点模型 + 平滑加权轮询 + 高优节点优先。
//
// 选点语义（§10.5）：
//   1. 优先在高优节点（Priority=0）中选健康的；
//   2. 高优全部不健康 → 在备份节点（Priority=1）中选健康的；
//   3. 无任何健康节点（且已配置健康检查）→ 返回 ok=false，由 Handle 写 503 中断链。
package dispatch

import (
	"sync"
	"sync/atomic"
)

// Node 上游节点。
type Node struct {
	URL      string // http(s)://host[:port]
	Weight   int    // 权重（>0，默认 1）
	Priority int    // 0=高优（默认），1=备份（高优全挂才用）
	healthy  atomic.Bool
}

// healthyNodes 返回指定优先级的健康节点列表。
func (r *Rule) healthyNodes(priority int) []*Node {
	out := make([]*Node, 0, len(r.Nodes))
	for _, n := range r.Nodes {
		if n.Priority == priority && n.healthy.Load() {
			out = append(out, n)
		}
	}
	return out
}

// Select 按"高优优先 + 平滑加权轮询"选择一个健康节点的 URL。
// 全部不可达（已配置健康检查）返回 ok=false。
func (r *Rule) Select() (string, bool) {
	if high := r.healthyNodes(0); len(high) > 0 {
		return r.pick(high)
	}
	if backup := r.healthyNodes(1); len(backup) > 0 {
		return r.pick(backup)
	}
	return "", false
}

// pick 在候选健康节点上执行平滑加权轮询，返回选中节点 URL。
func (r *Rule) pick(nodes []*Node) (string, bool) {
	if len(nodes) == 0 {
		return "", false
	}
	if len(nodes) == 1 {
		return nodes[0].URL, true
	}
	r.rr.mu.Lock()
	defer r.rr.mu.Unlock()

	total := 0
	bestIdx := 0
	for _, n := range nodes {
		w := n.Weight
		if w <= 0 {
			w = 1
		}
		idx := r.index(n)
		r.rr.current[idx] += w
		total += w
		if r.rr.current[idx] > r.rr.current[bestIdx] {
			bestIdx = idx
		}
	}
	r.rr.current[bestIdx] -= total

	for _, n := range nodes {
		if r.index(n) == bestIdx {
			return n.URL, true
		}
	}
	return nodes[0].URL, true
}

// index 返回节点在 Rule.Nodes 中的位置（用于共享轮询游标）。
func (r *Rule) index(n *Node) int {
	for i, cand := range r.Nodes {
		if cand == n {
			return i
		}
	}
	return 0
}

// rrState 平滑加权轮询状态（与 Rule.Nodes 等长）。
type rrState struct {
	mu      sync.Mutex
	current []int
}

func newRRState(n int) *rrState {
	return &rrState{current: make([]int, n)}
}
