// WAF 拦截事件记录器（WAF 监控统计；数据字典见 docs/DATA_DICT.md）。
//
// 职责边界：
//   - EventRecorder：拦截事件持久化（异步批量落库 shield_event 表）+ 保留期清理任务；
//   - eventCounter：  1 分钟滑动窗口内存计数（挂在 Shield 上，无 DB 也可用），
//     供 /admin/shield/metrics 实时读取，无需查库。
//
// 架构约束：拦截请求被转发链短路（adapter.go return false），obs 收不到被拦请求，
// 因此拦截事件必须在 shield 拦截点就地记录，不能依赖 obs——拦截侧 shield_event 与
// 放行侧 access_log 各记各的，互不关联。
//
// 注入方式（setter）：shield.New 签名不变，DB 就绪后由 main.go 调
// shieldMw.SetEventRecorder(recorder) 注入；未注入时 Record 静默 no-op，
// shield 仍正常拦截（DB 未配置降级，不阻断防护）。
package shield

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iotames/easydb"
	"github.com/iotames/easyserver/log"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/db"
	"rocksys/internal/netutil"
)

// ── 内存滑动窗口计数器 ────────────────────────────────────────────────

// 计数器窗口：1 分钟 × 60 桶（每桶 1s），与 obs.metrics 滑动窗口同构（本实现无锁）。
const (
	counterWindow   = time.Minute
	counterBuckets  = 60
	counterBucketMs = int64(counterWindow) / int64(time.Millisecond) / counterBuckets // 1000ms
)

// counterBucket 单桶：覆盖 1s 时间段（slot = UnixMilli/1000）。
// 全部字段原子化（无锁实现）：Add 仅 CAS 抢占过期桶后原子自增，不持互斥锁。
type counterBucket struct {
	slot   atomic.Int64
	total  atomic.Int64
	counts [blockTypeCount]atomic.Int64 // 索引 = BlockType - 1
}

// eventCounter 按 block_type 维度的 1 分钟滑动窗口计数器（实时看板用）。
// 无锁实现：固定桶数组，Add 热路径零锁；桶按 (slot % counterBuckets) 复用，
// 同一桶被相距 60s 整倍数的两个 slot 并发抢占时由 CAS 保证 slot 唯一，
// 被清空侧的并发自增可能丢失 1-2 条计数（统计类指标可容忍，与 channel 满丢弃同哲学）。
type eventCounter struct {
	buckets [counterBuckets]counterBucket
}

// Add 记录一次拦截：按 now 定位桶，CAS 抢占过期桶后 total 与对应类别计数原子 +1。
func (c *eventCounter) Add(bt BlockType, now time.Time) {
	slot := now.UnixMilli() / counterBucketMs
	b := &c.buckets[slot%counterBuckets]
	for {
		old := b.slot.Load()
		if old == slot {
			break // 当前桶已就位，直接自增
		}
		if b.slot.CompareAndSwap(old, slot) {
			// 抢占成功：桶归本 slot，清零旧计数。清零先于后续自增，
			// 快照读到 total=0 即跳过，不会把旧计数算进新窗口。
			b.total.Store(0)
			for i := range b.counts {
				b.counts[i].Store(0)
			}
			break
		}
		// CAS 失败：另一 goroutine 刚抢占同一目标 slot（同秒），重读后自增。
	}
	b.total.Add(1)
	if bt >= 1 && int(bt) <= blockTypeCount {
		b.counts[bt-1].Add(1)
	}
}

// CounterSnapshot 窗口聚合快照（admin /admin/shield/metrics 输出）。
type CounterSnapshot struct {
	Total  int64            `json:"total"`   // 窗口内拦截总数
	ByType map[string]int64 `json:"by_type"` // 类别名 → 拦截次数
}

