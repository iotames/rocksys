# 日志系统开发计划（AI Agent 深度自主开发文档）

> 配套设计文档：`docs/log-system.md`（v2.1）。本文档为**落地实现计划**，含函数原型、接口、数据结构、算法、边界处理、集成点与验收标准，供 AI Agent 自主开发，无需人工干预决策。
> 目标仓库：`easyserver/log`（子仓库）+ `rocksys/internal/conf` + `rocksys/internal/adminapi` + `rocksys/cmd/rocksys`。
> 语言：所有注释、日志、错误信息用中文；标识符用英文。

---

## 0. 开发总纲

### 0.1 阶段划分与依赖

```
P1 (easyserver/log 增强) ──→ P2 (conf 集成) ──→ P3 (admin API + SSE) ──→ P4 (AI 集成验证)
```

- P1 完全独立（子仓库，不依赖 rocksys），先行。
- P2 依赖 P1 的 `SetLevel` 钩子、`SetFileWriter`、`SetMaxSize`。
- P3 依赖 P1 的 `Tail`/`GetInfo` 与 P2 的 `conf.Set`。
- P4 为端到端验证。

### 0.2 硬性约束

- **红线原则（最高优先级）**：console + ring buffer 两个必备通道必须正常运行（ERROR 观测 + 实时监控）。**文件日志是可选增强，其任何故障（磁盘满、权限、慢写、崩溃）都不得影响 console + ring 与主业务**——文件 writer 采用**异步写入**（写请求入队后立即返回，后台 goroutine 落盘，队列满/失败时按策略丢弃），主写路径（slog handler）永不等待文件 IO。
- **不得**反向依赖 `rocksys/internal/conf`（easyserver/log 是独立子仓库）。
- **不得**使用 `as any`、`@ts-ignore` 类抑制（Go 无此问题，但禁止 `//nolint` 掩盖）。
- 所有并发路径必须 `go test -race` 通过。
- 默认路径（不开启任何增强）**级别过滤/时间格式/通道**与现状一致（15 处调用方回归）；默认模板仅输出 `time/level/msg` 三字段，**attr 不输出**（属模板化输出的既定行为变化）；行级格式贴近 slog text，含空格值不做引号（引号差异为既定变化，`TestCompatRegression` 明确豁免）。
- 文件 writer 的 truncate 竞态**一期不处理**，代码留 `// TODO` 备注。
- **审查收敛原则**：文件日志的细节（清空策略、轮转、丢失、失败恢复）属可选增强的可接受取舍，不再作为阻塞问题评审——红线只保证「文件故障不影响主通道与主业务」。

### 0.3 现状关键事实（已核查）

- `easyserver/log/log.go`：`Debug/Info/Warn/Error` → `getLogger()`（`sync.Once` 惰性初始化）→ `slog.NewTextHandler(logWriter, opts)`。
- `easyserver/log/setter.go`：`lgLevel *slog.LevelVar`、`logWriter io.Writer`、`opts *slog.HandlerOptions`、`isSetLevel bool`；`SetLevel` 直接 `lgLevel.Set`（无值比较、无钩子）。
- 外部调用方仅用 `Debug/Info/Warn/Error/SetLevel`；`SetLogWriter*`/`SetOptions`/`ResetLogger` 无外部调用。
- **15 处调用方清单**（import + 实际调用 `easyserver/log`）：`cmd/rocksys/main.go`、`plugins/mq/mq.go`、`easyserver/tcpsvr/tcpserver.go`、`easyserver/tcpsvr/handler.go`、`plugins/obs/store.go`、`plugins/obs/obs.go`、`plugins/copy/copy.go`、`plugins/auth/auth.go`、`plugins/dispatch/dispatch.go`、`plugins/dispatch/healthcheck.go`、`plugins/result/result.go`、`plugins/script/script.go`、`internal/hotswap/impl.go`、`internal/chain/adapter.go`、`internal/adminapi/adminapi.go`。（`easyserver/tcpsvr/user.go` 为注释掉的 import，不计）
- `internal/conf/impl.go`：`confManager.Set` 已含 `ec.UpdateFile` 持久化；`reloadFiles` 会重放命令行参数（`parseArgsToMap(m.args)`）。
- `internal/hotswap/script.go`：`NewScriptDir(embedFS, firstDir, moreDirs...)` 提供「外置优先、内嵌兜底」加载，`GetScriptBytes(fpath)`。
- `internal/adminapi/adminapi.go`：路由前缀 `/admin/*`；`RegisterPlugin(path, h)` 自动套鉴权并注册 GET+POST；`requireAuth()` 包装器。
- `easyserver/httpsvr` **无 WebSocket**，但 `AddHandler` 可注册流式响应（SSE 用标准库实现）。

---

## 1. P1 — `easyserver/log` 增强

### 1.1 新增文件 `easyserver/log/ring.go` — 内存环形缓冲

**类型与常量**

```go
// ringCap 环形缓冲容量，钉死 8MB，不可配置。
const ringCap = 8 << 20 // 8MB

// RingBuffer 内存环形缓冲，作为实时监控数据源（基座，恒开）。
// 写满覆盖最旧；游标为全局单调递增字节 offset。
type RingBuffer struct {
    mu    sync.Mutex
    data  []byte // 容量 ringCap
    total int64  // 已写入总字节数（单调递增，只增不减）
}
```

**构造**

```go
// NewRingBuffer 创建容量为 ringCap 的环形缓冲。
func NewRingBuffer() *RingBuffer
```

**写入（实现 `io.Writer`）**

```go
// Write 追加 p 到缓冲；超容量覆盖最旧。返回 len(p), nil（永不失败）。
func (r *RingBuffer) Write(p []byte) (int, error)
```

落地逻辑：
1. `r.mu.Lock()`。
2. 若 `len(p) >= ringCap`：只保留 p 的最后 `ringCap` 字节（单条超长日志截断，避免覆盖整个缓冲）；**截断后 `total` 只累加截断后的长度 `ringCap`**（而非原始 `len(p)`）——否则逻辑写入起点与物理环形位置错位，导致后续所有读取读到错位数据。
3. 将 p（或其截断段）写入环形位置 `int(r.total % ringCap)`，分两段（尾部 + 头部回绕）写入。
4. `r.total += int64(written)`，其中 `written` 为**实际写入缓冲的字节数**（与物理写入一致，未截断时等于 `len(p)`，截断时为 `ringCap`）。
5. 解锁，返回 `len(p), nil`。

> **超长截断行语义（L2，既定取舍）**：截断段起点是行中间但以 `\n` 结尾，`Tail` 会把该段作为「完整行」返回（内容是原日志的后半段）。此为超大行场景的既定行为，半行缓存不解决此场景；文档 §1.7 不为此断言。超大行（>8MB 单条）本身属异常输入。

**读取（Tail）**

```go
// TailResult 一次增量读取的结果。
type TailResult struct {
    Lines      []string // 完整行（按行对齐，无半行）
    NextOffset int64    // 下一次读取的游标（指向最后返回行的行尾）
    Reset      bool     // since 已被覆盖，需重新拉尾部
    EOF        bool     // 无新数据
}

// Tail 从 since 读取增量，最多 n 行（n<=0 表示不限制）。
// since==-1 表示「尾部首拉」：从窗口尾部向前取最后 n 个完整行，NextOffset 指向窗口最新游标。
// 其余 since 为增量游标。
func (r *RingBuffer) Tail(n int64, since int64) TailResult
```

落地逻辑：
1. `r.mu.Lock()`。
2. 有效窗口起点 `validStart = max(0, r.total - ringCap)`。
3. **尾部首拉（since == -1）——必须先于步骤 4 拦截并 `return`（M1）**：`since==-1` 是特殊哨兵，若不先 return，字面上会落入步骤 4 的 `since < validStart` 分支（-1 < validStart 恒成立）返回 Reset 空行，首次 HTTP 拉取全失效。实现：`if since == -1 { ...尾部首拉...; return }`。
   - 从 `r.total` 向前扫描，收集最后 n 个完整行（**扫描下界为 `validStart`，勿扫到物理 0（M9）**——否则会读到环形残留旧数据污染结果）。
   - **末尾半行处理（M2）**：无论是否有完整行，只要末尾存在半行（末字节非 `\n`），`NextOffset` 统一指向**半行起点**（与增量路径步骤 5 的 M5 定义一致）；无半行时 `NextOffset = r.total`。
   - **EOF 值（M8）**：非空窗口尾部首拉恒 `EOF=false`（NextOffset 未达 total 即还有新数据可续读）；仅 `total==0` 时 `EOF=true`（与设计 §8「ring 空返回 eof=true」一致）。
