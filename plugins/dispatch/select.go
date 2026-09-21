// 选点引擎（新对象图）：策略选点 + 高优/备份回落 + 在途计数 +1。
//
// 选点统一语义：
//  1. 高优节点（priority=0）健康集优先；
//  2. 高优全部不健康 → 回落备份节点（priority=1）健康集；
//  3. 再无 → 不可用（ok=false，由调用方写 503 中断链）。
//
// 算法（UpstreamRT.Algo）：
//   - round_robin（1）：平滑加权轮询（复用 balancer.go rrState 思路，作用于新对象图）；
//   - least_conn（2）：读 registry 在途计数取最小；平局回落轮询游标。
//
// 选中即调 registry 在途 +1（sticky 直路由同样真实计入；递减由转发完成后
// 的 Tail 收尾件统一执行，见 tailfin.go）。
package dispatch

// SelectNode 按均衡器策略选择一个健康节点并计在途 +1。
// 全部不可用（无可达健康节点）返回 ok=false，不产生计数副作用。
func SelectNode(u *UpstreamRT, reg NodeRegistry) (*NodeRT, bool) {
	if n := pickHealthy(u, reg, PriorityPrimary); n != nil {
		return count(reg, n), true
	}
	if n := pickHealthy(u, reg, PriorityBackup); n != nil {
		return count(reg, n), true
	}
	return nil, false
}

// SelectStickyNode sticky 直路由：Cookie 所指节点在当前均衡器关系内且健康即直路由
// （优先于策略选点；不校验 priority——粘性优先于高优回落）。命中计在途 +1。
// 不在关系内 / 不健康 / 未登记返回 ok=false（调用方回落策略选点）。
func SelectStickyNode(u *UpstreamRT, reg NodeRegistry, nodeID int64) (*NodeRT, bool) {
	for _, n := range u.Nodes {
		if n.ID == nodeID && reg.Health(n.ID) == HealthOK {
			return count(reg, n), true
		}
	}
	return nil, false
}

// count 选中收尾：在途 +1 并返回该节点（统一收口，杜绝漏计）。
func count(reg NodeRegistry, n *NodeRT) *NodeRT {
	reg.IncInflight(n.ID)
	return n
}

// pickHealthy 在指定优先级的健康节点集中按算法选点；空集返回 nil。
func pickHealthy(u *UpstreamRT, reg NodeRegistry, p Priority) *NodeRT {
	cands := make([]*NodeRT, 0, len(u.Nodes))
	for _, n := range u.Nodes {
		if n.Priority == p && reg.Health(n.ID) == HealthOK {
			cands = append(cands, n)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	if u.Algo == AlgoLeastConn {
		return u.pickLeastConn(reg, cands)
	}
	return u.pickRR(cands)
}

// pickRR 平滑加权轮询选点（balancer.go rrState 同款算法，作用于新对象图：
// current 按节点在 UpstreamRT.Nodes 中的下标索引，候选子集共享同一游标——
// 与旧实现高优/备份子集共享游标的口径一致）。
func (u *UpstreamRT) pickRR(cands []*NodeRT) *NodeRT {
	if len(cands) == 1 {
		return cands[0]
	}
	u.rr.mu.Lock()
	defer u.rr.mu.Unlock()

	total := 0
	best := cands[0]
	bestCur := 0
	for _, n := range cands {
		w := n.Weight
		if w <= 0 {
			w = 1
		}
		cur := u.rr.current[u.indexOf(n)] + w
		u.rr.current[u.indexOf(n)] = cur
		total += w
		if cur > bestCur {
			best, bestCur = n, cur
		}
	}
	u.rr.current[u.indexOf(best)] -= total
	return best
}

// pickLeastConn 最小连接优先：候选中取在途最少者；平局集合回落轮询游标
// （平滑加权在平局集内推进，保持权重语义与分布稳定性）。
func (u *UpstreamRT) pickLeastConn(reg NodeRegistry, cands []*NodeRT) *NodeRT {
	minCands := make([]*NodeRT, 0, len(cands))
	minInflight := int64(-1)
	for _, n := range cands {
		inflight := reg.Inflight(n.ID)
		if minInflight < 0 || inflight < minInflight {
			minInflight = inflight
			minCands = minCands[:0]
			minCands = append(minCands, n)
			continue
		}
		if inflight == minInflight {
			minCands = append(minCands, n)
		}
	}
	if len(minCands) == 1 {
		return minCands[0]
	}
	return u.pickRR(minCands)
}

// indexOf 返回节点在 UpstreamRT.Nodes 中的下标（共享轮询游标）。
func (u *UpstreamRT) indexOf(n *NodeRT) int {
	for i, cand := range u.Nodes {
		if cand == n {
			return i
		}
	}
	return 0
}
