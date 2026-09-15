// Package taskcenter 任务执行中心：长任务（数据迁移、GeoIP 同步、结构对齐执行、SQL 后台执行）的
//
// 长任务（数据迁移、GeoIP 同步、结构对齐执行、SQL 后台执行）的统一注册与观测入口：
// 任务创建只有一个口（Submit）、查询只有一个口（Get/List）；业务逻辑留在各自模块，
// 中心不感知迁移/同步细节。纯内存实现：重启即失效（任务须可幂等重跑），不持久化、无配置项。
//
// 健壮性收口（中心生命线）：
//   - 任务 goroutine 内 defer 统一收口：recover panic、按时间先后裁定终态、释放全局互斥——
//     正常/出错/panic/取消四路径必然放锁，单任务异常不锁死中心；
//   - 终态任务仅保留最近 TaskKeepLimit 条（新终态落定即淘汰最旧，防常驻进程内存与列表无限增长），running 永不淘汰。
package taskcenter

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
)

// 任务状态。终态一经落定不再变更：取消与自然结束的竞态由中心按时间先后裁定（取消请求先于自然结束生效 → cancelled，否则保留自然终态）。
type Status string

const (
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Terminal 报告任务是否已终态。
func (s Status) Terminal() bool {
	return s == StatusDone || s == StatusFailed || s == StatusCancelled
}

// TaskKeepLimit 终态任务最大保留条数（D24，常量不开放配置）。
const TaskKeepLimit = 100

// Progress 进度快照：业务每次传入不可变新快照，中心原子指针存取；
// 禁止中心与业务共享可变结构——「原子读」以整体快照替换达成。
type Progress struct {
	// Text 人类可读进度摘要（如「3/10 表，access_log 500/12000 行」）。
	Text string `json:"text"`
	// Detail 业务自填的结构化明细（迁移传表级数组、SQL 执行传逐条结果），中心只透传不解释。
	Detail any `json:"detail,omitempty"`
}

// Task 任务快照（最小元数据集）。全部字段为只读快照，中心外不得修改。
type Task struct {
	ID         string     `json:"id"`
	CreatedBy  string     `json:"created_by"`
	Title      string     `json:"title"`
	Status     Status     `json:"status"`
	Progress   *Progress  `json:"progress,omitempty"`
	Result     string     `json:"result,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"` // running 时为零值（指针 nil）
}

// SetProgressFn 业务侧进度写入函数：传入整体新快照（D25）。
type SetProgressFn func(p *Progress)

// RunFn 任务执行体：ctx 取消即协作式落点（语句/批次边界停止）；返回 nil → done，error → failed。
type RunFn func(ctx context.Context, setProgress SetProgressFn) error

// Spec 提交参数。
type Spec struct {
	CreatedBy string // 提交来源标识（migrate / geoip_sync / sql_exec / schema_apply）
	Title     string // 人类可读标题
	Run       RunFn  // 执行体
}

// entry 中心内部记录（Task 快照 + 可变控制面）。
type entry struct {
	Task

	cancel context.CancelFunc // 取消函数（终态后置 nil 防重复调用）

	// 竞态裁定：终态按时间先后判定——取消请求时刻早于自然结束时刻 → cancelled。
	cancelReqAt time.Time // 取消请求时刻（零值 = 无取消请求）
	finishAt    time.Time // 自然结束时刻（Run 返回/panic 时刻）

	progress atomicPtr // *Progress 原子存取（D25 快照替换）
}

// Center 任务执行中心：全局同一时刻仅 1 个 running 任务（运维工具无并发诉求，串行最克制且语义最简单）。
type Center struct {
	mu       sync.Mutex
	epoch    int64    // 启动纪元（进程启动 Unix 秒，D26：重启后 ID 纪元变化，旧 ID 必然查不到）
	seq      int      // 自增序号
	running  *entry   // 当前运行中任务（nil = 空闲）
	terminal []*entry // 终态任务（按完成先后追加，超 TaskKeepLimit 淘汰最旧）
}

// New 创建任务中心。epoch 为进程启动 Unix 秒（装配处传 time.Now().Unix()）。
func New(epoch int64) *Center {
	return &Center{epoch: epoch}
}

// ErrBusy 已有任务在跑时的拒绝错误（文案三要素：现状 + 原因 + 下一步）。
type ErrBusy struct {
	ID    string // 进行中任务 ID
	Title string // 进行中任务标题
}

func (e *ErrBusy) Error() string {
	return fmt.Sprintf("任务进行中，请勿重复提交（当前任务 #%s「%s」）；可先查询 /admin/tasks 观察进度，待其完成后再提交", e.ID, e.Title)
}

// Submit 提交任务：全局互斥——已有 running 时返回 *ErrBusy（不排队）。
// 成功则立即返回任务 ID 并在后台 goroutine 执行。
func (c *Center) Submit(spec Spec) (string, error) {
	c.mu.Lock()
	if c.running != nil {
		e := c.running
		c.mu.Unlock()
		return "", &ErrBusy{ID: e.ID, Title: e.Title}
	}
	c.seq++
	id := fmt.Sprintf("%d-%d", c.epoch, c.seq)
	ctx, cancel := context.WithCancel(context.Background())
	e := &entry{
		Task: Task{
			ID:        id,
			CreatedBy: spec.CreatedBy,
			Title:     spec.Title,
			Status:    StatusRunning,
			CreatedAt: time.Now(),
		},
		cancel: cancel,
	}
	c.running = e
	c.mu.Unlock()

	go c.run(e, ctx, spec.Run)
	return id, nil
}

// run 任务执行与统一收口：recover、终态裁定、互斥释放同走一个 defer——正常/出错/panic/取消四路径必然放锁，业务任何写法都锁不死中心。
func (c *Center) run(e *entry, ctx context.Context, runFn RunFn) {
	var runErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("任务执行 panic：%v", r)
				// panic 摘要落 Result（D23）：首行堆栈辅助定位。
				if lines := splitLines(string(debug.Stack())); len(lines) > 0 {
					runErr = fmt.Errorf("%w（堆栈首行：%s）", runErr, lines[0])
				}
			}
		}()
		runErr = runFn(ctx, func(p *Progress) { c.setProgress(e, p) })
	}()

	// 统一收口：终态裁定 + 互斥释放（四路径必经）。
	e.finishAt = time.Now()
	c.mu.Lock()
	e.cancel = nil // 终态后取消函数作废（Cancel 对终态返回终态不报错，D27）
	e.Status = resolveFinalStatus(e, runErr)
	if runErr != nil {
		e.Result = runErr.Error()
	} else if e.Status == StatusCancelled {
		e.Result = "任务已取消"
	} else {
		e.Result = "任务完成"
	}
	e.FinishedAt = &e.finishAt
	c.terminal = append(c.terminal, e)
	if len(c.terminal) > TaskKeepLimit {
		c.terminal = c.terminal[len(c.terminal)-TaskKeepLimit:] // 终态限量：淘汰最旧，running 永不淘汰
	}
	c.running = nil
	c.mu.Unlock()
}

