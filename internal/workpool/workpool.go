// Package workpool 通用 goroutine 工作池：以有限的 worker 数量承载高并发任务，
// 避免无限 goroutine 堆叠导致的内存膨胀与调度开销。
//
// 参考 todo/hotswap/workpool.go 示例，修复并加固了原实现的并发缺陷：
//  1. 队列引用（taskQueue）读写用 RWMutex 保护，杜绝 Submit 与队列切换的竞态；
//  2. Stop/UpdateWorkers/UpdateQueueSize 用 stateMutex 串行化，防止并发重建；
//  3. worker 不随重建停止：队列调整仅切换 channel，worker 继续消费，天然规避
//     "停 worker 窗口 Submit 阻塞持锁 → 重建等待锁" 的死锁；
//  4. 减少 worker 采用"目标数优雅退出"：worker 处理完手头任务后自行退出；
//  5. Submit 系列经 stopped 原子标志 + quit 信号双重解除，杜绝向已关闭队列发送（panic）。
package workpool

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// workerCheckInterval 减少 worker 时，空闲 worker 定期醒来检查目标数的间隔。
const workerCheckInterval = 100 * time.Millisecond

// Task 任务接口。
type Task interface {
	Execute()
}

// TaskFunc 函数类型任务适配器。
type TaskFunc func()

// Execute 实现 Task 接口。
func (f TaskFunc) Execute() { f() }

// Config 工作池配置。
type Config struct {
	MinWorkers int // 最小工作线程数（默认 runtime.NumCPU()*2）
	MaxWorkers int // 最大工作线程数（动态扩展上限，0 表示不设上限）
	QueueSize  int // 任务队列容量（默认 MinWorkers*10）
}

// WorkerPool 工作池：动态 worker + 有界任务队列。
type WorkerPool struct {
	// stateMutex 串行化 Stop/UpdateWorkers/UpdateQueueSize（全程持锁，防并发重建）。
	stateMutex sync.Mutex
	stopped    atomic.Bool

	// queueMutex 保护 taskQueue 引用与队列长度读取。
	// Submit 系列持 RLock 发送；队列切换持 Lock，保证不向已关闭队列发送。
	queueMutex sync.RWMutex
	taskQueue  chan Task

	quit chan struct{}
	wg   sync.WaitGroup

	// initialWorkers 启动时 worker 数；targetWorkers 期望 worker 数；
	// activeWorkers 实际运行数；nextWorkerID 全局 worker 编号。
	// worker 编号 >= targetWorkers 时处理完手头任务后自行退出（减少 worker 机制）。
	initialWorkers int
	targetWorkers  atomic.Int32
	activeWorkers  atomic.Int32
	nextWorkerID   atomic.Int32
}

// NewWorkerPool 创建工作池（未启动，需调用 Start）。
func NewWorkerPool(config Config) *WorkerPool {
	if config.MinWorkers <= 0 {
		config.MinWorkers = runtime.NumCPU() * 2
	}
	if config.QueueSize <= 0 {
		config.QueueSize = config.MinWorkers * 10
	}
	return &WorkerPool{
		taskQueue:      make(chan Task, config.QueueSize),
		quit:           make(chan struct{}),
		initialWorkers: config.MinWorkers,
	}
}

// Start 启动工作池（启动初始 worker 数）。
func (wp *WorkerPool) Start() {
	wp.targetWorkers.Store(int32(wp.initialWorkers))
	wp.startWorkers(wp.initialWorkers)
}

// startWorkers 启动 n 个 worker goroutine（编号从 nextWorkerID 递增）。
func (wp *WorkerPool) startWorkers(n int) {
	for i := 0; i < n; i++ {
		id := int(wp.nextWorkerID.Add(1) - 1)
		wp.activeWorkers.Add(1)
		wp.wg.Add(1)
		go wp.worker(id)
	}
}

// Submit 提交任务（阻塞）：队列满时等待有空位；池停止时静默放弃。
func (wp *WorkerPool) Submit(task Task) {
	if task == nil || wp.stopped.Load() {
		return
	}
	wp.queueMutex.RLock()
	defer wp.queueMutex.RUnlock()
	if wp.stopped.Load() {
		return
	}
	select {
	case wp.taskQueue <- task:
	case <-wp.quit: // 池停止，放弃提交（避免永久阻塞持锁）
	}
}

// TrySubmit 尝试提交任务（非阻塞）：队列满或池已停止立即返回 false。
func (wp *WorkerPool) TrySubmit(task Task) bool {
	if task == nil || wp.stopped.Load() {
		return false
	}
	wp.queueMutex.RLock()
	defer wp.queueMutex.RUnlock()
	if wp.stopped.Load() {
		return false
	}
	select {
	case wp.taskQueue <- task:
		return true
	default:
		return false
	}
}

