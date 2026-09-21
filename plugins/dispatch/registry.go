// 节点状态 registry：健康三态与在途计数的单一事实源（S5 供应商-消费者模型）。
//
// STEP2 旁路新建：选点引擎（select.go）与 sticky（sticky.go）只依赖本接口，
// 健康态判定经注入完成——不依赖真实健康检查（STEP4 才生产）。MemRegistry 为
// 内存桩实现（map + 原子），供单测预设健康/失效态；STEP4 真 registry 实现同一
// 接口后无缝替换（接口方法集刻意最小：读健康三态、在途 +1/-1、写健康态）。
package dispatch

import (
	"sync"
	"sync/atomic"
)

// HealthState 节点健康三态（内存态，不落库；数据字典红线不涉及）。
type HealthState int32

// 健康三态枚举（内存展示口径：绿/红/灰，与 WebUI 健康列对应）。
const (
	HealthUnknown HealthState = iota // 未探活（灰）：尚未产生任何探活结论
	HealthOK                         // 健康（绿）：最近一次探活 2xx/3xx，或免探活登记
	HealthBad                        // 不健康（红）：最近一次探活失败
)

// NodeRegistry 节点状态接口（选点/sticky/收尾递减的唯一依赖面）。
//
// 设计约束：以节点 id 为键（跨热更稳定），不暴露底层存储形态；STEP4 真实现
// （探活 goroutine 生产 + atomic 承载）实现同接口即可替换，调用方零改动。
type NodeRegistry interface {
	// Health 查询节点健康三态。未登记节点返回 HealthUnknown。
	Health(nodeID int64) HealthState
	// IncInflight 在途计数 +1（选点选中与 sticky 直路由均须真实计入——S4）。
	IncInflight(nodeID int64)
	// Inflight 读取节点在途计数（least_conn 取最小；未登记返回 0）。
	Inflight(nodeID int64) int64
	// DecInflight 在途计数 -1（STEP4 Tail 收尾件调用；饱和处理 ≥0，
	// 记录不存在时 no-op——防节点失引用重登后迟到递减打成负数）。
	DecInflight(nodeID int64)
}

// memNode registry 单节点状态（原子字段，免锁读写）。
type memNode struct {
	health   atomic.Int32 // HealthState
	inflight atomic.Int64 // 在途请求计数
}

// MemRegistry 内存桩实现（map + 原子；测试可预设健康/失效态）。
// map 结构变化经互斥锁保护，字段读写走原子（与 STEP4 真实现同款并发口径）。
type MemRegistry struct {
	mu    *sync.RWMutex
	nodes map[int64]*memNode
}

// NewMemRegistry 创建空的内存 registry 桩。
func NewMemRegistry() *MemRegistry {
	return &MemRegistry{mu: &sync.RWMutex{}, nodes: make(map[int64]*memNode)}
}

// Ensure 登记节点（不存在则创建，默认未探活态）；已存在则幂等。
func (m *MemRegistry) Ensure(nodeID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes[nodeID]; !ok {
		m.nodes[nodeID] = &memNode{}
	}
}

// SetHealth 预设节点健康态（未登记自动登记；测试注入用）。
func (m *MemRegistry) SetHealth(nodeID int64, s HealthState) {
	m.mu.Lock()
	n := m.nodes[nodeID]
	if n == nil {
		n = &memNode{}
		m.nodes[nodeID] = n
	}
	m.mu.Unlock()
	n.health.Store(int32(s))
}

// Health 实现 NodeRegistry。未登记节点返回 HealthUnknown。
func (m *MemRegistry) Health(nodeID int64) HealthState {
	m.mu.RLock()
	n := m.nodes[nodeID]
	m.mu.RUnlock()
	if n == nil {
		return HealthUnknown
	}
	return HealthState(n.health.Load())
}

// IncInflight 实现 NodeRegistry（未登记自动登记——选中即真实计入）。
func (m *MemRegistry) IncInflight(nodeID int64) {
	m.mu.Lock()
	n := m.nodes[nodeID]
	if n == nil {
		n = &memNode{}
		m.nodes[nodeID] = n
	}
	m.mu.Unlock()
	n.inflight.Add(1)
}

