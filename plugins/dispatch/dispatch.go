// Package dispatch L2 路由分发（转发链中间件）。
//
// 职责（ROUTE_DISPATCH 三层模型）：请求 Host + 路径 → 路由快照匹配 → 均衡器
// 选点（sticky 感知）→ 写 DataFlow.Target（在途 +1、sticky 种值）；未命中 →
// 不写 Target（Adapter 回退默认 upstream）。
// 规则源：DB 路由四表（S6，DB 为唯一规则源）——Rebuild 全量拉取构建快照，
// 原子替换；请求路径只读快照，零锁零阻塞。
package dispatch

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/db"
	"rocksys/internal/hotswap"

	"github.com/iotames/easyserver/log"
)

// 编译期断言：Dispatch 实现 hotswap.MiddlewareLifecycle。
var _ hotswap.MiddlewareLifecycle = (*Dispatch)(nil)

// RowSource 路由四表行来源接口（依赖注入：生产走 internal/db 的 SQL 脚本查询，
// 测试注入内存行集）。口径约定与 BuildSnapshot 一致：规则取启用行，
// 均衡器/节点/关系取全量有效行（fail-closed，停用不剔除）。
type RowSource interface {
	LoadRules() ([]RuleRow, error)
	LoadUpstreams() ([]UpstreamRow, error)
	LoadNodes() ([]NodeRow, error)
	LoadRelations() ([]UpstreamNodeRow, error)
}

// DBSource 基于 internal/db 的四表行来源：逐脚本查询（外挂 SQL 优先、内嵌兜底，
// 经 db.DB.SQL 统一加载）。dataDB 未就绪（nil）时全部查询报错——调用方走降级。
type DBSource struct{ d *db.DB }

// NewDBSource 创建 DB 行来源（d 可为 nil，此时视为 DB 不可用）。
func NewDBSource(d *db.DB) *DBSource { return &DBSource{d: d} }

// loadRows 读脚本（含 .sql 后缀）并替换 {table} 表名占位符后查询多行
// （表名为编译期常量，非用户输入；db.DB 不做占位符替换，由消费方负责——mq/shield 同款约定）。
// 行以 map 承载后手工映射到 Row 结构（easydb 结构体扫描要求逐列 db tag，模型行不设
// tag，手工映射保持 model.go 与建表脚本解耦）。
func (s *DBSource) loadRows(script, table string) ([]map[string]any, error) {
	txt, err := s.d.SQL(script)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	err = s.d.EasyDB().GetMany(strings.ReplaceAll(txt, "{table}", table), &rows)
	return rows, err
}

// LoadRules 拉启用规则行（dispatch_rule_query_active：enabled=1 且未软删）。
func (s *DBSource) LoadRules() ([]RuleRow, error) {
	if s.d == nil {
		return nil, errors.New("dispatch: 数据访问层未就绪")
	}
	rows, err := s.loadRows("dispatch_rule_query_active.sql", "dispatch_rule")
	if err != nil {
		return nil, err
	}
	out := make([]RuleRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, RuleRow{
			ID:         dbRowInt64(r["id"]),
			MatchOrder: int(dbRowInt64(r["match_order"])),
			Domain:     dbRowString(r["domain"]),
			PathType:   int(dbRowInt64(r["path_type"])),
			PathValue:  dbRowString(r["path_value"]),
			Title:      dbRowString(r["title"]),
			UpstreamID: dbRowInt64(r["upstream_id"]),
			Enabled:    true, // query_active 仅返回启用行
		})
	}
	return out, nil
}

// LoadUpstreams 拉全量有效均衡器行（dispatch_upstream_query_all_active：未软删，含停用）。
func (s *DBSource) LoadUpstreams() ([]UpstreamRow, error) {
	if s.d == nil {
		return nil, errors.New("dispatch: 数据访问层未就绪")
	}
	rows, err := s.loadRows("dispatch_upstream_query_all_active.sql", "dispatch_upstream")
	if err != nil {
		return nil, err
	}
	out := make([]UpstreamRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, UpstreamRow{
			ID:            dbRowInt64(r["id"]),
			Name:          dbRowString(r["name"]),
			Algo:          int(dbRowInt64(r["algo"])),
			StickyEnabled: dbRowInt64(r["sticky_enabled"]) == 1,
			StickyCookie:  dbRowString(r["sticky_cookie"]),
			Enabled:       dbRowInt64(r["enabled"]) == 1,
		})
	}
	return out, nil
}