4. **过期游标判定（中4）——显式有序分支（M1，勿合并条件）**：
   - 若 `since > r.total`（旧游标超过新进程当前 total）→ 返回 `{Reset: true, NextOffset: r.total}`（视作 stale，客户端重新尾部首拉）。
   - 否则若 `since == r.total` → 返回 `{EOF: true, NextOffset: since}`（SSE 起始游标 `RingTotal` 与 HTTP 增量 `next_offset==total` 轮询都依赖此分支返回 EOF 而非 Reset）。
   - 否则若 `since < validStart` → 返回 `{Reset: true, NextOffset: r.total}`（客户端重新尾部首拉）。
   - **非行边界判定（M2）**：仅对 `validStart < since < total` 成立：
     - `since == validStart`（含 `since==0`）**视为行起点**，直接进入增量读取（不做前字节检查）——否则 `since==validStart` 时前一个字节在窗口外（物理残留旧数据），照字面检查会误 Reset；且 `since==0` 时负模索引 `data[-1]` 会数组越界 panic。
     - ⚠️ **覆盖边界（低-6）**：覆盖写入后 `validStart = total - ringCap` 可能恰落在行中间（最旧字节被截断），此时 `since==validStart` 作为行起点会返回一个脏首段。正常流程客户端不用 validStart（RingValidStart 仅诊断，增量协议用行尾游标），此边界仅影响「旧游标恰好命中新 validStart」的跨重启场景，属可接受的防御性偏差，文档 §1.7 不为此断言。
     - 安全取模公式：`prevByte := r.data[(since-1)%ringCap]`（**逻辑字节 i 的物理位置恒为 `i % ringCap`**，见 Write 步骤 3 与增量扫描步骤 7——切勿再加 total，`(since-1+total)%ringCap` 会读错字节导致每次增量误报 Reset，实时监控失效）。`prevByte != '\n'` → 返回 `{Reset: true, NextOffset: r.total}`。
     - ⚠️ 负索引防御：`since==0` 时 `(0-1)%ringCap == -1`（Go 负模为负），`data[-1]` 会 panic——但按上述检查顺序，`since==0` 时要么 `since==validStart` 被跳过、要么 `since<validStart` 先走 Reset 分支，**不会执行到取模**。实现时必须保证该守卫顺序（先 validStart，再取模）。
   - 统一语义：**跨进程重启后客户端应重新尾部首拉**（`Reset` 收敛），设计 §7.1 已约定。
5. 从 `since` 读到 `r.total`，按 `\n` 切分：
   - 只返回**完整行**（以 `\n` 结尾的行）。
   - 末尾若存在**半行**（无 `\n` 结尾），**不返回**，`NextOffset` 停在半行起点（留待下次）；此时若无完整行返回，`EOF=false`（存在半行即还有未就绪数据，客户端应继续轮询）。
6. 最多返回 n 行；若因 n 截断，`NextOffset` 指向被截断处。
7. 返回 `{Lines, NextOffset, EOF: false}`。

**按行对齐细节（M5 定义）**：`NextOffset` 指向**完整行边界**——要么行尾（某行 `\n` 之后），要么**半行起点**（末尾半行未就绪时不返回、游标停在其起点）。两种位置都能保证客户端下次 `since` 从**非半行中间**开始，续读不会产生「半行+新行」拼接的脏行。
> **半行缓存（M10，明确本方案不实现）**：文档**不采用**半行缓存方案（Tail 半行语义以步骤 5 为准：不返回半行、NextOffset 停半行起点）。模板强制 `\n` 结尾时半行实际不存在，半行分支为防御性（超大行截断场景）。实现者勿自行加缓存——那会改变 NextOffset 语义，与本节及测试断言不一致。

**状态访问器**（供 `GetInfo` 组装）：

```go
// Total 已写入总字节数（单调递增，即最新游标）。内部持 r.mu。
func (r *RingBuffer) Total() int64

// ValidStart 有效窗口起点（= max(0, total - ringCap)）。内部持 r.mu。
func (r *RingBuffer) ValidStart() int64

// Used 当前窗口内实际数据字节数（= total - ValidStart）。内部持 r.mu。
func (r *RingBuffer) Used() int64
```

### 1.2 文件 `easyserver/log/file.go` — 文件 writer（E1/E2，异步隔离）

> **红线原则**：文件 writer **异步写**——`Write` 只把字节入队立即返回，后台 goroutine 落盘。文件故障（磁盘满/权限/慢写）只影响后台 goroutine，**绝不阻塞 console + ring 主通道与主业务**。队列满/关闭时按丢弃策略处理。

**类型**

```go
// fileWriter 文件存档 writer（仅供存档，不参与实时监控）。异步：入队即返回。
type fileWriter struct {
    mu      sync.Mutex // 保护 maxSize / closed / q
    f       *os.File   // 仅在后台 goroutine 内访问（落盘线程单写，无需每写加锁）
    path    string
    maxSize int64      // 字节上限，0=不限制
    q       chan []byte // 写队列（有界，默认 1024）
    dropped atomic.Int64 // 丢弃计数（队列满时）
    wg      sync.WaitGroup // 后台 goroutine 生命周期
    closed  bool
}
```

**核心函数**

```go
// newFileWriter 打开（或创建）path 文件并启动后台落盘 goroutine；maxSize 为字节上限（0=不限）。
// ★ 若父目录不存在，先 os.MkdirAll(filepath.Dir(path))——os.OpenFile 不建父目录，全新环境开 E1 会失败。
// ★ 以 os.O_CREATE|os.O_APPEND|os.O_WRONLY 打开（O_APPEND 保证多实例/外部 logrotate 截断后仍追加到文件尾）。
func newFileWriter(path string, maxSize int64) (*fileWriter, error)

// Write 实现 io.Writer：**入队立即返回 len(p), nil**（不等待落盘）。
// 队列满时丢弃并计数（dropped），返回 len(p), nil（主通道永不阻塞）。
func (w *fileWriter) Write(p []byte) (int, error)

// Close 关闭：停收新写入、等待后台 goroutine 排空后关闭文件句柄。
func (w *fileWriter) Close() error

// SetMaxSize 热切更新大小上限（字节）。持锁写，后台 goroutine 持锁读。
func (w *fileWriter) SetMaxSize(n int64) {
    w.mu.Lock()
    defer w.mu.Unlock()
    w.maxSize = n
}
```

**后台落盘 goroutine**（`newFileWriter` 启动，单写线程）：

```go
// writeLoop 单写线程：串行处理队列，执行 stat→truncate→append。
// ⚠️ 本 goroutine 内**禁止调 log.Error**（递归风险，见 §1.3 fanout 说明）——失败仅计数。
func (w *fileWriter) writeLoop() {
    defer w.wg.Done()
    for p := range w.q {
        w.mu.Lock()
        maxSize := w.maxSize
        closed := w.closed
        w.mu.Unlock()
        if closed { return }
        // 写前检查大小，超限清空再写
        if maxSize > 0 {
            fi, err := w.f.Stat()
            if err != nil {
                // ⚠️ M2：Stat 失败（外部 logrotate 已删文件）跳过截断检查直接追加——O_APPEND 对已删 inode 无害
            } else if fi.Size() >= maxSize {
                _ = w.f.Truncate(0)
                _, _ = w.f.Seek(0, io.SeekStart)
                _, _ = w.f.WriteString("[log] 文件超限已清空\n")
            }
        }
        _, _ = w.f.Write(p) // 落盘失败：忽略（红线——不影响主通道），可计数留痕
    }
}
```

> **TODO（一期不处理）**：`stat → truncate → append` 非原子，多 fileWriter 实例共享同一文件时可能都截断丢数据。当前单实例 + 单写线程实际串行，代码留 `// TODO` 备注跨实例风险。
> **队列/丢弃**：队列满时丢弃最旧（或最新）并 `dropped.Add(1)`，`GetInfo` 可暴露丢弃计数供排查。文件日志仅存档，丢弃可接受（红线：不阻塞主通道）。
> **清空标记**：超限清空时写 `[log] 文件超限已清空` 一行，便于排查。

### 1.3 文件 `easyserver/log/fanout.go` — 多路分发 writer

**类型**

```go
// fanoutWriter 多路分发：console + ring（恒开）+ file（可选）。
type fanoutWriter struct {
    mu      sync.RWMutex
    console io.Writer // 恒开（stdout）
    ring    *RingBuffer // 恒开（监控通道）
    file    *fileWriter // nil = 未开文件
}
```

**核心函数**

```go
// Write 分发到所有通道；任一通道失败不影响其他。
func (f *fanoutWriter) Write(p []byte) (int, error)

// SetFile 开启/关闭文件通道（nil 关闭）。
func (f *fanoutWriter) SetFile(w *fileWriter)

// File 返回当前文件通道（nil 表示未开）。持读锁，避免与 SetFile 写竞争。
func (f *fanoutWriter) File() *fileWriter
```

落地逻辑（`Write`）：
1. `f.mu.RLock()`；`defer f.mu.RUnlock()`。
2. `f.console.Write(p)`（忽略错误）。
3. `f.ring.Write(p)`（忽略错误）。
4. 若 `f.file != nil`：`f.file.Write(p)`（忽略错误，文件失败不影响 console/ring）。
5. 返回 `len(p), nil`。

落地逻辑（`SetFile`）：
1. `f.mu.Lock()`；`defer f.mu.Unlock()`。
2. 旧 `f.file` 若非 nil：`Close()`（确定性关闭，无在途写入——因持写锁，无并发写）。
3. `f.file = w`。

### 1.4 文件 `easyserver/log/log.go` 改造

**包级状态**（在现有 `lg`/`once` 基础上扩展）：

```go
var (
    lg         *slog.Logger           // 现有：当前 logger（保留声明）
    lgLevel    *slog.LevelVar = &slog.LevelVar{}
    fanout     *fanoutWriter
    once       sync.Once
    onceDone   atomic.Bool // 供 SetTemplateLoader 判断 once 是否已执行（sync.Once 无公开 Done()）
    onLevelChange func(string) // 级别变更钩子（入参为级别字符串）
)
```

