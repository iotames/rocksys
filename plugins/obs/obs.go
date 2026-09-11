// Package obs L3 可观测性（RockObs）：访问日志 + 指标聚合 + 查询 API。
//
// 挂 chain.Tail 槽位，实现 chain.ResponseHook 与 chain.DoneHook：
// OnResponse 保持注册（obs 在 Tail 槽位是缓冲路径的前提，RespBody/RespBytes 依赖缓冲）；
// OnDone 在响应写回客户端完成后被 Adapter 回调，此时 ctx.DF 四时间戳（含 DoneAt 出网时刻）
// 均已就绪，据此构造 AccessRecord 异步落盘并聚合到 Metrics。
//
// 访问日志统一写数据库（复用统一数据访问层 internal/db 写 access_log 表，
// 依赖 DB_DRIVER/DB_DSN）；数据访问层未就绪时降级丢弃（不阻断转发）。
package obs

import (
	"context"
	"errors"
	"math/rand/v2"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/db"
	"rocksys/internal/geoip"
	"rocksys/internal/hotswap"
	"rocksys/internal/netutil"

	"github.com/iotames/easyserver/log"
)

// 默认配置（§14）。
const defaultLogPruneDays = 7 // access_log 表默认保留天数（自动清理开启后生效）

// 流量统计 SQL 超时（固定 25 秒，不做配置项）：统计聚合是大表重操作，超时后数据库侧终止
// 执行（QueryContext 下推取消）防慢查询占用连接；前端等待上限 30 秒，留 5 秒余量让后端先返回
// 超时说明。属实现细节而非用户可调策略，写死以免增加配置心智负担。
const trafficQueryTimeoutConst = 25 * time.Second

// 指标窗口：1 小时滑动窗口 × 60 桶（每桶 1 分钟，METRICS_WINDOW）。
// 窗口可切 1m/5m/15m/1h（/admin/metrics?window=），整分钟桶聚合：
//   - QPS/错误率 = 纯计数跨桶相加，零误差；
//   - 延迟分位数 = 各桶水库采样样本合并后最近秩分位（每桶上限 reservoirCap 个均匀样本，
//     60 桶 × 200 ≈ 96KB 内存上界），近精确且无直方图桶界误差——小窗口可即时发现慢请求。
//
// 长期（所选范围）的延迟分布由流量统计区按 SQL 精确统计，两者互补。
const (
	metricsWindow   = time.Hour
	metricsBuckets  = 60
	metricsBucketMs = int64(metricsWindow) / int64(time.Millisecond) / metricsBuckets // 60000 = 1 分钟
	reservoirCap    = 200
)

// Snapshot 指标快照（admin /admin/metrics 输出）。
type Snapshot struct {
	QPS       float64 // 每秒请求数（窗口总量 / 窗口秒数）
	P50       int64   // 耗时 P50（ms，合并水库样本近似精确）
	P95       int64   // 耗时 P95（ms）
	P99       int64   // 耗时 P99（ms）
	ErrorRate float64 // 错误率（4xx/5xx 占比）
}

// metricsBucket 单桶：覆盖 1 分钟时间段（slot = UnixMilli/60000）。
// 样本采集用水库抽样：该分钟请求量超上限后，新样本以 count/容量 概率等概率替换旧样本。
type metricsBucket struct {
	slot       int64
	count      int64
	errorCount int64
	seen       int64   // 本桶已见请求数（水库抽样计数）
	latencies  []int64 // 水库样本（len ≤ reservoirCap）
}

// Metrics 滑动窗口指标聚合（§14），互斥锁保护（Add/Snapshot 频度低，锁成本可忽略）。
type Metrics struct {
	mu      sync.Mutex
	buckets [metricsBuckets]*metricsBucket
}

// NewMetrics 创建空指标窗口。
func NewMetrics() *Metrics { return &Metrics{} }

// Add 记录一次请求：按 now 定位桶，错误码（>=400）计入错误，延迟进水库样本。
func (m *Metrics) Add(now time.Time, latencyMs int64, code int) {
	slot := now.UnixMilli() / metricsBucketMs
	m.mu.Lock()
	idx := int(slot % metricsBuckets)
	b := m.buckets[idx]
	if b == nil || b.slot != slot {
		b = &metricsBucket{slot: slot}
		m.buckets[idx] = b
	}
	b.count++
	if code >= 400 {
		b.errorCount++
	}
	// 水库抽样：未满直接收；满了以 seen/容量 概率等概率替换（每条请求入选概率恒为 容量/总数）
	b.seen++
	if int64(len(b.latencies)) < reservoirCap {
		b.latencies = append(b.latencies, latencyMs)
	} else if j := fastrandn(b.seen); j < int64(reservoirCap) {
		b.latencies[j] = latencyMs
	}
	m.mu.Unlock()
}

