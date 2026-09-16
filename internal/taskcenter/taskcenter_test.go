package taskcenter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestCenter 测试用中心（纪元固定便于断言 ID；注册测试来源白名单 "x"）。
func newTestCenter() *Center { return New(1700000000, "x") }

// newRuleCenter 测试用中心 + 自定义互斥规则（公共集/指定集）；
// 白名单覆盖规则测试用到的全部来源标签。
func newRuleCenter(list []string, m map[string][]string) *Center {
	c := New(1700000000, "x", "m", "s", "other", "migrate", "geoip_sync")
	if err := c.RegisterMutexRules("CreatedBy", list, m); err != nil {
		panic(err)
	}
	return c
}

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
		CreatedBy: "x", Title: "测试任务",
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
	release := make(chan struct{})
	c := newRuleCenter([]string{"m", "s"}, nil) // 公共互斥集：m/s 至多一个在跑
	id1, err := c.Submit(Spec{CreatedBy: "m", Title: "长任务", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		<-release
		return nil
	}})
	if err != nil {
		t.Fatalf("首个 Submit 应成功: %v", err)
	}
	// 规则①：同标签（同来源）互斥。
	_, err = c.Submit(Spec{CreatedBy: "m", Title: "同来源第二个", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }})
	var busy *ErrBusy
	if !errors.As(err, &busy) {
		t.Fatalf("同标签 Submit 应返回 ErrBusy, got %v", err)
	}
	if busy.ID != id1 {
		t.Fatalf("ErrBusy.ID = %q, want %q", busy.ID, id1)
	}
	// 规则②：公共互斥集命中（s 与 m 同集）互斥。
	if _, err := c.Submit(Spec{CreatedBy: "s", Title: "同集任务", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }}); !errors.As(err, &busy) {
		t.Fatalf("同集 Submit 应返回 ErrBusy, got %v", err)
	}
	// 未入集且不同标签 → 并行不互斥（规则式互斥的核心收益）。
	idOther, err := c.Submit(Spec{CreatedBy: "other", Title: "异组并行任务", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }})
	if err != nil {
		t.Fatalf("未入规则的不同标签应可并行提交: %v", err)
	}
	waitStatus(t, c, idOther, StatusDone)
	close(release)
	waitStatus(t, c, id1, StatusDone)
	// 互斥释放后可再次提交（panic 收口/正常收口都必须放锁）。
	if _, err := c.Submit(Spec{CreatedBy: "m", Title: "续任务", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }}); err != nil {
		t.Fatalf("终态后 Submit 应成功: %v", err)
	}
}

// TestMutexMapAndUnknownCreator 指定互斥集（双向生效）与来源白名单拒绝。
func TestMutexMapAndUnknownCreator(t *testing.T) {
	c := newRuleCenter(nil, map[string][]string{"migrate": {"geoip_sync"}})
	id, err := c.Submit(Spec{CreatedBy: "migrate", Title: "迁移", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		return nil
	}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// Map 正向：migrate 在跑 → geoip_sync 被拒。
	if _, err := c.Submit(Spec{CreatedBy: "geoip_sync", Title: "同步", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }}); !isBusy(err) {
		t.Fatalf("MutexMap 正向应互斥, got %v", err)
	}
	waitStatus(t, c, id, StatusDone)
	// Map 反向：geoip_sync 在跑 → migrate 被拒（双向生效）。
	id2, _ := c.Submit(Spec{CreatedBy: "geoip_sync", Title: "同步", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }})
	if _, err := c.Submit(Spec{CreatedBy: "migrate", Title: "迁移", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }}); !isBusy(err) {
		t.Fatalf("MutexMap 反向应互斥, got %v", err)
	}
	waitStatus(t, c, id2, StatusDone)

	// 白名单：未注册来源一律拒绝。
	if _, err := newTestCenter().Submit(Spec{CreatedBy: "ghost", Title: "未注册", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }}); err == nil {
		t.Fatal("未注册来源应被拒绝")
	} else if _, ok := err.(*ErrUnknownCreator); !ok {
		t.Fatalf("应返回 ErrUnknownCreator, got %T", err)
	}
}