**getLogger 改造**：

```go
// getLogger 惰性初始化：console + ring 恒开，输出格式由 log.tpl 模板决定。
func getLogger() *slog.Logger {
    ensureFanout()
    return lg
}

// ensureFanout 保证 fanout/lg 已初始化（sync.Once 幂等）。
// 所有可能被「首次日志调用之前」触发的 API（SetFileWriter/GetInfo/Tail/SetMaxSize）
// 必须先调用 ensureFanout()，避免 fanout==nil 时 nil 解引用 panic。
func ensureFanout() {
    once.Do(func() {
        fanout = &fanoutWriter{
            console: os.Stdout,
            ring:    NewRingBuffer(),
        }
        // 模板启动时加载，运行期定死（见 template.go）
        lg = buildLogger()
        onceDone.Store(true)
    })
}
```

**buildLogger**：

```go
// buildLogger 按 log.tpl 模板构建 slog.Logger（启动时定死，运行期不重建）。
func buildLogger() *slog.Logger {
    opts := &slog.HandlerOptions{Level: lgLevel}
    th, err := newTemplateHandler(fanout) // 加载 log.tpl，模板渲染输出
    if err != nil {
        // 模板加载失败 → 回退 slog 默认 text handler（保证可用）。
        // ⚠️ L1 落定：NewOptions() 内部 `if !isSetLevel { lgLevel.Set(LevelInfo) }` 会把级别重置为 info。
        //   本方案 SetLevel 不再置 isSetLevel。修复：**回退前保存 lgLevel.Level()，构建后再恢复**：
        //   saved := lgLevel.Level()              // 保存
        //   fallback := NewOptions()              // 可能重置 lgLevel 为 info
        //   lgLevel.Set(saved)                    // 恢复
        //   fallback.Level = lgLevel              // 绑定恢复后的级别
        //   或用不含级别重置的 HandlerOptions 手工构造（不调 NewOptions）。
        // 回退路径的级别过滤由 slog.HandlerOptions.Level（lgLevel）承担；因 SetLevel 上限为 error，
        // Error 恒输出的基座约束在回退路径下同样成立（设到 error 时 error 仍输出，只是 warn 以下被过滤）。
        saved := lgLevel.Level()
        fallback := NewOptions() // 含时间格式化的 HandlerOptions（其 Level 已绑定 lgLevel 指针）
        lgLevel.Set(saved)       // 恢复级别（NewOptions 可能把它重置为 info）
        return slog.New(slog.NewTextHandler(fanout, fallback)) // fallback.Level 无需再赋（同一指针）
    }
    return slog.New(th)
}
```

> 注意：`fanout` 在 `once.Do` 内创建，`buildLogger` 引用它。模板加载必须在首次日志调用前完成（启动装配期），运行期不可热切换。
> 旧 API 处置（L3）：`SetOptions`/`SetLogWriter` 保留但**语义不再生效**（fanout 接管输出），标记 deprecated。`ResetLogger` 改为 **no-op（仅打 deprecated 警告）**——若保留旧实现（重建 TextHandler 覆盖 `lg`），一旦被调用会切断 console+ring 通道，破坏基座。

### 1.5 文件 `log/setter.go` 改造

**新增/改造 API**：

```go
// SetLevel 设置级别；级别真正变化时触发钩子。
// ★ 基座约束：级别最高只能设到 error——若传入高于 error 的级别，钳制为 error。
//   （即使钳制失效，templateHandler.Enabled 仍对 >=error 无条件放行，双保险）
func SetLevel(level slog.Level) {
    if level > slog.LevelError {
        level = slog.LevelError
    }
    old := lgLevel.Level()
    lgLevel.Set(level)
    if old != level && onLevelChange != nil {
        onLevelChange(level.String())
    }
}

// SetOnLevelChange 注册级别变更钩子（rocksys 注入持久化回调）。
func SetOnLevelChange(fn func(string)) { onLevelChange = fn }

// SetFileWriter 开启/关闭文件存档通道（E1）。
// ★ 内部实现 setFileWriterUnlocked 不持 stateMu，供 SetLogWriterByFile 复用（避免死锁，见下）。
func SetFileWriter(on bool) error {
    ensureFanout()
    stateMu.Lock()
    defer stateMu.Unlock()
    return setFileWriterUnlocked(on)
}

// setFileWriterUnlocked 开启/关闭文件通道（调用方须已持 stateMu）。
// 禁止在本函数内再次 stateMu.Lock()——sync.Mutex 不可重入，否则与
// SetLogWriterByFile 嵌套调用时死锁。
func setFileWriterUnlocked(on bool) error {
    if on {
        if fanout.File() != nil { return nil } // 已开
        fw, err := newFileWriter(filePath, maxSize)
        if err != nil { return err }
        fanout.SetFile(fw)
    } else {
        fanout.SetFile(nil)
    }
    return nil
}

// SetLogWriterByFile 保留：创建文件并开启文件通道。
// ★ 不直接调用 SetFileWriter（两者都持 stateMu），改为调无锁内部实现，避免自死锁。
// ★ 返回值仅为「兼容旧语义」保留：返回的 *os.File 归 log 包托管，调用方**不应** Close 它
//   （否则会关闭 fanout 正在使用的句柄）。
func SetLogWriterByFile(path string) (*os.File, error) {
    ensureFanout()
    stateMu.Lock()
    defer stateMu.Unlock()
    if path == "" {
        return nil, errors.New("log: empty file path")
    }
    filePath = path
    if err := setFileWriterUnlocked(true); err != nil { return nil, err }
    return fanout.File().f, nil // 返回底层句柄（兼容旧语义）
}

// SetMaxSize 设置文件大小上限（E2，整数 MB，0=不限制；负数按 0 处理=不限制）。
func SetMaxSize(mb int64) {
    ensureFanout() // 兜底初始化
    stateMu.Lock()
    defer stateMu.Unlock()
    if mb < 0 {
        mb = 0
    }
    maxSize = mb * 1024 * 1024
    if fanout.File() != nil {
        fanout.File().SetMaxSize(maxSize)
    }
}

// GetInfo 返回当前日志状态（先 ensureFanout 兜底初始化）。
func GetInfo() Info {
    ensureFanout()
    stateMu.Lock()
    defer stateMu.Unlock()
    // RingCap=ringCap, RingTotal=fanout.ring.Total(),
    // RingUsed=fanout.ring.Used(), RingValidStart=fanout.ring.ValidStart()
    // ⚠️ 三个访问器各自持 r.mu，快照可能非同一时刻（仅诊断字段，无碍；若需一致快照，
    //   在 RingBuffer 加一次取三值的 Snapshot() 方法，内部单次持锁）。
    // Template 恒为 "log.tpl"（模板文件名，见包级 templateFile，启动时定死）
    // FileOn/FilePath/MaxSizeMB 取包级状态与 fanout.File() 的指针身份（非 nil），
    // ⚠️ L4：不得直接读 fileWriter 的 path/maxSize 内部字段（其由 fileWriter.mu 保护，
    //   与 SetMaxSize 并发时 -race）；FileOn 由 fanout.File()!=nil 判定即可。
}

// Tail 读 ring buffer 增量（供 HTTP tail / SSE）。
func Tail(n int64, since int64) TailResult {
    ensureFanout()
    return fanout.ring.Tail(n, since)
}
```

> 注：`SetOnLevelChange` 仅装配期单线程调用一次，`onLevelChange` 无锁可接受（L6，注明即可）。

**Tail 首次拉取契约（`since` 缺省语义）**：

```go
// 客户端首次请求时 since 缺省：统一由 admin 层 parseSince 返回 -1 → Tail 内部「尾部首拉」
// （取窗口尾部最后 n 行，见 §1.1）。切勿改用 GetInfo().RingValidStart——那会取窗口最旧 n 行，
// 与「尾部」契约相反，P4 首次拉取验收必失败。
```

**Info 类型**：

```go
type Info struct {
    Level         string `json:"level"`
    Template      string `json:"template"` // 当前模板文件名（log.tpl）
    FileOn        bool   `json:"file_on"`
    FilePath      string `json:"file_path"`
    MaxSizeMB     int64  `json:"max_size_mb"`
    RingCap       int64  `json:"ring_cap"`
    RingUsed      int64  `json:"ring_used"`
    RingTotal     int64  `json:"ring_total"`
    RingValidStart int64 `json:"ring_valid_start"` // 仅诊断用（有效窗口起点）；客户端首拉**不得**使用——首拉必须走 since 缺省→Tail 尾部首拉
}
```

**包级新增变量**：

```go
// 包级可变状态用 mutex 保护：SetLogWriterByFile/SetMaxSize 写、SetFileWriter/GetInfo 读，
// watcher 回调 goroutine 与 admin handler 并发时不得有数据竞争（L3）。
var (
    stateMu    sync.Mutex // 保护 filePath/maxSize（模板文件名启动时定死，不需锁）
    templateFile = "log.tpl"            // 模板文件名（经 ScriptDir 兜底加载）
    filePath     = "logs/rocksys.log"   // 文件存档路径（写时持 stateMu）
    maxSize      int64 = 50 * 1024 * 1024 // 默认 50MB（写时持 stateMu）
)
```

### 1.6 文件 `log/template.go` — log.tpl 模板渲染

**log.tpl 格式规范**（纯文本模板，占位符注入）：