// LoadNodes 拉全量有效节点行（dispatch_node_query_all_active：未软删，含停用）。
func (s *DBSource) LoadNodes() ([]NodeRow, error) {
	if s.d == nil {
		return nil, errors.New("dispatch: 数据访问层未就绪")
	}
	rows, err := s.loadRows("dispatch_node_query_all_active.sql", "dispatch_node")
	if err != nil {
		return nil, err
	}
	out := make([]NodeRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, NodeRow{
			ID:           dbRowInt64(r["id"]),
			Name:         dbRowString(r["name"]),
			URL:          dbRowString(r["url"]),
			HCIntervalMS: int(dbRowInt64(r["hc_interval_ms"])),
			HCTimeoutMS:  int(dbRowInt64(r["hc_timeout_ms"])),
			HCPath:       dbRowString(r["hc_path"]),
			Enabled:      dbRowInt64(r["enabled"]) == 1,
		})
	}
	return out, nil
}

// LoadRelations 拉全量有效关系行（dispatch_upstream_node_query_all_active：未软删）。
// 关系表无 enabled 列（软删即移除，整组替换语义），有效行即启用行 →
// Enabled 恒置 true（HealthCenter.Rebuild 的关系过滤口径需要）。
func (s *DBSource) LoadRelations() ([]UpstreamNodeRow, error) {
	if s.d == nil {
		return nil, errors.New("dispatch: 数据访问层未就绪")
	}
	rows, err := s.loadRows("dispatch_upstream_node_query_all_active.sql", "dispatch_upstream_node")
	if err != nil {
		return nil, err
	}
	out := make([]UpstreamNodeRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, UpstreamNodeRow{
			ID:         dbRowInt64(r["id"]),
			UpstreamID: dbRowInt64(r["upstream_id"]),
			NodeID:     dbRowInt64(r["node_id"]),
			Weight:     int(dbRowInt64(r["weight"])),
			Priority:   int(dbRowInt64(r["priority"])),
			Enabled:    true, // 有效行即启用行（见函数注释）
		})
	}
	return out, nil
}

// dbRowInt64/dbRowString 行值归一（各驱动扫描类型不一；与 admin.go 展示层同款口径）。
func dbRowInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case []byte:
		if i, err := strconv.ParseInt(strings.TrimSpace(string(n)), 10, 64); err == nil {
			return i
		}
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i
		}
	}
	return 0
}

func dbRowString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", s)
	}
}

// 编译期断言：DBSource 实现四表行来源接口。
var _ RowSource = (*DBSource)(nil)

// retryInterval 默认后台重试间隔（DB 不可用时的首次构建重试；成功即停，非常驻轮询）。
const retryInterval = 5 * time.Second

// Dispatch L2 路由分发主件（chain.Middle 槽位）。
// 运行态（RouteSnapshot）存于不可变快照，经 atomic.Value 原子替换，
// 保证 Rebuild 与在途 Handle 并发安全（请求路径零锁）。
type Dispatch struct {
	cfg     conf.Manager
	enabled bool          // *bool 注册：DISPATCH_ENABLED
	src     RowSource     // 四表行来源（装配注入；nil = DB 不可用，走降级）
	reg     *Registry     // 节点状态单一事实源（与收尾件共用）
	hc      *HealthCenter // 探活任务中心（供应商：写 reg；Rebuild 差量 / Stop 排空）
	snap    atomic.Value  // 持有 *RouteSnapshot 不可变快照

	rebuildMu sync.Mutex  // Rebuild 互斥：防并发保存下旧拉取结果覆盖新快照（S6）
	ready     atomic.Bool // 首次快照是否已成功构建（状态透出：false = 降级空表）
	stopped   atomic.Bool // 组件已停止（停止后拒绝 Rebuild，防探活任务泄漏）
	stopMu    sync.Mutex
	stopCh    chan struct{} // 后台重试停止信号（Start 创建 / Stop 关闭）

	retryEvery time.Duration // 重试间隔（默认 retryInterval；测试可调短）
}

