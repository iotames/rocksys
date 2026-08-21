// Package obs L3 可观测性（RockObs）：访问日志 + 指标聚合 + 查询 API。
//
// 挂 chain.Tail 槽位并实现 chain.ResponseHook——OnResponse 是获得"请求完成事件"
// 的唯一通道：Forward 完成后 Adapter 回调，此时 ctx.RespCode/RespHeader/RespBody
// 与 ctx.DF 三时间戳均已就绪，据此构造 AccessRecord 异步落盘并聚合到 Metrics。
//
// 存储后端可热切换（OBS_STORE=db|file，见 store.go/dim.go）：
// 默认 db 复用统一数据访问层 internal/db 写 access_log 表；file（OBS_STORE=file）
// 已弃用，将不再被支持（仅显式配置时启用，启动打弃用告警）。
package obs

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/db"
	"rocksys/internal/hotswap"
	"rocksys/internal/netutil"

	"github.com/iotames/easyserver/log"
)

// 默认配置（§14）。
const (
	defaultLogDir        = "logs"
	defaultRetentionDays = 30
	defaultStore         = "db" // 访问日志存储后端（默认）：db | file（已弃用）
	defaultLogPruneDays  = 7    // access_log 表默认保留天数（自动清理开启后生效）
)

// 指标窗口：1 分钟滑动窗口 × 100 桶（每桶 0.6s）。
const (
	metricsWindow   = time.Minute
	metricsBuckets  = 100
	metricsBucketMs = int64(metricsWindow) / int64(time.Millisecond) / metricsBuckets // 600
)

// Snapshot 指标快照（admin /admin/metrics 输出）。
type Snapshot struct {
	QPS       float64 // 每秒请求数（窗口总量 / 60s）
	P50       int64   // 耗时中位数（ms）
	P95       int64   // 耗时 P95（ms）
	P99       int64   // 耗时 P99（ms）
	ErrorRate float64 // 错误率（4xx/5xx 占比）
}

// metricsBucket 单桶：覆盖 0.6s 时间段（slot = UnixMilli/600）。
type metricsBucket struct {
	slot       int64
	count      int64
	errorCount int64
	latencies  []int64
}

// Metrics 1 分钟滑动窗口指标聚合（§14）。
type Metrics struct {
	mu      sync.Mutex
	buckets [metricsBuckets]*metricsBucket
}

// NewMetrics 创建空指标窗口。
func NewMetrics() *Metrics { return &Metrics{} }

// Add 记录一次请求：按 now 定位桶，统计耗时（ms）与错误码（>=400 计入错误）。
func (m *Metrics) Add(now time.Time, latencyMs int64, code int) {
	slot := now.UnixMilli() / metricsBucketMs
	m.mu.Lock()
	idx := slot % metricsBuckets
	b := m.buckets[idx]
	if b == nil || b.slot != slot {
		b = &metricsBucket{slot: slot}
		m.buckets[idx] = b
	}
	b.count++
	if code >= 400 {
		b.errorCount++
	}
	b.latencies = append(b.latencies, latencyMs)
	m.mu.Unlock()
}

// Snapshot 计算窗口内聚合值：仅统计最近 60s 内的桶。
func (m *Metrics) Snapshot(now time.Time) Snapshot {
	nowSlot := now.UnixMilli() / metricsBucketMs
	m.mu.Lock()
	var total, errs int64
	var lat []int64
	for _, b := range m.buckets {
		if b == nil || b.slot > nowSlot || nowSlot-b.slot >= metricsBuckets {
			continue
		}
		total += b.count
		errs += b.errorCount
		lat = append(lat, b.latencies...)
	}
	m.mu.Unlock()

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	s := Snapshot{
		QPS:       float64(total) / metricsWindow.Seconds(),
		P50:       percentile(lat, 0.50),
		P95:       percentile(lat, 0.95),
		P99:       percentile(lat, 0.99),
		ErrorRate: 0,
	}
	if total > 0 {
		s.ErrorRate = float64(errs) / float64(total)
	}
	return s
}