// SubmitWithTimeout 带超时提交：等待 timeout 内队列有空位则成功，否则返回 false。
func (wp *WorkerPool) SubmitWithTimeout(task Task, timeout time.Duration) bool {
	if task == nil || wp.stopped.Load() {
		return false
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	wp.queueMutex.RLock()
	defer wp.queueMutex.RUnlock()
	if wp.stopped.Load() {
		return false
	}
	select {
	case wp.taskQueue <- task:
		return true
	case <-wp.quit:
		return false
	case <-ctx.Done():
		return false
	}
}

// QueueSize 返回当前队列近似长度（worker 领取任务前的瞬间值，仅供监控）。
func (wp *WorkerPool) QueueSize() int {
	wp.queueMutex.RLock()
	defer wp.queueMutex.RUnlock()
	return len(wp.taskQueue)
}

// WorkerCount 返回当前期望运行的 worker 数。
func (wp *WorkerPool) WorkerCount() int {
	return int(wp.targetWorkers.Load())
}

// Stop 停止工作池：停止接收新任务 → 等待 worker 退出 → 串行执行队列中剩余任务。
// 幂等。
func (wp *WorkerPool) Stop() {
	wp.stateMutex.Lock()
	defer wp.stateMutex.Unlock()

	if wp.stopped.Swap(true) {
		return
	}

	// 关闭 quit：解除所有阻塞在 Submit 的 goroutine，并让 worker 退出
	close(wp.quit)
	wp.wg.Wait()

	// 队列剩余任务串行执行（保证已提交任务不丢失）
	wp.queueMutex.Lock()
	close(wp.taskQueue)
	for task := range wp.taskQueue {
		if task != nil {
			task.Execute()
		}
	}
	wp.queueMutex.Unlock()
}

// UpdateWorkers 动态调整 worker 数量。
// 增加：直接启动新 worker；减少：设目标数，多余 worker 处理完手头任务后自行退出。
func (wp *WorkerPool) UpdateWorkers(num int) error {
	if num <= 0 {
		num = runtime.NumCPU() * 2
	}
	wp.stateMutex.Lock()
	defer wp.stateMutex.Unlock()
	if wp.stopped.Load() {
		return fmt.Errorf("workpool: worker pool is stopped")
	}

	cur := int(wp.activeWorkers.Load())
	if num == cur {
		return nil
	}
	if num > cur {
		wp.targetWorkers.Store(int32(num))
		wp.startWorkers(num - cur)
	} else {
		// 减少：worker 在 select 定时醒来后按编号退出
		wp.targetWorkers.Store(int32(num))
	}
	return nil
}

// UpdateQueueSize 动态调整队列容量（仅切换 channel，不停止 worker）。
func (wp *WorkerPool) UpdateQueueSize(size int) error {
	if size <= 0 {
		return fmt.Errorf("workpool: queue size must be positive")
	}
	wp.stateMutex.Lock()
	defer wp.stateMutex.Unlock()
	if wp.stopped.Load() {
		return fmt.Errorf("workpool: worker pool is stopped")
	}

	wp.queueMutex.Lock()
	oldQueue := wp.taskQueue
	newQueue := make(chan Task, size)
	wp.taskQueue = newQueue
	wp.queueMutex.Unlock()

	// 此后 Submit 只会写 newQueue；oldQueue 无并发写入，可安全 close+遍历。
	// 若有 worker 恰在切换前快照了 oldQueue 并在 close 后读到 nil，则跳过（安全）。
	pending := wp.drainQueue(oldQueue)
	// 合并剩余任务到新队列：阻塞发送保证不丢任务（worker 持续消费，不会死锁）。
	for _, task := range pending {
		if task == nil {
			continue
		}
		newQueue <- task
	}
	return nil
}

// drainQueue 排空队列剩余任务并返回（调用方保证无并发写入）。
func (wp *WorkerPool) drainQueue(q chan Task) []Task {
	var tasks []Task
	close(q)
	for task := range q {
		tasks = append(tasks, task)
	}
	return tasks
}

// worker 从队列取任务执行；编号 >= targetWorkers 时处理完当前任务后退出；
// 收到 quit 信号立即退出（Stop）。
func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()
	defer wp.activeWorkers.Add(-1)

	for {
		if int(wp.targetWorkers.Load()) <= id {
			return // 减少 worker：按编号优雅退出
		}
		select {
		case task := <-wp.taskQueue:
			if task != nil {
				task.Execute()
			} else {
				// closed 且排空：短暂让步，等待 quit
				time.Sleep(time.Millisecond)
			}
		case <-wp.quit:
			return
		case <-time.After(workerCheckInterval):
			// 定期醒来检查 target（队列长期空闲时也能响应减少 worker）
		}
	}
}
