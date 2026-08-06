# RockSys 开发手册（详细技术规格，供 AI 智能体对照实现）

> 依据：ARCHITECTURE.md v2（架构）· PROJECT_STRUCTURE.md v3（目录）
> 用法：按章顺序实现，每章独立可交付；每章末尾"验收标准"达标后再进入下一章。
> 附录 A/B 提供 easyserver/easyconf 的实际接口签名——实现时对照查阅，禁止凭空假设。

---

## 第 0 章 手册使用说明与全局约定

### 0.1 实现顺序（依赖驱动）

```
第1章(骨架) → 第2章(conf) → 第3章(engine) → 第4章(chain) → 第5章(dataflow)
→ 第6章(hotswap) → 第7章(rocksys入口) → 第8章(rockctl)
→ 第9-15章(P1挂件) → 第16-19章(P2组件) → 第20-22章(业务侧) → 第23章(验证)
```

- P0（第 1-8 章）完成后即交付"最小可用"：`rocksys --upstream 127.0.0.1:8080` 裸代理 + 热开关能力。
- P1（第 9-15 章）为标准形态；P2（第 16-19 章）按需开启，可后置或砍掉。
- ★ **编译依赖说明**：第 3 章 `engine.New(cfgMgr, ch *chain.Chain)` 和 `Forward(df *dataflow.DataFlow)` 引用了第 4/5 章才定义的类型。第 3 章实现时需**同时创建** `chain.Chain` 和 `dataflow.DataFlow` 的最小骨架类型（仅结构体+字段声明，不带方法），使 Go 编译器通过。完整方法在第 4/5 章定义后补全。这是一种常见的"预声明编译桩"模式——骨架定义 30 行即可，不影响独立可交付性。

### 0.2 全局约定

- **语言/环境**：Go ≥1.24.1（`easyserver/go.mod` 要求 go 1.24.1），Linux。业务侧 Python ≥3.10。
- **模块**：根模块名 `rocksys`。
  ```go
  // go.mod（根）
  module rocksys
  go 1.24.1
  require (
      github.com/iotames/easyserver v0.0.0
      github.com/iotames/easyconf v0.0.0
  )
  replace (
      github.com/iotames/easyserver => ./easyserver
      github.com/iotames/easyconf => ./easyconf
  )
  ```
- **错误处理**：所有公共方法返回 error；日志统一经 `easyserver/log`（slog 封装），结构化 key-value 输出（当前为 text 格式；如需 JSON 输出，作为地基库可选增强改造 `easyserver/log`。访问日志本身为 JSONL，见第 14 章）。
- **并发**：中间件链与组件注册表须并发安全（`sync.RWMutex` 或 `atomic.Value` 持有不可变快照）。
- **开关语义**：组件三态 `enabled / disabled / draining`；关闭=降级，不阻塞转发。
- **配置优先级**：命令行 > 环境变量 > 配置文件(.env) > 内置默认值。
- **测试**：每章附最小单测（`go test -count=1 ./...`），使用 `httptest.NewServer` 模拟上游。
- **文件命名**：每包按职责拆文件——`interface.go`（公开接口定义）、`impl.go`（默认实现）、`config.go`（配置结构体与默认值）、`xxx_test.go`（测试）。单文件不超过 500 行。
- ★ **包名冲突处理**：`easyserver/hotswap`（框架层脚本管理工具，附录 A.6）与 `internal/hotswap`（我们的热运维引擎，第 6 章）包名相同。在 `cmd/rocksys` 等同时 import 两者的包中，必须用 import alias 区分，约定：`"github.com/iotames/easyserver/hotswap"` 别名为 `eshs`（easyserver hotswap），`"rocksys/internal/hotswap"` 用原名 `hotswap`。其他包通常只 import 其一，无需别名。

### 0.3 包间导入规则（强制）

| 规则 | 说明 |
|------|------|
| `plugins/*` 仅允许 import `internal/chain`、`internal/dataflow`、`internal/hotswap`、`internal/conf`（接口与工具层） | internal 是框架私有层；挂件禁止 import 装配层（`internal/engine`、`internal/adminapi`） |
| `plugins/*` 实现 `internal/*` 中定义的 Go 接口 | 挂件通过接口注入框架，框架不 import 挂件 |
| ★ 链中间件挂件必须 import `internal/hotswap` | 所有实现 `hotswap.MiddlewareLifecycle` 的挂件（shield/dispatch/result/trace/auth/script/obs）均须依赖 hotswap |
| `internal/*` 可 import `easyserver`、`easyconf` | 地基库是公共依赖 |
| `cmd/rocksys` 是唯一装配点 | 唯一可以同时 import internal 和 plugins 的包 |
| `plugins/*` 之间禁止相互依赖 | 每个挂件独立，通过 dataflow 共享请求上下文 |

### 0.4 关键接口层次（与 easyserver 的关系）

```
请求 → *easyserver.Server（HTTP 监听 + DataFlow 工厂）
       │
       └→ head 中间件（MiddleHandle）
            │
            └→ ★ chain 适配器（见第 4 章）← 这是我们定义的
                 │
                 ├→ L1 防护 (plugins/shield)  ─┤
                 ├→ L2 路由 (plugins/dispatch)  ├─ 全部实现 chain.Middleware
                 └→ L3 结果 (plugins/result)   ─┘
       │
       └→ tail 中间件（预留，默认空）
```

- **easyserver 的 `MiddleHandle`** 和我们的 **`chain.Middleware`** 是两种不同接口。我们通过"chain 适配器"（一个实现 `MiddleHandle` 的结构体）来适配。
- **chain 三槽位执行时序**（第 4 章详述）：`Head`/`Middle` 在**转发前**按序执行（防护、路由）；`Tail` 在 **Forward 完成、响应写回客户端之前**执行（L3 结果处理、日志记录），通过 `chain.ResponseHook` 接入。
- 参考附录 A 查看 easyserver 的实际接口签名。

---

## 第 1 章 项目骨架与 go.mod

### 1.0 前置：地基库 easyserver 变更（第 3/7 章依赖，先做）

easyserver 当前**无优雅停机能力**：`EasyServer.httpServer` 为私有字段，全库无 `Shutdown`/`Close` 方法（`httpsvr/server.go`）。第 3 章 `Engine.Shutdown` 与第 7 章 main 停机流程依赖此能力，需先在 `easyserver/httpsvr` 新增：

```go
// Shutdown 优雅停机：停止接收新连接，等待在途请求完成
func (s *EasyServer) Shutdown(ctx context.Context) error // 委托 s.httpServer.Shutdown(ctx)

// Close 立即关闭：委托 s.httpServer.Close()
func (s *EasyServer) Close() error
```

实现后跑通 easyserver 既有测试（`go test ./...`），再进入本章骨架搭建。

- **职责**：建立模块结构、依赖关系、空目录。
- **操作**：
  1. 创建根 `go.mod`（内容见 §0.2 全局约定）。
  2. 创建 `easyserver/go.mod`（已有，确认 `module github.com/iotames/easyserver`）。
  3. 创建 `easyconf/go.mod`（已有，确认 `module github.com/iotames/easyconf`）。
  4. 创建全部空目录（按 PROJECT_STRUCTURE.md）：

```
cmd/rocksys/       cmd/rockctl/
internal/conf/     internal/engine/    internal/chain/
internal/dataflow/ internal/hotswap/   internal/adminapi/
plugins/shield/    plugins/dispatch/   plugins/result/
plugins/trace/     plugins/config/     plugins/obs/
plugins/script/    plugins/auth/       plugins/registry/
plugins/mq/        plugins/object/
sdk/python/        contracts/openapi/  contracts/proto/  examples/stbiz_hello/
docs/
```

  5. 每个空目录放一个 `doc.go`（包注释）防 `go build` 报"no Go files"。
- **验收**：
  ```bash
  go build ./...      # 无错误
  go vet ./...         # 无告警
  ls cmd/rocksys/main.go  # 空 main 函数占位
  ```

---

## 第 2 章 internal/conf（极简配置）

- **职责**：基于 easyconf 封装配置加载；提供热更轮询（easyconf 无 Watch，需自实现 mtime 轮询）。
- **地基库**：`github.com/iotames/easyconf`（参考附录 B）。

### 2.1 关键类型

```go
package conf

// Config 底座全部配置的只读载体
type Config struct {
    ListenAddr      string        // 监听地址，默认 ":8080"
    DefaultUpstream string        // 默认后端，默认 "http://127.0.0.1:8080"
    UpstreamTimeout time.Duration // 转发超时，默认 18s
    ConfigFile      string        // .env 配置文件路径，空=极简模式（只用环境变量+命令行）
    AdminAddr       string        // 管理接口监听地址，默认 "127.0.0.1:19527"
    LogLevel        string        // 日志级别，默认 "info"
}

// Manager 配置管理器（封装 easyconf.Conf + 热更轮询）
type Manager struct {
    cfg       atomic.Value  // 持有 *Config，并发安全读取
    ec        *easyconf.Conf
    watchers  []func(*Config)  // 热更订阅者
    args      []string      // ★ 保存 Load 时改写后的命令行参数（映射为 --ROCKSYS_* 注册名），
                            //   热更重放优先级（§2.4）与 Set 回放（§8.1）依赖它
    // ... 未导出字段
}
```

### 2.2 构造函数与核心方法

```go
// Load 从命令行/环境变量/.env文件加载配置
// 用法：mgr, err := conf.Load(os.Args[1:])
func Load(args []string) (*Manager, error)

// Current 返回当前只读配置（原子读取，无锁）
func (m *Manager) Current() *Config

// Watch 订阅配置变更；回调在独立 goroutine 执行
func (m *Manager) Watch(fn func(*Config))

// StartWatcher 启动配置文件 mtime 轮询。
// ★ 默认始终监听 ".env"（§2.4）；当 ConfigFile 非空时，额外监听该文件。
// 轮询间隔默认 3s；检测到 mtime 变更后重新加载 → 广播
func (m *Manager) StartWatcher() error

// Shutdown 停止热更轮询，阻塞直到后台 goroutine 退出
func (m *Manager) Shutdown(ctx context.Context) error

// Register 挂件配置项注册（委托 easyconf.addItem；name 即环境变量名）
// 挂件在构造时调用，注册自身配置项（如 SHIELD_RATE_LIMIT_RPS）。
// ★ 注册后必须触发一次"重载 + 广播"：easyconf 的 Parse 只执行一次，
//    main 中挂件注册晚于 conf.Load，若不重载，SHIELD_* 等项永远不会从
//    环境变量/.env 读入。内部实现：注册项 → SetValuesByEnv + SetValuesByEnvFile(".env")
//    + 命令行重放（复用 §2.3 重放逻辑）→ 重建 Config → atomic.Value.Store → 广播 watchers。
// ★ 重建 Config 要点：easyconf 绑定变量（包括固定字段和挂件注册项）在重载时已被自动写入，
//    因此重建 Config 就是重新从这些变量取值并组装：底座字段直接赋值，
//    UpstreamTimeout 重复秒→Duration 换算（与 §2.3 Load 相同逻辑）。
//    挂件注册项不回填 Config 结构体——它们由挂件自行持有指针引用读取（见下方"挂件读取"）。
//
// ★ 挂件如何读取自身配置：挂件在 Register 时传入的 pval 是挂件结构体内部字段的指针
//   （如 &s.RateLimitRPS），easyconf 写入 .env 或环境变量值时自动更新该指针指向的值。
//   挂件直接读自己的字段即可——无需通过 conf.Manager.Current() 读取（Config 不含挂件配置项）。
// ★ defval 类型分发：defval 统一为 string 入参（因 easyconf 无泛型 addItem），内部按 pval 实际类型分发：
//   *string → ec.StringVar(pval, name, defval, ...)
//   *int    → ec.IntVar(pval, name, strconv.Atoi(defval), ...)
//   *bool   → ec.BoolVar(pval, name, strconv.ParseBool(defval), ...)
//   其他类型 → 返回 error。实现参考：switch pval.(type) + strconv。
// ★ 实现提示：Register 内部重载逻辑与 §2.3 Load 的"加载→重放"流程高度相似。
//   建议 extract 公共方法 `reloadFromEnvFile(m *Manager)` 在 Load 和 Register 两处复用，
//   避免"重建 Config + Duration 换算"代码在两处散落。
func (m *Manager) Register(pval any, name, defval, title string, usage ...string) error

// Set 运行期按注册名全名设值并广播：写 easyconf → 重建 Config → atomic.Value.Store → 广播 watchers → 写回配置文件（UpdateFile）。
// ★ 第一原则「热更即持久化」：Set 必须「立即生效 + 持久化」——写回配置源文件（--config 存在时写 configFile，否则 .env），重启后保留；持久化失败返回 error（此时热更已生效，调用方需知晓持久化未落盘）。
// 供 PUT /admin/config（§8.1）与 registry→dispatch 联动（§17）使用。
func (m *Manager) Set(name, value string) error
```

### 2.3 配置项注册（内部调用 easyconf）