// percentile 升序切片分位（nearest-rank）；空切片返回 0。
func percentile(sorted []int64, p float64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(float64(n) * p)
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

// Obs 可观测性中间件：挂 chain.Tail，实现 chain.ResponseHook。
type Obs struct {
	cfg           conf.Manager
	storeCfg      string // *string 注册：OBS_STORE（db|file，默认 db；file 已弃用）
	logDir        string // *string 注册：OBS_LOG_DIR
	retentionDays int    // *int 注册：OBS_RETENTION_DAYS（仅 file 后端清理用，file 已弃用）
	enabled       bool   // *bool 注册：OBS_ENABLED
	dataDB        *db.DB // 统一数据访问层：默认 db 后端依赖（nil 时回退 file 并告警）

	// access_log 自动清理（DB 后端专用，数据保留见 DATA_DICT 维护约定）：
	// 默认不开启，未开启时登录管理后台有警告提示。
	pruneLogEnabled bool // *bool 注册：OBS_LOG_PRUNE_ENABLED
	pruneLogDays    int  // *int 注册：OBS_LOG_RETENTION_DAYS
	pruneMu         sync.Mutex
	pruneRunning    bool
	pruneStop       chan struct{}

	sink    atomic.Value // *AsyncStore：当前存储后端（异步写入包装），热切换时原子替换
	metrics *Metrics
}

// 编译期断言：Obs 实现 hotswap.MiddlewareLifecycle 与 chain.ResponseHook。
var (
	_ hotswap.MiddlewareLifecycle = (*Obs)(nil)
	_ chain.ResponseHook          = (*Obs)(nil)
)

// New 创建 obs 挂件并注册自身配置项（§14）。
// dataDB 为统一数据访问层（可 nil）：默认 db 存储后端依赖它（DB_DRIVER/DB_DSN），
// 未就绪则回退 file 并告警（file 已弃用，仅作过渡保留）。
func New(cfgMgr conf.Manager, dataDB *db.DB) *Obs {
	o := &Obs{
		cfg:           cfgMgr,
		storeCfg:      defaultStore,
		logDir:        defaultLogDir,
		retentionDays: defaultRetentionDays,
		dataDB:        dataDB,
		metrics:       NewMetrics(),
	}
	if err := cfgMgr.Register(&o.storeCfg, "OBS_STORE", defaultStore, "访问日志存储后端（默认 db；file 已弃用，将不再被支持）"); err != nil {
		log.Warn("obs: 注册配置项失败", "name", "OBS_STORE", "err", err)
	}
	if err := cfgMgr.Register(&o.logDir, "OBS_LOG_DIR", defaultLogDir, "访问日志目录"); err != nil {
		log.Warn("obs: 注册配置项失败", "name", "OBS_LOG_DIR", "err", err)
	}
	if err := cfgMgr.Register(&o.retentionDays, "OBS_RETENTION_DAYS", strconv.Itoa(defaultRetentionDays), "访问日志保留天数"); err != nil {
		log.Warn("obs: 注册配置项失败", "name", "OBS_RETENTION_DAYS", "err", err)
	}
	if err := cfgMgr.Register(&o.pruneLogEnabled, "OBS_LOG_PRUNE_ENABLED", "false", "是否开启访问日志（access_log 表）自动清理（默认不开启；未开启时登录管理后台有警告提示）"); err != nil {
		log.Warn("obs: 注册配置项失败", "name", "OBS_LOG_PRUNE_ENABLED", "err", err)
	}
	if err := cfgMgr.Register(&o.enabled, "OBS_ENABLED", "false", "是否启用访问日志与指标观测（false=不挂载）"); err != nil {
		log.Warn("obs: 注册配置项失败", "name", "OBS_ENABLED", "err", err)
	}
	if err := cfgMgr.Register(&o.pruneLogDays, "OBS_LOG_RETENTION_DAYS", strconv.Itoa(defaultLogPruneDays), "访问日志保留天数（自动清理开启后生效；DB 后端专用，与已弃用 file 后端的 OBS_RETENTION_DAYS 相互独立）"); err != nil {
		log.Warn("obs: 注册配置项失败", "name", "OBS_LOG_RETENTION_DAYS", "err", err)
	}
	o.sink.Store(NewAsyncStore(o.buildStore()))
	return o
}

// Name 中间件名（hotswap 按此名启停）。
func (o *Obs) Name() string { return "obs" }

// Slot 挂载位置：Tail（响应阶段，配合 ResponseHook）。
func (o *Obs) Slot() chain.Slot { return chain.Tail }

// Handle 占位：响应处理全在 OnResponse，不参与转发前逻辑（§14）。
func (o *Obs) Handle(ctx *chain.Context) (next bool) { return false }

// Start 按当前配置重建存储后端（热更 OBS_STORE/OBS_LOG_DIR/留存天数 / Enable 时调用）。
// 原子替换 AsyncStore：旧后端排空缓冲后关闭，新请求写入新后端。
// 同时确保 access_log 自动清理循环在运行（幂等；Disable 后再 Enable 可恢复）。
func (o *Obs) Start(cfg any) error {
	o.rebuildStore()
	o.startPruneLoop()
	return nil
}

// Stop 桥接 Shutdown（hotswap.MiddlewareLifecycle.Stop 无 context 参数，§14 旁注）。
func (o *Obs) Stop() error { return o.Shutdown(context.Background()) }

// Shutdown flush 内存缓冲区中未写入的日志，然后关闭存储（进程退出前必须调用）。
// 同时停止 access_log 自动清理循环。
func (o *Obs) Shutdown(ctx context.Context) error {
	o.stopPruneLoop()
	return o.sink.Load().(*AsyncStore).Flush(ctx)
}

// Metrics 返回指标聚合器（供 admin handler 读取）。
func (o *Obs) Metrics() *Metrics { return o.metrics }

// StoreStats 返回当前存储后端的丢弃与连续失败计数（admin 观测用）。
func (o *Obs) StoreStats() (dropCount, consecutiveFails int64) {
	as := o.sink.Load().(*AsyncStore)
	return as.DropCount(), as.ConsecutiveFails()
}

// Query 按条件查询访问日志（转发当前启用的存储后端）。
func (o *Obs) Query(q Query) ([]map[string]any, error) {
	return o.sink.Load().(*AsyncStore).Query(q)
}

// StorageSize 当前日志存储总占用（字节）：
// file 为 OBS_LOG_DIR 下所有 access-*.jsonl 合计（遗留数据，file 已弃用）；
// db 为 access_log 表 + 索引占用。两者独立统计并求和（与当前启用后端无关，
// 切换后端后旧数据仍计入）。
type StorageSize struct {
	FileBytes  int64 `json:"file_bytes"`  // 文件日志占用
	DBBytes    int64 `json:"db_bytes"`    // 数据库日志表占用
	TotalBytes int64 `json:"total_bytes"` // 合计
}

// StorageSize 统计两类后端的日志存储占用。
func (o *Obs) StorageSize() StorageSize {
	var fs, dbs int64
	if v, err := NewFileStore(o.logDir, 0).SizeBytes(); err == nil {
		fs = v
	}
	if o.dataDB != nil {
		if v, err := NewDBStore(o.dataDB, accessLogTable).SizeBytes(); err == nil {
			dbs = v
		}
	}
	return StorageSize{FileBytes: fs, DBBytes: dbs, TotalBytes: fs + dbs}
}

// OnResponse 实现 chain.ResponseHook：构造 AccessRecord → 异步落盘 → 聚合指标。
func (o *Obs) OnResponse(ctx *chain.Context) error {
	al := &AccessRecord{
		Time:       time.Now(),
		TraceID:    ctx.DF.TraceID(),
		TenantID:   ctx.DF.TenantID(),
		Path:       ctx.R.URL.Path,
		Method:     ctx.R.Method,
		ClientIP:   netutil.GetClientIP(ctx.R),
		StatusCode: ctx.RespCode,
		Upstream:   ctx.DF.Target(),
		ShieldMs:   ctx.DF.ShieldMs(),
		BizMs:      ctx.DF.BizMs(),
		TotalMs:    ctx.DF.TotalMs(),
		ReqBytes:   ctx.R.ContentLength,
		RespBytes:  int64(len(ctx.RespBody)),
	}
	// 负载维度预留点：后期采集纯文本 POST 请求体等扩展字段时，
	// 先在 dim.go Dims 注册 payload 维度，再在此写 Extras（存储零改动）。
	o.sink.Load().(*AsyncStore).Write(al)
	o.metrics.Add(time.Now(), al.TotalMs, al.StatusCode)
	return nil
}

// buildStore 按 OBS_STORE 配置构造底层存储后端。
// 默认（含空值）与非法值均走 db；file 仅显式配置时启用并打弃用告警
// （file 将不再被支持）；db 因数据访问层未就绪/初始化失败时回退 file 并告警
// （过渡保留，避免日志静默丢失）。
func (o *Obs) buildStore() Store {
	store := strings.ToLower(strings.TrimSpace(o.storeCfg))
	switch store {
	case "file":
		log.Warn("obs: OBS_STORE=file 已弃用，将不再被支持，请改用 db 存储")
		return NewFileStore(o.logDir, o.retentionDays)
	case "db", "":
		// 默认 db
	default:
		log.Warn("obs: 未知存储后端，回退默认 db 存储", "store", o.storeCfg)
	}
	if o.dataDB == nil {
		log.Warn("obs: OBS_STORE=db 但数据访问层未就绪，回退 file 存储（file 已弃用，请配置 DB_DRIVER/DB_DSN）")
		return NewFileStore(o.logDir, o.retentionDays)
	}
	st := NewDBStore(o.dataDB, accessLogTable)
	if err := st.EnsureTable(); err != nil {
		log.Warn("obs: 数据库存储初始化失败，回退 file 存储（file 已弃用）", "err", err.Error())
		return NewFileStore(o.logDir, o.retentionDays)
	}
	return st
}

// rebuildStore 原子替换存储后端：新建 AsyncStore 挂入，旧后端排空并关闭。
func (o *Obs) rebuildStore() {
	newSink := NewAsyncStore(o.buildStore())
	old := o.sink.Swap(newSink).(*AsyncStore)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := old.Flush(ctx); err != nil {
		log.Warn("obs: 旧存储排空失败", "err", err.Error())
	}
	_ = old.Close()
}

// ── access_log 自动清理（DB 后端专用，数据保留见 DATA_DICT 维护约定）──────

// startPruneLoop 启动自动清理循环（幂等，锁保护）。首次延迟 1 分钟（避开启动高峰），
// 此后每 24h 执行一次；OBS_LOG_PRUNE_ENABLED=false 时跳过执行（默认不开启）。
func (o *Obs) startPruneLoop() {
	o.pruneMu.Lock()
	defer o.pruneMu.Unlock()
	if o.pruneRunning {
		return
	}
	o.pruneRunning = true
	o.pruneStop = make(chan struct{})
	go o.pruneLoop(o.pruneStop)
}

// stopPruneLoop 停止自动清理循环（幂等）。
func (o *Obs) stopPruneLoop() {
	o.pruneMu.Lock()
	defer o.pruneMu.Unlock()
	if !o.pruneRunning {
		return
	}
	o.pruneRunning = false
	close(o.pruneStop)
}

// pruneLoop 清理循环主体：定时执行，收到停止信号退出。
func (o *Obs) pruneLoop(stop <-chan struct{}) {
	first := time.NewTimer(time.Minute)
	defer first.Stop()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-first.C:
			o.pruneOnce()
		case <-ticker.C:
			o.pruneOnce()
		case <-stop:
			return
		}
	}
}