// Snapshot 计算窗口内聚合值：仅统计最近 1 分钟内的桶（无锁遍历）。
func (c *eventCounter) Snapshot(now time.Time) CounterSnapshot {
	nowSlot := now.UnixMilli() / counterBucketMs
	s := CounterSnapshot{ByType: make(map[string]int64, blockTypeCount)}
	for i := range c.buckets {
		b := &c.buckets[i]
		slot := b.slot.Load()
		if slot == 0 || slot > nowSlot || nowSlot-slot >= counterBuckets {
			continue // 未使用或已滑出窗口
		}
		total := b.total.Load()
		if total == 0 {
			continue // 抢占清零中（尚未写入新计数），跳过
		}
		s.Total += total
		for j := range b.counts {
			if n := b.counts[j].Load(); n > 0 {
				s.ByType[blockTypeNames[j]] += n
			}
		}
	}
	return s
}

// ── 拦截事件与记录器 ─────────────────────────────────────────────────

// ShieldEvent 一条 WAF 拦截事件（与 shield_event 表列一一对应）。
type ShieldEvent struct {
	Time       time.Time // 拦截时刻
	TraceID    string    // 链路 ID（仅内部追溯，不与 access_log 关联）
	BlockType  BlockType // 拦截类别
	ClientIP   string
	Method     string
	Path       string    // URL 路径
	RawURL     string    // 含查询串的原始 URL（攻击特征常在此）
	UserAgent  string
	Host       string
	StatusCode int    // 拦截响应码（403/413/429）
	RuleHit    string // 命中的规则/特征名（如 sql_pattern / crawler_ua）
	ReqBytes   int64
	Extra      string // 扩展 JSON（referer / x_forwarded_for）
}

// eventExtra extra 列的 JSON 结构（扩展字段向后兼容：新增字段加 here）。
type eventExtra struct {
	Referer       string `json:"referer,omitempty"`
	XForwardedFor string `json:"x_forwarded_for,omitempty"`
}

// blockStatus 拦截响应码：与拦截点 http.Error 的状态码一一对应
// （BlockRateLimit=429、BlockBodyTooLarge=413、其余 403）。
func blockStatus(b BlockType) int {
	switch b {
	case BlockRateLimit:
		return http.StatusTooManyRequests
	case BlockBodyTooLarge:
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusForbidden
	}
}

// newEvent 从请求上下文构造拦截事件。
func newEvent(ctx *chain.Context, bt BlockType, ruleHit string) *ShieldEvent {
	extra, _ := json.Marshal(eventExtra{
		Referer:       ctx.R.Referer(),
		XForwardedFor: ctx.R.Header.Get("X-Forwarded-For"),
	})
	return &ShieldEvent{
		Time:       time.Now(),
		TraceID:    ctx.DF.TraceID(),
		BlockType:  bt,
		ClientIP:   netutil.GetClientIP(ctx.R),
		Method:     ctx.R.Method,
		Path:       ctx.R.URL.Path,
		RawURL:     ctx.R.URL.RequestURI(),
		UserAgent:  ctx.R.UserAgent(),
		Host:       ctx.R.Host,
		StatusCode: blockStatus(bt),
		RuleHit:    ruleHit,
		ReqBytes:   ctx.R.ContentLength,
		Extra:      string(extra),
	}
}

