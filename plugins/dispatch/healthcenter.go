// 健康检查中心（S5 供应商-消费者模型的生产者侧）：探活任务管理器。
//
// 职责（STEP4）：
//   - 任务集计算：Rebuild 时从对象图输入行计算「被启用均衡器经有效关系引用的
//     启用节点」去重集合（多均衡器引用同节点只探一份）；
//   - 差量增减：新增启探、移除停探并等待退出、探活参数变更视为差量重启该节点
//     任务；任务保留的节点 registry 记录不动（跨热更保序），差量移除即清记录
//     转灰；
//   - 探活执行：每节点一个 goroutine，启动即探一次（防窗口期流量全打坏节点），
//     随后按 hc_interval_ms 周期探测；2xx/3xx 健康、其余（含网络错误/超时）判死；
//     hc_path 为空 = 不探活、视为健康（免探活登记集，登记显绿、不启任务）；
//   - 供应商角色：探活结论经 Registry.SetHealth 写入，消费侧（选点/sticky）只读。
//
// 停止语义：Stop 全量排空（停止全部任务并等待 goroutine 退出，供 STEP5 主件
// Stop 调用）；单任务经 chan 关闭 + done 应答排空，无泄漏。
package dispatch

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/iotames/easyserver/log"
)

// 探活参数兜底默认值：节点表 hc_interval_ms/hc_timeout_ms 非正值时使用
// （hc_path 为空的节点不探活，不涉及本默认值）。
const (
	defaultHCIntervalMS = 5000 // 默认探活周期 5s
	defaultHCTimeoutMS  = 3000 // 默认单次探测超时 3s
)

// hcTaskKey 探活任务的差量比对键：参数任一变化即视为不同任务（差量重启）。
type hcTaskKey struct {
	nodeID   int64
	url      string
	path     string
	interval int
	timeout  int
}

// hcTask 单节点探活任务运行态。
type hcTask struct {
	key  hcTaskKey
	stop chan struct{} // 关闭即通知 goroutine 退出
	done chan struct{} // goroutine 退出应答（排空用）
}

// HealthCenter 探活任务管理器：维护「节点 id → 探活任务」映射并驱动 registry 记录集。
type HealthCenter struct {
	reg    *Registry    // 供应商：探活结论写入
	client *http.Client // 探活专用客户端（复用连接池；超时经 per-request context 独立控制）

	mu      sync.Mutex
	members map[int64]hcTaskKey   // 记录集成员表：当前占用 registry 记录的节点（任务集 ∪ 免探活登记集）
	tasks   map[hcTaskKey]*hcTask // 探活任务集（仅 path 非空节点；键含参数）
}

// NewHealthCenter 创建健康检查中心（绑定真 registry；装配归 STEP5）。
func NewHealthCenter(reg *Registry) *HealthCenter {
	return &HealthCenter{
		reg:     reg,
		client:  &http.Client{},
		members: make(map[int64]hcTaskKey),
		tasks:   make(map[hcTaskKey]*hcTask),
	}
}