```
# log.tpl — 日志输出模板（纯文本，任意风格）
# 语法：text/template，字段名遵循 slog 标准（time/level/msg/attr key）
# 程序把日志变量注入 data，替换 {{.字段}} 占位符
{{.time}} [{{.level}}] {{.msg}} {{.key1}}={{.val1}}
```

json 风格模板示例：

```
{"time":"{{.time}}","level":"{{.level}}","msg":"{{.msg}}"}
```

- **字段集合**：`time`（Go 时间串）、`level`（slog.Level.String()）、`msg`（日志消息），以及该条日志的全部 attr key（`slog.Attr` 展开为 `key → value`）。
- **建议模板自带换行 `\n` 结尾**：实现层兜底强制——渲染后若 `buf` 末尾无 `\n` 则补一个（保证 ring buffer 按行切分与实时监控可用）。
- **json 风格模板不做自动转义**：attr 值含 `"` 时输出非法 JSON（text/template 默认不转义）。需 JSON 结构时，模板作者自行处理（或对 msg 等字段预转义后注入）。
- 模板缺失/为空/解析失败 → 回退内嵌默认模板。

**核心类型**：

```go
// templateHandler 按 log.tpl 渲染每条日志。
type templateHandler struct {
    mu    sync.Mutex
    w     io.Writer
    tpl   *template.Template // text/template，解析后的模板
    buf   *bytes.Buffer      // 复用缓冲（避免每行分配）
    attrs []slog.Attr        // WithAttrs 累积的附加 attrs（WithGroup 为 group 前缀，可合并）
}

// newTemplateHandler 加载 log.tpl 并解析。
// ★ 回退层级（L2）：内部先经 tplLoader（外部）加载 → 失败回退内嵌 log.tpl → 仍失败才返回 error
//   （调用方 buildLogger 收到 error 才回退 slog text handler）。三级语义：
//     外部 log.tpl → 内嵌 log.tpl → slog 默认 text handler。
//   解析失败（非加载失败）同样回退内嵌，最后返回 error。
func newTemplateHandler(w io.Writer) (*templateHandler, error)

// Handle 实现 slog.Handler：渲染模板 → 写 w。
func (h *templateHandler) Handle(_ context.Context, r slog.Record) error

// Enabled 实现 slog.Handler：级别过滤。
func (h *templateHandler) Enabled(_ context.Context, l slog.Level) bool

// WithAttrs 实现 slog.Handler。⚠️ 返回**新 handler**：复制本 handler、追加 attrs 到 h.attrs、
//   **新建独立的 buf**（不得共享原 buf——并发 With 后各 handler 的 buf 由各自 mu 保护），
//   不得返回自身（否则 logger.With() 的 attr 静默丢弃）。当前 15 处调用方均不用 With()，本实现仅供 API 完整性。
func (h *templateHandler) WithAttrs(attrs []slog.Attr) slog.Handler

// WithGroup 实现 slog.Handler。⚠️ 同理返回新 handler（attrs 带 group 前缀，新建 buf）。
// ⚠️ group 注入用**嵌套 map**（实测 text/template 的 `{{.req.id}}` 是字段链解析，对扁平键
//   `"req.id"` 会输出 `<no value>`）：
//   data["req"] = map[string]any{"id": v, ...}（可递归嵌套）。
//   模板作者按 `{{.req.id}}` 引用；不支持的部分场景（如 group 名为空/重复）按废弃处理。
// **嵌套组装规格（M4，Handle 侧必须实现）**：h.attrs 中的 group attr 以 `group.key` 形式存储；
//   Handle 组装 data 时按 `.` 分割 key、逐级构造 `map[string]any`（`data["req"] = map{"id": v}`），
//   同名冲突时内层覆盖外层。
func (h *templateHandler) WithGroup(name string) slog.Handler
```

**Handle 落地逻辑**：
1. 组装 `data := map[string]any{ "time": r.Time.Format(time.DateTime), "level": r.Level.String(), "msg": r.Message }`。
2. 先展开 `h.attrs`（WithAttrs 累积的）注入 data。
3. 再 `r.Attrs(func(a slog.Attr) bool { data[a.Key] = a.Value.Any(); return true })` 展开记录级 attr——**记录级后写覆盖 With 级**（中-1：与「记录级优先」语义一致；实现时注意先后顺序，先 With 后 Record）。
4. **保留键防御（L3）**：`time`/`level`/`msg` 同名 attr（无论记录级还是 With 级）跳过，保证模板的 time/level/msg 恒为注入的标准值。
5. `h.mu.Lock()`；复用 `h.buf` 渲染模板；**若 `h.buf` 末尾无 `\n` 则补写 `\n`**；`h.w.Write(buf.Bytes())`；`h.mu.Unlock()`。
6. 返回 nil。

> **并发安全**：模板渲染用 `h.mu` 串行化，缓冲复用安全。
> **内嵌兜底**：`easyserver/log` 内嵌一份默认 `log.tpl`（`//go:embed log.tpl`），默认模板为静态形态 `time={{.time}} level={{.level}} msg={{.msg}}` 且以 `\n` 结尾。rocksys 装配时把 `hotswap.ScriptDir` 注入（见 §2.4），使外部可覆盖。
> **双份 embed 一致性（L4）**：`easyserver/log` 与 `cmd/rocksys` 各内嵌一份 `log.tpl`。集成后以 **`cmd/rocksys` 副本为准**（它作为 `NewScriptDir` 的 embedFS，是运行时实际加载源）；`easyserver/log` 内的副本仅作子仓库独立测试/回退用。两份内容应保持一致（同一下发）。
> **Error 恒输出**：`templateHandler.Enabled` 须对 `l >= slog.LevelError` **无条件放行**（不受 `lgLevel` 限制），保证任何级别下 Error 恒输出——这是基座约束的实现机制。
> **time 注入格式**：统一 `r.Time.Format(time.DateTime)`，与现状 `NewOptions` 的 ReplaceAttr 一致，保证默认路径时间戳格式不变。

### 1.7 P1 单测（`easyserver/log/*_test.go`）

| 用例 | 断言 |
|---|---|
| `TestRingBufferWriteRead` | 写入→Tail 增量正确；`NextOffset` 续读无重复无遗漏 |
| `TestRingBufferOverwrite` | 写满覆盖最旧；旧 `since` → `Reset=true` |
| `TestRingBufferSinceZero` | `since==0` 且 `total>0`：不 panic、视为行起点进入增量读取（M1 边界） |
| `TestRingBufferSinceValidStart` | `since==validStart`：不误 Reset、正常增量读取（M1 边界） |
| `TestRingBufferLineAlign` | 半行不返回；`NextOffset` 指向完整行边界（行尾或半行起点，非半行中间）；续读无「半行+新行」拼接脏行 |
| `TestRingBufferHugeLine` | 单条超 `ringCap` 截断为最后 `ringCap` 字节 |
| `TestFanoutBase` | `SetFileWriter(false)` 后 Info/Error 仍写 console+ring |
| `TestFanoutFile` | 开文件后 console+ring 立即输出、file 异步落盘；文件写失败不影响 console/ring（红线）；开文件后 GetInfo/Tail 仍可用（无死锁） |
| `TestFileTruncate` | `maxSize=1` 连写超限 → 文件清空并留标记（后台异步）；`maxSize=0` 不限制 |
| `TestFileAsync` | 文件队列满/写入失败时 console+ring 不受影响；`dropped` 计数递增；Close 排空后文件完整 |
| `TestLevelHook` | `SetLevel` 值变化触发钩子；相同值不触发 |
| `TestFormatTemplate` | 外挂 `log.tpl` 生效；缺失回退内嵌；json 风格模板可用；模板解析失败回退默认 |
| `TestCompatRegression` | 默认路径（无增强）输出**关键字段与行结构**贴近现状（`time=... level=... msg=...`，time 用 time.DateTime 格式）；**模板未引用的 attr 不输出**（默认模板即丢弃全部 attr，属既定取舍）；15 处调用方行为不变 |

**验收**：`cd easyserver && go test -race ./log/...` 全绿。

---

## 2. P2 — `internal/conf` 集成

### 2.1 新增配置项（`conf.bindBaseVars`）

在 `bindBaseVars` 增加指针并注册：

```go
m.logToFile = new(bool)
m.logFile   = new(string)
m.logMaxSize = new(string) // 整数 MB，字符串存储便于校验

m.ec.BoolVar(m.logToFile, "ROCKSYS_LOG_TO_FILE", false, "文件存档（E1）")
m.ec.StringVar(m.logFile, "ROCKSYS_LOG_FILE", "logs/rocksys.log", "日志文件路径")
m.ec.StringVar(m.logMaxSize, "ROCKSYS_LOG_MAX_SIZE", "50", "文件大小上限（整数 MB，0=不限制；E2）")
```

`rebuildConfig` 增加对应字段（`LogToFile bool`、`LogFile string`、`LogMaxSize int64`）。