// DecInflight 实现 NodeRegistry：饱和递减；记录不存在时 no-op（S4）。
func (m *MemRegistry) DecInflight(nodeID int64) {
	m.mu.RLock()
	n := m.nodes[nodeID]
	m.mu.RUnlock()
	if n == nil {
		return
	}
	for {
		cur := n.inflight.Load()
		if cur <= 0 || n.inflight.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

// Inflight 读取在途计数（测试断言与 WebUI 透出用）。
func (m *MemRegistry) Inflight(nodeID int64) int64 {
	m.mu.RLock()
	n := m.nodes[nodeID]
	m.mu.RUnlock()
	if n == nil {
		return 0
	}
	return n.inflight.Load()
}

// Registry 真 registry 实现（STEP4）：健康检查中心（生产者）与选点/收尾件
// （消费者）之间的单一事实源，与 MemRegistry 桩实现同一 NodeRegistry 接口。
//
// 记录集口径（S5）：记录集 = 探活任务集 ∪ 免探活登记集（hc_path 为空的被引用
// 节点登记为健康态显绿）；健康检查中心 Rebuild 差量时经 Ensure/SetHealth/Remove
// 维护：任务保留的节点记录不动（跨热更保序，状态不清零），差量移除即 Remove
// 转灰（HealthUnknown）。map 结构变化经互斥锁保护，字段读写走原子（与桩同款
// 并发口径）。
type Registry struct {
	mu    *sync.RWMutex
	nodes map[int64]*memNode
}

// NewRegistry 创建空的真 registry。
func NewRegistry() *Registry {
	return &Registry{mu: &sync.RWMutex{}, nodes: make(map[int64]*memNode)}
}

// Ensure 登记节点并设初始健康态：探活任务节点登记 HealthUnknown（等首探出结论），
// 免探活节点登记 HealthOK（显绿）。已存在则幂等保留现态（跨热更保序：仅更新
// 健康态语义上的初始值不覆盖既有探活结论，重登不清零）。
func (r *Registry) Ensure(nodeID int64, init HealthState) {
	r.mu.Lock()
	n := r.nodes[nodeID]
	if n == nil {
		n = &memNode{}
		r.nodes[nodeID] = n
	}
	r.mu.Unlock()
	// 仅在从未产生结论时落初始态（原子 CAS 语义：未知态才写，避免覆盖探活结论）。
	n.health.CompareAndSwap(int32(HealthUnknown), int32(init))
}

// SetHealth 写节点健康态（健康检查中心探活结论的唯一写入口；未登记自动登记）。
func (r *Registry) SetHealth(nodeID int64, s HealthState) {
	r.mu.Lock()
	n := r.nodes[nodeID]
	if n == nil {
		n = &memNode{}
		r.nodes[nodeID] = n
	}
	r.mu.Unlock()
	n.health.Store(int32(s))
}

// Remove 清除节点记录（任务差量移除时调用）：节点转灰（HealthUnknown），
// 在途计数随记录一并消失。之后重新登记从零开始，迟到递减因记录缺失 no-op，
// 不会打成负数。
func (r *Registry) Remove(nodeID int64) {
	r.mu.Lock()
	delete(r.nodes, nodeID)
	r.mu.Unlock()
}

// Len 返回当前记录数（健康检查中心自检与测试断言用）。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.nodes)
}

// Health 实现 NodeRegistry。未登记节点返回 HealthUnknown。
func (r *Registry) Health(nodeID int64) HealthState {
	r.mu.RLock()
	n := r.nodes[nodeID]
	r.mu.RUnlock()
	if n == nil {
		return HealthUnknown
	}
	return HealthState(n.health.Load())
}

// IncInflight 实现 NodeRegistry（未登记自动登记——选中即真实计入）。
func (r *Registry) IncInflight(nodeID int64) {
	r.mu.Lock()
	n := r.nodes[nodeID]
	if n == nil {
		n = &memNode{}
		r.nodes[nodeID] = n
	}
	r.mu.Unlock()
	n.inflight.Add(1)
}

// DecInflight 实现 NodeRegistry：饱和递减（≥0）；记录不存在时 no-op（S4——
// 节点失引用清记录后重新登记，迟到递减不得打成负数）。
func (r *Registry) DecInflight(nodeID int64) {
	r.mu.RLock()
	n := r.nodes[nodeID]
	r.mu.RUnlock()
	if n == nil {
		return
	}
	for {
		cur := n.inflight.Load()
		if cur <= 0 || n.inflight.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

// Inflight 实现在途计数读取（least_conn 取最小）。
func (r *Registry) Inflight(nodeID int64) int64 {
	r.mu.RLock()
	n := r.nodes[nodeID]
	r.mu.RUnlock()
	if n == nil {
		return 0
	}
	return n.inflight.Load()
}

// 编译期断言：真 registry 实现节点状态接口。
var _ NodeRegistry = (*Registry)(nil)
