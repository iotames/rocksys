// Package workpool 通用 goroutine 工作池：以有限的 worker 数量承载高并发任务，
// 避免无限 goroutine 堆叠导致的内存膨胀与调度开销。
//
// # 使用说明
//
// 基本流程：创建 → 启动 → 提交 → 停止。
//
//	pool := workpool.NewWorkerPool(workpool.Config{
//		MinWorkers: 16,  // 启动即就绪的 worker 数（默认 runtime.NumCPU()*2）
//		QueueSize:  160, // 有界队列容量（默认 MinWorkers*10）
//		MaxWorkers: 64,  // UpdateWorkers 扩展上限（0 表示不设上限）
//	})
//	pool.Start()          // 幂等；已 Stop 后再次调用无效（一次性生命周期）
//	defer pool.Stop()
//
//	// 阻塞提交：队列满时等待空位（池已停止则静默放弃）
//	pool.Submit(workpool.TaskFunc(func() { /* 业务逻辑 */ }))
//	// 非阻塞提交：队列满或池已停止立即返回 false
//	ok := pool.TrySubmit(task)
//	// 带超时提交：timeout 内未入队返回 false（timeout<=0 时默认 3 秒）
//	ok = pool.SubmitWithTimeout(task, time.Second)
//
// 动态调整（需在 Start 之后调用；运行期安全，不停止 worker、不丢任务）：
//
//	pool.UpdateWorkers(32)   // 增加直接扩容；减少则多余 worker 处理完手头任务后自行退出
//	pool.UpdateQueueSize(64) // 仅切换 channel，排空旧队列并把剩余任务合并到新队列
//
// 语义约定：
//   - 创建后先 Start 再使用：Start 之前 Submit 的任务会堆积在队列，Start 后才被消费；
//   - Stop 幂等：先停止接收新任务，再等待 worker 退出，最后串行执行队列剩余任务（不丢失）；
//   - 停止后 Submit 系列不会 panic：Submit 静默放弃（含回调内再提交的任务，会被丢弃），
//     TrySubmit/SubmitWithTimeout 返回 false；
//   - WorkerCount() 返回"期望" worker 数（target），减少 worker 后实际数量稍后收敛到该值；
//   - 约束：任务回调（Execute）内不要调用池管理方法——Stop 在回调内调用会
//     确定性自死锁（wg.Wait 等待包括自身在内的 worker 退出）；UpdateQueueSize
//     在 worker 忙时也可能自死锁（合并搬运的阻塞发送等待自身消费）；UpdateWorkers
//     与 Stop 并发时同样可能互等。最稳妥约定：回调内只做业务；
//     QueueSize()/WorkerCount() 等只读方法在回调内可安全调用；
//   - 参数参考：CPU 密集取 CPU 核数×1-2，I/O 密集取 ×2-4，混合型从 ×2 起步并基准测试；
//     经验公式：最佳 worker 数 ≈ CPU 核数×2 + 阻塞 I/O 操作数；队列容量一般取 worker 数的 10 倍；
//   - 动态调参建议（结合 QueueSize 与提交失败率监控）：内存占用飙升 → 降低 QueueSize/MinWorkers；
//     队列经常爆满 / TrySubmit、SubmitWithTimeout 失败率高 → 增加 QueueSize 或 MinWorkers。
package workpool

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// workerCheckInterval 减少 worker 时，空闲 worker 定期醒来检查目标数的间隔。
const workerCheckInterval = 100 * time.Millisecond

// submitRetryInterval Submit 系列在队列满时重试入队的间隔。
// 提交者不在持锁期间阻塞等待空位（瞬时尝试后释放锁再等待重试），
// 与队列切换的写锁、worker 的读锁互不形成等待环。
const submitRetryInterval = 500 * time.Microsecond

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
	MaxWorkers int // 最大工作线程数（动态扩展上限；<=0 表示不设上限）
	QueueSize  int // 任务队列容量（默认 MinWorkers*10）
}