```go
// 命令行短参数名 → 注册名映射表
// ★ 必须在 Parse 前完成：flag 包遇到未注册参数会直接 os.Exit(2) 崩溃
var shortFlagMap = map[string]string{
    "--listen":    "--ROCKSYS_LISTEN",
    "--upstream":  "--ROCKSYS_UPSTREAM",
    "--timeout":   "--ROCKSYS_TIMEOUT",
    "--config":    "--ROCKSYS_CONFIG",
    "--admin":     "--ROCKSYS_ADMIN",
    "--log-level": "--ROCKSYS_LOG_LEVEL",
}

func Load(args []string) (*Manager, error) {
    // 0. ★ 短参数名映射（支持 "--listen=:9090" 与 "--listen :9090" 两种形态）。
    //    easyconf.Parse(true) 内部调用全局 flag.Parse()，解析的是 os.Args[1:]，
    //    而不是本函数的局部 args——因此必须把映射结果写回 os.Args（进程内唯一入口，安全）。
    //    ★ 不改写 os.Args 时，`--upstream` 等短名是"未注册 flag"，
    //      flag 包会打印 "flag provided but not defined" 并 os.Exit(2) 崩溃。
    args = mapShortFlags(args)
    os.Args = append([]string{os.Args[0]}, args...)

    ec := easyconf.NewConf(".env", "default.env")
    cfg := &Config{}

    // 注意：easyconf 注册名即环境变量名，不自动加前缀
    // ★ UpstreamTimeout 单位换算：easyconf.IntVar 只接受 *int，而 Config.UpstreamTimeout 是 time.Duration。
    //   必须以"秒"为单位的中间变量注册，Parse 完成后乘 time.Second 再赋值。
    //   禁止用 (*int)(&cfg.UpstreamTimeout) 强转后直接把秒数赋给 Duration——5 会被当成 5ns 而非 5s。
    var timeoutSec int
    ec.StringVar(&cfg.ListenAddr, "ROCKSYS_LISTEN", ":8080", "监听地址")
    ec.StringVar(&cfg.DefaultUpstream, "ROCKSYS_UPSTREAM", "http://127.0.0.1:8080", "默认后端")
    ec.IntVar(&timeoutSec, "ROCKSYS_TIMEOUT", 18, "转发超时(秒)")
    ec.StringVar(&cfg.ConfigFile, "ROCKSYS_CONFIG", "", "配置文件路径")
    ec.StringVar(&cfg.AdminAddr, "ROCKSYS_ADMIN", "127.0.0.1:19527", "管理接口地址")
    ec.StringVar(&cfg.LogLevel, "ROCKSYS_LOG_LEVEL", "info", "日志级别")

    // Parse(true) 启用 flag 解析 → 三级优先级：命令行 > 环境变量 > .env文件
    if err := ec.Parse(true); err != nil {
        return nil, err
    }

    // 1. ★ 指定 --config 时的优先级修补：
    //    ConfigFile 的路径只能 Parse 后才知道，此时直接 SetValuesByEnvFile 补读
    //    会覆盖命令行/环境变量已生效的值。按"命令行 > 环境变量 > ConfigFile > .env"重放修正：
    if cfg.ConfigFile != "" {
        if err := ec.SetValuesByEnvFile(cfg.ConfigFile); err != nil {
            return nil, err
        }
        // 重放环境变量（覆盖 ConfigFile 中的同名项）
        if err := ec.SetValuesByEnv(); err != nil {
            return nil, err
        }
        // 重放命令行值（覆盖环境变量）。禁止再次 flag.Parse（flag 重复注册会 panic），
        // 用 SetItemValue 直接写值；未注册 key 会被静默忽略，属预期
        for k, v := range parseArgsToMap(args) {
            _ = ec.SetItemValue(k, v)
        }
    }
    // ... 填充 cfg（此处执行单位换算）：
    //     cfg.UpstreamTimeout = time.Duration(timeoutSec) * time.Second
    //     其余字段直接赋值
    // 保存改写后的 args 到 Manager（热更重放依赖，见 §2.4）：
    // mgr := &Manager{ec: ec, args: args}
    // mgr.cfg.Store(cfg)
    // return mgr, nil
}

// mapShortFlags 将 --listen 等短名改写为 --ROCKSYS_* 注册名
func mapShortFlags(args []string) []string

// parseArgsToMap 解析 "--KEY value" / "--KEY=value" 为 map（键为注册名，不含 "--" 前缀）
func parseArgsToMap(args []string) map[string]string
```

### 2.4 热更轮询实现要点

- easyconf 无 Watch 机制，所以用 `os.Stat` 轮询配置文件的 `ModTime`。
- **监听文件**：默认监听 `.env`（`.env` 不存在时 easyconf 会自动创建，故始终存在）；`--config` 指定 ConfigFile 时**额外**监听该文件——两者都触发热更。
- 检测到变更后调用 `ec.SetValuesByEnvFile(变更文件)` 重新加载 → 构造新 `Config` → `atomic.Value.Store` → 逐个回调 `watchers`。
- ★ 与 §2.3 同理：重载后须重放环境变量与命令行值（`SetValuesByEnv()` + 用 `m.args`（§2.1，Load 时保存的改写后参数）重放 `SetItemValue`），保证"命令行 > 环境变量 > 配置文件"优先级在热更路径下不失效。
- 轮询间隔 3s，用 `time.Ticker` + `context.CancelFunc` 控制生命周期。

### 2.5 配置项速查表

| 命令行参数 | 环境变量 | 含义 | 默认 |
|---|---|---|---|
| `--listen` | `ROCKSYS_LISTEN` | 监听地址 | `:8080` |
| `--upstream` | `ROCKSYS_UPSTREAM` | 默认后端 | `http://127.0.0.1:8080` |
| `--timeout` | `ROCKSYS_TIMEOUT` | 转发超时(秒) | `18` |
| `--config` | `ROCKSYS_CONFIG` | .env 配置文件路径 | 空=极简模式 |
| `--admin` | `ROCKSYS_ADMIN` | 管理接口地址 | `127.0.0.1:19527` |
| `--log-level` | `ROCKSYS_LOG_LEVEL` | 日志级别 | `info` |

> **命名约定**：底座的 6 个配置项以 `ROCKSYS_` 为前缀（环境变量名 = 注册名），挂件配置项**不带前缀**（如 `SHIELD_RATE_LIMIT_RPS`、`DISPATCH_RULES`）。命名空间的划分是约定性的——easyconf 无 prefix 自动补全机制，全凭 Register 时传入的 name 决定。`/admin/config` 写入时使用注册名（即环境变量名），不可混用短命令行名。

### 2.6 边界

- `.env` 文件不存在时不报错（easyconf 会自动创建并写入默认值）。
- 热更不重建已建立的 TCP listener；`atomic.Value` 保证并发读无锁。

### 2.7 验收

```bash
# 命令行参数生效
go run ./cmd/rocksys --upstream http://127.0.0.1:9000
# → 日志输出 "config: upstream=http://127.0.0.1:9000"

# 环境变量生效
ROCKSYS_UPSTREAM=http://127.0.0.1:9001 go run ./cmd/rocksys
# → 日志输出 "config: upstream=http://127.0.0.1:9001"

# 配置文件热更（修改 .env 后 3s 内）
# → 日志输出 "config: hot-reload detected, new upstream=..."
```

---

## 第 3 章 internal/engine（反向代理引擎）

- **职责**：包装 easyserver，注入转发链；执行 HTTP 转发。**无任何业务逻辑。**
- **地基库**：`github.com/iotames/easyserver/httpsvr`（参考附录 A）。

### 3.1 关键类型

```go
package engine

// Engine 反向代理引擎，包装 *easyserver.Server（即 *httpsvr.EasyServer）
type Engine struct {
    server  *easyserver.Server      // 底层 HTTP 服务器（来自 easyserver.NewServer）
    chain   *chain.Chain            // 转发链
    conf    *conf.Manager           // 配置管理器
    pool    *UpstreamPool           // 上游连接池
}

// UpstreamPool 按 host 复用 HTTP transport 连接
type UpstreamPool struct {
    transport *http.Transport
    // MaxIdleConns: 100, MaxIdleConnsPerHost: 10, IdleConnTimeout: 90s
}
```

### 3.2 构造与启动

```go
// New 创建引擎：装配 easyserver + 注册 chain 适配器为 head 中间件
func New(cfgMgr *conf.Manager, c *chain.Chain) *Engine

// ListenAndServe 启动 HTTP 监听（委托给内部 *easyserver.Server）
func (e *Engine) ListenAndServe() error

// Shutdown 优雅停机：排空存量请求 → 摘除健康检查 → 关闭 listener
// ★ 依赖地基库变更：easyserver/httpsvr 需新增 Shutdown/Close（见 §1.0）
func (e *Engine) Shutdown(ctx context.Context) error
```

### 3.3 核心流程（与 easyserver 的集成）

```
1. Engine.New() 调用 easyserver.NewServer(cfg.ListenAddr)
2. 创建"chain 适配器"（实现 httpsvr.MiddleHandle 接口，见第 4 章）
3. 调用 server.AddMiddleHead(chainAdapter) 注册适配器
4. ListenAndServe() → server.ListenAndServe() 启动监听
```

easyserver 的 `ServeHTTP` 流程（框架自动执行）：
```
接收请求 → NewDataFlow() → head 中间件链（含 chain 适配器）
→ 路由中间件 → handler → tail 中间件 → 响应
```

chain 适配器在 head 阶段执行，完成后返回 `false` 中断 easyserver 的后续链——转发已由适配器内部完成。

### 3.4 Forward 方法

```go
// Forward 将请求转发到目标 upstream，保留 Method/Header/Body
// 自动追加 X-Forwarded-For、X-Trace-Id（始终从 DataFlow 读取，与 trace 挂件状态无关）
// target 格式：http://host:port
//
// ★ 时间戳取点责任划分：
//   - SetBeginBizAt：调用方（Adapter.Handler）在 Forward 调用前取点（见 §4.4）
//   - SetDoneBizAt：Forward 内部取点——收到上游响应（或判定 502/504 失败）后、
//     写回客户端之前调用 df.SetDoneBizAt(time.Now())。
//     原因：Adapter 位于 Forward 外部，无法插入"写回前"的取点；
//     DataFlow.SetDoneBizAt 仅写一次、重复调用忽略，天然防双写冲突。
func (e *Engine) Forward(w http.ResponseWriter, r *http.Request, target string, df *dataflow.DataFlow) error
```

- 超时时返回 `504 Gateway Timeout`。
- 上游不可达返回 `502 Bad Gateway`。
- 转发超时使用 `conf.UpstreamTimeout`，通过 `context.WithTimeout` 控制。
- WebSocket Upgrade 请求：走 `forwardWebSocket` 隧道分支（直连后端、原样转发握手；101 后劫持客户端连接双向字节对拷）。非 101 响应（后端拒绝升级）按普通响应透传。**w 必须支持 `http.Hijacker`**——ws 请求由 Adapter 绕过缓冲路径直写底层连接。
- **w 参数说明**：通常为客户端 ResponseWriter。当存在响应处理中间件（Tail 槽位实现 `chain.ResponseHook`）时，Adapter 会传入缓冲 Writer（§4.4 步骤 7a），Forward 无需感知、按普通 writer 写入即可；缓冲与回写由 Adapter 统一处理。
- 响应体默认不缓存、不解析、不修改（直接流式回传）；仅当 L3 result 等 `ResponseHook` 挂件开启时才进入缓冲路径（§4.6）。

### 3.5 验收

```bash
# 启动裸代理
go run ./cmd/rocksys --upstream http://127.0.0.1:9000

# 测试 GET
curl -v http://localhost:8080/api/test
# → 200, 响应体与上游一致

# 测试 WebSocket 隧道（需真实 ws 后端；示例为手动 101 的 echo 服务）
# 客户端 Upgrade 握手 → 101 → 双向字节透传（ws 帧原样）
# 后端拒绝升级（如返回 400）时，客户端收到透传的 400

# 测试超时（模拟 10s 慢上游）
# → 504 Gateway Timeout（5s 内返回）
```

---

## 第 4 章 internal/chain（转发链）

- **职责**：中间件链编排（head/middle/tail 三段），适配 easyserver 的 `MiddleHandle` 接口，支持运行时插拔。
- **依赖**：`dataflow`（§5）。

### 4.1 两种接口的关系

```
easyserver 的接口（已有，不变）：
  httpsvr.MiddleHandle{ Handler(w, r, dataFlow) (next bool) }

我们的接口（新定义）：
  chain.Middleware{ Name() string; Handle(ctx *Context) (next bool) }

适配器：
  chain.Adapter 实现 MiddleHandle，内部调用 chain.Middleware 列表
```

### 4.2 关键类型

```go
package chain

// Middleware 转发链中间件接口（我们的接口，非 easyserver 的 MiddleHandle）
type Middleware interface {
    Name() string
    Handle(ctx *Context) (next bool)  // false = 中断链，中间件已自行响应
    // ★ 约束：返回 true 的中间件禁止写入 ResponseWriter（Write/WriteHeader），
    // 否则后续链或 Adapter.Forward 写入时会 panic（http: superfluous response.WriteHeader call）
}

// Context 请求级上下文（在链上流转）
type Context struct {
    W  http.ResponseWriter
    R  *http.Request
    DF *dataflow.DataFlow  // ← 封装了 httpsvr.DataFlow（第 5 章）
    // 转发目标存在 DF.Target()/DF.SetTarget()，由 dispatch 写入、Adapter 读取

    // ★ 以下字段仅响应阶段（Tail 槽位，见 Slot 注释）有效，由 Adapter 填充：
    RespCode   int            // 上游响应状态码（无上游响应时为 0）
    RespHeader http.Header    // 上游响应头
    RespBody   []byte         // 上游响应体（仅存在 ResponseHook 时由 Adapter 缓冲，见 §4.4）
    RespW      http.ResponseWriter // 中间件写入目标：默认 = W（客户端）；响应头须在 WriteHeader 前设置
    done       bool           // 内部标记：是否已有 Tail 中间件写入最终响应（通过 WriteFinal 置位）
}

// Slot 枚举
// 执行时序：转发前依次执行 Head → Middle；转发完成后执行 Tail（响应处理阶段）。
// ★ 关键：Head/Middle 是"转发前"中间件；Tail 是"转发后"中间件（响应处理），
//   不要把它们当成同一时序的连续槽位——Tail 依赖上游响应已到达。
type Slot int
const (
    Head   Slot = iota  // 转发前最先执行（防护/认证，如 shield、auth）
    Middle              // 转发前执行（路由分发等，如 dispatch、script）
    Tail                // ★ 转发完成后执行（响应处理，如 result、obs）
)

// ResponseHook 可选接口：实现此接口的中间件必须挂 Tail 槽位，
// 在 Forward 完成（收到上游响应并写入缓冲）后、写回客户端之前被 Adapter 调用。
// 用于：L3 result（脱敏/统一封装响应）、obs（记录访问日志）。
type ResponseHook interface {
    // OnResponse 转发完成后执行；ctx.RespCode/RespHeader/RespBody 为上游响应。
    // 需要改写响应时：调用 ctx.WriteFinal(code int, header http.Header, body []byte)；
    // 仅读取（如 obs 记录日志）则无需写响应。
    // 返回 err 仅记录告警，不中断后续 hook。
    OnResponse(ctx *Context) error
}

// Chain 中间件链
type Chain struct {
    segments [3][]Middleware  // 索引 = Slot，不可变快照
    mu       sync.RWMutex
}
```

