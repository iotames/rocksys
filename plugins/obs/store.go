// 访问日志存储抽象：Store 接口 + 通用异步写入包装 AsyncStore。
//
// 存储后端可热切换（OBS_STORE=db|file，默认 db）：Obs 持有当前 AsyncStore 的原子引用，
// 配置热更时重建底层 Store 并替换（见 obs.go rebuildStore）。
// 异步排队是通用能力（file/db 都受益于批量写入），故与具体后端解耦：
//   - AsyncStore：pending 队列 + worker 批量落盘 + 队列满降级丢弃（不阻塞请求）
//   - FileStore / DBStore：仅实现同步的底层写入与查询
package obs

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iotames/easyserver/log"
)

// defaultQueryLimit 查询默认返回上限（与前端展示上限一致）。
const defaultQueryLimit = 2000

// Query 访问日志查询条件（/admin/logs 参数映射）。
// 时间范围含端点；Path/PathLike/TraceID 为空串表示不过滤。
type Query struct {
	From     time.Time // 开始时间（含）
	To       time.Time // 结束时间（含）
	Path     string    // path 精确匹配
	PathLike string    // path 模糊匹配（Contains / LIKE %v%）
	TraceID  string    // trace_id 模糊匹配
	Limit    int       // 返回上限，<=0 用 defaultQueryLimit
}

// Store 访问日志存储后端接口。
// Write 为同步批量写（异步排队由 AsyncStore 承担）；Query 返回平铺维度 map（time 升序）。
type Store interface {
	// Name 存储后端名（"file" / "db"）。
	Name() string
	// Write 同步写入一批记录；失败返回 error（调用方负责告警/丢弃）。
	Write(batch []*AccessRecord) error
	// Query 按条件查询，返回平铺维度 map 列表（含负载维度）。
	Query(q Query) ([]map[string]any, error)
	// SizeBytes 返回该后端已存储日志的总字节数（file 为 access-*.jsonl 文件合计；db 为表+索引）。
	SizeBytes() (int64, error)
	// Flush 冲刷后端缓冲（file 无缓冲返回 nil；DB 由事务/连接层保证）。
	Flush(ctx context.Context) error
	// Close 释放后端资源（FileStore 关文件；DBStore 连接由 dataDB 统一管理，no-op）。
	Close() error
}

// asyncCap 异步队列上限，超出降级丢弃（不阻塞请求）。
const asyncCap = 4096

// AsyncStore 通用异步写入包装：为任意 Store 提供"异步排队 + 批量落盘 + 队列满降级"语义。
// 线程安全：Write/Query 可并发；Replace 原子切换底层后端；Close 后 run 退出（无 goroutine 泄漏）。
type AsyncStore struct {
	mu      sync.Mutex // 保护 store/pending（Write 的 closed 检查也在锁内，与 Flush 排空互斥）
	store   Store
	pending []*AccessRecord
	drop    atomic.Int64 // 丢弃计数（告警用）

	wake   chan struct{}   // 有数据待写信号（cap 1）
	flush  chan chan error // flush 请求通道
	done   chan struct{}   // 关闭信号：Close 后 run 退出
	closed atomic.Bool
}

// NewAsyncStore 创建异步包装并启动 worker goroutine。
func NewAsyncStore(s Store) *AsyncStore {
	a := &AsyncStore{
		store: s,
		wake:  make(chan struct{}, 1),
		flush: make(chan chan error),
		done:  make(chan struct{}),
	}
	go a.run()
	return a
}

// Name 返回当前底层后端名。
func (a *AsyncStore) Name() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.store.Name()
}

// Replace 原子替换底层后端，返回旧后端（调用方负责 Flush/Close 旧后端）。
// 替换后 pending 中的存量记录仍由 worker 写往新后端（时间戳为记录自身，落位正确）。
func (a *AsyncStore) Replace(s Store) Store {
	a.mu.Lock()
	old := a.store
	a.store = s
	a.mu.Unlock()
	return old
}