// pruneOnce 执行一次清理：开关开启且当前为 db 后端时，清理保留期外的 access_log 记录。
// file 后端（已弃用）自带按天清理，跳过；数据访问层未就绪跳过。
func (o *Obs) pruneOnce() {
	if !o.pruneLogEnabled {
		return
	}
	if strings.EqualFold(strings.TrimSpace(o.storeCfg), "file") {
		return
	}
	if o.dataDB == nil {
		return
	}
	n, err := NewDBStore(o.dataDB, accessLogTable).Prune(o.pruneLogDays)
	if err != nil {
		log.Warn("obs: 访问日志自动清理失败", "err", err.Error())
		return
	}
	if n > 0 {
		log.Info("obs: 访问日志自动清理完成", "deleted", n, "retention_days", o.pruneLogDays)
	}
}

// PruneLog 手动触发访问日志清理（admin 端点 POST /admin/logs/prune 用）。
// retentionDays <= 0 时用配置的 OBS_LOG_RETENTION_DAYS。仅 DB 后端可清理。
func (o *Obs) PruneLog(retentionDays int) (int64, error) {
	if o.dataDB == nil {
		return 0, errors.New("obs: 数据访问层未就绪，无法清理")
	}
	if retentionDays <= 0 {
		retentionDays = o.pruneLogDays
	}
	return NewDBStore(o.dataDB, accessLogTable).Prune(retentionDays)
}