> **`ROCKSYS_LOG_MAX_SIZE` 解析（L3）**：`rebuildConfig` 中 `strconv.ParseInt(trimSpace(LogMaxSize), 10, 64)` 解析为字节 MB 数；解析失败**回退默认 50**（并 `log.Warn`），`0` 表示不限制，负数视为非法回退默认。避免静默把非法值当 0（不限制）。**解析失败时同步把 easyconf 项修正为 "50"**（否则后续 `conf.Set` 的 `UpdateFile` 会把非法原始串写回 .env）。**修正写入点（M5）**：`rebuildConfig` 是纯函数不应带副作用——修正放在 `publishLocked` 内（重建 Config 前检查并 `SetItemValue("ROCKSYS_LOG_MAX_SIZE","50")`）或在 `Set` 写盘前统一校验，实现者任选其一并在代码注释标明。**负数语义统一**：conf 层负数→回退默认 50；log 层 `SetMaxSize` 负数→按 0（不限制）——两处语义不同，但 `SetMaxSize` 由装配/watcher 传入的是 conf 解析后的值（已非负数），实际不会冲突，文档标注即可。

> ⚠️ **同步 default.env（L7）**：新增三项 `ROCKSYS_LOG_TO_FILE/LOG_FILE/MAX_SIZE` 需同步写入仓库根 `default.env`。easyconf 在 `.env`/default.env 缺失时由全部已注册项的默认值创建；default.env 是低优先级配置文件。不写 default.env 不会导致缺默认值（注册时已生效），但新部署的 `.env` 会缺这三项的注释说明。行动建议仍然成立。

> 无 `ROCKSYS_LOG_FORMAT`——输出格式由 `log.tpl` 模板文件决定，不是配置项。

### 2.2 `conf.Set` 值比较防循环（`impl.go`）

> ⚠️ **以 §2.3 的最终时序为准**——本节的 `Set` 值比较与防循环语义直接并入 §2.3 的持锁版本。**不得**在本节单独实现一份无锁 `Set`（否则与 §2.3 的 `publishLocked`/`syncArgsLocked` 冲突：要么 -race，要么二次加锁死锁）。

**值比较语义**（供 §2.3 的 `Set` 使用）：

```go
// currentValue 读当前注册值。ok=false 表示未注册。
// ★ 注意：easyconf.Conf 无 GetItem(name) 方法，只有 GetItems()。
// 用 GetItems() 遍历匹配 name；bool/int 项的 GetValue() 返回字符串形态
//（"true"/"false"），与 Set 入参同为字符串，可直接比较。
func (m *confManager) currentValue(name string) (string, bool) {
    for _, it := range m.ec.GetItems() {
        if it != nil && it.Name == name && it.Value != nil {
            return it.GetValue(), true
        }
    }
    return "", false
}

// isCaseInsensitiveKey 哪些 key 的值比较忽略大小写（级别类枚举）；其余（路径等）精确比较。
// ⚠️ 仅 ROCKSYS_LOG_LEVEL 用 EqualFold；路径类值（ROCKSYS_LOG_FILE）Linux 上大小写敏感，不得 EqualFold。
func (m *confManager) isCaseInsensitiveKey(name string) bool {
    return name == "ROCKSYS_LOG_LEVEL"
}
```

> **bool 项取值（L3）**：easyconf 对 bool 项只认 `"true"`（其余如 `"1"` 解析为 false）。`ROCKSYS_LOG_TO_FILE` 的 Set 调用方（admin `POST /admin/log/output`）恒传 `"true"/"false"`，风险低；文档统一此约定即可。

> ⚠️ **行为变更标注（M7）**：新增「未注册 key 直接 return nil（不写盘不广播）」。现有 `Set`（impl.go:277-287）对未注册 key 会无条件 `SetItemValue`（easyconf 静默忽略）+ `publish` + `UpdateFile`。此为本方案的**行为变更**（避免未注册 key 无意义全量写盘），`handleConfigPut` 调用方行为随之改变——属预期。

### 2.3 命令行参数同步（`impl.go`）

```go
// syncArgs 热更写回后同步更新内存命令行参数，避免 watcher 重放旧值覆盖。
// ★ 仅当 key 原本已存在于 m.args 时才更新其值；否则**不追加**——追加会使后续
//    用户直接改 .env 的同一 key 永久失效（命令行优先级覆盖，直到重启），见下。
// ★ 需同时处理两种形态：`--name=value` 与 `--name value`（空格分隔，见 impl.go:331-352 的 parseArgsToMap）。
//   遍历 m.args：命中任一形态则替换为新值；两种形态都不存在则跳过（不追加）。
// ★ 本函数为「无锁内部版」，调用方须已持 m.mu（由 Set 持有）；禁止自行加锁。
func (m *confManager) syncArgsLocked(name, value string) {
    // 遍历 m.args：--name=value 形态直接替换；--name value 形态替换其后续元素
    // ⚠️ L5：仅处理两种 value 形态；无值开关型（--name 后直接下一个参数）不在本方案覆盖
    //   （日志相关键均为字符串值参数，不触发），遇到 `--name` 后跟 `--` 开头的参数时跳过。
}

// syncArgs 公开入口：加锁后调内部版（供其他路径复用）。
func (m *confManager) syncArgs(name, value string) {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.syncArgsLocked(name, value)
}

// publishLocked 无锁内部版（调用方已持 m.mu）：
//   1. cfg := m.rebuildConfig()   // 移入锁内（M1，防与 Set 并发 race）
//   2. m.cfg.Store(cfg)           // ★ 必须（M3）：否则 Set 热更后 Current() 读到旧配置，
//                                   直到下次 watcher 轮询（≤3s）才刷新，GET /admin/config 陈旧
//   3. 快照 watchers（依赖调用方持锁）
//   4. 逐个 go fn(cfg)            // 回调在独立 goroutine（锁外），不二次加锁
// ⚠️ 本函数**不得加任何锁**（不持 m.mu，也不二次加锁）——watcher 快照的读安全
//   由调用方已持有的 m.mu 保证（写 watchers 的 Watch() 同样持 m.mu，读写互斥成立）。
func (m *confManager) publishLocked() {
```

**并发闭环（必须实现，否则 `-race` 必失败）**：

0. **`reloadFiles` 最终形态（M2，单一实现，杜绝死锁/-race 双风险）**：

```go
// watchLoop（impl.go:189-215）调用方：直接调 reloadFilesLocked，无需外部 args 快照。
func (m *confManager) watchLoopIteration() {
    m.reloadFilesLocked() // 无锁内部版；args/files 均在内部持锁读取（避免 TOCTOU，见下）
}

// reloadFilesLocked 无锁内部版：**本函数整体持 m.mu**（调用方不持锁）。
// ★ args 快照与重载必须在同一次锁持有内完成（M4）：若先快照 args 再释放锁、进入本函数
//   再加锁，conf.Set（更新 args + 写盘）插入两次持锁之间时，重载会用旧 args 覆盖热更值——
//   内存回退、磁盘已更新、热更静默失败不自愈。故本函数在持锁内直接读 m.args，不做外部快照。
func (m *confManager) reloadFilesLocked() error {
    m.mu.Lock()
    defer m.mu.Unlock()
    files := m.watchFiles()  // 锁内读 *m.configFile（中-2：锁外读与 Set 写竞争）
    args := m.args           // 锁内读（M4：与 syncArgsLocked 同锁串行）
    // 1. 逐文件 SetValuesByEnvFile / SetValuesByEnv / 重放 args（对 easyconf 项写入）
    // 2-3. publishLocked() 内含 rebuildConfig + cfg.Store + 快照 + go fn(cfg)（M3）
    //      —— 本函数不重复 rebuild/Store
    _ = files
    _ = args
    return nil
}
```

> 说明：`reloadFilesLocked` **自行持锁并锁内读 `m.args`**（无外部快照传递——M4：快照与重载若分两次锁持有会引入 TOCTOU）。`watchLoop` 只调 `reloadFilesLocked()`；`defaultLoader`/`Register` 装配期可无锁调用同函数（单线程）。
> ⚠️ **files 快照与热更（L4）**：`watchLoop`（impl.go:189-215）在循环开始时快照 `watchFiles()`；运行期热更 `ROCKSYS_CONFIG` 后，该快照与实际监听文件可能不一致（既有行为，本方案不改动），文档标注即可。
> 现有 `reloadFiles`（impl.go:113-127）需替换为 `reloadFilesLocked`，并把内部 `m.publish()` 同步改为 `m.publishLocked()`。

1. **`m.args` 竞争**：`syncArgsLocked` 由 `Set` 持 `m.mu` 写 `m.args`，`watchLoop` 读 `m.args` 前持 `m.mu` 拷贝快照（见上）——闭环。
2. **easyconf 配置项竞争（M2）**：`Set` 中的 `currentValue()`（遍历 `GetItems`+`GetValue`）、`m.ec.SetItemValue`、`UpdateFile`（内部读 `ValueStr`）、**`List()`（impl.go:291-308，`GET /admin/config/list` 常轮询，遍历 `GetItems`+`GetValue` 无锁）**与 `reloadFilesLocked→SetValuesByEnvFile/SetItemValue` 对同一 `ConfItem.Value`（指针目标）与 `ValueStr` 的读写均无锁，watcher 回调与 admin handler/级别钩子并发时构成数据竞争。**落死方案**：在 `confManager` 内把 easyconf 项的全部读写路径（含 `List()`）收口到 `m.mu`（`currentValue`/`Set` 的 `SetItemValue`/`UpdateFile`/`List()`/`reloadFilesLocked` 均持 `m.mu`），并在 P2 验收加并发触发用例（含并发 `GET /admin/config/list` + 热更）。
3. **`Register`（impl.go:238-271）**：目前无锁直调 `SetValuesByEnvFile`/重放 args。**装配期例外（中-6）**：当前 `Register` 仅在 `StartWatcher` 前的装配期调用、实际无并发；若未来运行期再注册，须同样持 `m.mu`——M2 的「全部读写路径收口 m.mu」对 `Register` 仅限运行期注册场景，装配期保持无锁。**最终锁形态（L1）**：装配期保持现有加锁版 `publish()` 亦可（无并发）；若未来运行期注册，改调自锁的 `reloadFilesLocked()` 语义——勿在装配期误用 `publishLocked()`（其依赖调用方持锁，装配期未持锁会导致 watchers 快照无锁读）。