// EventRecorder 拦截事件持久化器：复用 obs.DBStore 范式
// （注入统一数据访问层 *db.DB、SQL 外置 sql/<dbtype>/、{table} 占位符）。
//
// 异步落库：拦截热路径只做非阻塞 ch <- event（满则丢弃计 dropped，不阻塞转发）；
// 后台 goroutine 攒批 INSERT。SHIELD_EVENT_LOG_ENABLED=false 时只内存计数不落库。
type EventRecorder struct {
	edb      *easydb.EasyDb // 统一数据访问层连接
	sqls     db.SQLSource   // SQL 脚本源（按驱动方言选 sql/<dbtype>/）
	tableName string

	// 配置项字段（构造时经 conf.Manager.Register 注册，热更直接写入）。
	logEnabled    bool // SHIELD_EVENT_LOG_ENABLED：是否落库（false 只内存计数）
	retentionDays int  // SHIELD_EVENT_RETENTION_DAYS：保留天数
	pruneEnabled  bool // SHIELD_EVENT_PRUNE_ENABLED：是否自动清理（默认不开启）

	flushRows     int           // 攒批行数阈值（装配期生效）
	flushInterval time.Duration // flush 间隔（装配期生效）

	ch      chan *ShieldEvent // 异步落库缓冲通道（容量装配期生效）
	dropped atomic.Int64      // 累计未落库条数（channel 满/脚本读取失败/单条写入失败，admin 观测用）
	written atomic.Int64      // 累计落库条数（admin 观测用）

	stopCh  chan struct{} // 停止信号（Stop 关闭，两个后台 goroutine 退出）
	doneCh  chan struct{} // flush goroutine 退出应答（Stop 等待排空完成）
	started atomic.Bool
}