// New 创建路由分发主件并注册 DISPATCH_ENABLED 配置项。
// src 为四表行来源（可 nil = DB 不可用，启动走降级重试）；reg 为节点状态
// registry（与收尾件 NewTailFin 共用同一实例；nil 时内部自建，便于独立测试）。
func New(cfg conf.Manager, src RowSource, reg *Registry) *Dispatch {
	if reg == nil {
		reg = NewRegistry()
	}
	d := &Dispatch{
		cfg:        cfg,
		src:        src,
		reg:        reg,
		hc:         NewHealthCenter(reg),
		retryEvery: retryInterval,
	}
	d.snap.Store(&RouteSnapshot{Upstreams: map[int64]*UpstreamRT{}})
	if cfg != nil {
		if err := cfg.Register(&d.enabled, "DISPATCH_ENABLED", "false", "是否启用 L2 路由分发（false=不挂载；规则数据经数据库路由四表管理）"); err != nil {
			log.Warn("dispatch: 注册配置项失败", "name", "DISPATCH_ENABLED", "err", err)
		}
	}
	return d
}

// Name 返回中间件名称。
func (d *Dispatch) Name() string { return "dispatch" }

// Slot 挂载位置：路由分发在防护之后、转发之前执行。
func (d *Dispatch) Slot() chain.Slot { return chain.Middle }

// Rebuild 热更重建入口（S6；adminapi 保存四表后触发 / 手动重载）：
// 拉全部四表 → ValidateGraph 校验 → 构建快照 → 原子替换 → 探活任务集差量。
// 全程持互斥锁串行（防并发保存下旧拉取结果覆盖新快照）；任一步失败保留旧快照
// 并返回 error。成功后 ready 置位（后台重试循环随下次到点成功即停）。
func (d *Dispatch) Rebuild() error {
	if d.stopped.Load() {
		return errors.New("dispatch: 组件已停止，拒绝重建（防探活任务泄漏）")
	}
	d.rebuildMu.Lock()
	defer d.rebuildMu.Unlock()

	in, err := d.loadInput()
	if err != nil {
		return err
	}
	snap, err := BuildSnapshot(in) // 内含 ValidateGraph，任一校验失败返回 error
	if err != nil {
		return err
	}
	d.snap.Store(snap)
	d.hc.Rebuild(in) // 探活任务集差量增减（任务保留节点健康态不清零）
	d.ready.Store(true)
	return nil
}

// loadInput 从行来源拉取四表行组装构建输入（逐表报错聚合定位）。
func (d *Dispatch) loadInput() (*GraphInput, error) {
	if d.src == nil {
		return nil, errors.New("dispatch: 未注入路由数据源（RowSource）")
	}
	rules, err := d.src.LoadRules()
	if err != nil {
		return nil, fmt.Errorf("dispatch: 拉取路由规则失败: %w", err)
	}
	ups, err := d.src.LoadUpstreams()
	if err != nil {
		return nil, fmt.Errorf("dispatch: 拉取均衡器失败: %w", err)
	}
	nodes, err := d.src.LoadNodes()
	if err != nil {
		return nil, fmt.Errorf("dispatch: 拉取上游节点失败: %w", err)
	}
	rels, err := d.src.LoadRelations()
	if err != nil {
		return nil, fmt.Errorf("dispatch: 拉取节点关系失败: %w", err)
	}
	return &GraphInput{Rules: rules, Upstreams: ups, Nodes: nodes, Relations: rels}, nil
}

// Start 启动：同步构建一次快照；成功即返回。失败（DB 不可用/数据非法）保持
// 空表快照（全部请求走默认 upstream，S6 降级），转入后台按固定间隔重试直至
// 首次构建成功即停（非常驻轮询）——进程重启赶上 DB 慢启时路由自动恢复，
// 无需人工点重载。Start 自身不因构建失败报错（降级属预期语义，非致命）。
func (d *Dispatch) Start(_ any) error {
	d.stopped.Store(false)
	stopCh := make(chan struct{})
	d.stopMu.Lock()
	d.stopCh = stopCh
	d.stopMu.Unlock()

	if err := d.Rebuild(); err != nil {
		log.Warn("dispatch: 首次快照构建失败，已降级空表快照并转入后台重试（成功即停）",
			"retry_interval", d.retryEvery.String(), "err", err)
		go d.retryUntilBuilt(stopCh)
	}
	return nil
}