**Set 最终时序（含锁，⚠️ 防死锁）**：

```go
func (m *confManager) Set(name, value string) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    // 防循环：值相同则跳过（不重写配置源、不重复广播）
    cur, ok := m.currentValue(name)
    if !ok {
        return nil // 未注册 key：easyconf 静默忽略，直接返回
    }
    // ⚠️ EqualFold 仅对级别类枚举键（m.isCaseInsensitiveKey）；路径类值（ROCKSYS_LOG_FILE）
    //    Linux 大小写敏感，不得 EqualFold，否则大小写差异被误判相同而跳过写盘。
    if (m.isCaseInsensitiveKey(name) && strings.EqualFold(cur, value)) || cur == value {
        return nil
    }
    if err := m.ec.SetItemValue(name, value); err != nil { return err }
    m.publishLocked()                 // ★ 无锁内部版（不得调用加锁的 publish()）
    m.syncArgsLocked(name, value)     // ★ 无锁内部版（不得调用加锁的 syncArgs()）
    files := m.watchFiles()
    if err := m.ec.UpdateFile(files[len(files)-1]); err != nil { ... } // 后写盘
    return nil
}
```

> ⚠️ **防死锁（H1）**：`Set` 已持 `m.mu`，其内部**只能调用 `publishLocked`/`syncArgsLocked`**。若调用加锁版 `publish()`（impl.go:145 内部 `m.mu.Lock()`）或 `syncArgs()`，同一 goroutine 二次 Lock 永久阻塞——级别热更、`PUT /admin/config`、`POST /admin/log/level`、`POST /admin/log/output` 全部卡死。实现时必须把现有 `publish()` 重构为「无锁内部版 + 加锁公开版」两层。

> ⚠️ **用户直接改 `.env` 的边界**：若进程以命令行级别参数启动（如 `--log-level=info`），用户手动编辑 `.env` 改级别后，watcher 重放 `m.args` 会把旧命令行值覆盖回 info——命令行优先级高于 .env（既有 conf 设计）。本方案只保证**经 `conf.Set` 的热更**（含 POST 端点）写回后不被覆盖。**`syncArgsLocked` 不追加**，故热更后用户仍可直接改 .env 生效（该 key 不在 m.args 中时）。
> ⚠️ **环境变量覆盖（L2）**：`reloadFiles` 含 `SetValuesByEnv`（impl.go:119）——若 `ROCKSYS_LOG_LEVEL` 以**系统环境变量**方式启动，`conf.Set` 写盘后 ≤3s 会被 watcher 重放环境变量覆盖回滚（环境变量优先级高于 .env）。此场景下「热更即持久化/重启保留」不成立，属既有 conf 设计；本方案仅处理命令行覆盖，环境变量场景需明确文档边界。

### 2.4 日志装配（`cmd/rocksys/main.go`）

在 `buildServer` 中，`conf.Load` 之后、首次日志调用前：

```go
// 0. cmd/rocksys 侧内嵌 log.tpl（兜底源），显式声明 embed 变量：
//    //go:embed log.tpl
//    var embedLogTplFS embed.FS
//    ⚠️ M3：//go:embed 只能用于**包级 var**，不能放函数内局部变量（会编译失败）。
//    文件须位于 cmd/rocksys/ 下；勿用 GetScriptDir 单例，直接传 NewScriptDir 构造值

// 1. 注入模板加载器（hotswap.ScriptDir 适配，外置优先/内嵌兜底），
//    必须在首次日志调用前完成——日志格式启动时定死，运行期不可热切换。
//    ⚠️ hotswap.GetScriptDir 是 sync.Once 单例：首个调用者锁定返回值。若其他组件
//    抢先调用（本仓库当前无此场景），log.tpl 外挂会被静默吞掉。稳妥做法：直接传
//    hotswap.NewScriptDir(...) 构造的 *ScriptDir 值（不经 GetScriptDir 单例）。
log.SetTemplateLoader(hotswap.NewScriptDir(embedLogTplFS, "log"))

// 2. 文件存档（E1/E2）
if cfgMgr.Current().LogToFile {
    log.SetLogWriterByFile(cfgMgr.Current().LogFile)
    log.SetMaxSize(cfgMgr.Current().LogMaxSize)
}

// 3. 级别钩子 → 持久化
log.SetOnLevelChange(func(level string) {
    _ = cfgMgr.Set("ROCKSYS_LOG_LEVEL", level)
})

// 4. 启动级别（★ 必须在钩子注册之后调用）
//    ★ 中-2：本步骤**替换**现有 main.go:133 的 `log.SetLevel(slogLevel(cfgMgr.Current().LogLevel))`——
//      保留两处会导致时序与文档假定不一致。替换后语义：
//    ——若先 SetLevel 后注册钩子，启动时的非默认级别不触发持久化，重启不保留；
//    ——若钩子注册于 SetLevel 之前且级别非默认，钩子会调 conf.Set("ROCKSYS_LOG_LEVEL", "DEBUG")。
//      ⚠️ M5（启动写盘副作用）：.env 为默认 info、命令行 --log-level=debug 时，
//      EqualFold("info","DEBUG") 不等 → 钩子 conf.Set → **每次启动都把 "DEBUG" 写回 .env**。
//      这是「热更即持久化」的既定行为（无循环、mtime 一次稳定），不是缺陷；
//      P2 验收勿断言「启动不写盘」。若 .env 已是 debug，EqualFold 相等则跳过不写盘。
log.SetLevel(slogLevel(cfgMgr.Current().LogLevel))
```

> **注入时序（M4 约定）**：`SetTemplateLoader` 必须插在 **main.go:133 的 `log.SetLevel(...)` 位置（替换该行前）**——即首次日志调用 `log.Info("rocksys starting")`（main.go:134）**之前**、且早于所有 `conf.Register`（见 L6）。`SetTemplateLoader` 实现层加防御：若 `onceDone.Load()` 为 true（once 已执行）则 `log.Warn("模板加载器注入过晚，外挂 log.tpl 不生效")` 并拒绝静默忽略——用 `atomic.Bool` 标志，因 `sync.Once` 无公开 `Done()`。
> **⚠️ 装配期触发初始化的隐藏路径（L6）**：`conf.Register` 的 `publish` 会触发日志 watcher 回调（回调调 `log.GetInfo()` → `ensureFanout`）。因此 `SetTemplateLoader` 不仅要早于首次日志调用，还要**早于任何 `conf.Register`**（含 adminapi.New 内部注册、DB_DRIVER 注册等）。若放在任一 Register 之后，`onceDone` 已被置位，外挂 log.tpl 静默失效（仅 log.Warn）。**建议**：`SetTemplateLoader` 放在 `buildServer` 内 `conf.Load` 之后、所有 `Register` 之前。
> **外置 log.tpl 定位约定**：用户把自定义 `log.tpl` 放在工作目录 `log/` 下即可覆盖内嵌默认；缺失时回退内嵌。
> **cmd/rocksys 必须内嵌一份 log.tpl**（作为 `NewScriptDir` 的 embedFS），否则传 nil fs.FS 会 panic（`fs.ReadFile(nil,...)` 触发 nil 接口调用），而模板加载失败回退逻辑只兜 error 不兜 panic。

### 2.5 watcher 联动

> ⚠️ **本订阅是 `PUT /admin/config` 改级别/文件的唯一生效通道（M3）**：`handleConfigPut` 只调 `conf.Set` 不调 `log.SetLevel`，级别真正生效靠 `conf.Set → publish → 本回调 → log.SetLevel`（异步，秒级内）。**若缺失本订阅，`PUT /admin/config` 带 `ROCKSYS_LOG_LEVEL` 会静默不生效**。
> **注册位置（M4）**：`cfgMgr.Watch` 建议放在 `buildServer` 中 **`SetTemplateLoader` 之后、首个 `conf.Register`（main.go:168 起）之前**——与 L6 警告一致（Register 的 publish 会触发回调 → GetInfo → ensureFanout，若模板加载器尚未注入则外挂模板失效）。

日志初始化处订阅 `cfgMgr.Watch`：

```go
cfgMgr.Watch(func(cfg *conf.Config) {
    log.SetLevel(slogLevel(cfg.LogLevel))
    if cfg.LogToFile && !log.GetInfo().FileOn {
        log.SetLogWriterByFile(cfg.LogFile)
        log.SetMaxSize(cfg.LogMaxSize)
    } else if cfg.LogToFile && log.GetInfo().FileOn {
        // 文件通道已开：同步 E2 上限热更（M4 修复——否则改 ROCKSYS_LOG_MAX_SIZE 静默无效）
        log.SetMaxSize(cfg.LogMaxSize)
    } else if !cfg.LogToFile && log.GetInfo().FileOn {
        log.SetFileWriter(false)
    }
})
```