// NewEventRecorder 构造记录器：注册配置项 → 幂等建表 → 启动后台任务。
// dataDB 为统一数据访问层（DB_DRIVER/DB_DSN，默认 sqlite 零配置）。
// 生命周期由装配方管理：进程停机前须调用 Stop() flush 剩余事件（防丢）。
func NewEventRecorder(cfgMgr conf.Manager, dataDB *db.DB) *EventRecorder {
	r := &EventRecorder{
		edb:       dataDB.EasyDB(),
		sqls:      dataDB,
		tableName: "shield_event",
		ch:        make(chan *ShieldEvent, defaultEventBuffer),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
	// 注册配置项（遵循配置中心红线：一律经 Register，禁止另开读取入口）。
	// 注册失败仅告警不阻断（与 obs.New 一致），配置项缺省回落代码默认值。
	items := []struct {
		pval   any
		name   string
		defval string
		title  string
	}{
		{&r.logEnabled, "SHIELD_EVENT_LOG_ENABLED", "true", "是否记录 WAF 拦截事件到库（false 时只内存计数不落库）"},
		{&r.retentionDays, "SHIELD_EVENT_RETENTION_DAYS", "90", "拦截明细保留天数"},
		{&r.pruneEnabled, "SHIELD_EVENT_PRUNE_ENABLED", "false", "是否开启拦截明细自动清理（默认不开启；未开启时登录管理后台有警告提示）"},
	}
	for _, it := range items {
		if err := cfgMgr.Register(it.pval, it.name, it.defval, it.title); err != nil {
			log.Warn("shield: 注册拦截事件配置项失败", "name", it.name, "err", err.Error())
		}
	}
	var table string
	var bufRows, flushRows, flushSec int
	if err := cfgMgr.Register(&table, "SHIELD_EVENT_TABLE", "shield_event", "拦截事件表名", "修改后需重启服务生效"); err != nil {
		log.Warn("shield: 注册拦截事件配置项失败", "name", "SHIELD_EVENT_TABLE", "err", err.Error())
	} else if table != "" {
		r.tableName = table
	}
	if err := cfgMgr.Register(&bufRows, "SHIELD_EVENT_BUFFER", "1024", "拦截事件落库缓冲队列长度（超出丢弃并计数，不阻塞请求）", "修改后需重启服务生效"); err != nil {
		log.Warn("shield: 注册拦截事件配置项失败", "name", "SHIELD_EVENT_BUFFER", "err", err.Error())
	} else if bufRows > 0 {
		r.ch = make(chan *ShieldEvent, bufRows)
	}
	if err := cfgMgr.Register(&flushRows, "SHIELD_EVENT_FLUSH_ROWS", "200", "拦截事件批量写库行数阈值", "修改后需重启服务生效"); err != nil {
		log.Warn("shield: 注册拦截事件配置项失败", "name", "SHIELD_EVENT_FLUSH_ROWS", "err", err.Error())
	} else if flushRows > 0 {
		r.flushRows = flushRows
	}
	if err := cfgMgr.Register(&flushSec, "SHIELD_EVENT_FLUSH_INTERVAL", "5", "拦截事件批量写库间隔（秒）", "修改后需重启服务生效"); err != nil {
		log.Warn("shield: 注册拦截事件配置项失败", "name", "SHIELD_EVENT_FLUSH_INTERVAL", "err", err.Error())
	} else if flushSec > 0 {
		r.flushInterval = time.Duration(flushSec) * time.Second
	}
	if r.flushRows <= 0 {
		r.flushRows = defaultEventFlushRows
	}
	if r.flushInterval <= 0 {
		r.flushInterval = defaultEventFlushInterval
	}
	// 幂等建表 + 索引：失败不阻断启动（后续批量写入失败会持续告警，防护不受影响）。
	if err := r.EnsureTable(); err != nil {
		log.Error("shield: 拦截事件表初始化失败（拦截仍正常，事件不落库）", "err", err.Error())
	}
	r.start()
	return r
}

// 落库任务默认参数（对应配置项缺省值，注册失败时兜底）。
const (
	defaultEventBuffer        = 1024
	defaultEventFlushRows     = 200
	defaultEventFlushInterval = 5 * time.Second
)

// start 启动后台任务（flush 攒批落库 + prune 定期清理）。幂等。
func (r *EventRecorder) start() {
	if !r.started.CompareAndSwap(false, true) {
		return
	}
	go r.flushLoop()
	go r.pruneLoop()
}

// Stop 优雅停机：通知后台任务退出并等待 flush 排空剩余事件（防丢）。幂等。
func (r *EventRecorder) Stop() {
	if !r.started.CompareAndSwap(true, false) {
		return
	}
	close(r.stopCh)
	<-r.doneCh // 等 flush goroutine 排空退出（prune goroutine 自行退出，无需应答）
}

// Record 记录一次拦截（热路径调用，非阻塞）。
// ★ nil 安全：recorder 未注入（DB 未配置）时静默 no-op。
// SHIELD_EVENT_LOG_ENABLED=false 时跳过入队（内存计数由 Shield 侧负责，不受影响）。
func (r *EventRecorder) Record(ctx *chain.Context, bt BlockType, ruleHit string) {
	if r == nil || !r.logEnabled {
		return
	}
	select {
	case r.ch <- newEvent(ctx, bt, ruleHit):
	default:
		// 通道满（拦截洪流）丢弃该条并计数，绝不阻塞转发。
		r.dropped.Add(1)
	}
}

// Stats 观测计数（admin 输出用）。
func (r *EventRecorder) Stats() (written, dropped int64) {
	return r.written.Load(), r.dropped.Load()
}

// ── SQL 执行（SQL 外置铁律：脚本位于 sql/<dbtype>/，禁止 Go 内联）──────

// sqlText 读取脚本并替换 {table} 表名占位符（表名来自配置注册项，非用户输入，安全）。
func (r *EventRecorder) sqlText(name string) (string, error) {
	txt, err := r.sqls.SQL(name)
	if err != nil {
		return "", fmt.Errorf("shield: 读取 SQL 脚本 %s 失败（切换数据库时缺少 sql/<dbtype>/ 下对应脚本）: %w", name, err)
	}
	return strings.ReplaceAll(txt, "{table}", r.tableName), nil
}

// EnsureTable 幂等建表 + 索引。
func (r *EventRecorder) EnsureTable() error {
	ddl, err := r.sqlText("shield_event_create_table.sql")
	if err != nil {
		return err
	}
	if _, err := r.edb.Exec(ddl); err != nil {
		return fmt.Errorf("shield: 建拦截事件表失败: %w", err)
	}
	idx, err := r.sqlText("shield_event_create_index.sql")
	if err != nil {
		return err
	}
	// 多语句脚本逐条执行 + 幂等容错："已存在"类错误忽略
	// （MySQL 的 CREATE INDEX 不支持 IF NOT EXISTS，重复执行报 "Duplicate key name"）。
	for _, stmt := range db.SplitSQLStatements(idx) {
		if _, err := r.edb.Exec(stmt); err != nil {
			msg := err.Error()
			if strings.Contains(msg, "already exists") || strings.Contains(msg, "Duplicate key name") || strings.Contains(msg, "duplicate key") {
				continue
			}
			return fmt.Errorf("shield: 建拦截事件索引失败: %w", err)
		}
	}
	return nil
}

// flushLoop 攒批落库循环：攒满 flushRows 条或每 flushInterval 秒批量 INSERT 一次；
// 收到停止信号后先排空 channel 再写出剩余批次（优雅停机防丢）。
func (r *EventRecorder) flushLoop() {
	defer close(r.doneCh)
	batch := make([]*ShieldEvent, 0, r.flushRows)
	ticker := time.NewTicker(r.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case ev := <-r.ch:
			batch = append(batch, ev)
			if len(batch) >= r.flushRows {
				r.writeBatch(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				r.writeBatch(batch)
				batch = batch[:0]
			}
		case <-r.stopCh:
			// 优雅停机：排空缓冲通道中已入队的事件，一次性写出。
			for {
				select {
				case ev := <-r.ch:
					batch = append(batch, ev)
				default:
					r.writeBatch(batch)
					return
				}
			}
		}
	}
}

// writeBatch 同步逐条插入一批事件（与 obs DBStore.Write 同范式）。
// 单条失败丢弃该条并计数告警，不影响批内其余事件。
func (r *EventRecorder) writeBatch(batch []*ShieldEvent) {
	if len(batch) == 0 {
		return
	}
	ins, err := r.sqlText("shield_event_insert.sql")
	if err != nil {
		r.dropped.Add(int64(len(batch)))
		log.Warn("shield: 拦截事件批量写入失败（整批丢弃）", "err", err.Error(), "rows", len(batch))
		return
	}
	var lastErr error
	for _, ev := range batch {
		if _, err := r.edb.Exec(ins,
			ev.Time.UTC(),
			ev.TraceID, int(ev.BlockType), ev.ClientIP, ev.Method,
			ev.Path, ev.RawURL, ev.UserAgent, ev.Host,
			ev.StatusCode, ev.RuleHit, ev.ReqBytes, ev.Extra,
		); err != nil {
			lastErr = err
			r.dropped.Add(1)
			continue
		}
		r.written.Add(1)
	}
	if lastErr != nil {
		log.Warn("shield: 拦截事件写入失败（失败条目已丢弃）", "err", lastErr.Error(), "drop_count", r.dropped.Load())
	}
}

// pruneLoop 定期清理循环：首次延迟 1 分钟（避开启动高峰），此后每 24h 执行一次。
// SHIELD_EVENT_PRUNE_ENABLED=false 时跳过执行（默认不开启，登录管理后台有警告提示）。
func (r *EventRecorder) pruneLoop() {
	first := time.NewTimer(time.Minute)
	defer first.Stop()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-first.C:
			r.pruneIfEnabled()
		case <-ticker.C:
			r.pruneIfEnabled()
		case <-r.stopCh:
			return
		}
	}
}