### 4.3 方法

```go
// New 创建空转发链
func New() *Chain

// Add 添加中间件到指定槽位末尾
func (c *Chain) Add(slot Slot, m Middleware)

// Remove 按名称移除
func (c *Chain) Remove(name string) error

// Replace 原子替换整个槽位（用于热切换）
// newList 可以为 nil（等效清空该槽位）
func (c *Chain) Replace(slot Slot, newList []Middleware)

// Execute 执行转发前链（仅 Head → Middle，不执行 Tail）。
// 任一返回 false 则中断（中间件已自行响应）；全部返回 true 则 engine 执行 Forward。
// ★ Tail 不在此执行——它在 Forward 完成后由 Adapter 调用 ResponseHooks（见 §4.4）。
func (c *Chain) Execute(ctx *Context) (shouldForward bool)

// HasResponseHook 判断指定槽位是否存在实现了 ResponseHook 的中间件。
// Adapter 据此决定是否缓冲上游响应体（§4.4 步骤 7a）。
func (c *Chain) HasResponseHook(slot Slot) bool

// ResponseHooks 返回指定槽位中实现了 ResponseHook 的中间件。
// ★ 返回顺序 = 注册顺序的**逆序**（后注册的在切片前面），因此调用方用正向 for range 遍历即为逆序执行。
// Adapter 在 Forward 完成后逆序调用其 OnResponse（result 先处理、obs 后记录）。
func (c *Chain) ResponseHooks(slot Slot) []ResponseHook

// WriteFinal 由 Tail 中间件调用：写入最终响应并置 done=true。
// 若已有中间件写过（done=true）则返回 error；响应头须在调用前设置完。
func (c *Context) WriteFinal(code int, header http.Header, body []byte) error

// ActiveCount 返回当前活跃请求数（供 hotswap 排空轮询，见 §6.3）
func (a *Adapter) ActiveCount() int64
```

### 4.4 Adapter（适配 easyserver）—— ★ 转发链唯一入口

```go
// Adapter 实现 httpsvr.MiddleHandle 接口
// 负责将 easyserver 的 DataFlow 包装为 dataflow.DataFlow，然后执行 Chain
// 是 easyserver 进入 rocksys 转发链的唯一入口
type Adapter struct {
    chain           *Chain
    defaultUpstream string
    forward         func(w http.ResponseWriter, r *http.Request, target string, df *dataflow.DataFlow) error
    activeCount     atomic.Int64  // ★ 活跃请求计数：Handler 入口 +1、出口 -1，hotswap 排空依赖此计数（§6.3）
}

// NewAdapter 创建适配器
func NewAdapter(ch *Chain, defaultUpstream string, forward func(http.ResponseWriter, *http.Request, string, *dataflow.DataFlow) error) *Adapter

// Handler 实现 httpsvr.MiddleHandle 接口
// easyserver 每次请求调用此方法 — 这是 rocksys 处理请求的唯一入口
func (a *Adapter) Handler(w http.ResponseWriter, r *http.Request, innerDF *httpsvr.DataFlow) (next bool) {
    // 0. ★ 活跃请求计数 +1（hotswap 排空依赖，见 §6.3）
    a.activeCount.Add(1)
    defer a.activeCount.Add(-1)

    // 1. 包装 easyserver DataFlow → rocksys DataFlow（BeginAt 已由 easyserver 自动记录）
    df := dataflow.New(innerDF, r)

    // 2. 创建链上下文（Tail 响应阶段字段由 Adapter 在步骤 6-7 填充）
    ctx := &Context{W: w, R: r, DF: df, RespW: w}

    // 3. 执行转发前链（Head → Middle；Tail 不在本阶段执行）
    shouldForward := a.chain.Execute(ctx)

    // 4. 链中断 → 中间件已自行写入响应，直接返回
    if !shouldForward {
        return false
    }

    // 5. 确定转发目标（从 DataFlow 读取，dispatch 中间件负责写入）
    target := df.Target()
    if target == "" {
        target = a.defaultUpstream
    }

    // 6. ★ 转发前一刻取点（必须在 Forward 前，禁止 defer）
    df.SetBeginBizAt(time.Now())

    // 7. 执行转发
    // ★ DoneBizAt 由 Forward 内部在"收到上游响应后、写回客户端前"取点（见 §3.4/§5.3），
    //    Adapter 不负责、也不再调用 SetDoneBizAt
    var bufW *respBufferWriter
    if a.chain.HasResponseHook(chain.Tail) {
        // 7a. 存在响应处理中间件 → 缓冲上游响应，供 Tail 阶段读取/改写
        bufW = newRespBufferWriter(w)
        err := a.forward(bufW, r, target, df)
        if err != nil {
            // forward 内部已写入 502/504 错误响应（此时 bufW 中为错误响应，同样进入响应阶段）
        }
        ctx.RespCode, ctx.RespHeader, ctx.RespBody = bufW.Status(), bufW.Header(), bufW.Body()
    } else {
        // 7b. 无响应处理中间件 → 直接流式写回客户端，不缓冲、不增加内存开销
        err := a.forward(w, r, target, df)
        if err != nil {
            // forward 内部已写入 502/504 错误响应
        }
    }

    // 8. ★ 响应处理阶段：执行 Tail 槽位中间件。
    //    ResponseHooks 返回逆序切片（后注册在前），正向 for range 即为逆序执行。
    //    装配时先注册 obs、后注册 result → result 先执行（改写响应）、obs 后执行（记录最终状态）。
    for _, h := range a.chain.ResponseHooks(chain.Tail) {
        if err := h.OnResponse(ctx); err != nil {
            log.Warn("response hook error", "name", hookName(h), "err", err)
            // ★ hookName(h) 实现：h 实际实现 chain.Middleware（Name()），
            //   通过类型断言 h.(chain.Middleware).Name() 获取；断言失败回退 "unknown"
        }
    }

    // 9. 若缓冲未被任何 Tail 中间件消费（ctx.done == false），把缓冲内容写回客户端
    if bufW != nil && !ctx.done {
        copyHeader(w.Header(), ctx.RespHeader)
        w.WriteHeader(ctx.RespCode)
        w.Write(ctx.RespBody)
    }

    // 10. 始终返回 false — 转发已完成，easyserver 后续链不再执行
    return false
}
```

> 上文中 `respBufferWriter` / `newRespBufferWriter` / `copyHeader` / `hookName` 为**示意性 helper**，命名与实现可自由调整，但必须满足以下规格：
>
> **respBufferWriter 实现规格**（实现 `http.ResponseWriter`）：
> - **Header()**：返回可修改的 `http.Header` 对象，`WriteHeader` 调用后仍可读取（与 `httptest.ResponseRecorder` 行为一致）。
> - **WriteHeader(code int)**：记录状态码。**重复调用忽略**（不 panic，不覆盖首次记录的 code），与 `http.ResponseWriter` 的"superfluous"行为不同，此处为防御性设计——Forward 内部可能多次尝试写响应头。
> - **Write(data []byte)**：2 阶段——
>   1. 缓冲区未满（≤ 4MB）：追加到内部 buffer。
>   2. 缓冲区满：**停止缓冲**，后续数据直写底层 `http.ResponseWriter`（不再追加到 buffer），同时设置截断标记（`ctx.RespBody` 将被标记为截断状态）。
> - **可选接口**：不实现 `http.Hijacker`、`http.Flusher`（缓冲路径仅服务普通 HTTP 转发；WebSocket 隧道请求由 Adapter 绕过缓冲、直写底层连接以支持 Hijack）。
> - **Status() / Header() / Body()**：供 Adapter 在 Forward 完成后读取缓冲结果。
>
> **Forward 失败时的缓冲状态契约**：
> - Forward 在判定 502/504/其他不可恢复错误后，**先通过 `w.WriteHeader` + `w.Write` 写入完整错误响应**，再返回 error。
> - 因此当 §4.4 步骤 7a 中 Forward 返回 error 时，`bufW` 已包含完整 HTTP 响应（状态码 + 头 + 体），`ctx.RespCode/RespHeader/RespBody` 可直接读取，Tail 中间件可正常处理（包括改写）。
> - 这与 §3.4 Forward 注释中的语义一致："Forward 内部在收到上游响应（或判定 502/504 失败）后、写回客户端之前取点"——失败路径同样走完整写入流程。
>
> ★ **Tail 执行顺序与缓冲上限**：
> - 执行顺序 = 注册顺序的**逆序**（后注册的先执行）。装配时务必**先注册 obs、后注册 result**（result 后注册 → 先执行 → 改写响应；obs 先注册 → 后执行 → 记录最终状态）。若顺序颠倒，obs 会记录到未脱敏的原始响应。
> - `respBufferWriter` 应设**缓冲上限**（建议 4MB）：超出上限时停止缓冲并直写客户端，`ctx.RespBody` 标记截断，防止大响应体撑爆内存（小响应场景不影响）。

### 4.5 Target 决策规则

| 情况 | Target 值 | 行为 |
|------|----------|------|
| chain 为空（或中间件全返回 true 且未设 Target） | 空字符串 | engine.Forward 使用 `conf.DefaultUpstream` |
| dispatch 中间件命中路由 | `http://service:port` | engine.Forward 使用该值 |
| dispatch 中间件未命中 | 空字符串 | engine.Forward 使用默认值 |

### 4.6 边界

- `Replace` 原子替换：构造新切片 → 持写锁整体替换 `segments[slot]`（§4.2 的 `mu`）→ 在途请求（Execute 持读锁遍历旧切片）继续使用旧快照。
  ⚠ Tail 阶段 `ResponseHooks` 在 Forward 完成后才获取，极端并发下可能与转发前的 Head/Middle 属不同代快照；对 result/obs 无实际危害（Tail 只读响应与 DataFlow），不必为此加同步。
- 中间件禁止跨请求共享可变状态（状态放 DataFlow）。
- ★ **中间件执行超时**：chain 不做 goroutine 超时（goroutine 无法被强制终止，会泄漏且继续执行副作用）。需要超时保护的中间件自行实现——如 script 执行 Lua 时用 `lua.ContextDeadline` / `context.WithTimeout` 内部控制（默认 100ms，见 §15）。
- **响应缓冲**：仅当 Tail 槽位存在 `ResponseHook` 中间件时，Adapter 才缓冲上游响应体（§4.4 步骤 7a）。无 `ResponseHook` 时直接流式写回，零缓冲开销。
- Tail 槽位中间件不执行 `chain.Execute`（它只在 Forward 后以 `OnResponse` 形式被调用）；因此挂 Tail 的中间件应实现 `ResponseHook`，其 `chain.Middleware.Handle` 可返回 `false` 占位（不参与转发前逻辑）。
- 链为空时：Adapter 直接调用 Forward → 等价裸反向代理。

### 4.7 验收

```bash
# 单元测试
go test -count=1 ./internal/chain/...

# 场景1：空链 → 请求直通默认 upstream
# 场景2：添加一个返回 false 的中间件 → 请求被拦截，返回自定义 403
# 场景3：Replace 替换中间件 → 在途请求不受影响
# 场景4：挂一个实现 ResponseHook 的 Tail 中间件 → Forward 后 OnResponse 被调用、
#         ctx.RespBody 为上游响应体；未调 WriteFinal 时缓冲内容原样回写客户端
```

---

## 第 5 章 internal/dataflow（请求级数据流）

- **职责**：包装 `httpsvr.DataFlow`，提供强类型的 rocksys 专有字段（三时间戳、trace_id、租户、Target）。
- **地基库**：`github.com/iotames/easyserver/httpsvr`（DataFlow 已提供 `SetData/GetData/SetDataReadonly`）。

### 5.1 关键类型

```go
package dataflow

// DataFlow 包装 httpsvr.DataFlow，添加 rocksys 专有字段
type DataFlow struct {
    inner  *httpsvr.DataFlow  // easyserver 原生 DataFlow（已含 GetStartAt、SetData 等）

    // 以下字段存储在 inner 的 KV 中（使用 SetData/GetData）：
    // key: "rocksys:trace_id"   → TraceID
    // key: "rocksys:begin_biz"  → BeginBizAt
    // key: "rocksys:done_biz"   → DoneBizAt
    // key: "rocksys:tenant_id"  → TenantID
    // key: "rocksys:target"     → Target
}
```

### 5.2 方法

```go
// New 包装已有的 httpsvr.DataFlow
// r 为当前请求：TraceID() 入口需从 X-Trace-Id 请求头读取（r 不可为 nil）
func New(inner *httpsvr.DataFlow, r *http.Request) *DataFlow

// 三时间戳（BeginAt 直接读取 inner.GetStartAt()）
func (df *DataFlow) BeginAt() time.Time          // = inner.GetStartAt()
func (df *DataFlow) SetBeginBizAt(t time.Time)   // 仅写一次，重复调用忽略
func (df *DataFlow) BeginBizAt() time.Time
func (df *DataFlow) SetDoneBizAt(t time.Time)
func (df *DataFlow) DoneBizAt() time.Time

// TraceID：优先返回已设置的 TraceID；否则从请求头 X-Trace-Id 读取；
// 仍无则生成 32 位 hex 并缓存。全程幂等——同一请求内多次调用返回同一值。
func (df *DataFlow) TraceID() string
func (df *DataFlow) SetTraceID(id string)

// TenantID：由 auth 挂件设置
func (df *DataFlow) TenantID() string
func (df *DataFlow) SetTenantID(id string)

// Target：由 dispatch 挂件设置
func (df *DataFlow) Target() string
func (df *DataFlow) SetTarget(t string)

// 通用 KV（穿透到 inner.SetData/GetData）
func (df *DataFlow) Set(key string, val any)
func (df *DataFlow) Get(key string) (any, bool)

// 耗时分解
func (df *DataFlow) ShieldMs() int64   // BeginBizAt - BeginAt
func (df *DataFlow) BizMs() int64      // DoneBizAt - BeginBizAt
func (df *DataFlow) TotalMs() int64    // DoneBizAt - BeginAt
```