> 幂等：`SetLevel` 值相同不触发钩子；`SetFileWriter` 已开则跳过；`SetMaxSize` 重复调用无害。防循环由 §2.2 保证。

> **时序说明（L8）**：watcher 回调先 `SetLogWriterByFile`（内部读旧包级 `maxSize`）后 `SetMaxSize`——若 `maxSize` 在两次调用间未变则一致；首次开启时存在一次瞬态不一致（用旧值开了文件，随后纠正），功能自愈，属已知时序假设。
> **路径变更不生效（L10）**：文件通道已开时修改 `ROCKSYS_LOG_FILE`，watcher 因 `FileOn==true` 不会重开文件，新路径静默无效。约定：路径变更需重启，或经 `POST /admin/log/output`（该端点显式先关后开，见 §3.2）用新路径重开。
> **路径读取时序（L7）**：`POST /admin/log/output` 用 `s.confMgr.Current().LogFile` 读路径。若刚编辑 .env 尚未被 watcher 重载（≤3s），`Current()` 仍是旧值——路径以 watcher 重载后的值为准，改动后建议等待一个轮询周期再操作。

### 2.6 P2 验收

- 改 `.env` 的 `ROCKSYS_LOG_LEVEL` → 3s 内生效。
- `PUT /admin/config` 带 `ROCKSYS_LOG_LEVEL` → 生效且写回 `.env`。
- 热更后 6s 内 `.env` mtime 不再变化（无自循环写盘）。
- 命令行 `--log-level` 启动时，热更写回后不被 watcher 覆盖。
- **并发触发用例（M2）**：并发跑 watcher 轮询（reloadFiles）与 `conf.Set`（POST 热更），`go test -race` 无竞争。

---

## 3. P3 — admin API + SSE

### 3.1 新增端点（`internal/adminapi`）

| 方法 | 路径 | Handler | 说明 |
|---|---|---|---|
| GET | `/admin/log/info` | `handleLogInfo` | 返回 `log.GetInfo()` |
| POST | `/admin/log/level` | `handleLogLevel` | `{"level":"debug"}` → `log.SetLevel` + `conf.Set` |
| POST | `/admin/log/output` | `handleLogOutput` | `{"file":true}` → `log.SetFileWriter` + `conf.Set` |
| GET | `/admin/log/tail` | `handleLogTail` | `?n=&since=` → `log.Tail` |
| GET | `/admin/log/stream` | `handleLogStream` | SSE 实时推送 |

**注册**（`registerBuiltin` 或 `New` 内）：

```go
check := s.requireAuth()
s.srv.AddHandler(http.MethodGet, "/admin/log/info", check(s.handleLogInfo))
s.srv.AddHandler(http.MethodPost, "/admin/log/level", check(s.handleLogLevel))
s.srv.AddHandler(http.MethodPost, "/admin/log/output", check(s.handleLogOutput))
s.srv.AddHandler(http.MethodGet, "/admin/log/tail", check(s.handleLogTail))
s.srv.AddHandler(http.MethodGet, "/admin/log/stream", check(s.handleLogStream))
```

### 3.2 Handler 原型

```go
// handleLogInfo 返回当前日志状态。
// ★ 用 writeJSON（writeJSON(ctx.Writer, v, code) 接受任意值），
//   ctx.Json 只接受 map[string]any，不能直接传 log.GetInfo()。
func (s *AdminServer) handleLogInfo(ctx httpsvr.Context) {
    _ = writeJSON(ctx.Writer, log.GetInfo(), http.StatusOK)
}

// handleLogLevel 热切级别并持久化。
func (s *AdminServer) handleLogLevel(ctx httpsvr.Context) {
    var body struct{ Level string `json:"level"` }
    if err := ctx.GetPostJson(&body); err != nil || body.Level == "" {
        _ = ctx.Json(map[string]any{"ok": false, "error": "invalid body"}, http.StatusBadRequest)
        return
    }
    // 校验级别合法（debug/info/warn/error）
    if !validLevel(body.Level) { ... 400 }
    log.SetLevel(slogLevel(body.Level))          // 生效（触发钩子→conf.Set→写盘）
    // ⚠️ M7（既定行为）：持久化经钩子异步 conf.Set，handler 无法直接拿到其错误。
    //   实现可选：SetOnLevelChange 回调收集最近一次持久化错误，本 handler 读取并返回 500；
    //   或文档明确「持久化失败静默」为既定行为。二者选一（推荐前者）。
    _ = ctx.Json(map[string]any{"ok": true}, http.StatusOK)
}

// handleLogOutput 热切文件通道并持久化。
// ★ 换路径重开（M2 约定）：文件已开时，setFileWriterUnlocked 会早退（已开 return nil），
//   不会用新路径重开。故开启时先 SetFileWriter(false) 关闭旧句柄，再 SetLogWriterByFile 用新路径开。
func (s *AdminServer) handleLogOutput(ctx httpsvr.Context) {
    var body struct{ File bool `json:"file"` }
    if err := ctx.GetPostJson(&body); err != nil { ... 400 }
    if body.File {
        // ⚠️ M6：**先打开新句柄成功，再关旧**——避免新路径打开失败时文件通道已关、失去存档。
        //   SetLogWriterByFile 内部先 newFileWriter（失败返回 err，旧句柄未动），成功后才 SetFile 替换。
        if _, err := log.SetLogWriterByFile(s.confMgr.Current().LogFile); err != nil {
            // 打开失败（路径不可写/父目录创建失败）→ 旧句柄仍在 → 返回 500 且不写 ROCKSYS_LOG_TO_FILE=true
            _ = ctx.Json(map[string]any{"ok": false, "error": "open log file: " + err.Error()}, http.StatusInternalServerError)
            return
        }
        log.SetMaxSize(s.confMgr.Current().LogMaxSize)
        if err := s.confMgr.Set("ROCKSYS_LOG_TO_FILE", "true"); err != nil {
            // ⚠️ L4：文件通道已生效但持久化失败 → 返回 500 告警（热更已生效，落盘失败需知晓）
            _ = ctx.Json(map[string]any{"ok": false, "error": "persist ROCKSYS_LOG_TO_FILE: " + err.Error()}, http.StatusInternalServerError)
            return
        }
    } else {
        log.SetFileWriter(false)
        if err := s.confMgr.Set("ROCKSYS_LOG_TO_FILE", "false"); err != nil {
            _ = ctx.Json(map[string]any{"ok": false, "error": "persist ROCKSYS_LOG_TO_FILE: " + err.Error()}, http.StatusInternalServerError)
            return
        }
    }
    _ = ctx.Json(map[string]any{"ok": true}, http.StatusOK)
}

// handleLogTail 增量读取 ring buffer。完整实现见 §3.4（含首次拉取 since 缺省契约）。
```

### 3.3 SSE 实时推送（`handleLogStream`）

```go
// handleLogStream SSE 实时推送：订阅 ring buffer，新日志即推。
func (s *AdminServer) handleLogStream(ctx httpsvr.Context) {
    w := ctx.Writer
    fl, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "streaming unsupported", http.StatusInternalServerError)
        return
    }
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.WriteHeader(http.StatusOK)
    fl.Flush()

    // 从最新开始（断线重连不续读历史）。
    // ★ 用 GetInfo().RingTotal 作为起始游标——Tail(since>=total) 返回 EOF，
    //    后续轮询只取新日志；切勿用 math.MaxInt64（恒 EOF 导致一行都不推）。
    since := log.GetInfo().RingTotal
    for {
        res := log.Tail(100, since)
        // ★ 无条件推进游标：Tail 在 since 已被覆盖时返回 Reset=true 且 Lines 为空，
        //    若只在 len(Lines)>0 时推进，8MB 溢出后将永远 Reset→空，SSE 永久停推。
        //    since = res.NextOffset 在 EOF 分支等价 no-op（NextOffset==since），不引入重复。
        since = res.NextOffset
        for _, line := range res.Lines {
            // ★ M3/L4：msg 含裸 \n/\r 时替换为空格，避免破坏 event-stream 帧格式
            line = strings.ReplaceAll(line, "\n", " ")
            line = strings.ReplaceAll(line, "\r", "")
            fmt.Fprintf(w, "data: %s\n\n", line)
        }
        if len(res.Lines) > 0 {
            fl.Flush()
        }
        select {
        case <-ctx.Request.Context().Done():
            return // 客户端断开，goroutine 退出，无泄漏
        case <-time.After(500 * time.Millisecond):
        }
        // ★ 心跳：无条件每 500ms 发送注释行 `: ping`（防止代理空闲断开；简单可靠，不判断是否有日志）
        fmt.Fprintf(w, ": ping\n\n")
        fl.Flush()
    }
}
```

> **SSE 优雅停机提示**：长期存活的 SSE 连接会阻塞 `http.Server.Shutdown`（main 中 30s 超时后强制关闭），属可接受运维行为，无需特殊处理。
> **msg 含换行限制（L5）**：若日志消息含裸 `\n`，ring 按行切分会把一条日志拆成多行（HTTP tail 语义变化）；SSE 的 `data: %s\n\n` 拼接含换行的 line 会破坏 event-stream 帧格式。约定：SSE 推送前把 line 中的 `\n` 替换为空格（或转义为 `\n` 字面量）；ring 侧保持原样（HTTP tail 的调用方按行语义自行理解）。

### 3.4 admin 层工具函数（本包内定义，勿用 main 包的 `slogLevel`）

