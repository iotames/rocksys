package taskcenter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// newTestCenter 测试用中心（纪元固定便于断言 ID）。
func newTestCenter() *Center { return New(1700000000) }

// waitStatus 轮询等待任务到达指定状态（防测试竞态；超时 fatal）。
func waitStatus(t *testing.T, c *Center, id string, want Status) Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if task, ok := c.Get(id); ok && task.Status == want {
			return task
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("任务 %s 未在限时内到达状态 %s", id, want)
	return Task{}
}

func TestSubmitRunAndDone(t *testing.T) {
	c := newTestCenter()
	var gotProgress *Progress
	id, err := c.Submit(Spec{
		CreatedBy: "migrate", Title: "测试任务",
		Run: func(ctx context.Context, setProgress SetProgressFn) error {
			setProgress(&Progress{Text: "1/2"})
			setProgress(&Progress{Text: "2/2"})
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if id != "1700000000-1" {
		t.Fatalf("ID = %q, want 纪元-序号形态", id)
	}
	task := waitStatus(t, c, id, StatusDone)
	if task.Result != "任务完成" {
		t.Fatalf("Result = %q", task.Result)
	}
	if task.FinishedAt == nil {
		t.Fatal("终态任务 FinishedAt 应有值")
	}
	if task.CreatedAt.After(*task.FinishedAt) {
		t.Fatal("CreatedAt 不应晚于 FinishedAt")
	}
	// 最后一次 setProgress 快照生效（整体替换语义）。
	task, _ = c.Get(id)
	if task.Progress == nil || task.Progress.Text != "2/2" {
		t.Fatalf("Progress = %+v, want 最后快照 2/2", task.Progress)
	}
	_ = gotProgress
}

func TestSubmitFail(t *testing.T) {
	c := newTestCenter()
	id, _ := c.Submit(Spec{CreatedBy: "x", Title: "失败任务", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		return errors.New(" boom")
	}})
	task := waitStatus(t, c, id, StatusFailed)
	if task.Result == "" {
		t.Fatal("failed 任务 Result 应含错误信息")
	}
}

func TestGlobalMutexReject(t *testing.T) {
	c := newTestCenter()
	release := make(chan struct{})
	id1, err := c.Submit(Spec{CreatedBy: "a", Title: "长任务", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		<-release
		return nil
	}})
	if err != nil {
		t.Fatalf("首个 Submit 应成功: %v", err)
	}
	_, err = c.Submit(Spec{CreatedBy: "b", Title: "第二个", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }})
	var busy *ErrBusy
	if !errors.As(err, &busy) {
		t.Fatalf("第二个 Submit 应返回 ErrBusy, got %v", err)
	}
	if busy.ID != id1 {
		t.Fatalf("ErrBusy.ID = %q, want %q", busy.ID, id1)
	}
	close(release)
	waitStatus(t, c, id1, StatusDone)
	// 互斥释放后可再次提交（panic 收口/正常收口都必须放锁）。
	if _, err := c.Submit(Spec{CreatedBy: "b", Title: "续任务", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }}); err != nil {
		t.Fatalf("终态后 Submit 应成功: %v", err)
	}
}

