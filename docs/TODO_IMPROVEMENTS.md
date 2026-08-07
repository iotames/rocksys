# RockSys 前端代码组件化

为提高前端界面的用户体验一致性，对现有前端代码，进行高内聚，低耦合的封装改造。重复部分尽量抽离封装重组，鼓励前端模块组件化。

# RockSys 组件容错改进计划（TODO 任务卡）

> 状态：待审视（未实施）
> 来源：2026-08-07 组件容错审计。结论：业务转发不依赖任何组件，组件故障被隔离在旁路/异步路径，**业务连续性达标**。
> 但存在三个值得正视的风险点，按下列任务卡逐项改进。供 AI 员工按步骤实施，实施前须先审视本计划。

---

## 背景与审计结论（简述）

- 转发链 = 纯 HTTP 反向代理（`internal/engine/engine.go:Forward`），不碰数据库、不依赖组件。
- obs/mq/copy 等均为异步或旁路，故障只丢日志/消息，不阻塞请求（`plugins/obs/store.go:96-118` 队列满丢弃、`mq.go:324-329` 跳轮）。
- 三个风险点不威胁业务连续性，但损害**可观测性可靠性**与**单请求健壮性**：
  1. sqlite 默认无 `busy_timeout`/`WAL` → SQLITE_BUSY 频繁（已见 `database is locked (5)` 日志），obs 日志批量丢弃、mq 投递延迟。
  2. 请求路径无 recover 兜底 → 中间件 panic 击穿到 net/http，**单个请求连接中断**。
  3. obs 写失败无重试、运行期不降级 → 瞬时锁错误直接整批丢弃，无补救。

---

## TODO-1：sqlite 连接参数补全（busy_timeout + WAL）

### 现状
- `cmd/rocksys/main.go:171`：默认 `DB_DSN = "rocksys.db"`（裸路径，无任何参数）。
- `internal/db/db.go:77`：`sql.Open(driver, dsn)` 原样透传。
- 已验证驱动 `modernc.org/sqlite v1.55.0` 支持 DSN 参数（源码 `sqlite.go:264/285/381`）：
  - `_busy_timeout=5000` → `PRAGMA busy_timeout = 5000`（每连接生效）
  - `_journal_mode=WAL` → `PRAGMA journal_mode = WAL`
  - `_pragma=busy_timeout(5000)` 可重复，原样执行
- 影响：obs 批量写、mq outbox 写、admin 用户表写共用同一 sqlite 文件，并发写互锁 → `SQLITE_BUSY`。

### 目标
DB 层对 sqlite 自动保证「写等待 5s + WAL 并发读」，从根源消除大多数 SQLITE_BUSY。对 mysql/postgres 无影响。

### 实施方案（推荐：默认值 + Open 层自动补参双保险）
1. `internal/db/db.go` 新增 `ensureSQLitePragma(dsn string) string`：
   - 仅 `driver == "sqlite"` 时生效。
   - DSN 已含 `_busy_timeout` / `_journal_mode` / `_pragma` 任一 → 原样返回（尊重显式配置）。
   - 否则拼接：裸路径用 `?` 连接，已有 `?` 参数用 `&` 连接，追加 `_busy_timeout=5000&_journal_mode=WAL`。
   - 在 `Open()` 的 `sql.Open(driver, dsn)` 处调用。
2. `cmd/rocksys/main.go:171` 默认值同步改为带参形式 `rocksys.db?_busy_timeout=5000&_journal_mode=WAL`（双保险，覆盖"仅改默认值"路径）。
3. 补单测 `internal/db/db_test.go`：裸路径 / 已带 `?` 参数 / 已含 pragma / mysql dsn 不受影响，四例。

### 实施步骤
1. 实现 `ensureSQLitePragma` 并接入 `Open()`。
2. 修改默认 `DB_DSN` 常量。
3. 添加单测并运行 `go test ./internal/db/...`。
4. 手工验证（见下）。
5. 更新 `docs/DEV_HANDBOOK.md` 数据库铁律说明（DSN 参数约定）。