// WorkerPool 工作池：动态 worker + 有界任务队列。
type WorkerPool struct {
	// stateMutex 串行化 Stop/Start/UpdateWorkers/UpdateQueueSize（全程持锁，防并发重建）。
	stateMutex sync.Mutex
	stopped    atomic.Bool
	started    atomic.Bool // 是否已 Start（Start 幂等防护）

	// queueMutex 保护 taskQueue 引用与队列长度读取。
	// Submit 系列持 RLock 做瞬时入队尝试；队列切换持 Lock，保证不向已关闭队列发送。
	queueMutex sync.RWMutex
	taskQueue  chan Task

	quit chan struct{}
	wg   sync.WaitGroup

	// initialWorkers 启动时 worker 数；targetWorkers 期望 worker 数；
	// activeWorkers 实际运行数；nextWorkerID 全局 worker 编号（仅作标识，不决定退出）。
	// 减少 worker 时，活跃数超过 targetWorkers 的 worker 处理完手头任务后认领退出名额退出。
	// maxWorkers 为 UpdateWorkers 扩展上限（0 表示不设上限）。
	initialWorkers int
	maxWorkers     int
	targetWorkers  atomic.Int32
	activeWorkers  atomic.Int32
	nextWorkerID   atomic.Int32
}

// NewWorkerPool 创建工作池（未启动，需调用 Start）。
func NewWorkerPool(config Config) *WorkerPool {
	if config.MinWorkers <= 0 {
		config.MinWorkers = runtime.NumCPU() * 2
	}
	// 配置边界防御：初始 worker 数不超 MaxWorkers（否则初始启动即超限）。
	if config.MaxWorkers > 0 && config.MinWorkers > config.MaxWorkers {
		config.MinWorkers = config.MaxWorkers
	}
	if config.QueueSize <= 0 {
		config.QueueSize = config.MinWorkers * 10
	}
	return &WorkerPool{
		taskQueue:      make(chan Task, config.QueueSize),
		quit:           make(chan struct{}),
		initialWorkers: config.MinWorkers,
		maxWorkers:     config.MaxWorkers,
	}
}