```go
// slogLevel 将配置字符串映射为 slog.Level（debug/info/warn/error；未知默认 info）。
// ★ 必须在 adminapi 包内自行定义——cmd/rocksys/main 包的 slogLevel 不可见。
func slogLevel(s string) slog.Level {
    switch strings.ToLower(s) {
    case "debug":  return slog.LevelDebug
    case "warn", "warning": return slog.LevelWarn
    case "error":  return slog.LevelError
    default:       return slog.LevelInfo
    }
}

// validLevel 校验级别字符串合法性。
func validLevel(s string) bool {
    switch strings.ToLower(s) {
    case "debug", "info", "warn", "warning", "error":
        return true
    }
    return false
}

// parseN 解析 n 参数；非法回退 def；结果夹取到 [1,1000]（避免 ?n=0/-1 绕过上限拉到全量 8MB）。
func parseN(v string, def int) int {
    n := def
    if i, err := strconv.Atoi(v); err == nil {
        n = i
    }
    if n < 1 { n = 1 }
    if n > 1000 { n = 1000 }
    return n
}

// parseSince 解析 since 参数；缺省/非法/**任意负数**返回 -1 表示「尾部首拉」（首次请求取窗口尾部最后 n 行）。
// ⚠️ 中-4：`since=0` 是**合法增量游标**（从窗口起点读 n 行），返回的将是窗口**最旧** n 行而非尾部 n 行。
//    WebUI/AI 首次请求请**勿传 since=0**（缺省才走尾部首拉）。
// handler 内 since==-1 时直接透传给 log.Tail（Tail 内部处理尾部首拉）。
func parseSince(v string) int64
```

> **reset 后客户端协议**：Tail 返回 `reset=true` 时，`next_offset` 为当前 total（最新游标）。客户端收到 `reset=true` 后应**丢弃本次 lines，以缺省 since 重新尾部首拉**（即下一次请求不带 since），而非用 `next_offset` 续读（那会跳过当前窗口内容）。设计文档 §7.1 与本节共同构成此约定。

**`handleLogTail` 完整实现**（首次拉取契约）：

```go
func (s *AdminServer) handleLogTail(ctx httpsvr.Context) {
    n := parseN(ctx.Request.URL.Query().Get("n"), 100)
    since := parseSince(ctx.Request.URL.Query().Get("since"))
    // since==-1 透传给 Tail 做「尾部首拉」（取窗口尾部最后 n 行），
    // 保证首次 ?n=100 拉到的是尾部 100 行而非窗口最旧 100 行。
    res := log.Tail(int64(n), since)
    // ★ L6：Lines 为空时序列化为空数组而非 null（与设计示例 "lines":[...] 一致）
    lines := res.Lines
    if lines == nil {
        lines = []string{}
    }
    _ = writeJSON(ctx.Writer, map[string]any{
        "lines":       lines,
        "next_offset": res.NextOffset,
        "eof":         res.EOF,
        "reset":       res.Reset,
    }, http.StatusOK)
}
```

### 3.5 P3 验收

- `httptest` 覆盖：鉴权 401、`/level` 热改、`/output` 热改、`/tail` 增量与 reset、`/stream` SSE 推送。
- SSE 连接建立后持续收到新日志；客户端断开后 goroutine 退出（无泄漏）。

---

## 4. P4 — AI 集成验证

- HTTP tail 增量游标正确、覆盖 reset 恢复。
- SSE 实时推送、断线重连从最新开始。
- 端到端：改 `.env` → 级别/文件生效 → 重启保留。

---

## 5. 接口解耦（easyserver/log 不依赖 rocksys）

因 `easyserver/log` 是独立子仓库，不能 import `rocksys/internal/hotswap`。模板加载需**接口注入**：

```go
// log 包内定义
type TemplateLoader interface {
    GetScriptBytes(fpath string) ([]byte, error)
}

var tplLoader TemplateLoader

// SetTemplateLoader 注入模板加载器（rocksys 装配时传入 hotswap.ScriptDir 适配器）。
func SetTemplateLoader(l TemplateLoader) { tplLoader = l }
```

rocksys 侧适配：

```go
// rocksys 装配
// ⚠️ 直接传 NewScriptDir 构造的 *ScriptDir 值（不经 GetScriptDir 单例）——
//   GetScriptDir 是 sync.Once 单例，首个调用者锁定返回值，其他组件抢先调用会吞掉外挂模板。
log.SetTemplateLoader(hotswap.NewScriptDir(embedLogTplFS, "log")) // ScriptDir 已实现 GetScriptBytes
```

`newTemplateHandler` 优先用 `tplLoader` 加载 `log.tpl`，nil 时回退内嵌 `log.tpl`。

---

## 6. 风险与回滚

- **P1 回归**：默认路径零变化由 `TestCompatRegression` 保证；若破坏，回滚 `easyserver/log` 到基线。
- **SSE 连接泄漏**：必须监听 `ctx.Request.Context().Done()`，客户端断开即退出。
- **命令行覆盖**：`syncArgs` 必须在 `Set` 写盘前调用。
- **模板加载失败**：回退内嵌默认，不 panic。

---

## 7. 交付清单

- [x] `easyserver/log/ring.go`（RingBuffer + Tail + RingValidStart）
- [x] `easyserver/log/file.go`（异步 fileWriter：队列 + 后台落盘 goroutine + 丢弃计数 + TODO）
- [x] `easyserver/log/fanout.go`（fanoutWriter）
- [x] `easyserver/log/template.go`（templateHandler + TemplateLoader 接口 + 内嵌 log.tpl）
- [x] `easyserver/log/log.go` / `setter.go` 改造（新 API + ensureFanout）
- [x] `easyserver/log/*_test.go`（P1 单测）
- [x] `internal/conf/impl.go`（配置项 + 值比较 + syncArgs）
- [x] `cmd/rocksys/main.go`（装配 + watcher 联动）
- [x] `internal/adminapi/*`（5 端点 + SSE + 工具函数 slogLevel/validLevel/parseN/parseSince）
- [x] `docs/log-system.md` 同步（如实现偏离）——见 §8 实现状态与偏离记录；设计文档 §1-§11 与实现一致，未承诺 Go 类型名，无需改动

---

## 8. 实现状态与偏离记录

**状态：全部实现完成并通过验收（2026-08-07）。**

### 8.1 验收证据

| 阶段 | 验收 |
|---|---|
| P1 | `cd easyserver && CGO_ENABLED=1 go test -race ./log/...` 全绿（13 用例）；`go vet ./log/...` 无告警 |
| P2 | `CGO_ENABLED=1 go test -race ./internal/conf/...` 全绿（既有 6 + 新增 5）；`go build ./...` 通过 |
| P3 | `CGO_ENABLED=1 go test -race ./internal/adminapi/... ./cmd/rocksys/...` 全绿；`go vet` 无告警 |
| P4 | 主仓库 + easyserver 子仓库 `go test -race ./...` 全绿；真实二进制启动端到端验证：tail 尾部首拉/增量/reset、level 热改 DEBUG 生效并写回 `.env`、output 开启文件通道、SSE 心跳推送、**重启后级别与文件通道保留、启动日志异步落盘** |

### 8.2 实现偏离记录（相对本文档规格）

| # | 规格点 | 实现 | 原因与影响 |
|---|---|---|---|
| 1 | §1.5 `type Info struct` | 命名为 `LogInfo` | Go 包级命名唯一，包内已有日志函数 `func Info(...)`（15 处调用方依赖），二者不可共存。字段与 JSON tag 按规格原样保留（`level/template/file_on/...`），对外 HTTP/JSON 契约零变化。不改会编译失败。 |
| 2 | §3.2 `handleLogLevel` M7 二选一 | 取「持久化失败静默，返回 200」 | `SetOnLevelChange` 为单槽注册（main.go 装配注入持久化回调），adminapi 若叠加注册会顶掉持久化钩子；故不实现「收集最近一次持久化错误返回 500」。注释已标明。 |
| 3 | §1.2 `writeLoop` 伪代码 `if closed { return }` | 不做 closed 提前退出 | 伪代码该行会丢弃队列中已入队项，与「Close 排空后文件完整」语义及 §1.7 `TestFileAsync` 断言冲突。实现采用 channel 关闭 + range 排空全部已入队项后退出。 |
| 4 | §1.4 `buildLogger` 伪代码 `opts := &slog.HandlerOptions{...}` | 移除未使用的 opts 变量 | 伪代码中该变量在模板成功路径下未使用，直接编译失败（声明未使用）。模板路径本就无需 HandlerOptions（handler 自带级别过滤）；回退路径仍严格按「保存 lgLevel.Level() → NewOptions() → 恢复」处理级别重置。 |
| 5 | §1.1 尾部首拉扫描逻辑 | 修复空行死循环 | 按伪代码扫描，当 `\n` 恰在行右边界前一位（空行）时 `curEnd` 不前进造成死循环。改为「跳过当前行末尾 `\n` 再向前找行起点」，保证 `curEnd` 单调递减；空行（如 `"a\n\nb\n"`）作为完整行返回，与增量路径语义一致。 |

### 8.3 文档同步结论

- `docs/log-system.md`（设计文档）：§1-§11 与实现一致，未承诺 Go 类型名，**无需改动**。
- 本开发计划文档：交付清单勾选（§7），实现状态与偏离记录见本节。