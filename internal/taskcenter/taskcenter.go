// Package taskcenter 任务执行中心：长任务（数据迁移、GeoIP 同步、结构对齐执行、SQL 后台执行）的
// 统一注册与观测入口：任务创建只有一个口（Submit）、查询只有一个口（Get/List）；业务逻辑留在
// 各自模块，中心不感知迁移/同步细节。纯内存实现：重启即失效（任务须可幂等重跑），不持久化、
// 无配置项、默认无超时机制（任务时长不限——后台化的意义就是摆脱请求超时；停止只有人工 Cancel
// 一条路，ctx 为纯 WithCancel、无 deadline）。
//
// 并发模型（规则式互斥，非全局串行）：互斥规则是任务中心域的内存态配置，任务实例不携带
// 互斥字段——三个域变量共同决定「能否与运行中任务并存」，满足其一即互斥、提交被拒：
//
//	MutexTaskField  互斥键字段名（默认 "CreatedBy"，当前唯一取值）：提交比对时取任务该字段的值作互斥标签；
//	MutexList       公共互斥集：集内标签至多 1 个出现在运行中任务（如都碰运行库的四类任务）；
//	MutexMap        指定互斥集：key 标签与列表内标签互斥（双向生效），表达点对点冲突。
//
// 未命中任何规则即并行不设限——未来新增长任务默认（各自 CreatedBy 不同、未入规则）天然并行，
// 确有冲突面的任务由装配期把标签登记进规则集实现互斥。
//
// 任务来源白名单（AllowCreatorList）：Submit 的 CreatedBy 必须已注册（内存态，装配期
// RegisterCreators），未注册来源一律拒绝，防未知调用方混入任务列表；白名单经任务列表
// 端点透出，前端「后台任务」页以下拉框呈现。
//
// 健壮性收口（中心生命线）：
//   - 任务 goroutine 内 defer 统一收口：recover panic、按时间先后裁定终态、释放分组互斥——
//     正常/出错/panic/取消四路径必然放锁，单任务异常不锁死中心；
//   - 终态任务仅保留最近 TaskKeepLimit 条（新终态落定即淘汰最旧，防常驻进程内存与列表无限
//     增长——流水账不无限记），running 永不淘汰。
package taskcenter

import (
	"context"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
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

// TaskKeepLimit 终态任务最大保留条数（只留最近流水，常量不开放配置；500 条仅元数据快照，
// 内存可忽略——按定时 GeoIP 同步每小时 1 条计约可回溯 3 周）。
const TaskKeepLimit = 500

// resultMaxRunes Result 存入上限（rune 数，防业务侧拼接的超长错误信息撑爆内存与列表响应体；
// 超限截断并附提示，完整信息应由业务自身日志留痕）。
const resultMaxRunes = 2000

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
	CreatedBy  string     `json:"created_by"` // 提交来源（默认互斥键：MutexTaskField="CreatedBy" 时提交比对即取本字段）
	Title      string     `json:"title"`
	Status     Status     `json:"status"`
	Progress   *Progress  `json:"progress,omitempty"`
	Result     string     `json:"result,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`            // 中心自动记录（耗时口径起点）
	FinishedAt *time.Time `json:"finished_at,omitempty"` // running 时为 nil
}

// SetProgressFn 业务侧进度写入函数：传入整体新快照。
type SetProgressFn func(p *Progress)

// RunFn 任务执行体：ctx 取消即协作式落点（语句/批次边界停止）；返回 nil → done，error → failed。
type RunFn func(ctx context.Context, setProgress SetProgressFn) error

// Spec 提交参数。
type Spec struct {
	CreatedBy string // 提交来源标识（须已 RegisterCreators 注册，如 migrate / geoip_sync / sql_exec）
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

	progress atomicPtr // *Progress 原子存取（快照替换）
}

// Center 任务执行中心：规则式互斥（MutexTaskField/MutexList/MutexMap）、来源白名单、终态限量保留。
type Center struct {
	mu            sync.Mutex
	epoch         int64           // 启动纪元（进程启动 Unix 秒：重启后 ID 纪元变化，旧 ID 必然查不到）
	seq           int             // 自增序号
	running       []*entry        // 运行中任务（互斥规则逐个比对；并行数量 = 未命中规则的提交数）
	terminal      []*entry        // 终态任务（按完成先后追加，超 TaskKeepLimit 淘汰最旧）
	allowCreators map[string]bool // 来源白名单（内存态；未注册来源 Submit 一律拒绝）

	// 互斥规则三件套（任务中心域的内存态配置，RegisterMutexRules 整体替换；经任务列表端点透出展示）：
	mutexTaskField string              // 互斥键字段名（默认 "CreatedBy"，当前唯一取值）
	mutexList      []string            // 公共互斥集：集内标签至多 1 个出现在运行中任务
	mutexMap       map[string][]string // 指定互斥集：key 标签与列表内标签互斥（双向生效）
}

