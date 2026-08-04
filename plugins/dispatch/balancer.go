// 负载均衡：上游节点模型 + 平滑加权轮询 / 一致性哈希 + 高优节点优先。
//
// 选点语义（§10.5）：
//   1. 优先在高优节点（Priority=0）中选健康的；
//   2. 高优全部不健康 → 在备份节点（Priority=1）中选健康的；
//   3. 无任何健康节点（且已配置健康检查）→ 返回 ok=false，由 Handle 写 503 中断链。
//
// 算法（Rule.Algo）：
//   - roundrobin（默认）：平滑加权轮询，请求均匀分散到各节点。
//   - chash：一致性哈希（按 key 稳定取模），同一 key 的请求固定打到同一节点，
//     用于会话保持 / 缓存亲和。key 支持 $remote_addr（默认）、$http_<name>、$cookie_<name>。
package dispatch

import (
	"net/http"
	"sync"
	"sync/atomic"
)

// 负载均衡算法名。
const (
	AlgoRoundRobin = "roundrobin"
	AlgoCHash      = "chash"
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

// Select 按"高优优先 + 算法（轮询/一致性哈希）"选择一个健康节点的 URL。
// req 用于 chash 提取哈希 key（roundrobin 忽略，传 nil 亦可）。
// 全部不可达（已配置健康检查）返回 ok=false。
func (r *Rule) Select(req *http.Request) (string, bool) {
	if high := r.healthyNodes(0); len(high) > 0 {
		return r.pick(req, high)
	}
	if backup := r.healthyNodes(1); len(backup) > 0 {
		return r.pick(req, backup)
	}
	return "", false
}

// pick 在候选健康节点上按算法选点。
func (r *Rule) pick(req *http.Request, nodes []*Node) (string, bool) {
	if len(nodes) == 0 {
		return "", false
	}
	if r.Algo == AlgoCHash {
		return r.pickChash(req, nodes)
	}
	return r.pickRR(nodes)
}

// pickRR 平滑加权轮询选点。
func (r *Rule) pickRR(nodes []*Node) (string, bool) {
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

// pickChash 一致性哈希选点：按 key（remote_addr / header / cookie）稳定取模。
// 未配置 key 或提取为空时回退平滑加权轮询（避免全部哈希到同一节点）。
func (r *Rule) pickChash(req *http.Request, nodes []*Node) (string, bool) {
	if req == nil {
		return r.pickRR(nodes)
	}
	key := extractHashKey(req, r.ChashKey)
	if key == "" {
		return r.pickRR(nodes)
	}
	if len(nodes) == 1 {
		return nodes[0].URL, true
	}
	idx := hashKey(key) % uint32(len(nodes))
	return nodes[idx].URL, true
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