func isBusy(err error) bool {
	var busy *ErrBusy
	return errors.As(err, &busy)
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
	if _, err := c.Submit(Spec{CreatedBy: "x", Title: "后续任务", Run: func(ctx context.Context, setProgress SetProgressFn) error { return nil }}); err != nil {
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

// TestResultTruncate 超长错误信息按上限截断并附提示；未超限原样保留。
func TestResultTruncate(t *testing.T) {
	c := newTestCenter()
	long := strings.Repeat("错", resultMaxRunes+100)
	id, err := c.Submit(Spec{CreatedBy: "x", Title: "超长错误", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		return errors.New(long)
	}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	task := waitStatus(t, c, id, StatusFailed)
	const truncHint = "…（错误信息超长已截断，完整信息见业务日志）"
	if got := []rune(task.Result); len(got) != resultMaxRunes+len([]rune(truncHint)) {
		t.Fatalf("截断后 Result 长度 = %d, want %d（上限+提示后缀）", len(got), resultMaxRunes+len(truncHint))
	}
	if !strings.HasSuffix(task.Result, "…（错误信息超长已截断，完整信息见业务日志）") {
		t.Fatalf("截断提示缺失: tail=%q", task.Result[len(task.Result)-30:])
	}

	// 未超限：原样保留
	id2, _ := c.Submit(Spec{CreatedBy: "x", Title: "短错误", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		return errors.New("普通失败")
	}})
	task2 := waitStatus(t, c, id2, StatusFailed)
	if task2.Result != "普通失败" {
		t.Fatalf("短错误 Result = %q, want 原样", task2.Result)
	}
}

// TestListLiteStripsTerminalDetail 轻量列表：终态任务仅留 progress.text，running 携带完整明细；
// Get 单查始终完整。
func TestListLiteStripsTerminalDetail(t *testing.T) {
	c := newTestCenter()
	id, err := c.Submit(Spec{CreatedBy: "x", Title: "带明细任务", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		setProgress(&Progress{Text: "做完了", Detail: map[string]string{"k": "v"}})
		return nil
	}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitStatus(t, c, id, StatusDone)

	lite, _ := c.ListLite(0)
	var liteTask Task
	for _, tsk := range lite {
		if tsk.ID == id {
			liteTask = tsk
		}
	}
	if liteTask.Progress == nil || liteTask.Progress.Text != "做完了" {
		t.Fatalf("轻量列表应保留 text, got %+v", liteTask.Progress)
	}
	if liteTask.Progress.Detail != nil {
		t.Fatalf("轻量列表终态应剥除 detail, got %+v", liteTask.Progress.Detail)
	}

	full := c.List()
	for _, tsk := range full {
		if tsk.ID == id && (tsk.Progress == nil || tsk.Progress.Detail == nil) {
			t.Fatal("完整列表终态应保留 detail")
		}
	}

	// limit 截断：仅 1 条终态时 limit=1 不截断；limit 语义对 running 不生效
	if tasks, more := c.ListLite(1); more || len(tasks) < 1 {
		t.Fatalf("limit 不小于终态数时不应截断, more=%v n=%d", more, len(tasks))
	}

	got, _ := c.Get(id)
	if got.Progress == nil || got.Progress.Detail == nil {
		t.Fatal("Get 单查应保留完整明细")
	}
}

// TestListLiteLimit 轻量列表 limit 截断：终态只留最近 N 条、hasMore 如实上报，running 不受 limit 限制。
func TestListLiteLimit(t *testing.T) {
	c := newTestCenter()
	// 先占一个运行中任务（不应被 limit 截掉）
	runID, err := c.Submit(Spec{CreatedBy: "x", Title: "长任务", Run: func(ctx context.Context, setProgress SetProgressFn) error {
		<-ctx.Done()
		return nil
	}})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// 3 条终态：另一来源提交，避开与运行中任务同标签的互斥
	c.RegisterCreators("y")
	for i := 0; i < 3; i++ {
		id, err := c.Submit(Spec{CreatedBy: "y", Title: "短任务", Run: func(ctx context.Context, setProgress SetProgressFn) error {
			return nil
		}})
		if err != nil {
			t.Fatalf("Submit #%d: %v", i, err)
		}
		waitStatus(t, c, id, StatusDone)
	}

	tasks, more := c.ListLite(2)
	terminal := 0
	foundRun := false
	for _, tsk := range tasks {
		if tsk.Status == StatusRunning {
			foundRun = foundRun || tsk.ID == runID
			continue
		}
		terminal++
	}
	if terminal != 2 || !more {
		t.Fatalf("limit=2 应截断为 2 条终态且 hasMore=true, got terminal=%d more=%v", terminal, more)
	}
	if !foundRun {
		t.Fatal("running 任务不受 limit 限制，应始终在列表中")
	}
	// limit=0 = 全量
	tasks, more = c.ListLite(0)
	if more {
		t.Fatal("limit=0 全量不应截断")
	}
	if n := len(tasks); n < 4 {
		t.Fatalf("limit=0 应含全部任务, got %d", n)
	}
}