> ★ **"仅写一次、重复调用忽略"的实现规则**：`inner.SetData` 对可重写 key 是**覆盖**语义（见 easyserver dataflow.go），因此 `SetBeginBizAt/SetDoneBizAt` 必须先 `inner.GetData(key)` 判断该 key 是否已存在，存在则直接返回、不存在才 `SetData`——否则多次调用会覆盖取点，破坏 §5.4 的精度验收。其余写方法（SetTraceID/SetTenantID/SetTarget/Set）同理，遵循"可覆盖，但不允许破坏写一次语义"即可。

### 5.3 时间戳取点位置（严格遵守）

```
engine 收到请求 → inner.GetStartAt() 已有（easyserver 自动记录）
                → 此即 BeginAt

chain.Execute 完成后 → Forward 调用前 → Adapter 显式调用 SetBeginBizAt(time.Now())
  ↑ 必须在此精确位置取点，禁止 defer、禁止在连接池获取后

Forward 内部：收到上游响应（或判定 502/504 失败）后 → 写回客户端前
  → 显式调用 SetDoneBizAt(time.Now())
  ↑ 必须在写回客户端之前取点；成功与失败路径统一在此取点
```

### 5.4 验收

```bash
go test -count=1 ./internal/dataflow/...

# 测试：耗时分解精度：ShieldMs + BizMs ≈ TotalMs（误差 < 1ms）
# 测试：SetBeginBizAt 重复调用被忽略
# 测试：TraceID 为空时自动生成 32 位 hex
```

---

## 第 6 章 internal/hotswap（生产热运维引擎）★亮点

- **职责**：管理两类可切换实体——**独立组件**与**链中间件**。提供注册表、在线启停、原子切换、排空、审计、回滚。
- **依赖**：`conf`。

### 6.1 两类热切换实体（核心区分）

| 实体类型 | 实体 | 管理方式 | 切换路径 |
|---------|------|---------|---------|
| **链中间件**（挂在 chain 槽位，实现 MiddlewareLifecycle） | shield / dispatch / result / trace / auth / script / obs | `RegisterMiddleware` | Enable：`Start(cfg)` + `chain.Replace(slot, [实例])` 挂载；Disable：`chain.Replace(slot, nil)` 摘除 + `Stop()`；热更：`Start(newCfg)` 重载实例内部原子快照 |
| **独立组件**（自管理生命周期，实现 Component） | config / registry / mq / object | `RegisterComponent` | Enable/Disable → `Component.Start/Stop`，不挂 chain |

> ★ 判定依据：凡需要**在每个请求/响应上做事的实体**（防护/路由/结果/日志/鉴权/脚本）必须是**链中间件**；
> 只有**不参与单请求处理**的后台服务（KV 配置、注册中心、消息、对象存储）才是**独立组件**。

### 6.2 关键类型

```go
package hotswap

type State int
const (
    StateDisabled State = iota
    StateEnabled
    StateDraining  // 排空中：不再接受新请求，等待存量完成
)

// Component 独立组件接口（config/registry/mq/object 实现此接口）
type Component interface {
    Name() string
    Start(cfg any) error
    Stop() error
    State() State
}

// MiddlewareLifecycle 链中间件接口（shield/dispatch/result/trace/auth/script/obs 实现此接口）
// 中间件实现 chain.Middleware + 此接口即可被 hotswap 管理生命周期
type MiddlewareLifecycle interface {
    chain.Middleware
    // Start 用新配置重新初始化【本实例】（挂件实例可复用，不存在"新/旧实例"两套）。
    // 内部必须用不可变快照承载运行状态（如 dispatch 的 RouteTable 整体重建后 atomic 替换），
    // 保证 Start 与在途请求的 Handle 并发安全（§6.3）。
    Start(cfg any) error
    // Stop 清理资源（如关闭文件、清空连接池）
    Stop() error
    // Slot 返回中间件挂载位置（chain.Head / chain.Middle / chain.Tail）
    Slot() chain.Slot
}

// ★ 注意：MiddlewareLifecycle 不包含 State() 方法——中间件的 Enabled/Disabled 状态由
// Manager 内部通过 `map[name]State` 统一追踪（与 Component.State() 不同，
// Component 自行持有状态、Manager 直接读取）。Manager 的 Enable/Disable 操作同步此簿记map。
// 中间件不需要也不应该自行暴露 State()。

// Manager 统一管理所有可切换实体
type Manager struct {
    chain       *chain.Chain       // 用于中间件的热替换
    confMgr     *conf.Manager      // 订阅配置热更
    components  map[string]Component
    middlewares map[string]MiddlewareLifecycle
    mu          sync.RWMutex
}

// NewManager 创建热运维管理器
func NewManager(ch *chain.Chain, cfgMgr *conf.Manager) *Manager

// RegisterComponent 注册独立组件
func (m *Manager) RegisterComponent(c Component)

// RegisterMiddleware 注册链中间件（注册进 hotswap 管理，默认 Disabled，不自动挂载；
// 由 Enable 触发 Start + chain.Replace 挂载，见 §6.3 流程 A）
func (m *Manager) RegisterMiddleware(ml MiddlewareLifecycle)

// Enable / Disable 切换（适用两种实体）
func (m *Manager) Enable(name string) error
func (m *Manager) Disable(name string) error

// GetMiddleware / GetComponent 按名称获取已注册实例。
// ★ 供 admin handler 操作特定实例使用（如 script publish 需拿到 *script.Engine 执行编译），
//   避免在装配代码中手动传递引用链条。返回 nil 表示未注册。
func (m *Manager) GetMiddleware(name string) MiddlewareLifecycle
func (m *Manager) GetComponent(name string) Component

// List 列出所有实体状态
func (m *Manager) List() []Status

// Shutdown 排空并停止所有已启用的组件和中间件
// 调用顺序：先停中间件（从 Tail 到 Head），再停独立组件
func (m *Manager) Shutdown(ctx context.Context) error

// Status 实体状态
type Status struct {
    Name         string
    Kind         string  // "component" | "middleware"
    State        State
    StartedAt    time.Time
    LastSwitchAt time.Time
    Message      string
}
```

### 6.3 热切换流程（三种操作语义）

所有操作统一约定：
- **原子快照**：链中间件运行态必须存于实例内部的不可变快照（`atomic.Value` / 不可变结构体），`Handle` 每次读取当前快照。`Start(newCfg)` 只整体重建并替换快照，**绝不原地修改共享状态**——保证与在途请求的 `Handle` 并发安全。
- **排空判定**：Adapter 维护活跃请求计数（`Adapter.ActiveCount()`，请求进入 Handler `+1`、返回前 `-1`，见 §4.4）。hotswap 排空时轮询 `Adapter.ActiveCount() == 0`（上限 10s，超时强制推进并记录告警）。
- **Start(cfg) 的 cfg 来源**：各实体在构造时已持有 `*conf.Manager` 引用。`Start(cfg)` 中的 `cfg any` 按约定传 `nil`——实体内部自行从 `conf.Manager.Current()` 或自身注册的配置指针读取最新配置后重建快照。Manager 不负责为每个实体"构造特定类型的配置结构体"。

**A. Enable（开启/挂载）**
1. 收到开启指令（rockctl / admin API / 配置热更事件）。
2. 实例 `Start(cfg)` 初始化（构造运行态快照）。
3. Start 成功 → 链中间件：`chain.Add(slot, 实例)` 追加到槽位（不影响同槽位其他中间件）；组件：置 `Enabled`。
4. Start 失败 → 保持 `Disabled`，记录故障 + 告警（不中断服务）。

**B. Disable（关闭/摘除）——统一语义：从链上摘除，绝不"保持挂载但放行"**
1. 收到关闭指令。
2. 链中间件：`chain.Remove(name)` 仅移除目标中间件（在途请求持旧快照继续；同槽位其他中间件不受影响）；组件：置 `Draining` 等待自身业务排空。
3. 排空完成（活跃请求计数归零，上限 10s）→ 调用 `Stop()` 清理资源 → 置 `Disabled`。
4. 写审计日志（动作/实体/结果/时间）。

**C. 热更（配置变更，实例不摘除）**
1. 收到配置热更事件（Manager 订阅 `confMgr.Watch`）。
2. 判定本实体受影响的配置项是否变化 → 是则继续。
3. 构造新配置 → 调用实例 `Start(newCfg)`（内部整体重建快照并原子替换）。
4. Start 成功 → 完成（实例仍在链上，新快照对后续请求生效，无需 chain.Replace）。
5. Start 失败 → 保留旧快照（实例继续以旧配置服务），记录故障 + 告警。
6. 写审计日志。

### 6.4 配置热更

- Manager 在初始化时订阅 `confMgr.Watch(func(newCfg *Config))`。
- 收到变更后：逐实体检查其订阅的配置项是否变化 → 命中则走 §6.3 流程 **C（热更）**：构造新配置 → `Start(newCfg)` 替换内部快照。失败回退旧快照。
- 挂件通过 `conf.Manager.Watch` 自己订阅受影响的配置项（如 dispatch 订阅 `DISPATCH_RULES`），Manager 只负责转发广播。
- ★ **状态过滤**：hotswap 仅对当前 `State == StateEnabled` 的实体走流程 C（热更）。`StateDisabled` 的实体——即使其配置项已在 conf 中注册——不响应配置热更事件（其配置变更将在下次 `Enable` 时通过 `Start(cfg)` 首次生效）。此过滤避免"未启用的挂件因热更被误唤醒"以及"注册即触发误操作"（§2.2 Register 的重载广播对 Disabled 实体无副作用）。

### 6.5 验收

```bash
# 注册 shield 中间件后开启
curl -X POST http://127.0.0.1:19527/admin/switch/on  -d '{"name":"shield"}'
# → 200, shield 开始拦截

# 关闭 shield
curl -X POST http://127.0.0.1:19527/admin/switch/off -d '{"name":"shield"}'
# → 200, 请求直通

# 查看状态
curl http://127.0.0.1:19527/admin/switch/list
# → [{"name":"shield","kind":"middleware","state":"disabled",...}]
```

---

## 第 7 章 cmd/rocksys（底座唯一入口）

- **职责**：装配全部组件并启动。**唯一可以同时 import `internal/*` 和 `plugins/*` 的包。**

### 7.1 main 函数骨架

```go
package main

import (
    "context"
    "log/slog"
    "os"
    "os/signal"
    "strings"
    "syscall"
    "time"

    "rocksys/internal/conf"
    "rocksys/internal/chain"
    "rocksys/internal/engine"
    "rocksys/internal/hotswap"

    "github.com/iotames/easyserver/log"
)

func main() {
    // 1. 加载配置
    cfgMgr, err := conf.Load(os.Args[1:])
    if err != nil {
        panic(err)
    }
    log.SetLevel(slogLevel(cfgMgr.Current().LogLevel))
    log.Info("rocksys starting", "upstream", cfgMgr.Current().DefaultUpstream)

    // 2. 创建转发链（初始为空）
    ch := chain.New()

    // 3. 创建引擎（内部调用 easyserver.NewServer + AddMiddleHead 注册 chain 适配器）
    eng := engine.New(cfgMgr, ch)

    // 4. 创建 hotswap 管理器（订阅配置热更）
    mgr := hotswap.NewManager(ch, cfgMgr)

    // 5. 注册挂件（链中间件用 RegisterMiddleware，独立组件用 RegisterComponent；见 §6.1）
    // mgr.RegisterMiddleware(shield.New(cfgMgr))   // L1 防护 → chain.Head
    // mgr.RegisterMiddleware(dispatch.New(cfgMgr)) // L2 路由 → chain.Middle
    // mgr.RegisterMiddleware(result.New(cfgMgr))   // L3 结果 → chain.Tail(+ResponseHook)
    // mgr.RegisterMiddleware(trace.New(cfgMgr))    // trace 透传 → chain.Head
    // mgr.RegisterMiddleware(script.New(cfgMgr))   // Lua 策略 → chain.Middle
    // mgr.RegisterMiddleware(obs.New(cfgMgr))      // 访问日志/指标 → chain.Tail(+ResponseHook)
    // mgr.RegisterComponent(config.New(cfgMgr))    // KV 配置服务 → 独立组件
    // ... 随 P1/P2 逐步注册（auth → Head；registry/mq/object → 独立组件）

    // 5a. 启动 admin API（独立 listener，回环地址，见第 8 章）
    // adminSrv := adminapi.New(cfgMgr.Current().AdminAddr, cfgMgr, mgr)
    // go adminSrv.ListenAndServe()

    // 6. 启动配置热更监听（★ 始终启动：默认监听 .env；--config 指定时额外监听该文件，见 §2.4）
    if err := cfgMgr.StartWatcher(); err != nil {
        log.Error("start config watcher", "err", err)
    }

    // 7. 启动 HTTP 监听
    go func() {
        if err := eng.ListenAndServe(); err != nil {
            log.Error("server error", "err", err)
        }
    }()

    // 8. 等待信号，优雅停机
    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
    <-quit
    log.Info("rocksys shutting down...")
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    // 8a. 停止接收新请求（主代理 + admin listener）
    eng.Shutdown(ctx)
    // adminSrv.Shutdown(ctx)  // 见第 8 章

    // 8b. 关闭挂件（逆序：先停 obs flush 日志，再停 hotswap 排空组件，最后停配置热更）
    mgr.Shutdown(ctx)
    cfgMgr.Shutdown(ctx)
}
```