// New 创建任务中心。epoch 为进程启动 Unix 秒（装配处传 time.Now().Unix()）；
// allowCreators 为初始来源白名单（可空，后续 RegisterCreators 追加）。
func New(epoch int64, allowCreators ...string) *Center {
	c := &Center{
		epoch:          epoch,
		allowCreators:  map[string]bool{},
		mutexTaskField: "CreatedBy", // 默认互斥键 = 提交来源
		mutexMap:       map[string][]string{},
	}
	c.RegisterCreators(allowCreators...)
	return c
}

// RegisterMutexRules 注册互斥规则（内存态，装配期调用；三参数整体替换，nil/空 = 清空对应项）。
// field 当前仅支持 "CreatedBy"（互斥标签 = 提交来源）；传其他值按误配置拒绝。
func (c *Center) RegisterMutexRules(field string, mutexList []string, mutexMap map[string][]string) error {
	if field != "CreatedBy" {
		return fmt.Errorf("互斥键字段 %q 不受支持（当前仅 CreatedBy）", field)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mutexTaskField = field
	c.mutexList = append([]string(nil), mutexList...)
	m := map[string][]string{}
	for k, v := range mutexMap {
		m[k] = append([]string(nil), v...)
	}
	c.mutexMap = m
	return nil
}

// MutexTaskField / MutexList / MutexMap 互斥规则只读快照（供端点透出/UI 展示）。
func (c *Center) MutexTaskField() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mutexTaskField
}

func (c *Center) MutexList() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.mutexList...)
}