// Rebuild 热更重建入口（STEP5 主件 Rebuild 时调用）：按对象图输入行计算任务集，
// 与当前任务集差量增减（新增启探 / 移除停探排空 / 参数变更重启），并同步维护
// registry 记录集（任务集 ∪ 免探活登记集口径）。
func (h *HealthCenter) Rebuild(in *GraphInput) {
	if in == nil {
		return
	}
	// ① 任务集计算：被启用均衡器（Enabled）经启用关系（Enabled）引用的启用节点
	// （Enabled），按节点 id 去重；参数取节点表 hc_* 字段。
	// （GraphInput 约定调用方只传参与构建的行，此处再按 Enabled 过滤兜底。）
	enabledUp := make(map[int64]bool, len(in.Upstreams))
	for i := range in.Upstreams {
		if in.Upstreams[i].Enabled {
			enabledUp[in.Upstreams[i].ID] = true
		}
	}
	nodeByID := make(map[int64]*NodeRow, len(in.Nodes))
	for i := range in.Nodes {
		if in.Nodes[i].Enabled {
			nodeByID[in.Nodes[i].ID] = &in.Nodes[i]
		}
	}
	// 去重集合：节点 id → 探活参数键（多均衡器引用同节点只算一份）。
	taskKeys := make(map[int64]hcTaskKey)
	for i := range in.Relations {
		rel := &in.Relations[i]
		if !rel.Enabled || !enabledUp[rel.UpstreamID] {
			continue
		}
		n := nodeByID[rel.NodeID]
		if n == nil {
			continue
		}
		if _, ok := taskKeys[n.ID]; ok {
			continue // 去重：同节点已被其他均衡器引用
		}
		taskKeys[n.ID] = hcTaskKey{
			nodeID:   n.ID,
			url:      n.URL,
			path:     n.HCPath,
			interval: n.HCIntervalMS,
			timeout:  n.HCTimeoutMS,
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// ② 差量移除（按记录集成员表）：新集不含的节点 → 停探清记录转灰；参数变更
	// 的节点 → 停旧任务、记录保留（保序），交由下方按新参数重新登记启探。
	// 「跨热更保序」保留不清零仅指任务仍保留的节点。
	for id, old := range h.members {
		key, keep := taskKeys[id]
		if !keep {
			if t, ok := h.tasks[old]; ok {
				h.stopTaskLocked(t)
				delete(h.tasks, old)
			}
			delete(h.members, id)
			h.reg.Remove(id) // 差量移除：记录清空，节点转灰
			continue
		}
		if key != old { // 参数变更（含探活 ↔ 免探活转换）：差量重启
			if t, ok := h.tasks[old]; ok {
				h.stopTaskLocked(t)
				delete(h.tasks, old)
			}
			delete(h.members, id)
		}
	}

	// ③ 差量新增：登记记录（免探活显绿 / 探活待首探）并启任务；键相同的保留
	// 成员记录与任务均不动（保序）。
	for id, key := range taskKeys {
		if _, ok := h.members[id]; ok {
			continue // 任务与记录保留：探活 goroutine 继续在跑，结论不清零
		}
		h.members[id] = key
		if key.path == "" {
			// 免探活登记集：hc_path 为空的被引用节点不启任务，登记健康态显绿。
			h.reg.Ensure(id, HealthOK)
			continue
		}
		h.reg.Ensure(id, HealthUnknown) // 探活任务节点：先登记灰态，首探出结论
		t := &hcTask{key: key, stop: make(chan struct{}), done: make(chan struct{})}
		h.tasks[key] = t
		go h.run(t)
	}
}

// Stop 组件级全量排空：停止全部探活任务并等待 goroutine 退出（供 STEP5 主件
// Stop 调用；幂等——空任务集时为 no-op）。
func (h *HealthCenter) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, t := range h.tasks {
		h.stopTaskLocked(t)
		delete(h.tasks, key)
	}
	h.members = make(map[int64]hcTaskKey)
	// 排空探活客户端连接池：空闲连接的读写 goroutine 随之退出，全量排空不留尾巴。
	h.client.CloseIdleConnections()
}

// stopTaskLocked 停止单个任务并阻塞等待 goroutine 退出（须持 h.mu）。
// run() 内部不取 h.mu（仅写 registry），无死锁风险。
func (h *HealthCenter) stopTaskLocked(t *hcTask) {
	close(t.stop)
	<-t.done
}

// run 单节点探活主循环：启动即探一次（防窗口期），随后按周期探测；
// stop 关闭即排空退出。
func (h *HealthCenter) run(t *hcTask) {
	defer close(t.done)
	interval := time.Duration(t.key.interval) * time.Millisecond
	if interval <= 0 {
		interval = time.Duration(defaultHCIntervalMS) * time.Millisecond
	}
	h.probe(t)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
			h.probe(t)
		}
	}
}

// probe 单次探测：GET url+path（per-request context 超时独立于转发链），
// 2xx/3xx 健康、其余/网络错误/超时判死；结论写 registry（供应商角色）。
func (h *HealthCenter) probe(t *hcTask) {
	timeout := time.Duration(t.key.timeout) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Duration(defaultHCTimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	url := t.key.url + t.key.path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		h.reg.SetHealth(t.key.nodeID, HealthBad)
		log.Warn("dispatch: 健康探测请求构建失败", "node_id", t.key.nodeID, "url", url, "err", err)
		return
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.reg.SetHealth(t.key.nodeID, HealthBad)
		log.Warn("dispatch: 健康探测失败", "node_id", t.key.nodeID, "url", url, "err", err)
		return
	}
	_ = resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 400
	h.reg.SetHealth(t.key.nodeID, healthState(ok))
	if !ok {
		log.Warn("dispatch: 健康探测判定不健康", "node_id", t.key.nodeID, "url", url, "status", resp.StatusCode)
	}
}

// healthState 探活结论 → 健康三态。
func healthState(ok bool) HealthState {
	if ok {
		return HealthOK
	}
	return HealthBad
}

// taskCount 当前任务数（测试断言与自检用：多均衡器引用同节点只探一份）。
func (h *HealthCenter) taskCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.tasks)
}