// fastrandn 返回 [0,n) 伪随机数（水库抽样用；指标采样不需要密码学强度）。
func fastrandn(n int64) int64 {
	return rand.Int64N(n)
}

// Snapshot 计算窗口内聚合值（window 须为分钟整数倍且 ≤ metricsWindow）。
func (m *Metrics) Snapshot(now time.Time, window time.Duration) Snapshot {
	nowSlot := now.UnixMilli() / metricsBucketMs
	winSlots := int64(window / time.Minute)
	if winSlots < 1 {
		winSlots = 1
	}
	if winSlots > metricsBuckets {
		winSlots = metricsBuckets
	}
	m.mu.Lock()
	var total, errs int64
	var lat []int64
	for _, b := range m.buckets {
		if b == nil || b.slot > nowSlot || nowSlot-b.slot >= winSlots {
			continue
		}
		total += b.count
		errs += b.errorCount
		lat = append(lat, b.latencies...)
	}
	m.mu.Unlock()

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	s := Snapshot{
		QPS:       float64(total) / window.Seconds(),
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
	cfg     conf.Manager
	enabled bool            // *bool 注册：OBS_ENABLED
	dataDB  *db.DB          // 统一数据访问层（nil 时降级 discardStore，日志不落盘）
	geo     *geoip.Resolver // GeoIP 解析器（装配期 SetGeoip 注入，可 nil=geo 列空串）

	// 流量统计（TRAFFIC_ANALYSIS D1）：TTL 秒数注册进配置中心（0=禁用），结果缓存常驻。
	trafficCacheTTL int // *int 注册：OBS_TRAFFIC_CACHE_TTL

	tcache *trafficCache

	// 统计 SQL 超时（防慢查询无限占用连接）：经 QueryContext 下推取消。
	// 固定 25 秒（见 trafficQueryTimeoutConst）：前端等待上限 30 秒，留 5 秒余量让后端先返回超时说明。
	trafficQueryTimeout time.Duration

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

// 编译期断言：Obs 实现 hotswap.MiddlewareLifecycle、chain.ResponseHook 与 chain.DoneHook。
var (
	_ hotswap.MiddlewareLifecycle = (*Obs)(nil)
	_ chain.ResponseHook          = (*Obs)(nil)
	_ chain.DoneHook              = (*Obs)(nil)
)

// New 创建 obs 挂件并注册自身配置项（§14）。
// dataDB 为统一数据访问层（可 nil）：访问日志写 access_log 表依赖它（DB_DRIVER/DB_DSN），
// 未就绪时降级 discardStore（日志不落盘，仅告警）。
func New(cfgMgr conf.Manager, dataDB *db.DB) *Obs {
	o := &Obs{
		cfg:     cfgMgr,
		enabled: false,
		dataDB:  dataDB,
		metrics: NewMetrics(),
	}
	if dataDB == nil {
		log.Warn("obs: 数据访问层未就绪，访问日志不落盘（请配置 DB_DRIVER/DB_DSN）")
	}
	if err := cfgMgr.Register(&o.pruneLogEnabled, "OBS_LOG_PRUNE_ENABLED", "false", "是否开启访问日志（access_log 表）自动清理（默认不开启；未开启时登录管理后台有警告提示）"); err != nil {
		log.Warn("obs: 注册配置项失败", "name", "OBS_LOG_PRUNE_ENABLED", "err", err)
	}
	if err := cfgMgr.Register(&o.enabled, "OBS_ENABLED", "false", "是否启用访问日志与指标观测（false=不挂载）"); err != nil {
		log.Warn("obs: 注册配置项失败", "name", "OBS_ENABLED", "err", err)
	}
	if err := cfgMgr.Register(&o.pruneLogDays, "OBS_LOG_RETENTION_DAYS", strconv.Itoa(defaultLogPruneDays), "访问日志保留天数（自动清理开启后生效）"); err != nil {
		log.Warn("obs: 注册配置项失败", "name", "OBS_LOG_RETENTION_DAYS", "err", err)
	}
	if err := cfgMgr.Register(&o.trafficCacheTTL, "OBS_TRAFFIC_CACHE_TTL", "900", "流量统计结果缓存 TTL（秒；缺省 900=15 分钟，0=禁用缓存）", "统计为按需 SQL 聚合，缓存防大表重复聚合拖库；命中时响应 computed_at 保持首次计算时刻；可在 WebUI 流量统计卡「清空缓存」手动失效"); err != nil {
		log.Warn("obs: 注册配置项失败", "name", "OBS_TRAFFIC_CACHE_TTL", "err", err)
	}
	o.trafficQueryTimeout = trafficQueryTimeoutConst
	o.tcache = newTrafficCache()
	o.sink.Store(NewAsyncStore(o.buildStore()))
	return o
}

// SetGeoip 注入 GeoIP 解析器（装配期一次；写时解析填 country/city 列，nil=不解析）。
func (o *Obs) SetGeoip(res *geoip.Resolver) { o.geo = res }

// Name 中间件名（hotswap 按此名启停）。
func (o *Obs) Name() string { return "obs" }

// Slot 挂载位置：Tail（响应阶段，配合 ResponseHook）。
func (o *Obs) Slot() chain.Slot { return chain.Tail }

// Handle 占位：响应处理全在 OnDone，不参与转发前逻辑（§14）。
func (o *Obs) Handle(ctx *chain.Context) (next bool) { return false }

// Start 重建存储后端并确保 access_log 自动清理循环在运行
// （Enable 热更时调用；幂等，Disable 后再 Enable 可恢复）。
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

// Count 按相同过滤条件统计访问日志总数（转发当前启用的存储后端，服务端分页用）。
func (o *Obs) Count(q Query) (int64, error) {
	return o.sink.Load().(*AsyncStore).Count(q)
}

// StorageSize 当前日志存储总占用（字节）：access_log 表 + 索引占用。
func (o *Obs) StorageSize() int64 {
	if o.dataDB == nil {
		return 0
	}
	v, err := NewDBStore(o.dataDB, accessLogTable).SizeBytes()
	if err != nil {
		return 0
	}
	return v
}

// OnResponse 实现 chain.ResponseHook：保持 no-op。
// obs 必须留在 Tail 响应钩子链上（HasResponseHook(Tail) 为真是缓冲路径 7b 的前提，
// RespBody/RespBytes 依赖缓冲）；记录落盘迁移至 OnDone（"完成时刻"语义取点）。
func (o *Obs) OnResponse(ctx *chain.Context) error { return nil }

// OnDone 实现 chain.DoneHook：响应写回客户端完成后构造 AccessRecord → 异步落盘 → 聚合指标。
// 此时 ctx.DF.DoneAt 已由 Adapter 取点，time 列复用其取值（D7：单一数据源）。
func (o *Obs) OnDone(ctx *chain.Context) {
	doneAt := ctx.DF.DoneAt()
	if doneAt.IsZero() {
		doneAt = time.Now()
	}
	al := &AccessRecord{
		Time:       doneAt,
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
		EgressMs:   ctx.DF.EgressMs(),
		ReqBytes:   ctx.R.ContentLength,
		RespBytes:  int64(len(ctx.RespBody)),
		UserAgent:  ctx.R.UserAgent(),
	}
	// geo 写时解析：装配期注入的共享 Resolver（未注入=nil 时保持空串，统计计「未知」，不阻断转发）。
	if o.geo != nil {
		// country 列存 ISO 码（聚合口径），city 列存省市
		gi := o.geo.Lookup(al.ClientIP)
		al.Country, al.City = gi.Code, gi.City
	}
	// 负载维度预留点：后期采集纯文本 POST 请求体等扩展字段时，
	// 先在 dim.go Dims 注册 payload 维度，再在此写 Extras（存储零改动）。
	o.sink.Load().(*AsyncStore).Write(al)
	// 指标时间源与落库 Time 同源（DoneAt），口径为真·总耗时（含客户端写回时间）。
	o.metrics.Add(doneAt, al.TotalMs, al.StatusCode)
}

// buildStore 构造底层存储后端：复用统一数据访问层写 access_log 表；
// 数据访问层未就绪/初始化失败时降级 discardStore（日志不落盘，不阻断转发）。
func (o *Obs) buildStore() Store {
	if o.dataDB == nil {
		return discardStore{}
	}
	st := NewDBStore(o.dataDB, accessLogTable)
	if err := st.EnsureTable(); err != nil {
		log.Warn("obs: 数据库存储初始化失败，访问日志不落盘", "err", err.Error())
		return discardStore{}
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

// pruneOnce 执行一次清理：开关开启时清理保留期外的 access_log 记录；数据访问层未就绪跳过。
func (o *Obs) pruneOnce() {
	if !o.pruneLogEnabled {
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
