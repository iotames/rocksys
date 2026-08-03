// Package obs L3 可观测性（RockObs）：访问日志 + 指标聚合 + 查询 API。
//
// 挂 chain.Tail 槽位并实现 chain.ResponseHook——OnResponse 是获得"请求完成事件"
// 的唯一通道：Forward 完成后 Adapter 回调，此时 ctx.RespCode/RespHeader/RespBody
// 与 ctx.DF 三时间戳均已就绪，据此构造 AccessLog 异步落盘并聚合到 Metrics。
package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/hotswap"

	"github.com/iotames/easyserver/log"
)

// 默认配置（§14）。
const (
	defaultLogDir        = "logs"
	defaultRetentionDays = 30

	// pendingCap 异步落盘队列上限，超出降级丢弃（不阻塞请求）。
	pendingCap = 4096
)

// AccessLog 访问日志（§14 关键类型）。
type AccessLog struct {
	Time       time.Time `json:"time"`
	TraceID    string    `json:"trace_id"`
	TenantID   string    `json:"tenant_id,omitempty"`
	Path       string    `json:"path"`
	Method     string    `json:"method"`
	ClientIP   string    `json:"client_ip"`
	StatusCode int       `json:"status_code"`
	Upstream   string    `json:"upstream"`
	ShieldMs   int64     `json:"shield_ms"`
	BizMs      int64     `json:"biz_ms"`
	TotalMs    int64     `json:"total_ms"`
	ReqBytes   int64     `json:"req_bytes"`
	RespBytes  int64     `json:"resp_bytes"`
}

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

// FileSink 访问日志落盘器：异步写入、按天切分、超期清理（§14 文件管理）。
// 写盘失败不阻塞请求——队列满或落盘出错时降级丢弃并计数告警。
type FileSink struct {
	dir           string
	retentionDays int

	mu      sync.Mutex // 保护 pending/f/curDate/dir/retentionDays
	pending []*AccessLog
	f       *os.File
	curDate string
	drop    atomic.Int64 // 丢弃计数（告警用）

	wake   chan struct{}   // 有数据待写信号（cap 1）
	flush  chan chan error // flush 请求通道
	closed atomic.Bool
}

// NewFileSink 创建落盘器并启动异步写入 goroutine。
func NewFileSink(dir string, retentionDays int) *FileSink {
	s := &FileSink{
		dir:           dir,
		retentionDays: retentionDays,
		wake:          make(chan struct{}, 1),
		flush:         make(chan chan error),
	}
	go s.run()
	return s
}

// Configure 热更配置（Start 调用）：重建目录/留存，并重置关闭态（支持重新启用）。
func (s *FileSink) Configure(dir string, retentionDays int) {
	s.mu.Lock()
	s.dir = dir
	s.retentionDays = retentionDays
	s.closed.Store(false)
	s.mu.Unlock()
}