// Write 异步入队一条记录；队列满或已关闭时降级丢弃 + 计数告警（不阻塞请求）。
// closed 检查与入队同持 mu：与 Flush 排空互斥，保证"排空后入队"必然被丢弃、"排空前入队"必然被排空。
func (a *AsyncStore) Write(al *AccessRecord) {
	a.mu.Lock()
	if a.closed.Load() {
		a.mu.Unlock()
		a.drop.Add(1)
		log.Warn("obs: 存储已关闭，丢弃日志", "trace_id", al.TraceID, "drop_count", a.drop.Load())
		return
	}
	if len(a.pending) >= asyncCap {
		a.mu.Unlock()
		a.drop.Add(1)
		log.Warn("obs: 日志队列已满，丢弃该条", "trace_id", al.TraceID, "drop_count", a.drop.Load())
		return
	}
	a.pending = append(a.pending, al)
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Query 转发当前底层后端查询。
func (a *AsyncStore) Query(q Query) ([]map[string]any, error) {
	a.mu.Lock()
	s := a.store
	a.mu.Unlock()
	return s.Query(q)
}

// Flush 阻塞直到已入队记录全部写入底层并冲刷后端；成功后标记关闭。
// ctx 超时返回 ctx.Err()（缓冲可能未全部落盘）。重复调用幂等。
func (a *AsyncStore) Flush(ctx context.Context) error {
	if a.closed.Load() {
		return nil
	}
	ack := make(chan error, 1)
	select {
	case a.flush <- ack:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-ack:
		if err == nil {
			a.closed.Store(true)
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 排空并关闭底层后端资源（等价 Flush + 底层 Close），并令 run goroutine 退出。
func (a *AsyncStore) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Flush(ctx); err != nil {
		return err
	}
	select {
	case <-a.done:
	default:
		close(a.done)
	}
	a.mu.Lock()
	s := a.store
	a.mu.Unlock()
	return s.Close()
}

// DropCount 返回累计丢弃条数（告警观测用）。
func (a *AsyncStore) DropCount() int64 { return a.drop.Load() }

// run 异步写入循环：唤醒即批量落盘，收到 flush 请求则排空缓冲并冲刷底层，关闭则退出。
func (a *AsyncStore) run() {
	for {
		select {
		case <-a.wake:
			a.writePending()
		case ack := <-a.flush:
			ack <- a.flushAll()
		case <-a.done:
			return
		}
	}
}

// writePending 取走全部 pending 并写入底层；失败时整批降级丢弃并告警。
func (a *AsyncStore) writePending() {
	a.mu.Lock()
	if len(a.pending) == 0 {
		a.mu.Unlock()
		return
	}
	batch := a.pending
	a.pending = nil
	s := a.store
	a.mu.Unlock()

	if err := s.Write(batch); err != nil {
		a.drop.Add(int64(len(batch)))
		log.Warn("obs: 访问日志写入失败，丢弃该批", "store", s.Name(), "err", err, "drop_count", a.drop.Load())
	}
}

// flushAll 排空缓冲 → 写底层 → 冲刷底层（供 Flush 调用）。
// 排空与 closed 置位在锁内完成：此后 Write 必被丢弃（见 Write 注释）。
func (a *AsyncStore) flushAll() error {
	a.mu.Lock()
	batch := a.pending
	a.pending = nil
	s := a.store
	a.closed.Store(true)
	a.mu.Unlock()

	var err error
	if len(batch) > 0 {
		if err = s.Write(batch); err != nil {
			a.drop.Add(int64(len(batch)))
			log.Warn("obs: flush 写入失败，丢弃该批", "store", s.Name(), "err", err, "drop_count", a.drop.Load())
		}
	}
	if cerr := s.Flush(context.Background()); err == nil {
		err = cerr
	}
	return err
}