// pruneIfEnabled 清理开关开启时执行一次保留期外清理。
func (r *EventRecorder) pruneIfEnabled() {
	if !r.pruneEnabled {
		return
	}
	days := r.retentionDays
	if days <= 0 {
		days = 90
	}
	n, err := r.Prune(days)
	if err != nil {
		log.Warn("shield: 拦截明细自动清理失败", "err", err.Error())
		return
	}
	if n > 0 {
		log.Info("shield: 拦截明细自动清理完成", "deleted", n, "retention_days", days)
	}
}

// ── 查询与清理（admin API 底层）──────────────────────────────────────

// EventQuery 拦截明细查询条件（/admin/shield/events 参数映射）。
// 时间范围含端点；BlockType=0、ClientIP 空串表示不过滤。
type EventQuery struct {
	From      time.Time
	To        time.Time
	BlockType BlockType
	ClientIP  string
	Limit     int // <=0 用默认值
}

// defaultEventQueryLimit 明细查询默认返回上限。
const defaultEventQueryLimit = 500

// QueryEvents 按条件查询拦截明细，返回归一化后的行（time → RFC3339 字符串）。
func (r *EventRecorder) QueryEvents(q EventQuery) ([]map[string]any, error) {
	sel, err := r.sqlText("shield_event_query.sql")
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultEventQueryLimit
	}
	args := []any{
		q.From.UTC(), q.To.UTC(),
		int(q.BlockType), int(q.BlockType),
		q.ClientIP, q.ClientIP,
		limit,
	}
	var rows []map[string]any
	if err := r.edb.GetMany(sel, &rows, args...); err != nil {
		return nil, fmt.Errorf("shield: 查询拦截明细失败: %w", err)
	}
	for _, row := range rows {
		normalizeEventRow(row)
	}
	return rows, nil
}