### 验收标准
- 启动后 `sqlite3 rocksys.db "PRAGMA journal_mode;"` 输出 `wal`。
- 并发压测下 obs 日志不再出现 `SQLITE_BUSY`（或显著下降）。
- `go test ./...` 全绿。

### 风险与注意
- **WAL 依赖本地文件系统**：网络盘/NFS 上不可靠；若部署在网络盘需评估或跳过 WAL 只留 busy_timeout。
- **pragma 失败会让连接 Ping 失败** → `main.go:177-181` 已容错（dataDB=nil → obs 回退 file、mq 不注册），业务仍不中断，但功能降级；单测必须覆盖参数拼接正确性，避免手误写坏 DSN。
- 老部署 `.env` 中显式 `DB_DSN` 裸值：Open 层自动补参已覆盖，无需改 `.env`。

---

## TODO-2：请求路径 panic 兜底（recover）

### 现状
- `internal/chain/impl.go:60-77` `Chain.Execute`：无 recover。
- `internal/chain/adapter.go:54-117` `Adapter.Handler`：无 recover；`adapter.go:102-106` ResponseHook 循环只处理 err、不处理 panic。
- `easyserver/httpsvr/server.go:54-68` `ServeHTTP`：无 recover（本地 replace 子库，可改）。
- 后果：中间件真 panic → 冒泡到 Go 标准库 net/http 连接级 recover → **该请求连接被关闭、无响应/连接重置**；进程存活，其他请求不受影响。

### 目标
单请求故障时：客户端收到明确 5xx 而非连接中断；日志含中间件名 + 完整堆栈；其他请求零影响。

### 实施方案
1. `internal/chain/impl.go` 新增 `safeHandle(m Middleware, ctx *Context) (next bool)`：
   - `defer recover()`：panic 时 `log.Error`（中间件名 + 堆栈），尝试写 500（`ctx.W` 未写响应时）；返回 false（中断链）。
   - `Execute()` 中 Head/Middle 循环改用 `safeHandle`。
2. `internal/chain/adapter.go:102-106` ResponseHook 循环外包 recover：单个 hook panic → `log.Error`（hook 名 + 堆栈），继续后续 hook（语义与现有"err 不中断后续 hook"一致）。
3. （可选最后防线）`easyserver/httpsvr/server.go` `ServeHTTP` 外层 recover：写 500，避免未来新中间件漏包。
4. 单测 `internal/chain/chain_test.go`：构造会 panic 的假中间件（Head/Middle/Tail 各一），断言：该请求返回 500、进程不崩、后续请求正常、日志含堆栈。

### 实施步骤
1. 实现 `safeHandle` 并替换 `Execute` 循环。
2. ResponseHook 循环加 recover。
3. 可选：easyserver ServeHTTP 兜底。
4. 单测 + `go test ./...`。
5. 更新 `ARCHITECTURE.md`（§5.2 降级语义补充 panic 行为说明）。

### 验收标准
- panic 中间件存在时：客户端收到 500（非连接重置），并发请求全部正常。
- 日志可定位：中间件名 + panic 堆栈。

### 风险与注意
- **panic 可能发生在持锁状态**：实例级 mutex 未释放会锁死该实例，影响后续请求。缓解：配合可选增强 2b（连续 panic 自动摘除）。
- recover 会掩盖编程错误：必须记录完整堆栈，禁止吞掉。
- 写 500 用 `http.Error` 幂等（已 WriteHeader 时无害），不会二次 panic。

### TODO-2b（可选增强，二期评估）
连续 panic 计数超阈值 → 自动 `hotswap.Disable` 该中间件（需可配置开关、默认关闭）。防止故障中间件持续拖垮请求。评估点：偶发 panic 不该触发摘除，阈值与恢复策略需评审。

---

## TODO-3：obs 写入失败重试 + 运行期降级