`slogLevel` 辅助函数（main 包内定义，映射字符串 → slog.Level，未知值回退 Info）：
```go
// slogLevel 将 LogLevel 配置字符串映射为 slog.Level：debug/info/warn/error；未知值默认 Info
func slogLevel(s string) slog.Level {
    switch strings.ToLower(s) {
    case "debug":
        return slog.LevelDebug
    case "warn", "warning":
        return slog.LevelWarn
    case "error":
        return slog.LevelError
    default:
        return slog.LevelInfo
    }
}
```

### 7.2 边界

- main 包除装配外无任何业务代码。
- 启动日志输出关键配置（不含密钥/Token）。
- 多副本无状态，可水平扩展。

### 7.3 验收

```bash
go run ./cmd/rocksys --upstream http://127.0.0.1:9000
# 另一终端：
curl http://localhost:8080/  # → 200，来自 9000 的响应

# Ctrl+C 触发优雅停机
# → 日志输出 "shutting down..." → 排空 → 退出
```

---

## 第 8 章 cmd/rockctl（运维命令行） + Admin API

- **职责**：在线热操作配置与组件开关。通过底座暴露的本地管理 HTTP API 通信。
- **通信协议**：HTTP JSON，回环地址 `127.0.0.1:19527`（由 `conf.AdminAddr` 配置），不对外网。

### 8.1 Admin API 端点规格

> **归属**：Admin API handler 实现在 `internal/adminapi/` 包，由 `cmd/rocksys` 启动。
> 实现方式：创建**独立** easyserver 实例（`easyserver.NewServer(cfg.AdminAddr)`，回环地址），
> 用 `AddHandler` 注册 admin 端点后 `ListenAndServe`；该实例**不得注册 chain 适配器**（不是主代理）。
> 优雅停机复用 §1.0 新增的 `Shutdown`/`Close`，在 main 的停机流程中调用（§7.1 步骤 8a）。
> admin 端点仅监听 `127.0.0.1`，不与主代理端口复用。

#### 8.1.0 关键类型

```go
package adminapi

// AdminServer 管理接口服务器
type AdminServer struct {
    srv        *easyserver.Server   // 独立 easyserver 实例（回环地址）
    confMgr    *conf.Manager        // ★ 用于内建 PUT /admin/config（调用 conf.Manager.Set）
    hotswapMgr *hotswap.Manager     // ★ 用于内建 /admin/switch/on|off|list
}

// New 创建 Admin 服务器，自动注册内建端点（/admin/switch/on|off|list、/admin/config GET|PUT）。
// ★ plugin 端点（/admin/metrics、/admin/script/*）不在 New 中注册——由 cmd/rocksys 装配时通过 RegisterPlugin 注入。
func New(addr string, confMgr *conf.Manager, hotswapMgr *hotswap.Manager) *AdminServer

// ListenAndServe 启动独立 HTTP listener（阻塞，通常 go 调用）
func (s *AdminServer) ListenAndServe() error

// Shutdown 优雅停机
func (s *AdminServer) Shutdown(ctx context.Context) error

// RegisterPlugin 注册挂件提供的 admin 端点（由 cmd/rocksys 装配时调用）。
// ★ 返回值 error：仅用于告知注册失败（如路径冲突）；与其他 easyserver 方法一致（AddHandler 返回 error）。
func (s *AdminServer) RegisterPlugin(path string, h func(http.ResponseWriter, *http.Request)) error
```

> **插件端点注册机制**（保持 §0.3 约束——adminapi 不得 import plugins）：
> `adminapi` 提供通用注册入口，由 `cmd/rocksys`（唯一装配点）在启动时把挂件的 handler 注入：
> ```go
> // adminapi 包内：
> func (s *AdminServer) RegisterPlugin(path string, h func(http.ResponseWriter, *http.Request))
>
> // cmd/rocksys 装配时：
> // obsAdmin := obs.NewAdminHandler(cfgMgr)
> // adminSrv.RegisterPlugin("/admin/metrics", obsAdmin.Metrics)
> // adminSrv.RegisterPlugin("/admin/logs", obsAdmin.Logs)
> // scriptAdmin := script.NewAdminHandler(mgr)          // ★ 传入 hotswap.Manager 以获取 script Engine 实例
> // adminSrv.RegisterPlugin("/admin/script/publish", scriptAdmin.Publish)
> // adminSrv.RegisterPlugin("/admin/script/rollback", scriptAdmin.Rollback)
> ```
> 各挂件包的 `NewAdminHandler` 函数签名——对链中间件挂件：应传入 `*hotswap.Manager`（内部通过 `mgr.GetMiddleware("script")` 获取实例）；对独立组件挂件：同样传入 `*hotswap.Manager`（通过 `mgr.GetComponent("config")` 获取实例）。`/admin/metrics` 与 `/admin/logs` 需要读取 obs 实例的指标数据，按同模式传入 Manager。`PUT /admin/config` 由 adminapi 内建，调用 `conf.Manager.Set`（§2.2）实现。

| 方法 | 路径 | 请求体 | 响应 | 说明 |
|------|------|--------|------|------|
| POST | `/admin/switch/on` | `{"name":"shield"}` | `{"ok":true}` | 开启组件 |
| POST | `/admin/switch/off` | `{"name":"shield"}` | `{"ok":true}` | 关闭组件 |
| GET | `/admin/switch/list` | — | `[{"name":"shield","state":"enabled",...}]` | 列出状态 |
| GET | `/admin/config` | — | `{"listen":":8080","upstream":"...",...}` | 查看配置 |
| PUT | `/admin/config` | `{"ROCKSYS_UPSTREAM":"http://..."}` | `{"ok":true}` | 热改配置（★ key 必须为注册名全名，即环境变量名；短 key 会被 easyconf 静默忽略） |
| POST | `/admin/script/publish` | `{"name":"rule1","source":"..."}` | `{"ok":true,"version":1}` | 发布 Lua 脚本 |
| POST | `/admin/script/rollback` | `{"name":"rule1","version":0}` | `{"ok":true}` | 回滚脚本 |

### 8.2 rockctl CLI 子命令映射

```
rockctl switch on <comp>    → POST /admin/switch/on  {"name":"<comp>"}
rockctl switch off <comp>   → POST /admin/switch/off {"name":"<comp>"}
rockctl switch list         → GET  /admin/switch/list
rockctl config get          → GET  /admin/config
rockctl config set <KEY> <v>  → PUT  /admin/config     {"<KEY>":"<v>"}
                                ★ <KEY> 为注册名全名（如 ROCKSYS_UPSTREAM、SHIELD_IP_BLACKLIST）
rockctl script publish <f>  → POST /admin/script/publish
rockctl script rollback     → POST /admin/script/rollback
```

### 8.3 鉴权

管理接口鉴权由 `adminapi.adminAuth` 统一完成，策略优先级从高到低：

1. **回环信任**：绑定 `127.0.0.1`/`localhost` 且未配置静态 token 时，本机免登录放行。
2. **公开路径**：`/admin/auth/status|login|register|reset` 免鉴权（前置条件由各 handler 校验）。
3. **静态预共享 token**：`ROCKSYS_ADMIN_TOKEN` 环境变量 → 请求头 `Authorization: Bearer <token>` 校验（供 rockctl/脚本使用，双轨兼容）。
4. **登录 JWT**：已初始化（存在管理员）时校验登录签发的 JWT；未初始化时拒绝（仅注册引导可用）。

> `ROCKSYS_ADMIN_TOKEN` 由 adminapi 包直接 `os.Getenv` 读取，**不注册进 easyconf**（无需持久化）。

### 8.4 认证端点（登录/注册/重置）

管理接口支持账号密码登录，超级管理员仅一个，密码只存 PBKDF2 哈希（标准库 `crypto/pbkdf2`，随机盐 + 100k 迭代），.env 中永不出现明文密码。

| 端点 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/auth/status` | GET | 返回 `auth_required`/`has_user`/`username`/`setup_mode`，供 WebUI 启动引导 |
| `/admin/auth/register` | POST | 首次注册（仅未初始化时开放），成功后置 `ADMIN_INITIALIZED=true` |
| `/admin/auth/login` | POST | 校验用户名+密码，成功签发登录 JWT（有效期 12h） |
| `/admin/auth/reset` | POST | 重置凭证（忘记密码，需处于重置模式） |

**三种场景流程：**

- **首次初始化**（全新系统，无用户）：`ADMIN_INITIALIZED=false` → WebUI 显示注册引导页 → 注册成功 → 置 `true`。
- **正常使用**：登录页输入账号密码 → 签发 JWT → 前端存 localStorage，后续请求带 `Authorization: Bearer <token>`。
- **忘记密码**：运维把 `.env` 中 `ADMIN_INITIALIZED` 改为 `false`（热更生效）→ 系统进入重置模式 → WebUI 显示重置页 → 重设用户名与密码 → 恢复 `true`。

**安全约束：**

- 注册仅在无用户时开放，已初始化后无条件拒绝（超管只能一个）。
- 重置仅在 `setup_mode`（`ADMIN_INITIALIZED=false` 且已有用户）时开放，需服务器文件权限触发，攻击者无法自行进入。
- 登录接口按 IP 限流：5 分钟窗口内失败 5 次锁定 5 分钟（`loginLimiter`）。

**配置项：**

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| `ADMIN_INITIALIZED` | `false` | 是否已初始化超级管理员；忘记密码时运维手动改 `false` 触发重置 |
| `ADMIN_JWT_SECRET` | 空 | 登录 JWT 签名密钥；为空时进程内随机（重启后需重新登录），可配置固定值保证跨重启有效 |

### 8.5 验收

```bash
go run ./cmd/rocksys --upstream http://127.0.0.1:9000 &
go run ./cmd/rockctl switch list
# → 输出组件列表

go run ./cmd/rockctl config get
# → 输出当前配置

go run ./cmd/rockctl config set ROCKSYS_UPSTREAM http://127.0.0.1:9001
# → {"ok":true}，代理立即切换到 9001
```

---

## 第 9 章 plugins/shield（L1 防护）

- **职责**：IP 黑白名单、UA/路径规则、令牌桶限流。实现 `chain.Middleware` + `hotswap.MiddlewareLifecycle`。
- **依赖**：`internal/chain`、`internal/dataflow`、`internal/hotswap`、`internal/conf`。

### 9.1 关键类型

```go
package shield

type Shield struct {
    cfg     *conf.Manager
    enabled atomic.Bool

    ipBlacklist []string           // IP 黑名单
    ipWhitelist []string           // IP 白名单
    pathRules   []PathRule         // 路径规则
    limiter     *RateLimiter       // 令牌桶限流
}

type PathRule struct {
    Pattern string  // glob 风格（支持 * 通配），如 "/admin/*"
    Action  string  // "allow" | "deny"
}

// Slot 挂载位置：L1 防护在转发前最先执行（同槽位顺序排在 auth 之后）
func (s *Shield) Slot() chain.Slot { return chain.Head }

type RateLimiter struct {
    rps     int
    burst   int
    buckets sync.Map  // key → *tokenBucket，LRU 淘汰上限 10000
}

// WAF 检测编译态（§9.6）：构建于 Start，随不可变快照整体原子替换。
type wafSnapshot struct {
    sqlEnabled      bool   // SQL 注入开关
    xssEnabled      bool   // XSS 开关
    pathTravEnabled bool   // 路径遍历开关
    riskPathEnabled bool   // 风险路径开关
    crawlerEnabled  bool   // 爬虫 UA 开关
    allowMethods    map[string]struct{} // 方法白名单（nil=不限）
    maxBodySize     int64               // 请求体上限（0=不限）
    sqlPatterns     []string            // 检测模式（来自规则文件）
    xssPatterns     []string
    pathPatterns    []string
    crawlerUAs      []string
    riskPaths       map[string]struct{} // 文件风险路径 + 配置追加
}
```

### 9.2 处理流程

```
白名单 IP → 放行
黑名单 IP → 403 Forbidden
WAF 检测链（§9.6，各项独立开关，默认关闭）→ 命中 → 403 Forbidden
路径/UA 规则匹配 deny → 403
路径规则匹配 allow → 跳过限流
限流检查 → 超限 → 429 Too Many Requests + Retry-After 头
全部通过 → return true（继续转发链）
```

### 9.3 边界

- 关闭（disabled）：**从链上摘除**（hotswap 流程 B，§6.3），不再被调用——不是"Handle 返回 true 保持挂载"。摘除后请求直通。
- 限流键数量上限 10000，LRU 淘汰防内存膨胀。
- 规则热更：hotswap 流程 C（§6.3）——重建规则快照，`Start(newCfg)` 内部原子替换。

### 9.4 配置项

```env
SHIELD_ENABLED=true
SHIELD_IP_BLACKLIST=192.168.1.100,10.0.0.0/8
SHIELD_IP_WHITELIST=127.0.0.1
SHIELD_RATE_LIMIT_RPS=100
SHIELD_RATE_LIMIT_BURST=20
SHIELD_RATE_LIMIT_BY=ip

# WAF 检测（§9.6，全部默认关闭 = 演进开关切换，不影响存量行为）
SHIELD_WAF_SQL_INJECTION=false
SHIELD_WAF_XSS=false
SHIELD_WAF_PATH_TRAVERSAL=false
SHIELD_WAF_RISK_PATH=false
SHIELD_WAF_RISK_PATHS=
SHIELD_WAF_CRAWLER_UA=false
SHIELD_ALLOW_METHODS=
SHIELD_MAX_BODY_SIZE=0
SHIELD_RULES_DIR=rules
```

> 以上配置项由挂件在构造时通过 `cfgMgr.Register(...)` 注册（见 §2.2），注册后自动纳入 .env 读写与热更广播；未注册的 key 在 `/admin/config` 写入时静默无效。
> WAF 配置项为 `*bool`/`*string`/`*int`（easyconf Register 支持类型）。

### 9.5 验收

```bash
# 设置黑名单
curl -X PUT http://127.0.0.1:19527/admin/config \
  -d '{"SHIELD_IP_BLACKLIST":"192.168.1.100"}'

