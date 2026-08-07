# RockSys 容错加固实施计划（设计文档）

> 依据：`docs/TODO_IMPROVEMENTS.md`（组件容错改进计划任务卡）。
> 本设计文档把任务卡升级为可执行蓝图；实施细节见 `docs/IMPROVEMENT_PLAN-dev.md`。
> 状态：待用户确认后实施。本文件只写现状与新增，不写历史修改记录。

---

## 1. 现状

- 转发链 = 纯 HTTP 反向代理（`internal/engine/engine.go:79 Forward`），不碰数据库、不依赖组件；业务转发不依赖任何组件，组件故障被隔离在旁路/异步路径，**业务连续性达标**。
- obs/mq/copy 等均为异步或旁路，故障只丢日志/消息，不阻塞请求（`plugins/obs/store.go:98-118` 队列满丢弃、`plugins/mq/mq.go` 跳轮）。
- 三个风险点不威胁业务连续性，但损害**可观测性可靠性**与**单请求健壮性**：

| # | 风险点 | 现状位置 |
|---|---|---|
| R1 | sqlite 默认无 `busy_timeout`/`WAL` → SQLITE_BUSY 频繁 | `cmd/rocksys/main.go:214` 默认 `DB_DSN="rocksys.db"`（裸路径）；`internal/db/db.go:77` `sql.Open(driver, dsn)` 原样透传 |
| R2 | 请求路径无 recover 兜底 → 中间件 panic 击穿到 net/http，单请求连接中断 | `internal/chain/impl.go:60-77` `Chain.Execute`；`internal/chain/adapter.go:54-117` `Adapter.Handler`；`easyserver/httpsvr/server.go:54-68` `ServeHTTP`（本地 replace 子库，可改） |
| R3 | obs 写失败无重试、运行期不降级 → 瞬时锁错误直接整批丢弃，无补救 | `plugins/obs/store.go:187-202` `writePending`；`store.go:206-225` `flushAll`；`plugins/obs/obs.go:251-272` `buildStore` |

> 行号核对说明：任务卡原文 R1 引用 `cmd/rocksys/main.go:171`，经核对实际默认值注册在 `main.go:214`（`cfgMgr.Register(&dbDSN, "DB_DSN", "rocksys.db", ...)`），本文档以代码现状为准。

---

## 2. 新增功能总览

- **TODO-1（P1）**：DB 层对 sqlite 自动保证「写等待 5s + WAL 并发读」，从根源消除大多数 SQLITE_BUSY。对 mysql/postgres 无影响。
- **TODO-2（P3）**：请求路径 panic 兜底（recover）：单请求故障时客户端收到明确 5xx 而非连接中断；日志含中间件名 + 完整堆栈；其他请求零影响。
- **TODO-3 第一期（P2）**：obs 写入失败重试 1 次 + 连续失败计数告警升级 + drop/连续失败指标暴露。**不引入自动切换后端的复杂性**。

实施顺序（依赖关系）：P1（TODO-1）→ P2（TODO-3 第一期）→ P3（TODO-2）。
理由：P1 直接消除 SQLITE_BUSY 根源，使 P2 的重试触发频率大降；P2 防御残余锁冲突并提升可观测性；P3 是架构级最后防线，逻辑独立、最后收尾。

---

## 3. 基座（必做，P1–P3）

| 阶段 | 内容 | 核心文件 |
|---|---|---|
| P1 | `ensureSQLitePragma` 自动补参 + 默认 `DB_DSN` 带参 + 单测 | `internal/db/db.go`、`cmd/rocksys/main.go`、`internal/db/db_test.go` |
| P2 | obs `writePending`/`flushAll` 重试 1 次；`consecutiveFails` 计数与告警升级；admin 指标暴露 | `plugins/obs/store.go`、`plugins/obs/obs.go`、`plugins/obs/admin.go`、`plugins/obs/obs_test.go` |
| P3 | `safeHandle` 链内 recover；ResponseHook 循环 recover；easyserver `ServeHTTP` 兜底（最后防线） | `internal/chain/impl.go`、`internal/chain/adapter.go`、`internal/chain/chain_test.go`、`easyserver/httpsvr/server.go` |

---

## 4. 可选增强（二期评估，本次不做）

- **TODO-2b**：连续 panic 计数超阈值 → 自动 `hotswap.Disable` 该中间件（需可配置开关、默认关闭）。评估点：偶发 panic 不该触发摘除，阈值与恢复策略需评审。
- **TODO-3b**：运行期熔断自动降级：连续失败超阈值 → 自动 `Replace` 到 file 后端；恢复探测成功后切回 db。评估点（重要）：
  - `obs.go:Query` 只转发当前后端，自动切换后旧 db 数据查不到；需决定查询合并（双后端并行查）或接受"切换后只能查新后端"。
  - 自动切换涉及"后端工厂"注入（AsyncStore 是通用包装，不感知 file 如何构建）——架构上需把构建逻辑上提到 Obs 层并注入失败回调。
  - 建议二期单独立项评审，第一期不做。

---

## 5. 关键机制

### 5.1 P1：sqlite DSN 自动补参（`ensureSQLitePragma`）