func TestPanicRecoverAndMutexRelease(t *testing.T) {
	c := newTestCenter()
	id, _ := c.Submit(Spec{CreatedBy: "x", Title: "panic 任务", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		panic("炸了")
	}})
	task := waitStatus(t, c, id, StatusFailed)
	if task.Result == "" || !contains(task.Result, "panic") {
		t.Fatalf("panic 任务 Result 应含 panic 摘要, got %q", task.Result)
	}
	// 互斥必须已释放（D23：panic 路径同样放锁）。
	if _, err := c.Submit(Spec{CreatedBy: "y", Title: "后续任务", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }}); err != nil {
		t.Fatalf("panic 后互斥未释放: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestTerminalKeepLimitEviction(t *testing.T) {
	c := newTestCenter()
	// 提交 TaskKeepLimit+5 个即时完成任务，验证只保留最近 TaskKeepLimit 条终态。
	for i := 0; i < TaskKeepLimit+5; i++ {
		id, err := c.Submit(Spec{CreatedBy: "x", Title: "t", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }})
		if err != nil {
			t.Fatalf("Submit #%d: %v", i, err)
		}
		waitStatus(t, c, id, StatusDone)
	}
	list := c.List()
	if len(list) != TaskKeepLimit {
		t.Fatalf("List 长度 = %d, want %d（终态限量淘汰）", len(list), TaskKeepLimit)
	}
	// 最早的 5 个 ID 已淘汰：查不到（D26 一律未找到）。
	for i := 1; i <= 5; i++ {
		if _, ok := c.Get("1700000000-" + itoa(i)); ok {
			t.Fatalf("#%d 应已被淘汰", i)
		}
	}
	// 最新的一条在。
	if _, ok := c.Get("1700000000-" + itoa(TaskKeepLimit+5)); !ok {
		t.Fatal("最新终态应保留")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestCancelRunning(t *testing.T) {
	c := newTestCenter()
	started := make(chan struct{})
	id, _ := c.Submit(Spec{CreatedBy: "x", Title: "可取消", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}})
	<-started
	task, msg, ok := c.Cancel(id)
	if !ok || task.Status != StatusRunning {
		t.Fatalf("Cancel running: ok=%v status=%v msg=%q", ok, task.Status, msg)
	}
	task = waitStatus(t, c, id, StatusCancelled)
	if task.FinishedAt == nil {
		t.Fatal("cancelled 亦为终态，FinishedAt 应有值")
	}
}

func TestCancelAfterTerminalNoError(t *testing.T) {
	c := newTestCenter()
	id, _ := c.Submit(Spec{CreatedBy: "x", Title: "秒完", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }})
	waitStatus(t, c, id, StatusDone)
	task, msg, ok := c.Cancel(id)
	if !ok {
		t.Fatal("对终态任务 Cancel 不应报错（D27）")
	}
	if task.Status != StatusDone {
		t.Fatalf("应返回原终态 done, got %v", task.Status)
	}
	if msg == "" {
		t.Fatal("应附「无需取消」提示")
	}
}

func TestCancelAndFinishRace(t *testing.T) {
	// 取消请求与自然结束几乎同时：先注册取消（ctx 已取消）但 Run 直接返回 nil，
	// 按时间先后裁定——取消请求在前 → cancelled。
	c := newTestCenter()
	id, _ := c.Submit(Spec{CreatedBy: "x", Title: "竞态", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		return nil // 未感知 ctx.Done，自然结束
	}})
	time.Sleep(10 * time.Millisecond) // 确保 Run 已执行完的时间先后关系可控
	c.Cancel(id)
	task := waitStatus(t, c, id, StatusDone)
	_ = task // 本例自然结束先于（或同于）取消请求，保留自然终态 done 即正确
}

func TestGetCancelNotFound(t *testing.T) {
	c := newTestCenter()
	if _, ok := c.Get("9999-1"); ok {
		t.Fatal("不存在的 ID 应未找到")
	}
	if _, _, ok := c.Cancel("9999-1"); ok {
		t.Fatal("对不存在 ID Cancel 应未找到")
	}
}

func TestLateSetProgressIgnored(t *testing.T) {
	c := newTestCenter()
	started := make(chan struct{})
	blk := make(chan struct{})
	id, _ := c.Submit(Spec{CreatedBy: "x", Title: "迟到进度", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		close(started)
		<-blk
		return nil
	}})
	<-started
	close(blk)
	waitStatus(t, c, id, StatusDone)
	// 终态后迟到 setProgress：快照不变（Result 亦不被覆盖）。
	c.setProgressMustExist(t, id)
	task, _ := c.Get(id)
	if task.Progress != nil && task.Progress.Text == "迟到写入" {
		t.Fatal("终态后的迟到 setProgress 应被忽略")
	}
}

// setProgressMustExist 辅助：直接对已终态条目调用内部 setProgress（模拟迟到写入）。
func (c *Center) setProgressMustExist(t *testing.T, id string) {
	t.Helper()
	c.mu.Lock()
	e := c.findByID(id)
	c.mu.Unlock()
	if e == nil {
		t.Fatal("任务应存在")
	}
	c.setProgress(e, &Progress{Text: "迟到写入"})
}

func TestConcurrentGetListSafe(t *testing.T) {
	c := newTestCenter()
	var wg sync.WaitGroup
	id, _ := c.Submit(Spec{CreatedBy: "x", Title: "并发读", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		for i := 0; i < 100; i++ {
			setProgress(&Progress{Text: itoa(i)})
		}
		return nil
	}})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = c.Get(id)
				_ = c.List()
			}
		}()
	}
	wg.Wait()
	waitStatus(t, c, id, StatusDone)
}