# 从黑名单 IP 请求
curl http://localhost:8080/api/test
# → 403 Forbidden

# 关闭 shield
curl -X POST http://127.0.0.1:19527/admin/switch/off -d '{"name":"shield"}'
# → 同一 IP 请求恢复 200

# 限流测试（100 RPS）
for i in $(seq 1 110); do curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/test & done
# → 前 100 个 200，之后 429
```

### 9.6 WAF 检测（批次10 新增）

WAF 检测链在 IP 黑白名单之后、路径/UA 规则之前执行，各检测项独立开关、**全部默认关闭**（符合"演进 = 开关切换"红线）。

**检测顺序**：方法白名单 → 请求体大小预检 → 风险路径 → 路径遍历 → SQL 注入 → XSS → 爬虫 UA。命中任一 → 403 Forbidden 并中断转发链。

设计要点：

- **不读请求体**：检测仅基于 URL 路径/查询串与 User-Agent，避免 Body 重放问题；请求体大小仅按 `ContentLength` 预检，`-1`（chunked/未知）跳过。
- **注入检测用组合特征子串**（如 `union select`、`select * from`）而非单关键词，避免误杀 URL 中的普通单词。
- **规则全部外置文件**：`plugins/shield/rules/*.txt` 经 `internal/hotswap.ScriptDir` 加载（**外置目录优先、嵌入兜底**，与 `internal/db` 加载 `sql/<dbtype>/` 同机制），**改规则无需重新编译**。

规则文件清单（每行一个模式，`#` 注释、空行忽略；`SHIELD_RULES_DIR` 外置同名文件整体替换嵌入文件）：

| 文件 | 内容 | 对应开关 |
|------|------|---------|
| `rules/risk_paths.txt` | 风险路径（`/.env`、`/.git`、`/.well-known` 等，目录前缀匹配其下全部子路径） | `SHIELD_WAF_RISK_PATH` |
| `rules/sql_patterns.txt` | SQL 注入组合特征 | `SHIELD_WAF_SQL_INJECTION` |
| `rules/xss_patterns.txt` | XSS 特征 | `SHIELD_WAF_XSS` |
| `rules/path_traversal.txt` | 路径遍历特征（同时匹配转义/解码双路） | `SHIELD_WAF_PATH_TRAVERSAL` |
| `rules/crawler_ua.txt` | 爬虫/扫描器 UA 特征 | `SHIELD_WAF_CRAWLER_UA` |

- **生命周期**：`Start` 时经 `ruleLoader` 加载规则文件构建 `wafSnapshot`，随不可变快照原子替换；加载失败返回 error 并**保留旧快照**（实例继续以旧规则服务）。
- **风险路径合并**：`SHIELD_WAF_RISK_PATHS` 配置追加到规则文件集合（需先开启 `SHIELD_WAF_RISK_PATH`）。

验收示例：

```bash
# 开启 SQL 注入检测
curl -X PUT http://127.0.0.1:19527/admin/config \
  -d '{"SHIELD_WAF_SQL_INJECTION":"true"}'
curl "http://localhost:8080/api/list?id=1%20union%20select%20*%20from%20users"
# → 403 Forbidden

# 开启风险路径检测后访问 /.env
curl -X PUT http://127.0.0.1:19527/admin/config \
  -d '{"SHIELD_WAF_RISK_PATH":"true"}'
curl http://localhost:8080/.env
# → 403 Forbidden

# 外置规则覆盖（不重新编译）：在 SHIELD_RULES_DIR 放同名 crawler_ua.txt 新增自定义 UA
```

---

## 第 10 章 plugins/dispatch（L2 路由分发）

- **职责**：URI 前缀路由表 → 目标节点组（多节点负载均衡）；未命中 → 默认 upstream。
- **依赖**：`internal/chain`、`internal/dataflow`、`internal/hotswap`、`internal/conf`。
- **v2（批次10）**：前缀可指向【节点组】——多节点 + 平滑加权轮询 + 主动健康检查 + 高优节点优先（借鉴 easywaf，修掉其"未加权轮询、健康检查空壳"不足）。

### 10.1 关键类型

```go
package dispatch

// Rule 一条路由规则：Prefix 匹配的 URI 前缀，Nodes 为转发目标节点组。
type Rule struct {
    Prefix      string       // 前缀路径，必须以 "/" 开头
    Nodes       []*Node      // 上游节点组（≥1）
    HealthCheck *HealthCheck // 主动健康检查（nil = 不探活，所有节点视为健康）
    rr          *rrState     // 平滑加权轮询状态（与 Nodes 等长）
}

// Node 上游节点。
type Node struct {
    URL      string // http(s)://host[:port]
    Weight   int    // 权重（>0，默认 1）
    Priority int    // 0=高优（默认），1=备份（高优全挂才用）
}

// HealthCheck 主动健康检查配置与运行态。
type HealthCheck struct {
    Interval time.Duration // 探活周期（如 10s）
    Timeout  time.Duration // 单次探测超时（如 2s）
    Path     string        // 探测路径（如 /healthz）
}

type RouteTable struct {
    rules []*Rule  // 有序列表，最长前缀优先
}

// Slot 挂载位置：路由分发在防护之后、转发之前执行（同一请求内只允许一个 dispatch 挂 Middle）
func (d *Dispatch) Slot() chain.Slot { return chain.Middle }
```

> ★ **DISPATCH_RULES 配置格式**：配置字符串为逗号分隔的规则列表，每条规则格式为 `<Prefix>=<spec>`。
> - `spec = <node>[;<node>...][@interval@timeout@path]`，`node = http(s)://host[:port][|w=<权重>][|p=<优先级>]`。
> - 分隔符 `,` `=` `;` `|` `@` **不可转义**：Prefix 以 `/` 开头，node URL 以 `http(s)://` 开头，因此这些分隔符不会出现在合法位置的取值中。无需转义机制。
> - 示例：`/api/order/=http://o1:9001;http://o2:9001|w=2@10s@2s@/healthz,/api/user/=http://user-svc:9002`
> - 旧格式 `/api/=http://host:port` 仍兼容（单节点，无健康检查，全节点视为健康）。
> - 空字符串或格式错误 → 路由表为空，所有请求走默认 upstream。

### 10.2 前缀匹配算法

```go
func (rt *RouteTable) Match(path string) (upstream string, ok bool) {
    // 1. path 末尾补 "/"（统一处理）
    //    例："/api/order" → "/api/order/"
    //    ★ 例外：根路径 "/" 不补 "/"——"/" 匹配 Prefix "/" 的路由
    // 2. Prefix 也要求以 "/" 结尾才算完整段
    //    例：Prefix "/api/order/" 匹配 "/api/order/123"，不匹配 "/api/ordering/123"
    //    例外：Prefix "/" 匹配所有未命中其他路由的路径（兜底路由）
    // 3. 从 rules 中找到匹配的最长前缀
    // 4. 返回对应 Upstream
}
```

### 10.3 边界

- 路由表热更：hotswap 流程 C（§6.3）——`Start(newCfg)` 重建 RouteTable 并**原子替换实例内部快照**，无需 chain.Replace（实例保持挂载在 Middle 槽）。解析失败保留旧快照并返回 error。
- **健康检查生命周期**：随路由表启停——`Start` 启动新表探活 goroutine、停止旧表（阻塞等待退出，避免 goroutine 泄漏）；`Stop` 停止当前表。探活启动即探一次（避免窗口期流量全打向坏节点），随后按 `Interval` 轮询，2xx/3xx 判健康。
- **全挂语义**：命中规则但节点组全部不可达（已配置健康检查）→ 写 **503** 并中断链，避免错误转发；未配置健康检查时节点恒健康（兼容旧版）。
- **选点语义**（§10.5）：高优节点优先 → 平滑加权轮询。
- **Target 写入约定**：dispatch 是唯一写入者，通过 `ctx.DF.SetTarget(target)` 写入 DataFlow。Adapter 从 `df.Target()` 读取。其余组件只读。

### 10.4 验收

```bash
# 配置路由（节点组 + 健康检查 + 权重）
curl -X PUT http://127.0.0.1:19527/admin/config \
  -d '{"DISPATCH_RULES":"/api/order/=http://o1:9001;http://o2:9001|w=2@10s@2s@/healthz,/api/user/=http://user-svc:9002"}'

# 测试路由：/api/order 请求在 o1/o2 间加权轮询（权重 1:2）
curl http://localhost:8080/api/order/123
# → 转发到 o1:9001 或 o2:9001

# o2 探活失败后流量全部切到 o1（健康节点）
# o1、o2 全部不可达 → 503
curl http://localhost:8080/api/order/123
# → 503 Service Unavailable

# 未配置路径 → 默认 upstream
curl http://localhost:8080/other/path
# → 转发到默认 upstream

# 前缀边界：/api/order 不匹配 /api/ordering
curl http://localhost:8080/api/ordering/list
# → 不命中 /api/order/，走默认 upstream
```

### 10.5 负载均衡选点语义（批次10 新增）

```
高优节点（Priority=0）中选健康的
  └─ 无健康高优 → 备份节点（Priority=1）中选健康的
       └─ 仍无健康（已配置健康检查）→ ok=false → Handle 写 503
候选内按【平滑加权轮询】选点：权重 w 越大被选概率越高，且分布平滑（非简单随机）
```

---

## 第 11 章 plugins/result（L3 结果处理）

- **职责**：统一响应格式 `{code, msg, data}`、错误码映射、基础脱敏。
- **依赖**：`internal/chain`、`internal/dataflow`、`internal/hotswap`。

### 11.1 关键类型

```go
package result

type Envelope struct {
    Code int            `json:"code"`
    Msg  string         `json:"msg"`
    Data interface{}    `json:"data,omitempty"`
}

type Result struct {
    enabled    atomic.Bool
    errorMap   map[int]int     // 上游 HTTP 状态码 → 业务错误码
    maskFields []string        // 需要脱敏的 JSON 字段名
}

// Slot 挂载位置：L3 结果处理在响应阶段执行（chain.Tail + chain.ResponseHook）
// 注意：Result 必须实现 chain.ResponseHook（处理 ctx.RespBody），其 Handle 返回 false 占位即可（§4.6）
func (r *Result) Slot() chain.Slot { return chain.Tail }

// OnResponse 实现 chain.ResponseHook：从 ctx.RespBody 读取上游 JSON → 脱敏 → Wrap → ctx.WriteFinal
func (r *Result) OnResponse(ctx *chain.Context) error
```

### 11.2 处理流程

```
上游响应到达 → 解析 Content-Type
  ├─ application/json → 可选：脱敏 → 可选：Wrap 成 Envelope
  └─ 非 JSON → 原样透传
```

- 脱敏：对 JSON body 中指定的字段（如 `phone`、`id_card`、`token`）做部分替换（`138****1234`）。
- Wrap：将上游的 JSON body 嵌套进 `Envelope.Data`，自动取 `code` 和 `msg`。

### 11.3 边界

- 关闭=原样回传，不修改任何字节。
- 非 JSON 不处理（不报错、不修改）。

### 11.4 验收

```bash
# 开启 result
curl -X POST http://127.0.0.1:19527/admin/switch/on -d '{"name":"result"}'

# 上游返回 {"user":"test","phone":"13812345678"}
curl http://localhost:8080/api/user/1
# → {"code":0,"msg":"ok","data":{"user":"test","phone":"138****5678"}}

# 关闭 result
curl -X POST http://127.0.0.1:19527/admin/switch/off -d '{"name":"result"}'
# → 原样 {"user":"test","phone":"13812345678"}
```

---

## 第 12 章 plugins/trace（trace_id 透传）

- **职责**：确保 trace_id 透传到上游（`X-Trace-Id` 头）和响应（`X-Trace-Id` 头）。
- **依赖**：`internal/chain`、`internal/dataflow`、`internal/hotswap`。
- **Slot 挂载位置**：`chain.Head`——它只需在转发前给 `ctx.W.Header()` 设置 `X-Trace-Id` 响应头（WriteHeader 前设置均有效），然后返回 true 继续转发。

### 核心说明

> **trace_id 生成与透传分离**：生成由 `internal/dataflow`（第 5 章）在请求入口自动完成——无论 trace 挂件是否开启。透传由 trace 挂件控制——仅决定是否写入 HTTP Header。

- dataflow（第 5 章）**始终生成** trace_id，即使本挂件关闭。
- 本挂件职责仅为 Header 透传。开启时：响应中注入 `X-Trace-Id` 头（请求头注入由 engine.Forward 始终执行，不依赖本挂件）。
- 关闭时：trace_id 仍存在于 DataFlow（供 RockObs 日志使用），但 `X-Trace-Id` 响应头不写入。
- **engine.Forward 行为**：始终从 DataFlow 读取 TraceID 注入上游请求头 `X-Trace-Id`，与 trace 挂件状态无关。

### 验收

```bash
curl -v http://localhost:8080/api/test
# ← X-Trace-Id: a1b2c3d4e5f6...（响应头中可见）

# 上游日志中检查
# → 收到 X-Trace-Id 头，值与底座一致
```

---

## 第 13 章 plugins/config（RockConfig）

- **职责**：KV 配置服务。本地文件默认实现（基于 easyconf），SPI 预留外部后端。
- **依赖**：`easyconf`、`internal/hotswap`、`internal/conf`。
- **热切换实体类型**：**独立组件**——实现 `hotswap.Component` 接口（§6.2），由 `mgr.RegisterComponent(config.New(cfgMgr))` 注册（§7.1）。不挂 chain。

### 关键类型

```go
package config

// KVStore SPI 接口（用于替换后端）
type KVStore interface {
    Get(key string) (string, error)
    Set(key, value string) error
    Watch(fn func(change ChangeEvent)) error
}

// FileStore builtin 实现（基于 internal/conf.Manager + easyconf）
type FileStore struct {
    cfgMgr *conf.Manager
}
```

### 边界

- 文件不存在用默认值（easyconf 自动创建）。
- FileStore 写操作**自行**原子落盘（临时文件 + os.Rename）；`easyconf.UpdateFile` 虽是增量更新（保留注释/引号格式）但以 `O_TRUNC` 全量重写文件、且无临时文件，**非原子**（写盘中途崩溃可能损坏文件），不可依赖其原子性。
- 热更通过 `conf.Manager.Watch` 广播，不重启。

### 验收

```bash
# 修改 .env 文件
echo "ROCKSYS_UPSTREAM=http://127.0.0.1:9090" >> .env

# 3s 内自动热更
curl http://127.0.0.1:19527/admin/config
# → "upstream":"http://127.0.0.1:9090"
```

---

## 第 14 章 plugins/obs（RockObs）

- **职责**：访问日志（三时间戳 + 耗时分解，存储后端可切换）、指标聚合、查询 API、滚动留存。
- **依赖**：`internal/chain`、`internal/dataflow`、`internal/hotswap`、`internal/conf`、`internal/db`（默认 db 存储后端依赖）。
- **Slot 挂载位置**：`chain.Tail`，且实现 `chain.ResponseHook`——**这是 obs 获得"请求完成事件"的唯一通道**：
  `OnResponse(ctx)` 在 Forward 完成后被调用，此时 `ctx.RespCode/RespHeader/RespBody` 与 `ctx.DF` 三时间戳均已就绪，obs 据此构造 `AccessRecord` 异步落盘。
  其 `chain.Middleware.Handle` 返回 false 占位（不参与转发前逻辑）。
  注册方式：`mgr.RegisterMiddleware(obs.New(cfgMgr, dataDB))`（§7.1 步骤 5，dataDB 可 nil），装配顺序**先于** result（§4.4 执行顺序说明）。

### 关键类型

```go
package obs

// 维度注册表（dim.go）：每条访问记录的字段清单，新增字段先登记再实现。
// 索引维度（indexed）：固定列，支持过滤/排序；负载维度（payload）：Extras map，仅记录/展示。
type DimSpec struct { Name string; Type DimType; Kind DimKind; Desc string }

// 一条 HTTP 访问记录：13 个索引维度固定字段 + 负载维度 Extras（可扩展 map）。
type AccessRecord struct {
    Time       time.Time // 请求完成时间
    TraceID    string    // 链路标识
    TenantID   string    // 租户（可空）
    Path       string    // 请求路径
    Method     string    // HTTP 方法
    ClientIP   string    // 客户端地址
    StatusCode int       // 响应状态码
    Upstream   string    // 最终转发目标
    ShieldMs   int64     // 防护耗时(ms)
    BizMs      int64     // 业务/转发耗时(ms)
    TotalMs    int64     // 总耗时(ms)
    ReqBytes   int64     // 请求流量(字节)
    RespBytes  int64     // 响应流量(字节)
    Extras     map[string]any // 负载维度（如 request_body），序列化平铺进 JSON
}

// 存储后端接口（store.go）：file / db 两个实现，OBS_STORE 热切换（默认 db，file 已弃用）。
type Store interface {
    Name() string
    Write(batch []*AccessRecord) error
    Query(q Query) ([]map[string]any, error)
    Flush(ctx context.Context) error
    Close() error
}

type Metrics struct {
    window  *slidingWindow  // 1 分钟滑动窗口 × 100 桶
    // QPS / P50 / P95 / P99 / 错误率
}
```

### 核心流程

请求结束 → 从 `dataflow.DataFlow` 提取 `AccessRecord` → `AsyncStore.Write`（有界异步队列）→ worker 批量写当前底层 `Store`（`FileStore` 写 JSONL / `DBStore` 写 `access_log` 表）→ 聚合到 `Metrics`。

### 存储后端与热切换

- 配置项 `OBS_STORE`（默认 `db`）：`db` = 数据库（复用统一数据访问层 `internal/db`，`DB_DRIVER`/`DB_DSN` 默认 sqlite `rocksys.db`），写 `access_log` 表；`file` = JSONL 文件（`OBS_LOG_DIR`，默认 logs）——**已弃用，将不再被支持**：显式配置 `OBS_STORE=file` 时打弃用告警，仅过渡保留。
- 切换语义：配置热更 → hotswap 对 enabled 的 obs 调 `Start(nil)` → 按当前配置重建底层 Store 并原子替换 `AsyncStore`（旧后端排空缓冲后关闭）。
- 查询只读当前启用的后端；旧数据保留（db 表保留在库、file 文件保留在磁盘，切回即可再看）。
- db 后端因 dataDB 未就绪（`DB_DRIVER`/`DB_DSN` 无效）或建表失败：回退 `file` 并告警（过渡兜底，避免日志静默丢失），不阻断底座。
- DB 表 `access_log`：14 个索引列 + `extra` JSON 列（负载维度），SQL 全部外置 `sql/<dbtype>/`（外置优先、嵌入兜底，遵循 SQL 铁律）。

### Shutdown

```go
// Shutdown flush 内存缓冲区中未写入的日志，然后关闭存储后端
func (o *Obs) Shutdown(ctx context.Context) error
```
进程退出前必须调用 `Shutdown`，防止丢失还在内存缓冲区的日志。

> ★ **与 MiddlewareLifecycle.Stop() 桥接**：`hotswap.MiddlewareLifecycle.Stop() error` 不接收 context，而 `Shutdown` 需要超时控制（flush 缓冲）。obs 应额外暴露 `Stop() error` 方法，内部调用 `Shutdown(context.Background())`。hotswap Disable 流程通过 `MiddlewareLifecycle.Stop()` 优雅 flush（阻塞至 flush 完成）；若需超时，在 obs 内部 `Shutdown` 中自行设置 deadline。

### 文件管理（file 后端，已弃用）

- 按天切分：`logs/access-2024-01-01.jsonl`（每行一个平铺维度 JSON）
- 保留 30 天，超期自动 `os.Remove`。
- 写盘失败不阻塞请求（降级丢弃 + 计数告警）。
- 仅显式配置 `OBS_STORE=file` 或 db 不可用（回退兜底）时启用，将不再被支持。

### Admin API

> 以下端点经 `adminapi.RegisterPlugin` 注册（§8.1 插件端点注册机制），由 obs 包提供 handler。

- `GET /admin/metrics` → `{"qps":123.4,"p95_ms":45,"error_rate":0.01}`
- `GET /admin/logs` → 按条件查询访问日志，返回 JSONL（每行一个平铺维度对象）：
  - `from` / `to`：时间范围，`YYYY-MM-DD`（当日全天）或 `YYYY-MM-DDTHH:MM`（精确到分），缺省当天全天
  - `path`：路径精确匹配；`path_like`：路径模糊匹配（包含）
  - `trace_id`：链路标识模糊匹配（API 层保留，WebUI 已移除输入框）
- `GET /admin/logs/storage` → 日志存储总占用（file 文件合计 + db 表占用，字节），WebUI 日志页顶部展示

### 验收

```bash
curl http://127.0.0.1:19527/admin/metrics
# → 返回 QPS 和耗时分位

# 默认 db 后端：access_log 表落库（sqlite rocksys.db，DB_DRIVER/DB_DSN）
sqlite3 rocksys.db "select count(*), min(path) from access_log;"
# → 应有访问日志记录（或直接经 /admin/logs 查询验证）

curl "http://127.0.0.1:19527/admin/logs?from=2024-01-01T10:00&to=2024-01-01T11:30&path_like=/api/order"
# → 返回该时段内路径包含 /api/order 的访问日志（JSONL）
```

---

## 第 15 章 plugins/script（RockScript）

- **职责**：Lua 策略引擎（安全规则/路由改写/分流/临时重定向）。**禁止承载业务逻辑。**
- **依赖**：`gopher-lua`、`internal/chain`、`internal/dataflow`、`internal/hotswap`、`internal/conf`。
- **挂载方式**：**链中间件**——实现 `chain.Middleware` + `hotswap.MiddlewareLifecycle`，`Slot() = chain.Middle`（路由分发后、转发前执行，可改写 Target 或直接响应）。每请求 `Handle` 执行匹配的 Lua 脚本（默认超时 100ms，用 `lua.ContextDeadline` / `context.WithTimeout` 控制，见 §4.6）；`Start(newCfg)` 重建脚本快照并原子替换。

### 关键类型

```go
package script

type Engine struct {
    scripts  map[string]*ScriptVersion  // 编译缓存
    vmPool   sync.Pool                  // Lua VM 池
    timeout  time.Duration              // 执行超时，默认 100ms
}

type ScriptVersion struct {
    Name    string
    Source  string
    Version int
    Proto   *lua.FunctionProto  // 预编译
}
```

### Sandbox 白名单

允许的 Lua API：
- `req.header(key)` → 读请求头
- `req.path()` / `req.method()` → 读路径/方法
- `ctx.set_target(target)` → 改写路由目标
- `ctx.respond(code, body)` → 直接响应
- **禁止**：`os`、`io`、`file`、`net`、`ffi` 模块。

### 验收

```bash
# 发布脚本
curl -X POST http://127.0.0.1:19527/admin/script/publish \
  -d '{"name":"test","source":"if req.path() == \"/block\" then ctx.respond(403, \"blocked\") end"}'

curl http://localhost:8080/block
# → 403 blocked

# 回滚
curl -X POST http://127.0.0.1:19527/admin/script/rollback \
  -d '{"name":"test","version":0}'
curl http://localhost:8080/block
# → 正常转发

# 恶意脚本测试
# 含 os.execute 的脚本 → 沙箱拦截，发布失败
```

---

## 第 16 章 plugins/auth（RockAuth）【P2】

- **职责**：JWT 认证、租户识别。实现 `chain.Middleware` + `hotswap.MiddlewareLifecycle`，挂在 Head 槽位。
- **依赖**：`internal/chain`、`internal/dataflow`、`internal/hotswap`、`internal/conf`。
- **关键类型**：
  - `JWTConfig`：`Issuer string`、`Secret string`、`TTL time.Duration`。
  - `Verifier`：公钥缓存 + 轮换，`Verify(token string) (claims, error)`。
- **构造函数**：`func New(cfg *conf.Manager) *Auth`；`func (a *Auth) Slot() chain.Slot { return chain.Head }`。
- **核心流程**：L1 阶段校验 `Authorization: Bearer <token>` → 解析 `tenant_id`/`user_id` → 写入 DataFlow → 放行；失败返回 `401 Unauthorized`。
- **验收**：合法令牌 200 + DataFlow 中有 tenant_id；非法令牌 401；关闭后无鉴权直通。

---

## 第 17 章 plugins/registry（RockRegistry）【P2】

- **职责**：服务注册与发现。实现 `hotswap.Component` 接口。
- **依赖**：`internal/hotswap`、`internal/conf`、`internal/chain`（联动 dispatch 时）。
- **与 dispatch 的联动机制**：registry 自身不直接改 dispatch。实例变更经 **配置热更通道** 传递——registry 把最新实例列表写入 `conf.Manager` 的某个配置项（如 `DISPATCH_RULES` 的注册名），dispatch 通过 `conf.Manager.Watch` 订阅到变更后走 §6.3 流程 C：`Start(newCfg)` 重建 RouteTable 并原子替换内部快照（无需 chain.Replace，实例保持挂载）。这样 registry→dispatch 解耦，且天然复用已有热更/审计链路。
- **关键类型**：
  - `StaticTable`（默认）：YAML/JSON 静态实例列表。`func NewStaticTable(path string) *StaticTable`。
  - `Server`：内置轻量注册服务（HTTP API：`POST /register` + `PUT /heartbeat`）。`func NewServer(addr string) *Server`。
  - `Watcher`：实例变更通知回调 `func(instances []Instance)`。
- **验收**：实例上线 `POST /register` → dispatch 路由到新实例；心跳超时 30s 未续约 → 自动摘除。

---

## 第 18 章 plugins/mq（RockMQ）【P2】

- **职责**：异步消息可靠投递（无需独立 MQ 即可工作）。
- **依赖**：`internal/hotswap`、数据库 driver（通过 `*sql.DB` 注入）。
- **热切换实体类型**：**独立组件**——实现 `hotswap.Component` 接口（§6.2）。
- **关键类型**：
  - `OutboxStore`：业务库内 outbox 表（`id/topic/payload/status/created_at`）。`func NewOutboxStore(db *sql.DB, tableName string) *OutboxStore`。
  - `PollingDeliverer`：定时轮询 + 直接 HTTP 调用消费方。`func NewPollingDeliverer(store *OutboxStore, interval time.Duration) *PollingDeliverer`。
- **核心流程**：stbiz 本地事务（业务表 + outbox 同事务）→ 轮询器取 `status=pending` → HTTP POST 投递 → 消费方幂等处理 → 标记 `done`；失败则指数退避重试（最大 3 次 → 死信）。
- **验收**：无 MQ 条件下消息可靠送达；重复投递时消费方幂等返回 ok。

---

## 第 19 章 plugins/object（RockObject）【P2】

- **职责**：本地对象存储。
- **依赖**：`internal/hotswap`。
- **热切换实体类型**：**独立组件**——实现 `hotswap.Component` 接口（§6.2）。
- **关键类型**：`LocalStore`（`baseDir string` + 路径安全校验）。`func NewLocalStore(baseDir string) *LocalStore`。
- **方法**：`Put(path string, data []byte) error`、`Get(path string) ([]byte, error)`、`Delete(path string) error`。
- **边界**：路径穿越防护（`filepath.Clean` + 检查不超出 `baseDir`）；大文件不走代理转发（客户端直传）。
- **验收**：正常读写文件；`Put("../../../etc/passwd", ...)` 被拒绝。

---

## 第 20 章 contracts（契约规范）

- **内容**：`contracts/openapi/` 下每业务服务一个 `*.yaml`（OpenAPI 3.0）。
- **规范**：统一错误码 `{code, msg, data}` + `X-Trace-Id` 响应头；接口版本只增不改删。
- **验收**：OpenAPI 文件通过 `redocly lint` 或 `spectral lint`。

---

## 第 21 章 sdk/python（RockBiz SDK）

- **包结构**：`rockbiz/{config,logging,client,trace,outbox,registry}`。
- **关键 API**（伪代码）：

```python
from rockbiz.config import load_config          # 环境变量 > yaml
from rockbiz.logging import get_logger           # 结构化 JSON，自动带 trace_id
from rockbiz.client import HttpClient            # HTTP 客户端，timeout=5s
from rockbiz.trace import get_trace_id, set_trace_id  # 上下文传递

# 用法
cfg = load_config("config.yaml")
log = get_logger(__name__)
client = HttpClient(default_timeout=5)

log.info("request received", extra={"trace_id": get_trace_id()})
resp = client.post("http://other-svc/api/action", json={...}, headers={"X-Trace-Id": get_trace_id()})
```

- **验收**：`import rockbiz` 成功；`HttpClient` 请求自动带 trace_id；日志 JSON 格式含 `trace_id`。

---

## 第 22 章 examples/stbiz_hello（最小模板）

- **文件**：`app.py`（FastAPI）、`config.yaml`、`README.md`。
- **接口**：
  ```python
  GET  /health     → {"status":"ok"}
  POST /api/hello  → {"msg":"hello","trace_id":"..."}
  ```
- **验收**：
  ```bash
  pip install fastapi uvicorn
  python app.py &         # 监听 localhost:9000
  go run ./cmd/rocksys --upstream http://127.0.0.1:9000 &
  curl -v http://localhost:8080/health     # → 200 {"status":"ok"}
  curl -v http://localhost:8080/api/hello  # → 响应带 X-Trace-Id 头
  ```

---

## 第 23 章 验证与高可用

### 23.1 降级链验证

```bash
# 全量开启所有挂件
# Step 1: 关闭 L3 result
curl -X POST http://127.0.0.1:19527/admin/switch/off -d '{"name":"result"}'
curl http://localhost:8080/api/test  # → 200，响应原样

# Step 2: 关闭 L2 dispatch
curl -X POST http://127.0.0.1:19527/admin/switch/off -d '{"name":"dispatch"}'
curl http://localhost:8080/api/test  # → 200，走默认 upstream

# Step 3: 关闭 L1 shield
curl -X POST http://127.0.0.1:19527/admin/switch/off -d '{"name":"shield"}'
curl http://localhost:8080/api/test  # → 200，裸转发

# 每步都验证不中断
```

### 23.2 全链路压测

```bash
# 前置：先启动上游（任意本地 echo 服务即可，如 examples/stbiz_hello 的 localhost:9000），
#       再启动 rocksys --upstream http://127.0.0.1:9000，最后开始压测。

# 工具：hey（github.com/rakyll/hey）
hey -n 10000 -c 100 http://localhost:8080/api/test

# 验证指标
curl http://127.0.0.1:19527/admin/metrics
# → QPS > 1000, P99 < 10ms, 错误率 < 0.1%

# 三时间戳精度检查
# → |(shield_ms + biz_ms) - total_ms| < 1ms
```

### 23.3 高可用部署检查清单

```bash
# 多副本启动
go run ./cmd/rocksys --listen :8081 --upstream http://127.0.0.1:9000 &
go run ./cmd/rocksys --listen :8082 --upstream http://127.0.0.1:9000 &

# 优雅停机
kill -TERM $(pgrep rocksys)
# → 日志输出 "shutting down...", 30s 内退出
# → 在途请求不丢失

# 组件故障回滚
curl -X POST http://127.0.0.1:19527/admin/switch/on -d '{"name":"broken_component"}'
# → {"ok":false,"error":"start failed: ...", "rolled_back":true}
```

### 23.4 验收清单（全手册）

- [ ] 第 1 章：`go build ./...` 通过
- [ ] 第 2 章：命令行/环境变量/配置文件热更三类加载均生效
- [ ] 第 3 章：裸代理可用，WebSocket 隧道穿透（101 后双向字节对拷），超时返回 504
- [ ] 第 4 章：中间件挂载/摘除不影响在途请求
- [ ] 第 5 章：三时间戳精度 < 1ms
- [ ] 第 6 章：`rockctl switch off` 后请求直通，`switch on` 后恢复
- [ ] 第 7 章：SIGTERM 优雅退出，在途请求不丢
- [ ] 第 8 章：admin API 全端点可用，非法参数返回明确错误
- [ ] 第 9 章：黑名单 403，限流 429
- [ ] 第 10 章：前缀路由正确分发，边界不误匹配
- [ ] 第 11 章：JSON 响应统一封装，关闭后原样
- [ ] 第 12 章：全链路同一 trace_id
- [ ] 第 13 章：改 .env 文件 3s 内热更
- [ ] 第 14 章：`/admin/metrics` 有数据，日志文件按天轮转
- [ ] 第 15 章：Lua 脚本热发布/回滚，恶意脚本被沙箱拦截
- [ ] 第 16-19 章：按需验收
- [ ] 第 20-22 章：Python 服务可被代理，链路日志含 trace_id
- [ ] 第 23.1 章：降级链每步转发不中断
- [ ] 第 23.2 章：压测通过

---

## 附录 A：easyserver 接口速查（地基库 1/2）

> 模块：`github.com/iotames/easyserver`（`go.mod`）
> 顶层别名：`Server = httpsvr.EasyServer`，`HttpContext = httpsvr.Context`，`HttpDataFlow = httpsvr.DataFlow`

### A.1 创建与启动

```go
// 顶层封装
srv := easyserver.NewServer(":8080")  // 返回 *httpsvr.EasyServer
srv.ListenAndServe()                  // 启动 HTTP

// 直接使用 httpsvr
srv := httpsvr.NewEasyServer(":8080")
srv.ListenAndServeTLS(certFile, keyFile string)
```

### A.2 中间件接口（核心）

```go
// MiddleHandle 是 easyserver 的中间件接口
type MiddleHandle interface {
    Handler(w http.ResponseWriter, r *http.Request, dataFlow *DataFlow) (next bool)
}
// next=false → 中断后续链

// 注册中间件
srv.AddMiddleHead(middle MiddleHandle)  // 前置
srv.AddMiddleTail(middle MiddleHandle)  // 后置

// 注册普通 HTTP Handler（不经过中间件链，直接路由到指定函数）
// ★ 用于 admin API 等独立端点（§8.1）
srv.AddHandler(pattern string, h func(http.ResponseWriter, *http.Request))

// 内置中间件（注意：均返回 error）
srv.AddStatic(urlPathBegin, wwwroot string) error  // 静态文件
srv.SetCORS(allowOrigin string) error              // CORS

// 创建匿名中间件
m := httpsvr.NewMiddle(func(w http.ResponseWriter, r *http.Request, df *httpsvr.DataFlow) (next bool) {
    // ... 处理逻辑
    return true
})
```

### A.3 DataFlow 工具

```go
df := httpsvr.NewDataFlow()
df.SetData(key string, value interface{}) error
df.SetDataReadonly(key string, value interface{}) error  // 后续不可改
df.GetData(key string) GlobalData
df.GetStr(key string) string
df.GetDataKeys() []string
df.GetStartAt() time.Time  // 请求到达时间（自动记录）
```

### A.4 Context（请求上下文）

```go
// Context 字段
ctx.Writer   http.ResponseWriter
ctx.Request  *http.Request
ctx.Server   *EasyServer
ctx.DataFlow *DataFlow

// 常用方法
ctx.GetPostJson(v any) error
ctx.GetQueryValue(k, defaultVal string) string
ctx.SetHeader(key, value string)
ctx.Json(data map[string]any, statusCode int) error
ctx.Text(text string, statusCode int) error
```

### A.5 日志

```go
import "github.com/iotames/easyserver/log"

log.Info("msg", "key", "val")
log.Debug("msg", "key", "val")
log.Warn("msg", "key", "val")
log.Error("msg", "key", "val")
log.SetLevel(slog.LevelInfo)
log.SetLogWriterByFile("/path/to/log.jsonl")  // 实际签名返回 (f *os.File, err error)，需处理返回值
```

### A.6 hotswap（easyserver 层的脚本管理，非我们的 hotswap）

> ★ **包名冲突提示**：本包与 `internal/hotswap`（第 6 章）包名相同。同时 import 时用 `eshs` 别名引入本包（约定见 §0.2）。

```go
sd := hotswap.NewScriptDir(embedFs, "dir1", "dir2")
sd.GetScriptText(fpath string) (string, error)
sd.DecodeJson(fpath string, v any) error
sd.LsDirByEmbedFS() []string
```

---

## 附录 B：easyconf 接口速查（地基库 2/2）

> 模块：`github.com/iotames/easyconf`（`go.mod`）
> 配置文件格式：`.env`（KEY=VALUE，# 注释）

### B.1 基本用法

```go
import "github.com/iotames/easyconf"

ec := easyconf.NewConf(".env", "default.env")  // 越靠左优先级越高

// 注册配置项（指针绑定）
var listenAddr string
ec.StringVar(&listenAddr, "LISTEN", ":8080", "监听地址")

var timeout int
ec.IntVar(&timeout, "TIMEOUT", 5, "超时秒数")

var enabled bool
ec.BoolVar(&enabled, "ENABLED", false, "是否启用")

var hosts []string
ec.StringListVar(&hosts, "HOSTS", []string{}, "主机列表")  // 逗号分隔

// 加载：命令行 > 环境变量 > .env 文件
ec.Parse(true)  // true=启用 flag 解析
```

### B.2 运行时操作

```go
ec.SetItemValue("KEY", "val")                          // 单键设值
ec.SetValuesByEnv()                                    // 从环境变量重新加载
ec.SetValuesByEnvFile(".env")                          // 从 .env 文件重新加载
ec.UpdateFile(".env")                                  // 增量写回 .env
ec.UpdateByMap(map[string]string{"K":"V"}, ".env")     // 批量更新+写回
```

### B.3 配置项类型

```go
ec.StringVar(pval *string, name, defval, title string)      // string
ec.BoolVar(pval *bool, name string, defval bool, title)      // bool
ec.IntVar(pval *int, name string, defval int, title)          // int
ec.Float64Var(pval *float64, name string, defval float64, title) // float64
ec.StringListVar(pval *[]string, name string, defval []string, title) // []string 逗号分隔
ec.IntListVar(pval *[]int, name string, defval []int, title)  // []int 逗号分隔
```

### B.4 注意事项

- **无 Watch 机制**：热更需自行实现 mtime 轮询（参考第 2 章）。
- `.env` 文件不存在时自动创建并写入默认值。
- `Parse(true)` 才解析 `flag`，若不用命令行参数传 `Parse(false)`。
- `UpdateFile` 是增量写回，保留原注释和空行。

## 附录 C：easydb 数据操作层速查（地基库 3/2）

> 数据访问层 = `internal/db`（装配）+ `easydb`（数据操作）+ `sql/<dbtype>/`（SQL 脚本）。

### C.1 配置项（cmd/rocksys 装配注册）

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `DB_DRIVER` | `sqlite` | 数据库驱动名（sqlite/mysql/postgres） |
| `DB_DSN` | `rocksys.db` | 连接串（sqlite 为文件路径） |
| `SQL_DIR` | `sql` | 外置 SQL 脚本目录（优先加载，嵌入兜底） |

### C.2 SQL 脚本目录约定（数据库铁律）

> **铁律**：① SQL 必须落盘 `sql/<dbtype>/`，禁止 Go 代码内联；② 换库只改 `.env`（`DB_DRIVER`/`DB_DSN`）；③ 纯 SQL 参数化，不用 ORM/对象模型；④ sqlite/mysql/postgres 三方言齐平，缺脚本即报错。

- 目录：`sql/<dbtype>/`（如 `sql/sqlite/`、`sql/mysql/`、`sql/postgres/`）。
- 占位符：参数化查询用 `?`（sqlite/mysql）或 `$1`（postgres）；动态表名等标识符用 `{xxx}`（运行时由组件替换，禁止来自用户输入）。
- 加载：`internal/hotswap/script.go` 的 `ScriptDir`——外置 `SQL_DIR` 优先，找不到回退编译期 embed。
- 缺脚本即报错：切换 `DB_DRIVER` 后若 `sql/<dbtype>/` 缺某条查询脚本，`SQL()` 直接返回错误。

### C.3 数据操作

```go
// 装配（cmd/rocksys）
dataDB, err := db.Open(dbDriver, dbDSN, sqlDir)   // 失败不阻断底座，仅 Warn
mqComp := mq.New(dataDB.EasyDB().GetSqlDB(), "outbox") // 复用 dataDB 连接（同库同方言）
mqComp.SetSQLSource(dataDB)                        // 组件经 SQLSource 读脚本

// 脚本读取
txt, err := dataDB.SQL("mq_insert.sql")            // = sql/<driver>/mq_insert.sql

// 参数化执行（脚本名 + 参数）
dataDB.Exec("mq_mark_done.sql", id)
dataDB.GetMany("some_query.sql", &rows, args...)
```

### C.4 工作池（internal/workpool）

```go
wp := workpool.NewWorkerPool(workpool.Config{MinWorkers: 4, QueueSize: 100})
wp.Start()
wp.Submit(workpool.TaskFunc(func() { /* 任务 */ }))       // 阻塞提交
wp.TrySubmit(task)                                        // 非阻塞
wp.UpdateWorkers(8)                                       // 动态扩/缩 worker
wp.Stop()                                                 // 停止并排空剩余任务
```