// StatsDaily 按日 × 拦截类别聚合（查询时聚合，不建物化表）。
func (r *EventRecorder) StatsDaily(from time.Time) ([]map[string]any, error) {
	sel, err := r.sqlText("shield_event_stats_daily.sql")
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := r.edb.GetMany(sel, &rows, from.UTC()); err != nil {
		return nil, fmt.Errorf("shield: 拦截统计（按日）查询失败: %w", err)
	}
	for _, row := range rows {
		normalizeEventRow(row)
	}
	return rows, nil
}

// StatsTopIP Top 攻击源 IP（查询时聚合）。
func (r *EventRecorder) StatsTopIP(from time.Time, limit int) ([]map[string]any, error) {
	sel, err := r.sqlText("shield_event_stats_top_ip.sql")
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	var rows []map[string]any
	if err := r.edb.GetMany(sel, &rows, from.UTC(), limit); err != nil {
		return nil, fmt.Errorf("shield: 拦截统计（Top IP）查询失败: %w", err)
	}
	for _, row := range rows {
		normalizeEventRow(row)
	}
	return rows, nil
}

// Prune 清理保留期外的拦截明细，返回删除行数（幂等可重复执行）。
func (r *EventRecorder) Prune(retentionDays int) (int64, error) {
	del, err := r.sqlText("shield_event_prune.sql")
	if err != nil {
		return 0, err
	}
	if retentionDays <= 0 {
		retentionDays = 90
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	res, err := r.edb.Exec(del, cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("shield: 清理拦截明细失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ── 行归一化（admin API 输出类型稳定）────────────────────────────────

// normalizeEventRow 将 DB 返回的行归一化：time → RFC3339 字符串、
// 数值列 → int64、其余 → string（与 obs normalizeRowTypes 同思路）。
func normalizeEventRow(row map[string]any) {
	for k, v := range row {
		switch k {
		case "time":
			row[k] = eventToString(v)
		case "block_type", "status_code", "req_bytes", "cnt":
			row[k] = eventToInt64(v)
		default:
			if v != nil {
				row[k] = eventToString(v)
			}
		}
	}
}

// eventToString 将数据库标量归一为 string（time.Time 归一为 RFC3339）。
func eventToString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	case time.Time:
		return s.UTC().Format(time.RFC3339)
	case int64:
		return strconv.FormatInt(s, 10)
	case int:
		return strconv.Itoa(s)
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(s)
	default:
		return fmt.Sprint(v)
	}
}

// eventToInt64 将数据库标量归一为 int64。
func eventToInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return int64(f)
		}
	case []byte:
		return eventToInt64(string(n))
	}
	return 0
}