// Write 异步入队一条日志；队列满或已关闭时降级丢弃 + 计数告警。
func (s *FileSink) Write(al *AccessLog) {
	if s.closed.Load() {
		s.drop.Add(1)
		log.Warn("obs: sink 已关闭，丢弃日志", "trace_id", al.TraceID, "drop_count", s.drop.Load())
		return
	}
	s.mu.Lock()
	if len(s.pending) >= pendingCap {
		s.mu.Unlock()
		s.drop.Add(1)
		log.Warn("obs: 日志队列已满，丢弃该条", "trace_id", al.TraceID, "drop_count", s.drop.Load())
		return
	}
	s.pending = append(s.pending, al)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Flush 阻塞直到已入队日志全部写盘并关闭文件句柄（§14 Shutdown 语义）。
// ctx 超时则返回 ctx.Err()（缓冲可能未全部落盘）。
func (s *FileSink) Flush(ctx context.Context) error {
	ack := make(chan error, 1)
	select {
	case s.flush <- ack:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-ack:
		if err == nil {
			s.closed.Store(true)
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// DropCount 返回累计丢弃条数（告警观测用）。
func (s *FileSink) DropCount() int64 { return s.drop.Load() }

// run 异步写入循环：唤醒即落盘，收到 flush 请求则排空缓冲并关文件。
func (s *FileSink) run() {
	for {
		select {
		case <-s.wake:
			s.writePending()
		case ack := <-s.flush:
			ack <- s.flushAll()
		}
	}
}

// writePending 取走全部 pending 并写盘；失败时整批降级丢弃并告警。
func (s *FileSink) writePending() {
	s.mu.Lock()
	batch := s.pending
	s.pending = nil
	s.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	if err := s.appendBatch(batch); err != nil {
		s.drop.Add(int64(len(batch)))
		log.Warn("obs: 访问日志写盘失败，丢弃该批", "err", err, "drop_count", s.drop.Load())
	}
}

// flushAll 排空缓冲 → 写盘 → 关闭文件句柄（供 Flush 调用）。
func (s *FileSink) flushAll() error {
	s.mu.Lock()
	batch := s.pending
	s.pending = nil
	s.mu.Unlock()
	var err error
	if len(batch) > 0 {
		if err = s.appendBatch(batch); err != nil {
			s.drop.Add(int64(len(batch)))
			log.Warn("obs: flush 写盘失败，丢弃该批", "err", err, "drop_count", s.drop.Load())
		}
	}
	if cerr := s.closeFile(); err == nil {
		err = cerr
	}
	return err
}

// appendBatch 将一批日志写入当天文件；跨天则切分新文件并执行留存清理。
// 调用方须持 s.mu。
func (s *FileSink) appendBatch(batch []*AccessLog) error {
	today := time.Now().Format("2006-01-02")
	if s.f == nil || s.curDate != today {
		if s.f != nil {
			_ = s.f.Close()
			s.f = nil
		}
		if s.dir == "" {
			s.dir = defaultLogDir
		}
		if err := os.MkdirAll(s.dir, 0o755); err != nil {
			return fmt.Errorf("obs: 创建日志目录 %s: %w", s.dir, err)
		}
		path := filepath.Join(s.dir, "access-"+today+".jsonl")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("obs: 打开日志文件 %s: %w", path, err)
		}
		s.f = f
		s.curDate = today
		s.cleanupOld()
	}
	for _, al := range batch {
		line, err := json.Marshal(al)
		if err != nil {
			continue // 单条序列化失败单独丢弃
		}
		if _, err := s.f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// closeFile 关闭当前文件句柄（调用方持锁）。
func (s *FileSink) closeFile() error {
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	s.curDate = ""
	return err
}

// cleanupOld 清理超过留存天数的历史日志文件（调用方持锁）。
func (s *FileSink) cleanupOld() {
	if s.retentionDays <= 0 {
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -s.retentionDays)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "access-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		dateStr := strings.TrimSuffix(strings.TrimPrefix(name, "access-"), ".jsonl")
		d, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if d.Before(cutoff) {
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

// Obs 可观测性中间件：挂 chain.Tail，实现 chain.ResponseHook。
type Obs struct {
	cfg           conf.Manager
	logDir        string // *string 注册：OBS_LOG_DIR
	retentionDays int    // *int 注册：OBS_RETENTION_DAYS
	sink          *FileSink
	metrics       *Metrics
}

// 编译期断言：Obs 实现 hotswap.MiddlewareLifecycle 与 chain.ResponseHook。
var (
	_ hotswap.MiddlewareLifecycle = (*Obs)(nil)
	_ chain.ResponseHook          = (*Obs)(nil)
)

// New 创建 obs 挂件并注册自身配置项（§14）。
func New(cfgMgr conf.Manager) *Obs {
	o := &Obs{
		cfg:           cfgMgr,
		logDir:        defaultLogDir,
		retentionDays: defaultRetentionDays,
		metrics:       NewMetrics(),
	}
	_ = cfgMgr.Register(&o.logDir, "OBS_LOG_DIR", defaultLogDir, "访问日志目录")
	_ = cfgMgr.Register(&o.retentionDays, "OBS_RETENTION_DAYS", strconv.Itoa(defaultRetentionDays), "访问日志保留天数")
	o.sink = NewFileSink(o.logDir, o.retentionDays)
	return o
}

// Name 中间件名（hotswap 按此名启停）。
func (o *Obs) Name() string { return "obs" }

// Slot 挂载位置：Tail（响应阶段，配合 ResponseHook）。
func (o *Obs) Slot() chain.Slot { return chain.Tail }

// Handle 占位：响应处理全在 OnResponse，不参与转发前逻辑（§14）。
func (o *Obs) Handle(ctx *chain.Context) (next bool) { return false }

// Start 用当前配置重建落盘器配置（热更/Enable 时调用）。
func (o *Obs) Start(cfg any) error {
	o.sink.Configure(o.logDir, o.retentionDays)
	return nil
}

// Stop 桥接 Shutdown（hotswap.MiddlewareLifecycle.Stop 无 context 参数，§14 旁注）。
func (o *Obs) Stop() error { return o.Shutdown(context.Background()) }

// Shutdown flush 内存缓冲区中未写入的日志，然后关闭文件句柄（进程退出前必须调用）。
func (o *Obs) Shutdown(ctx context.Context) error { return o.sink.Flush(ctx) }

// Metrics 返回指标聚合器（供 admin handler 读取）。
func (o *Obs) Metrics() *Metrics { return o.metrics }

// OnResponse 实现 chain.ResponseHook：构造 AccessLog → 异步落盘 → 聚合指标。
func (o *Obs) OnResponse(ctx *chain.Context) error {
	al := &AccessLog{
		Time:       time.Now(),
		TraceID:    ctx.DF.TraceID(),
		TenantID:   ctx.DF.TenantID(),
		Path:       ctx.R.URL.Path,
		Method:     ctx.R.Method,
		ClientIP:   ctx.R.RemoteAddr,
		StatusCode: ctx.RespCode,
		Upstream:   ctx.DF.Target(),
		ShieldMs:   ctx.DF.ShieldMs(),
		BizMs:      ctx.DF.BizMs(),
		TotalMs:    ctx.DF.TotalMs(),
		ReqBytes:   ctx.R.ContentLength,
		RespBytes:  int64(len(ctx.RespBody)),
	}
	o.sink.Write(al)
	o.metrics.Add(time.Now(), al.TotalMs, al.StatusCode)
	return nil
}