// Start 启动工作池（启动初始 worker 数）。幂等：重复调用不重复启动；
// 已 Stop 后调用无效（worker 池一次性生命周期）。
func (wp *WorkerPool) Start() {
	wp.stateMutex.Lock()
	defer wp.stateMutex.Unlock()
	// stopped/started 检查与 wg.Add 同锁串行：消除并发 Start/Stop 的
	// WaitGroup misuse（Add 与 Wait 并发）风险与重复 Start 双倍 worker 问题。
	if wp.stopped.Load() || wp.started.Swap(true) {
		return
	}
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

// Submit 提交任务（阻塞）：队列满时轮询等待空位（不持锁等待）；池停止时静默放弃。
// nil 任务会被静默丢弃。
func (wp *WorkerPool) Submit(task Task) {
	if task == nil || wp.stopped.Load() {
		return
	}
	var ticker *time.Ticker
	defer func() {
		if ticker != nil {
			ticker.Stop() // 所有返回路径统一 Stop：重试路径不泄漏 timer
		}
	}()
	for {
		// 瞬时尝试：持 RLock 期间 Stop 无法关闭 taskQueue（close 需写锁），
		// 故 RLock 内复查未停止后发送，绝不会向已关闭队列发送。
		wp.queueMutex.RLock()
		if wp.stopped.Load() {
			wp.queueMutex.RUnlock()
			return
		}
		select {
		case wp.taskQueue <- task:
			wp.queueMutex.RUnlock()
			return
		default:
			wp.queueMutex.RUnlock()
		}
		// 队列满：等待后重试。ticker 懒创建：正常路径（一次入队即成功）零额外分配。
		if ticker == nil {
			ticker = time.NewTicker(submitRetryInterval)
		}
		select {
		case <-wp.quit:
			return
		case <-ticker.C:
		}
	}
}

// TrySubmit 尝试提交任务（非阻塞）：队列满或池已停止立即返回 false；nil 任务返回 false。
// 建议将失败率纳入监控统计（配合 SubmitWithTimeout），用于后续动态调参。
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

// SubmitWithTimeout 带超时提交：timeout 内队列有空位则成功，否则返回 false
// （timeout<=0 时默认 3 秒）。同样采用"瞬时尝试 + 释放锁等待重试"模式，不持锁阻塞。
// 超时失败说明队列持续满载，建议计入监控统计以便后续调参
// （调参建议见包注释"动态调参建议"）。
func (wp *WorkerPool) SubmitWithTimeout(task Task, timeout time.Duration) bool {
	if task == nil || wp.stopped.Load() {
		return false
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var ticker *time.Ticker
	defer func() {
		if ticker != nil {
			ticker.Stop() // 所有返回路径统一 Stop：重试路径不泄漏 timer
		}
	}()
	for {
		// 与 Submit 相同：RLock 内先确认未停止再发送，绝不向已关闭队列发送。
		wp.queueMutex.RLock()
		if wp.stopped.Load() {
			wp.queueMutex.RUnlock()
			return false
		}
		select {
		case wp.taskQueue <- task:
			wp.queueMutex.RUnlock()
			return true
		default:
			wp.queueMutex.RUnlock()
		}
		if time.Now().After(deadline) {
			return false
		}
		if ticker == nil {
			ticker = time.NewTicker(submitRetryInterval)
		}
		// 等待：池停止或超时则失败，否则稍后重试
		select {
		case <-wp.quit:
			return false
		case <-ticker.C:
		}
	}
}

// QueueSize 返回当前队列近似长度（worker 领取任务前的瞬间值，仅供监控）。
func (wp *WorkerPool) QueueSize() int {
	wp.queueMutex.RLock()
	defer wp.queueMutex.RUnlock()
	return len(wp.taskQueue)
}

// WorkerCount 返回期望运行的 worker 数（targetWorkers，UpdateWorkers 的目标值）。
// 减少 worker 后实际运行数（activeWorkers）会在任务间隙收敛到该值，可能与返回值短暂不一致；
// 未 Start 时为 0，Stop 后为 0。
func (wp *WorkerPool) WorkerCount() int {
	return int(wp.targetWorkers.Load())
}

// ActiveWorkerCount 返回当前实际运行的 worker 数（activeWorkers）。
// 与 WorkerCount（targetWorkers，期望值）不同——动态调小 worker 后，实际运行数
// 会在任务间隙收敛到期望值，二者在过渡期短暂不一致；如需监控"真实负载承载"用本方法。
func (wp *WorkerPool) ActiveWorkerCount() int {
	return int(wp.activeWorkers.Load())
}

// MaxWorkerCount 返回 worker 扩展开放的最大容量（maxWorkers）。
// <=0 表示不设上限（UpdateWorkers 不受 MaxWorkers 约束）。
func (wp *WorkerPool) MaxWorkerCount() int {
	return wp.maxWorkers
}

// QueueCapacity 返回有界队列的总容量（动态调整队列后随之变化）。
// 与 QueueSize（当前积压长度）组合用于监控队列水位与调参，见包注释"动态调参建议"。
func (wp *WorkerPool) QueueCapacity() int {
	wp.queueMutex.RLock()
	defer wp.queueMutex.RUnlock()
	return cap(wp.taskQueue)
}

// Running 返回工作池是否处于运行状态（未调用过 Stop）
func (wp *WorkerPool) Running() bool {
	return !wp.stopped.Load()
}

// Stop 停止工作池：停止接收新任务 → 等待 worker 退出 → 串行执行队列中剩余任务。
// 幂等。剩余任务在 stateMutex 之外执行：stopped 已置位，回调内调用
// Stop/UpdateWorkers/UpdateQueueSize 只会拿到错误返回，不会与 stateMutex 互等死锁。
func (wp *WorkerPool) Stop() {
	wp.stateMutex.Lock()
	if wp.stopped.Swap(true) {
		wp.stateMutex.Unlock()
		return
	}
	// 语义对齐：已停止后期望 worker 数为 0（WorkerCount 不再返回旧 target）。
	wp.targetWorkers.Store(0)

	// 关闭 quit：解除等待重试中的 Submit goroutine，并让 worker 退出
	close(wp.quit)
	wp.wg.Wait()

	// 队列剩余任务收集（保证已提交任务不丢失）。收集阶段持锁 close 并取出
	// 全部剩余任务，随即释放写锁。
	wp.queueMutex.Lock()
	close(wp.taskQueue)
	var remaining []Task
	for task := range wp.taskQueue {
		if task != nil {
			remaining = append(remaining, task)
		}
	}
	wp.queueMutex.Unlock()
	wp.stateMutex.Unlock()

	// 锁外串行执行剩余任务：回调内调用 QueueSize()/WorkerCount() 等只读方法安全。
	for _, task := range remaining {
		task.Execute()
	}
}

// UpdateWorkers 动态调整 worker 数量（需在 Start 之后调用）。
// 增加：直接启动新 worker（受 MaxWorkers 上限约束，0 表示不设上限）；
// 减少：只更新目标数，超员 worker 处理完手头任务后自行认领退出。
func (wp *WorkerPool) UpdateWorkers(num int) error {
	if num <= 0 {
		num = runtime.NumCPU() * 2 // 0/负数：恢复为 runtime.NumCPU()*2（非构造时的 MinWorkers）
	}
	if wp.maxWorkers > 0 && num > wp.maxWorkers {
		num = wp.maxWorkers
	}
	wp.stateMutex.Lock()
	defer wp.stateMutex.Unlock()
	if wp.stopped.Load() {
		return fmt.Errorf("workpool: worker pool is stopped")
	}
	if !wp.started.Load() {
		return fmt.Errorf("workpool: worker pool not started")
	}

	// 比较基准用 targetWorkers（期望值）：减少 worker 是异步优雅退出（最长
	// workerCheckInterval 收敛），若以 activeWorkers 为基准，"减少未收敛窗口内的
	// 目标回弹"（16→8 后立即再调 16）会被静默忽略，target 停在 8。
	cur := int(wp.targetWorkers.Load())
	if num == cur {
		return nil
	}
	if num > cur {
		wp.targetWorkers.Store(int32(num))
		// 启动数按 activeWorkers 差值：若仍在减少收敛中（active >= num），
		// 超员 worker 因 target 回升而不再退出，无需（也不应）再启动；
		// 若 active < num 则补足差额。
		if active := int(wp.activeWorkers.Load()); active < num {
			wp.startWorkers(num - active)
		}
	} else {
		// 减少：只设目标，worker 醒来后按活跃数认领退出
		wp.targetWorkers.Store(int32(num))
	}
	return nil
}

// UpdateQueueSize 动态调整队列容量（需在 Start 之后调用；仅切换 channel，不停止 worker）。
// 注意：任务量很大而新容量较小时，本调用会持锁阻塞至剩余任务全部搬运完成
// （期间 Stop/UpdateWorkers 排队等待），以保证不丢任务。
func (wp *WorkerPool) UpdateQueueSize(size int) error {
	if size <= 0 {
		return fmt.Errorf("workpool: queue size must be positive")
	}
	wp.stateMutex.Lock()
	defer wp.stateMutex.Unlock()
	if wp.stopped.Load() {
		return fmt.Errorf("workpool: worker pool is stopped")
	}
	if !wp.started.Load() {
		return fmt.Errorf("workpool: worker pool not started")
	}

	// 容量未变：直接返回，避免无谓的 channel 重建与任务搬运。
	if size == cap(wp.taskQueue) {
		return nil
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

// worker 从队列取任务执行；活跃 worker 数超过 targetWorkers 时，处理完手头任务后
// 认领退出名额并退出（减少 worker 机制）；收到 quit 信号立即退出（Stop）。
func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()
	// 每个 worker 恰好扣减一次 activeWorkers：超员退出走 CAS 预扣（置位 retired，
	// 避免 defer 重复扣减）；quit 退出走这里兜底扣减。
	var retired atomic.Bool
	defer func() {
		if !retired.Swap(true) {
			wp.activeWorkers.Add(-1)
		}
	}()

	ticker := time.NewTicker(workerCheckInterval)
	defer ticker.Stop()

	for {
		// 减少 worker：活跃数超过目标时认领退出名额（CAS 保证并发下不多退）。
		// 采用"活跃数"而非"worker 编号"判定：编号只增不减，编号方案在
		// "减少未收敛时目标回弹"场景会启动编号超标、立即退出的新 worker。
		for {
			active := wp.activeWorkers.Load()
			if active <= wp.targetWorkers.Load() {
				break
			}
			if wp.activeWorkers.CompareAndSwap(active, active-1) {
				// 预扣成功。仅当"预扣后已不超员"（active-1 < target）才回滚：
				// 说明预扣期间 target 被回弹提升，本 worker 应留下；
				// 恰好达标（active-1 == target）时正常退出——否则最后一个超员
				// 名额也会被回滚，active 会恒为 target+1 永不收敛。
				if active-1 < wp.targetWorkers.Load() {
					wp.activeWorkers.Add(1)
					break
				}
				retired.Store(true) // 已预扣，defer 不再重复扣减
				return
			}
		}

		// 快照当前队列引用：RLock 与 UpdateQueueSize 的队列切换互斥，消除字段读写数据竞争。
		// 快照后队列可能已被切换/关闭，下方读到 nil 任务时跳过即可（安全）。
		wp.queueMutex.RLock()
		queue := wp.taskQueue
		wp.queueMutex.RUnlock()

		select {
		case task := <-queue:
			if task != nil {
				task.Execute()
			} else {
				// closed 且排空：短暂让步，等待 quit
				time.Sleep(time.Millisecond)
			}
		case <-wp.quit:
			return
		case <-ticker.C:
			// 定期醒来检查 target（队列长期空闲时也能响应减少 worker）
		}
	}
}
