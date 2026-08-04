package workpool

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSubmitExecutes 提交的任务全部执行。
func TestSubmitExecutes(t *testing.T) {
	wp := NewWorkerPool(Config{MinWorkers: 4, QueueSize: 16})
	wp.Start()
	defer wp.Stop()

	var n atomic.Int64
	for i := 0; i < 100; i++ {
		wp.Submit(TaskFunc(func() { n.Add(1) }))
	}

	// Stop 会串行执行队列剩余任务，这里主动等待完成
	deadline := time.Now().Add(3 * time.Second)
	for n.Load() != 100 {
		if time.Now().After(deadline) {
			t.Fatalf("任务未全部执行: got %d, want 100", n.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestTrySubmitQueueFull TrySubmit 队列满时返回 false，不满时返回 true。
func TestTrySubmitQueueFull(t *testing.T) {
	wp := NewWorkerPool(Config{MinWorkers: 1, QueueSize: 1})
	wp.Start()
	defer wp.Stop()

	// 阻塞唯一 worker，使队列保持满
	block := make(chan struct{})
	wp.Submit(TaskFunc(func() { <-block }))
	// 队列 cap=1 已满
	if ok := wp.TrySubmit(TaskFunc(func() {})); ok {
		t.Error("队列满时 TrySubmit 应返回 false")
	}
	close(block)
}

// TestSubmitWithTimeout 队列满且超时则返回 false。
func TestSubmitWithTimeout(t *testing.T) {
	wp := NewWorkerPool(Config{MinWorkers: 1, QueueSize: 1})
	wp.Start()
	defer wp.Stop()

	block := make(chan struct{})
	// 任务1 被唯一 worker 取走执行（阻塞）；任务2 占满队列 cap=1
	wp.Submit(TaskFunc(func() { <-block }))
	wp.Submit(TaskFunc(func() { <-block }))

	if ok := wp.SubmitWithTimeout(TaskFunc(func() {}), 50*time.Millisecond); ok {
		t.Error("队列满且超时应返回 false")
	}
	close(block)
}

// TestStopIsIdempotent Stop 幂等，不 panic。
func TestStopIsIdempotent(t *testing.T) {
	wp := NewWorkerPool(Config{MinWorkers: 2, QueueSize: 4})
	wp.Start()
	wp.Stop()
	wp.Stop() // 二次调用不 panic
}

// TestConcurrentSubmitAndRebuild Submit 与 UpdateWorkers/UpdateQueueSize 并发不 panic、不丢任务。
func TestConcurrentSubmitAndRebuild(t *testing.T) {
	wp := NewWorkerPool(Config{MinWorkers: 4, QueueSize: 32})
	wp.Start()
	defer wp.Stop()

	var done atomic.Int64
	const total = 2000
	var wg sync.WaitGroup

	// 提交者
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			wp.Submit(TaskFunc(func() { done.Add(1) }))
		}
	}()

	// 重建者（并发触发队列切换）
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = wp.UpdateWorkers(2 + i%4)
			_ = wp.UpdateQueueSize(16 + i%16)
		}
	}()

	wg.Wait()

	// 等所有任务执行（含队列剩余由 Stop 串行执行）
	deadline := time.Now().Add(5 * time.Second)
	for done.Load() != total {
		if time.Now().After(deadline) {
			// Stop 会兜底执行剩余任务，此处允许 Stop 后完成
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := done.Load(); got != total {
		t.Errorf("任务执行数 %d，want %d（差额将在 Stop 时兜底）", got, total)
	}
}

// TestUpdateWorkersStopped 停止后 UpdateWorkers 报错。
func TestUpdateWorkersStopped(t *testing.T) {
	wp := NewWorkerPool(Config{MinWorkers: 2, QueueSize: 4})
	wp.Start()
	wp.Stop()
	if err := wp.UpdateWorkers(4); err == nil {
		t.Error("停止后 UpdateWorkers 应报错")
	}
}