func (c *Center) MutexMap() map[string][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string][]string, len(c.mutexMap))
	for k, v := range c.mutexMap {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// conflictWith 互斥裁决（提交时调用，须持锁）：新任务标签 label 与运行中任务逐一比对，
// 满足任一规则即互斥——① 同标签；② 双方标签都在公共互斥集 MutexList；③ 指定互斥集
// MutexMap 命中（双向）。返回冲突的运行中任务；nil = 可并行。
func (c *Center) conflictWith(label string) *entry {
	inList := func(v string) bool { return containsStr(c.mutexList, v) }
	for _, e := range c.running {
		rl := e.CreatedBy // MutexTaskField 当前唯一取值 CreatedBy
		switch {
		case rl == label:
			return e
		case inList(rl) && inList(label):
			return e
		case containsStr(c.mutexMap[label], rl) || containsStr(c.mutexMap[rl], label):
			return e
		}
	}
	return nil
}

// containsStr 列表包含判断（空串永不相等，防御空标签）。
func containsStr(list []string, v string) bool {
	if v == "" {
		return false
	}
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// RegisterCreators 注册任务来源白名单（内存态，装配期调用；重复注册幂等）。
func (c *Center) RegisterCreators(names ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range names {
		if strings.TrimSpace(n) != "" {
			c.allowCreators[strings.TrimSpace(n)] = true
		}
	}
}

// AllowCreators 返回当前白名单（字母序快照，供端点透出/前端下拉）。
func (c *Center) AllowCreators() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.allowCreators))
	for n := range c.allowCreators {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ErrBusy 互斥规则命中（已有互斥的运行中任务）时的拒绝错误（文案三要素：现状 + 原因 + 下一步；
// 两个任务的标题与标签分列陈述，防「自己和自己互斥」的误读）。
type ErrBusy struct {
	SpecTitle string // 被拒任务的标题
	Label     string // 被拒任务的互斥标签（MutexTaskField 字段值，默认 = CreatedBy）
	ID        string // 冲突的运行中任务 ID
	Title     string // 冲突的运行中任务标题
}

func (e *ErrBusy) Error() string {
	return fmt.Sprintf("互斥规则命中：提交的任务「%s」（标签 %s）与进行中的 #%s「%s」不允许并行；"+
		"可先在「后台任务」页观察进度，待其完成/取消后再提交", e.SpecTitle, e.Label, e.ID, e.Title)
}

// ErrUnknownCreator 来源未注册的拒绝错误。
type ErrUnknownCreator struct {
	CreatedBy string
}

func (e *ErrUnknownCreator) Error() string {
	return "任务来源「" + e.CreatedBy + "」未注册白名单，拒绝提交；请确认装配期已 RegisterCreators 注册该来源"
}

// Submit 提交任务：同互斥分组已有 running 时返回 *ErrBusy（不排队）；来源未注册返回
// *ErrUnknownCreator。成功则立即返回任务 ID 并在后台 goroutine 执行（默认无超时，
// 停止仅经 Cancel）。
func (c *Center) Submit(spec Spec) (string, error) {
	c.mu.Lock()
	if !c.allowCreators[spec.CreatedBy] {
		c.mu.Unlock()
		return "", &ErrUnknownCreator{CreatedBy: spec.CreatedBy}
	}
	if cur := c.conflictWith(spec.CreatedBy); cur != nil {
		c.mu.Unlock()
		return "", &ErrBusy{SpecTitle: spec.Title, Label: spec.CreatedBy, ID: cur.ID, Title: cur.Title}
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
	c.running = append(c.running, e)
	c.mu.Unlock()

	go c.run(e, ctx, spec.Run)
	return id, nil
}

// run 任务执行与统一收口：recover、终态裁定、互斥释放同走一个 defer——正常/出错/panic/取消
// 四路径必然放锁，业务任何写法都锁不死中心。
func (c *Center) run(e *entry, ctx context.Context, runFn RunFn) {
	var runErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("任务执行 panic：%v", r)
				// panic 摘要落 Result：首行堆栈辅助定位。
				if lines := splitLines(string(debug.Stack())); len(lines) > 0 {
					runErr = fmt.Errorf("%w（堆栈首行：%s）", runErr, lines[0])
				}
			}
		}()
		runErr = runFn(ctx, func(p *Progress) { c.setProgress(e, p) })
	}()

	// 统一收口：终态裁定 + 分组互斥释放（四路径必经）。
	e.finishAt = time.Now()
	c.mu.Lock()
	e.cancel = nil // 终态后取消函数作废（Cancel 对终态返回终态不报错）
	e.Status = resolveFinalStatus(e, runErr)
	if runErr != nil {
		e.Result = truncateResult(runErr.Error())
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
	for i, cur := range c.running {
		if cur == e {
			c.running = append(c.running[:i], c.running[i+1:]...) // 释放互斥占用（收口四路径必经）
			break
		}
	}
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

// List 全部任务快照（running + 保留期内终态，终态含完整进度明细），按创建时间倒序（新在前）。
func (c *Center) List() []Task {
	tasks, _ := c.list(true, 0)
	return tasks
}

// ListLite 轻量列表（供列表端点高频轮询）：running 携带完整进度（页面恢复需要 detail）
// 且不受 limit 限制、始终全带；终态仅保留 progress.text、剥除 detail（终态明细是不变死数据，
// 需要时经 Get 单查），limit>0 时终态只取最近 limit 条。返回值 hasMore = 终态数超过 limit
// 被截断（供前端提示还有更早记录）。排序与 List 一致（新在前）。
func (c *Center) ListLite(limit int) (tasks []Task, hasMore bool) {
	return c.list(false, limit)
}

// list 任务快照列表：full 选择终态是否携带进度明细；limit 仅对轻量模式的终态生效。
func (c *Center) list(full bool, limit int) ([]Task, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Task, 0, len(c.running))
	for _, e := range c.running {
		out = append(out, e.snapshot())
	}
	terminal := make([]Task, 0, len(c.terminal))
	for _, e := range c.terminal {
		if full {
			terminal = append(terminal, e.snapshot())
		} else {
			terminal = append(terminal, e.snapshotLite())
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].CreatedAt.After(terminal[j].CreatedAt) })
	hasMore := false
	if !full && limit > 0 && len(terminal) > limit {
		terminal = terminal[:limit]
		hasMore = true
	}
	out = append(out, terminal...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, hasMore
}

// findByID 在 running 与终态记录中查找（调用方须持锁）。
func (c *Center) findByID(id string) *entry {
	for _, e := range c.running {
		if e.ID == id {
			return e
		}
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

// snapshotLite 轻量快照：剥除进度明细仅留摘要文本（终态任务的 detail 是不变死数据，
// 列表场景无需重复下发；调用方须持锁）。
func (e *entry) snapshotLite() Task {
	t := e.snapshot()
	if t.Progress != nil {
		t.Progress = &Progress{Text: t.Progress.Text}
	}
	return t
}

// truncateResult 超长错误信息按 rune 截断并附提示（防单条 Result 无界膨胀）。
func truncateResult(s string) string {
	runes := []rune(s)
	if len(runes) <= resultMaxRunes {
		return s
	}
	return string(runes[:resultMaxRunes]) + "…（错误信息超长已截断，完整信息见业务日志）"
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