- 仅 `driver == "sqlite"` 时生效；mysql/postgres 原样返回（由 Open 调用点 `if driver == "sqlite"` 保证）。
- 判定"已显式配置"：分离 query 段后，**任一参数名以 `_` 开头**（modernc 驱动所有 DSN 参数均以 `_` 前缀，含 `_busy_timeout`/`_journal_mode`/`_pragma`、别名键 `_timeout`/`_journal`/`_sync`/`_fk`/`_vacuum`/`_auto_vacuum` 等）→ 原样返回（尊重显式配置，用户自行管理 pragma）。**不用子串匹配**；普通参数（如 `cache=shared`）不视为已配置。
- 拼接规则：裸路径用 `?` 连接；已有 `?` 参数用 `&` 连接；追加 `_busy_timeout=5000&_journal_mode=WAL`。
- 边界：`:memory:` / `file::memory:` 前缀跳过补参（内存库无文件锁竞争，WAL/busy_timeout 无意义，避免无谓 PRAGMA 执行）。
- 已验证驱动 `modernc.org/sqlite v1.55.0` 支持 DSN 参数（源码 `sqlite.go:264/285/381`）：
  - `_busy_timeout=5000` → `PRAGMA busy_timeout = 5000`（每连接生效）
  - `_journal_mode=WAL` → `PRAGMA journal_mode = WAL`
  - `_pragma=busy_timeout(5000)` 可重复，原样执行

### 5.2 P3：链内 recover 兜底（`safeHandle`）

- `Chain.Execute` 的 Head/Middle 循环对每个中间件用 `safeHandle` 包装：`defer recover()`，panic 时 `log.Error`（中间件名 + 完整堆栈），尝试写 500，返回 false（中断链）。
- 写 500 用 `http.Error`：若 panic 发生在未写响应时（主流场景）→ 正常写 500；若中间件违规已写过响应（违反 `interface.go:13-15` 契约）→ net/http 状态码不覆盖、body 可能追加，属退化行为不崩溃（注释说明即可）。
- ResponseHook 循环（`adapter.go:102-106`）外包 recover：单个 hook panic → `log.Error`（hook 名 + 堆栈），继续后续 hook（与现有"err 不中断后续 hook"语义一致）。**响应阶段不写 500**（可能已写回客户端，旁路 hook 写 500 会污染响应）。
- **最后防线（必须）**：`easyserver/httpsvr/server.go` `ServeHTTP` 外层 recover：写 500，兜住 `Forward` 阶段 panic 与未来新中间件漏包（easyserver 为本地 replace 子库，可改且仅 rocksys 使用）。
- **附带语义**：Head/Middle panic → `Execute` 返回 false → `Adapter.Handler` 提前返回，Tail 阶段（ResponseHook，含 obs 日志）不执行——该请求无访问日志/指标，属合理行为（记录在案）。

### 5.3 P2：obs 写入重试 + 连续失败计数

- `writePending` / `flushAll`：底层 `Write` 失败后重试 1 次（间隔 50ms），仍失败才丢弃 + 告警。
- `AsyncStore` 新增 `consecutiveFails` 原子计数：写失败 +1、成功清零；连续失败 ≥ 阈值（10 次）→ 告警升级为 `log.Error`（提示运维检查 DB 或热切 `OBS_STORE`）。
- 重试次数/间隔/阈值集中为包级常量（禁止魔数散落多处）。
- 指标暴露：`/admin/metrics` 增加 `drop_count`、`consecutive_fails`（读取当前 AsyncStore 运行时状态，不改 Metrics 聚合结构）。

---

## 6. API 变更

| 端点 | 变更 |
|---|---|
| `GET /admin/metrics` | 响应新增 `drop_count`（累计丢弃条数）、`consecutive_fails`（当前连续失败次数）两字段；原字段不变，向后兼容 |

无其他对外 API 变更（`Store` 接口、`Middleware` 接口、`ResponseHook` 接口均不改变）。

---

## 7. 配置

| 项 | 现状 | 变更 |
|---|---|---|
| `DB_DSN` 默认值 | `rocksys.db`（裸路径） | `rocksys.db?_busy_timeout=5000&_journal_mode=WAL`（双保险：显式 `.env` 裸值由 Open 层自动补参覆盖，无需改 `.env`） |
| 新增配置项 | 无 | 无（重试/阈值均为代码常量，不进配置） |

文档同步：
- `docs/DEV_HANDBOOK.md`：数据库铁律（C.2 附近）补充 DSN 参数约定；obs 丢弃/失败语义说明。
- `ARCHITECTURE.md`：§5.2 降级语义补充 panic 行为说明。

---

## 8. 风险与注意

| 风险 | 等级 | 缓解 |
|---|---|---|
| WAL 依赖本地文件系统：网络盘/NFS 上不可靠 | 中 | 部署在网络盘需评估或跳过 WAL 只留 busy_timeout；文档标注 |
| pragma 失败会让连接 Ping 失败 → `main.go` 已容错（dataDB=nil → obs 回退 file、mq 不注册） | 中 | 业务不中断，仅功能降级；单测必须覆盖参数拼接正确性，避免手误写坏 DSN |
| recover 会掩盖编程错误 | 中 | 必须记录完整堆栈，禁止吞掉 panic；文档标注 |
| panic 可能发生在持锁状态：实例级 mutex 未释放会锁死该实例 | 低 | 配合可选增强 2b（连续 panic 自动摘除）二期评估；本期只保证单请求不中断 |
| 重试延长 worker 阻塞，队列满丢弃概率略增 | 低 | 1 次 50ms 影响可忽略（总量有限）；常量集中定义 |
| 老部署 `.env` 显式 `DB_DSN` 裸值 | 低 | Open 层自动补参已覆盖，无需改 `.env` |

---

## 9. 回滚策略

- 各项改动相互独立、可单独 revert（P1 仅 db 层 + 默认值；P2 仅 obs 插件；P3 含 chain/adapter 与 `easyserver/httpsvr/server.go`）。
- P1 若 pragma 导致 dataDB 初始化异常：直接 revert `ensureSQLitePragma`，恢复裸 DSN 即可（业务不受影响，仅回到锁冲突状态）。