// retryUntilBuilt 后台固定间隔重试：直至首次 Rebuild 成功即退出（非常驻轮询）。
// Stop 关闭 stopCh 即退出；外部（如手动重载）先一步构建成功时，下一次到点
// Rebuild 亦成功并退出，语义一致。
func (d *Dispatch) retryUntilBuilt(stopCh <-chan struct{}) {
	ticker := time.NewTicker(d.retryEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			if err := d.Rebuild(); err != nil {
				log.Warn("dispatch: 后台重建快照失败（继续重试，首次成功即停）", "err", err)
				continue
			}
			log.Info("dispatch: 后台重试首次快照构建成功，重试循环退出")
			return
		}
	}
}

// Stop 停止：置停止标记（拒绝后续 Rebuild）、终止后台重试、全量排空探活任务
// （等待 goroutine 退出，无泄漏）。幂等。
func (d *Dispatch) Stop() error {
	d.stopped.Store(true)
	d.stopMu.Lock()
	if d.stopCh != nil {
		close(d.stopCh)
		d.stopCh = nil
	}
	d.stopMu.Unlock()
	d.hc.Stop()
	return nil
}

// Ready 返回快照是否已成功构建（状态透出：false = 降级空表，全部请求走默认
// upstream；供管理面/WebUI 展示，STEP6 接线）。
func (d *Dispatch) Ready() bool { return d.ready.Load() }

// Handle 路由分发主流程（请求路径零锁：只读快照 + registry 原子读写）：
//  1. 归一 Host → 快照匹配（match_order 升序、命中即停）；
//  2. 未命中 → 返回 true 不写 Target（Adapter 默认 upstream 兜底）；
//  3. 命中：模式规则参数注入 X-Route-Param-* → fail-closed 校验（均衡器停用 /
//     无可用健康节点 → 503 中断链返回 false）→ sticky 感知选点（在途 +1）→
//     DF 写节点 id（收尾件递减）→ 按需种 sticky Cookie → 写 Target → 返回 true。
//
// 续链契约（IMPL_PLAN §4）：返回 true 时只设响应头（sticky 种 Cookie 依赖此
// 契约），禁止 Write/WriteHeader；503 属已自行响应（中断链）路径。
func (d *Dispatch) Handle(ctx *chain.Context) bool {
	rule, params := Match(d.snapshot(), ctx.R.Host, ctx.R.URL.Path)
	if rule == nil {
		return true
	}
	// 命中模式规则：捕获参数注入请求头（X-Route-Param-*，透传给上游）。
	for k, v := range RouteParamHeaders(params) {
		ctx.R.Header.Set(k, v)
	}
	u := rule.Upstream
	// fail-closed：停用均衡器不剔除规则，命中后无可用转发目标 → 503 中断链。
	if !u.Enabled {
		log.Warn("dispatch: 命中规则引用的均衡器已停用，返回 503",
			"rule_id", rule.ID, "upstream_id", u.ID, "upstream", u.Name)
		http.Error(ctx.W, "dispatch: upstream disabled", http.StatusServiceUnavailable)
		return false
	}
	// sticky 感知选点：Cookie 直路由优先，失效回落策略选点；选中即计在途 +1。
	out := AcquireNode(u, d.reg, ctx.R)
	if !out.OK {
		log.Warn("dispatch: 均衡器无可用健康节点，返回 503",
			"rule_id", rule.ID, "upstream_id", u.ID, "upstream", u.Name)
		http.Error(ctx.W, "dispatch: no available upstream node", http.StatusServiceUnavailable)
		return false
	}
	// 节点 id 跨热更稳定：DF 承载，收尾件（TailFin.OnResponse）按此递减在途。
	ctx.DF.Set(DFKeyDispatchNodeID, out.Node.ID)
	if out.NeedPlant {
		PlantSticky(ctx.W, u, out.Node.ID, ctx.R) // 只设响应头（续链契约）
	}
	ctx.DF.SetTarget(out.Node.URL)
	return true
}

// snapshot 读取当前不可变快照（atomic 原子读；空表快照 = 全部请求未命中）。
func (d *Dispatch) snapshot() *RouteSnapshot {
	snap, _ := d.snap.Load().(*RouteSnapshot)
	return snap
}
