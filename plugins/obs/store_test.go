package obs

// P2 单测：obs 底层写失败重试 + 连续失败计数 + 指标暴露。
// 覆盖 writePending/flushAll 的重试与告警路径、队列满丢弃不计入连续失败。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// failStore 可编程失败/成功切换的假 Store：fail 为真时 Write 恒失败。
// 内部用 mutex 保护 fail/writeCalls/records（worker 与测试 goroutine 并发访问，避免 -race 竞争）。
// ★ 必须传指针 NewAsyncStore(&failStore{...})：接口值拷贝会带锁副本，go vet copylocks 报错。
type failStore struct {
	mu         sync.Mutex
	fail       bool
	writeCalls int
	records    []*AccessRecord
}

func (s *failStore) Name() string { return "fail" }
func (s *failStore) Write(batch []*AccessRecord) error {
	s.mu.Lock()
	s.writeCalls++
	f := s.fail
	if !f {
		s.records = append(s.records, batch...)
	}
	s.mu.Unlock()
	if f {
		return errors.New("simulated write failure")
	}
	return nil
}
func (s *failStore) Query(q Query) ([]map[string]any, error) { return nil, nil }
func (s *failStore) Count(q Query) (int64, error)                { return 0, nil }
func (s *failStore) SizeBytes() (int64, error)               { return 0, nil }
func (s *failStore) Flush(ctx context.Context) error         { return nil }
func (s *failStore) Close() error                            { return nil }
func (s *failStore) setFail(f bool)                          { s.mu.Lock(); s.fail = f; s.mu.Unlock() }
func (s *failStore) calls() int                              { s.mu.Lock(); defer s.mu.Unlock(); return s.writeCalls }
func (s *failStore) saved() []*AccessRecord                  { s.mu.Lock(); defer s.mu.Unlock(); return append([]*AccessRecord(nil), s.records...) }

// waitFor 轮询直到 cond 满足或超时（worker 异步处理，断言前必须先等完成信号）。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", msg)
}

// TestWritePendingRetryThenDrop 恒失败：Write 入队后 worker 重试 obsRetryTimes 次仍失败 → 丢弃整批。
func TestWritePendingRetryThenDrop(t *testing.T) {
	st := &failStore{fail: true}
	a := NewAsyncStore(st)
	a.Write(&AccessRecord{TraceID: "r1"})

	waitFor(t, 3*time.Second, func() bool { return a.DropCount() == 1 }, "worker 应完成全部尝试并丢弃")
	if got := st.calls(); got != obsRetryTimes+1 {
		t.Errorf("Write 调用次数 = %d，want %d（总尝试 = retryTimes+1）", got, obsRetryTimes+1)
	}
	if got := a.ConsecutiveFails(); got != 1 {
		t.Errorf("ConsecutiveFails = %d，want 1", got)
	}
	if got := a.DropCount(); got != 1 {
		t.Errorf("DropCount = %d，want 1（整批 1 条）", got)
	}
}

// TestWritePendingRetryThenSuccess 第 2 次尝试成功：DropCount 保持 0、记录落盘。
func TestWritePendingRetryThenSuccess(t *testing.T) {
	st := &failStore{fail: true}
	a := NewAsyncStore(st)
	a.Write(&AccessRecord{TraceID: "r2"})
	st.setFail(false) // 第 1 次尝试失败后、重试前切回成功

	waitFor(t, 3*time.Second, func() bool { return len(st.saved()) == 1 }, "worker 重试后应成功写入")
	if got := a.DropCount(); got != 0 {
		t.Errorf("DropCount = %d，want 0（重试成功不应丢弃）", got)
	}
	if got := a.ConsecutiveFails(); got != 0 {
		t.Errorf("ConsecutiveFails = %d，want 0（成功应清零）", got)
	}
	if got := st.saved(); len(got) != 1 || got[0].TraceID != "r2" {
		t.Errorf("saved = %v，want 含 r2 的一条记录", got)
	}
}

// TestConsecutiveFailsThreshold 逐条失败驱动：连续失败数随批次累加，直到达阈值。
func TestConsecutiveFailsThreshold(t *testing.T) {
	st := &failStore{fail: true}
	a := NewAsyncStore(st)

	for i := 1; i <= obsFailThreshold; i++ {
		a.Write(&AccessRecord{TraceID: "t"})
		waitFor(t, 3*time.Second, func() bool { return a.DropCount() == int64(i) }, "第 i 批应已处理")
	}
	if got := a.ConsecutiveFails(); got != obsFailThreshold {
		t.Errorf("ConsecutiveFails = %d，want %d（每批恰 1 条，整批失败 +1）", got, obsFailThreshold)
	}
}

// TestFlushAllRetry flush 路径：写失败重试后返回 error，drop 只计一次。
func TestFlushAllRetry(t *testing.T) {
	st := &failStore{fail: true}
	a := NewAsyncStore(st)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 直接入队 3 条后 Flush（worker 可能已抢先 writePending，故用 Flush 前同步入队断言宽松化：
	// 无论 worker 抢先还是 Flush 处理，calls 与 drop 语义一致）。
	a.Write(&AccessRecord{TraceID: "f1"})
	a.Write(&AccessRecord{TraceID: "f2"})
	a.Write(&AccessRecord{TraceID: "f3"})

	_ = a.Flush(ctx) // flush 返回错误（底层写失败），断言在调用次数与 drop 上
	if st.calls() == 0 {
		t.Error("底层 Write 应被调用（worker 或 flushAll）")
	}
	// 无论 worker 抢先分批还是 flushAll 单批，drop 必须等于已入队条数且不重复计数。
	if got := a.DropCount(); got != 3 {
		t.Errorf("DropCount = %d，want 3（已入队 3 条整批丢弃，不重复计数）", got)
	}
	if a.ConsecutiveFails() == 0 {
		t.Error("ConsecutiveFails 应 > 0（底层写失败计入）")
	}
}

// TestQueueFullDropNotCountedAsFail 队列满丢弃：计入 drop 但不计入连续失败。
func TestQueueFullDropNotCountedAsFail(t *testing.T) {
	a := NewAsyncStore(NewFileStore(t.TempDir(), 30))
	a.mu.Lock()
	a.pending = make([]*AccessRecord, asyncCap)
	a.mu.Unlock()
	a.Write(&AccessRecord{TraceID: "dropped"})
	if got := a.DropCount(); got != 1 {
		t.Errorf("队列满应丢弃并计数，drop = %d", got)
	}
	if got := a.ConsecutiveFails(); got != 0 {
		t.Errorf("队列满丢弃不计入连续失败，ConsecutiveFails = %d", got)
	}
}

// TestConsecutiveFailsClearsOnSuccess 连续失败后恢复成功 → 计数清零（成功清零语义）。
func TestConsecutiveFailsClearsOnSuccess(t *testing.T) {
	st := &failStore{fail: true}
	a := NewAsyncStore(st)
	a.Write(&AccessRecord{TraceID: "c1"})
	waitFor(t, 3*time.Second, func() bool { return a.ConsecutiveFails() == 1 }, "首次失败应计入")

	st.setFail(false)
	a.Write(&AccessRecord{TraceID: "c2"})
	waitFor(t, 3*time.Second, func() bool { return len(st.saved()) == 1 }, "恢复后应写入成功")
	if got := a.ConsecutiveFails(); got != 0 {
		t.Errorf("恢复成功后 ConsecutiveFails = %d，want 0（成功清零）", got)
	}
}