### 现状
- `plugins/obs/store.go:198-201` `writePending`：底层写失败 → 整批丢弃 + WARN。
- `plugins/obs/store.go:216-219` `flushAll`：失败 → 丢弃。
- `plugins/obs/obs.go:262-271` `buildStore`：仅在启动/热更时回退 file；运行期瞬态失败不回退。
- `store.go:106-110`：队列满（4096）丢弃。

### 目标（第一期：轻量防御）
瞬时锁冲突可自动补救一次；丢弃/失败可观测（暴露到 admin 指标）。**不引入自动切换后端的复杂性**。

### 实施方案（第一期）
1. `store.go` `writePending` / `flushAll`：写失败后重试 1 次（间隔 50ms），仍失败才丢弃 + 告警。
2. `AsyncStore` 增加连续失败计数（`consecutiveFails` 原子计数，成功清零）：
   - 连续失败阈值（如 10 次）→ 告警升级为 `log.Error`（提示运维检查 DB 或热切 `OBS_STORE`）。
3. 暴露可观测指标：obs admin 插件（`/admin/logs/storage` 或 `/admin/metrics`）增加 `drop_count`、`consecutive_fails`。
4. 单测 `plugins/obs/store_test.go`：mock Store 恒失败 → 断言重试 1 次、drop 计数正确、Write 不阻塞。

### 实施步骤（第一期）
1. `writePending`/`flushAll` 加重试。
2. 加 `consecutiveFails` 计数与告警升级。
3. admin 指标暴露。
4. 单测 + `go test ./...`。
5. 更新 `docs/log-system.md`（丢弃/失败语义说明）。

### 验收标准
- 模拟 DB 锁定持续：日志先见重试、后见丢弃告警、连续失败升级 Error；请求侧全程正常。
- admin 接口可见 drop 计数与连续失败数。
- `go test ./...` 全绿。

### TODO-3b（可选增强，二期评估）
运行期熔断自动降级：连续失败超阈值 → 自动 `Replace` 到 file 后端；恢复探测成功后切回 db。
**评估点（重要）**：
- `obs.go:Query` 只转发当前后端，自动切换后**旧 db 数据查不到**；需决定查询合并（双后端并行查）或接受"切换后只能查新后端"。
- 自动切换涉及"后端工厂"注入（AsyncStore 是通用包装，不感知 file 如何构建）——架构上需把构建逻辑上提到 Obs 层并注入失败回调。
- 建议二期单独立项评审，第一期不做。

### 风险与注意
- 重试延长 worker 阻塞，队列满丢弃概率略增；1 次 50ms 影响可忽略（总量有限）。
- 重试次数/退避做成常量或配置，禁止写死魔数散落多处。

---

## 实施顺序建议

| 顺序 | 任务 | 理由 |
|---|---|---|
| 1 | TODO-1（sqlite pragma） | 收益最大、风险最低，直接消除 SQLITE_BUSY 根源，并让 TODO-3 的重试触发频率大降 |
| 2 | TODO-3 第一期（重试 + 指标） | 防御残余锁冲突，提升可观测性 |
| 3 | TODO-2（recover 兜底） | 架构级最后防线 |
| 4 | TODO-2b / TODO-3b（自动摘除/自动降级） | 行为变更大，需评审后再定 |

## 通用要求（每项任务必做）
- 全部改动后运行 `go test ./...` 与 `go vet ./...`。
- 同步更新受影响 .md 文档（README.md / docs/DEV_HANDBOOK.md / docs/log-system.md / ARCHITECTURE.md，按任务卡指定）。
- commit message 用中文，无 AI 协作署名；**commit 前须经用户确认**。
- 每项完成后回填「完成情况」：验证命令与结果、改动文件清单。

## 回滚策略
- 各项改动相互独立、可单独 revert（TODO-1 仅 db 层 + 默认值；TODO-2 仅 chain/adapter；TODO-3 仅 obs 插件）。
- TODO-1 若 pragma 导致 dataDB 初始化异常：直接 revert `ensureSQLitePragma`，恢复裸 DSN 即可（业务不受影响，仅回到锁冲突状态）。