// resolveFinalStatus 终态裁定：取消请求先于自然结束生效 → cancelled；
// 否则保留自然终态（nil→done、err→failed、panic→failed）。
func resolveFinalStatus(e *entry, runErr error) Status {
	if !e.cancelReqAt.IsZero() && !e.cancelReqAt.After(e.finishAt) {
		return StatusCancelled
	}
	if runErr != nil {
		return StatusFailed
	}
	return StatusDone
}

// Cancel 取消任务：向业务 ctx 发取消信号，落点由业务自定（迁移=当前批事务完成后停）。
// 竞态语义：已终态 → 返回该终态快照与提示、不报错；不存在（含已淘汰终态）→ ok=false。
func (c *Center) Cancel(id string) (Task, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.findByID(id)
	if e == nil {
		return Task{}, "", false
	}
	if e.Status.Terminal() {
		return e.Task, "任务已完成（终态 " + string(e.Status) + "），无需取消", true
	}
	if e.cancel != nil {
		e.cancel()
	}
	e.cancelReqAt = time.Now()
	return e.Task, "取消请求已送达（任务将在当前批次边界停止）", true
}

// Get 单任务快照；不存在（含已被限量淘汰的终态）返回 ok=false。
func (c *Center) Get(id string) (Task, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.findByID(id)
	if e == nil {
		return Task{}, false
	}
	return e.snapshot(), true
}

// List 全部任务快照（running + 保留期内终态），按创建时间倒序（新在前）。
func (c *Center) List() []Task {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Task, 0, len(c.terminal)+1)
	if c.running != nil {
		out = append(out, c.running.snapshot())
	}
	// terminal 追加序 = 完成先后；倒序 = 新终态在前。
	for i := len(c.terminal) - 1; i >= 0; i-- {
		out = append(out, c.terminal[i].snapshot())
	}
	return out
}

// findByID 在 running 与终态记录中查找（调用方须持锁）。
func (c *Center) findByID(id string) *entry {
	if c.running != nil && c.running.ID == id {
		return c.running
	}
	for _, e := range c.terminal {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// setProgress 整体快照替换：中心原子指针存取；终态后的迟到写入忽略（终态不可被迟到进度污染）。
func (c *Center) setProgress(e *entry, p *Progress) {
	if p == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.Status.Terminal() {
		return
	}
	e.progress.store(p)
}

// snapshot 生成只读快照（调用方须持锁；Progress 取原子指针当前值）。
func (e *entry) snapshot() Task {
	t := e.Task
	if p := e.progress.load(); p != nil {
		cp := *p
		t.Progress = &cp
	}
	return t
}

// splitLines 按行拆分文本（取非空行）。
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			line := s[start:i]
			start = i + 1
			if line != "" {
				out = append(out, line)
			}
		}
	}
	return out
}
