# RockSys 容错加固实施文档（IMPROVEMENT_PLAN-dev）

> 依据：`docs/IMPROVEMENT_PLAN.md`（设计文档）。本文档分阶段（P1→P4），任何 AI Agent 可无人工决策深度自主开发。
> 行号以当前代码为准（已核对 2026-08-07 快照）；实施时若行号漂移以符号定位为准。
> 通用纪律：全部改动后运行 `go test ./...` 与 `go vet ./...`；commit message 用中文、无 AI 协作署名；**commit 前须经用户确认**。

---

## P1：TODO-1 sqlite 连接参数补全（busy_timeout + WAL）

**目标**：DB 层对 sqlite 自动保证「写等待 5s + WAL 并发读」，从根源消除大多数 SQLITE_BUSY。对 mysql/postgres 无影响。

### P1.1 新增 `ensureSQLitePragma`（internal/db/db.go）

在 `internal/db/db.go` 的 `Open` 函数上方新增纯函数：

```go
// sqliteBusyTimeout / sqliteJournalMode 自动补全参数（modernc.org/sqlite DSN 参数）。
const (
	sqliteBusyTimeout = 5000 // ms，PRAGMA busy_timeout
	sqliteJournalMode = "WAL" // PRAGMA journal_mode
)

// ensureSQLitePragma 对 sqlite DSN 自动补全 busy_timeout 与 WAL 参数。
// 仅 driver=="sqlite" 时由 Open 调用；DSN 已含 _busy_timeout/_journal_mode/_pragma 或
// 驱动别名键（_timeout/_journal/_sync/_fk/_vacuum/_auto_vacuum）任一则原样返回
// （尊重显式配置，用户自行管理 pragma）。
// 拼接：裸路径用 "?" 连接；已有 "?" 参数用 "&" 连接；追加 "_busy_timeout=5000&_journal_mode=WAL"。
// 边界：":memory:" / "file::memory:" 前缀跳过补参（内存库无文件锁竞争，补参无意义）。
func ensureSQLitePragma(dsn string) string
```

**算法落地逻辑**：
1. 若 `dsn == ""` → 返回 `""`（空串不处理）。
2. 若 DSN 以 `:memory:` 或 `file::memory:` 开头 → 原样返回。
3. 用 `strings.Cut(dsn, "?")` 分离路径与 query 段；**仅对 query 段**调 `url.ParseQuery`（返回的 err 仅影响参数存在性判断，忽略不 abort——不因非法转义拒绝 DSN）。
4. 参数存在性判定：query 段中**任一参数名以 `_` 开头**（modernc 驱动所有 DSN 参数均以 `_` 前缀：`_busy_timeout`/`_journal_mode`/`_pragma`、别名键 `_timeout`/`_journal`/`_sync`/`_fk`/`_vacuum`/`_auto_vacuum`、以及 `_foreign_keys`/`_synchronous`/`_txlock`/`_time_format` 等）→ 视为已显式配置，原样返回（尊重显式配置，用户自行管理 pragma）。**禁止用子串匹配**（避免文件名/参数名恰好含关键字时误判"已配置"而漏补参）；普通参数（如 `cache=shared`，不以 `_` 开头）不视为已配置，正常追加。
5. 拼接分隔符：**原始 dsn 含 `?` 用 `&`，否则用 `?`**（与设计文档 §5.1「裸路径用 `?` 连接；已有 `?` 参数用 `&` 连接」一致；绝不把路径本身交给 query 解析器——`url.ParseQuery` 会把路径整体解析成键，导致裸路径误用 `&` 分隔）。
6. 返回 `dsn + sep + "_busy_timeout=5000&_journal_mode=WAL"`（常量拼装，禁止散落魔数）。

> import 变更：`internal/db/db.go` 现有 `strings` 保留（`strings.Cut` 可用）；`url.ParseQuery` 需新增 `net/url`。

### P1.2 接入 `Open`（internal/db/db.go:77）

```go
// 原：sqldb, err := sql.Open(driver, dsn)
dsn = ensureSQLitePragma(dsn)
sqldb, err := sql.Open(driver, dsn)
```

- 放在 `Open` 中 `sql.Open` 调用前、`driver` 校验之后（`driver` 已 `strings.ToLower`）。
- 仅在 `driver == "sqlite"` 时调用 `ensureSQLitePragma`；mysql/postgres 不调用（`if driver == "sqlite" { dsn = ensureSQLitePragma(dsn) }`）。

### P1.3 修改默认 DB_DSN（cmd/rocksys/main.go:214）

```go
// 原：cfgMgr.Register(&dbDSN, "DB_DSN", "rocksys.db", "数据库连接串（sqlite 为文件路径）")
cfgMgr.Register(&dbDSN, "DB_DSN", "rocksys.db?_busy_timeout=5000&_journal_mode=WAL", "数据库连接串（sqlite 为文件路径；默认已含 busy_timeout=5000 与 WAL，可显式覆盖）")
```

双保险：默认值带参 + Open 层自动补参（覆盖老部署 `.env` 中显式裸值）。

### P1.4 单测（internal/db/db_test.go 追加）

| 用例 | 输入 | 期望 |
|---|---|---|
| `TestEnsureSQLitePragmaBarePath` | `"/tmp/x.db"` | `"/tmp/x.db?_busy_timeout=5000&_journal_mode=WAL"` |
| `TestEnsureSQLitePragmaWithParams` | `"/tmp/x.db?cache=shared"` | `"/tmp/x.db?cache=shared&_busy_timeout=5000&_journal_mode=WAL"` |
| `TestEnsureSQLitePragmaAlreadySet` | `"/tmp/x.db?_busy_timeout=1000"` | 原样返回 |
| `TestEnsureSQLitePragmaJournalSet` | `"/tmp/x.db?_journal_mode=MEMORY"` | 原样返回 |
| `TestEnsureSQLitePragmaPragmaSet` | `"/tmp/x.db?_pragma=busy_timeout(3000)"` | 原样返回 |
| `TestEnsureSQLitePragmaMemory` | `":memory:"` / `"file::memory:?cache=shared"` | 原样返回 |
| `TestEnsureSQLitePragmaPathWithEqAmp` | `"/tmp/x=y&z.db"`（文件名含 `=` 与 `&`） | `"/tmp/x=y&z.db?_busy_timeout=5000&_journal_mode=WAL"`（路径原样保留、用 `?` 连接，路径不交给 query 解析器） |
| `TestEnsureSQLitePragmaEmpty` | `""` | `""` |
| `TestOpenSQLiteAutoPragma`（集成） | `Open("sqlite", filepath.Join(t.TempDir(), "x.db"), "sql")` | 打开成功后查询：`PRAGMA journal_mode` 返回 `wal`；`PRAGMA busy_timeout` 返回 `5000`（经 `d.EasyDB().GetSqlDB()` 用 `*sql.DB.QueryRow` 查询） |

> 说明：`ensureSQLitePragma` 是纯函数、不感知 driver；"mysql/postgres 不受影响"由 Open 调用点 `if driver == "sqlite" { dsn = ensureSQLitePragma(dsn) }` 保证（见 P1.2），无需单独单测。

### P1.5 文档更新

- `docs/DEV_HANDBOOK.md` §C.2 数据库铁律区补充 DSN 参数约定：「sqlite DSN 默认自动补 `_busy_timeout=5000&_journal_mode=WAL`（Open 层自动补全 + 默认值带参双保险）；已显式含 pragma 参数则尊重显式配置；mysql/postgres 不受影响；WAL 依赖本地文件系统，网络盘/NFS 部署需评估」。

### P1 验收标准

- `go test ./internal/db/...` 全绿；`go test ./...` 全绿。
- 启动后 `sqlite3 rocksys.db "PRAGMA journal_mode;"` 输出 `wal`。
- 并发压测下 obs 日志不再出现 `SQLITE_BUSY`（或显著下降）。

---

## P2：TODO-3 第一期 obs 写入失败重试 + 连续失败告警 + 指标暴露

**目标**：瞬时锁冲突可自动补救一次；丢弃/失败可观测（暴露到 admin 指标）。**不引入自动切换后端**。

### P2.1 常量与字段（plugins/obs/store.go）

`AsyncStore` 结构体（现 store.go:55-65）新增字段：

```go
// 重试与告警常量（集中定义，禁止魔数散落多处）。
const (
	obsRetryTimes        = 1          // 底层写失败后的重试次数（总尝试 = retryTimes + 1）
	obsRetryDelay        = 50 * time.Millisecond // 重试间隔
	obsFailThreshold     = 10         // 连续失败阈值：达到后告警升级为 Error
)

// AsyncStore 结构体新增：
consecutiveFails atomic.Int64 // 连续底层写失败次数（成功清零；队列满丢弃不计入）
```

### P2.2 新增 `writeBatchWithRetry`（store.go）

```go
// writeBatchWithRetry 写一批记录：失败重试 obsRetryTimes 次（间隔 obsRetryDelay）。
// 成功 → consecutiveFails 清零，返回 nil；
// 全部失败 → drop 计数累加整批、consecutiveFails+1，达 obsFailThreshold 告警升级 log.Error（提示运维检查 DB 或热切 OBS_STORE），否则 log.Warn；返回最终 err。
// 注意：consecutiveFails 只统计底层 Write 失败；Write 成功后的 s.Flush 失败（当前 FileStore/DBStore Flush 恒返回 nil，实际无影响）不计入。
func (a *AsyncStore) writeBatchWithRetry(s Store, batch []*AccessRecord) error
```

**算法落地逻辑**：
1. `var err error`
2. `for i := 0; i <= obsRetryTimes; i++`：
   - `err = s.Write(batch)`；`err == nil` → `a.consecutiveFails.Store(0)`；`return nil`。
   - `i < obsRetryTimes` → `time.Sleep(obsRetryDelay)`。
3. 循环结束（仍失败）：
   - `fails := a.consecutiveFails.Add(1)`
   - `a.drop.Add(int64(len(batch)))`
   - `fails >= obsFailThreshold` → `log.Error("obs: 访问日志写入连续失败，请检查数据库或热切 OBS_STORE", "store", s.Name(), "err", err, "consecutive_fails", fails, "drop_count", a.drop.Load())`
   - 否则 → `log.Warn("obs: 访问日志写入失败，丢弃该批", "store", s.Name(), "err", err, "consecutive_fails", fails, "drop_count", a.drop.Load())`
   - `return err`

### P2.3 改造 `writePending` / `flushAll`（store.go:187-225）

`writePending`（现 198-201 行）：

```go
// 原：
// if err := s.Write(batch); err != nil {
//     a.drop.Add(int64(len(batch)))
//     log.Warn(...)
// }
// 改为：
_ = a.writeBatchWithRetry(s, batch) // 重试/计数/告警已内聚；worker 循环无需感知 err
```

`flushAll`（现 214-224 行）：写失败语义不变（err 向上传播），但 drop/计数/告警已由 writeBatchWithRetry 内聚，**不得重复 drop**：

```go
var err error
if len(batch) > 0 {
	err = a.writeBatchWithRetry(s, batch) // 内部已处理 drop 计数与告警
}
if cerr := s.Flush(context.Background()); err == nil {
	err = cerr
}
return err
```

### P2.4 暴露 `ConsecutiveFails`（store.go，DropCount 旁 169-170 行）

```go
// ConsecutiveFails 返回当前连续底层写失败次数（告警观测用）。
func (a *AsyncStore) ConsecutiveFails() int64 { return a.consecutiveFails.Load() }
```

### P2.5 Obs 暴露统计方法（plugins/obs/obs.go）

在 `Obs` 上新增（放在 `Metrics` 方法 191-192 行附近）：

```go
// StoreStats 返回当前存储后端的丢弃与连续失败计数（admin 观测用）。
func (o *Obs) StoreStats() (dropCount, consecutiveFails int64) {
	as := o.sink.Load().(*AsyncStore)
	return as.DropCount(), as.ConsecutiveFails()
}
```

### P2.6 admin 指标暴露（plugins/obs/admin.go:29-43 `Metrics`）

```go
func (h *AdminHandler) Metrics(w http.ResponseWriter, r *http.Request) {
	// ... 现有 nil 检查与 Snapshot 不变 ...
	dropCount, consecutiveFails := h.obs.StoreStats()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"qps":               s.QPS,
		"p95_ms":            s.P95,
		"p50_ms":            s.P50,
		"p99_ms":            s.P99,
		"error_rate":        s.ErrorRate,
		"drop_count":        dropCount,
		"consecutive_fails": consecutiveFails,
	})
}
```

原字段不变，向后兼容。

### P2.7 单测（新建 plugins/obs/store_test.go）

新增 mock 存储（放 store_test.go）：

```go
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
func (s *failStore) SizeBytes() (int64, error)               { return 0, nil }
func (s *failStore) Flush(ctx context.Context) error         { return nil }
func (s *failStore) Close() error                            { return nil }
func (s *failStore) setFail(f bool)                          { s.mu.Lock(); s.fail = f; s.mu.Unlock() }
func (s *failStore) calls() int                              { s.mu.Lock(); defer s.mu.Unlock(); return s.writeCalls }
func (s *failStore) saved() []*AccessRecord                  { s.mu.Lock(); defer s.mu.Unlock(); return append([]*AccessRecord(nil), s.records...) }
```

| 用例 | 场景 | 断言 |
|---|---|---|
| `TestWritePendingRetryThenDrop` | `failStore` fail=true；`NewAsyncStore(&failStore{...})`；`Write` 一条 | **先轮询 `DropCount()==1`（或 `ConsecutiveFails()==1`）确保 worker 已完成全部尝试**，再断言 `calls() == obsRetryTimes+1`（总尝试 2 次）、`ConsecutiveFails()==1`；Write 本身不阻塞 |
| `TestWritePendingRetryThenSuccess` | fail 初始 true；`Write` 入队后立即 `setFail(false)`（第 2 次尝试成功） | **轮询 `saved()` 长度 == 1**（唯一可靠的完成信号：worker 写入成功后 records 长度单调为 1；不能用 `ConsecutiveFails()==0` 作信号——初始值即 0 会误通过）后断言 `DropCount()==0`、`saved()` 含该条记录 |
| `TestConsecutiveFailsThreshold` | fail=true；**逐条驱动**：Write 一条 → 轮询 `DropCount()==i`（该条已处理）→ 再写下一条，共 N 条（N=obsFailThreshold，远小于 asyncCap=4096，避免触发队列满丢弃） | `ConsecutiveFails()==N`（每批恰 1 条，整批失败 +1） |
| `TestFlushAllRetry` | fail=true，`Flush(ctx)`（batch 非空） | 返回 error；`calls() == obsRetryTimes+1`；`DropCount()==len(batch)`（不重复计数） |
| `TestQueueFullDropNotCountedAsFail` | 队列满场景（复用现有测试思路） | `DropCount()==1` 但 `ConsecutiveFails()==0`（队列满丢弃不计入连续失败） |

> 同步：现有 `obs_test.go:293` `TestAsyncStoreQueueFullDrops` 保持通过（队列满丢弃行为不变）。

### P2.8 文档更新

- `docs/DEV_HANDBOOK.md` obs 章节（§14 附近）补充丢弃/失败语义：「obs 底层写失败自动重试 1 次（50ms）；仍失败丢弃该批并告警，连续失败 ≥10 次告警升级 Error；/admin/metrics 暴露 drop_count 与 consecutive_fails；队列满丢弃不计入连续失败」。

### P2 验收标准

- 模拟 DB 锁定持续：日志先见重试、后见丢弃告警、连续失败升级 Error；请求侧全程正常。
- `/admin/metrics` 可见 `drop_count` 与 `consecutive_fails`。
- `go test ./plugins/obs/...` 与 `go test ./...` 全绿。

---

## P3：TODO-2 请求路径 panic 兜底（recover）

**目标**：单请求故障时：客户端收到明确 5xx 而非连接中断；日志含中间件名 + 完整堆栈；其他请求零影响。

**附带语义**：Head/Middle panic → `Execute` 返回 false → `Adapter.Handler` 提前返回，Tail 阶段（含 obs 访问日志/指标）不执行——该请求无 obs 日志，属合理行为（不视为缺陷）。

### P3.1 新增 `safeHandle`（internal/chain/impl.go）

```go
// safeHandle 包装单个中间件执行：panic 时记录中间件名 + 完整堆栈，尝试写 500，返回 false（中断链）。
// 写 500 用 http.Error：主流场景（panic 时未写响应）正常写 500；若中间件违规已写过响应
// （违反 interface.go:13-15 契约——返回 true 的中间件禁止写响应），net/http 状态码不覆盖、
// body 可能追加，属退化行为但不崩溃。
func safeHandle(m Middleware, ctx *Context) (next bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("chain: middleware panic recovered", "name", m.Name(), "panic", r, "stack", string(debug.Stack()))
			http.Error(ctx.W, "internal server error", http.StatusInternalServerError)
			next = false
		}
	}()
	return m.Handle(ctx)
}
```

依赖新增 import：`runtime/debug`、`rocksys` 的日志包（`github.com/iotames/easyserver/log`，chain 包内 adapter.go 已在用）。

### P3.2 改造 `Chain.Execute`（internal/chain/impl.go:60-77）

```go
for _, m := range head {
	if !safeHandle(m, ctx) {
		return false
	}
}
for _, m := range middle {
	if !safeHandle(m, ctx) {
		return false
	}
}
return true
```

语义不变：任一 false 中断链；panic 视为该中间件返回 false（中断 + 已写 500）。

### P3.3 ResponseHook 循环 recover（internal/chain/adapter.go:102-106）

```go
// 原：
// for _, h := range a.chain.ResponseHooks(Tail) {
//     if err := h.OnResponse(ctx); err != nil {
//         log.Warn("response hook error", "name", hookName(h), "err", err)
//     }
// }
// 改为：单 hook panic → log.Error（hook 名 + 堆栈），继续后续 hook（与"err 不中断后续 hook"语义一致）。
for _, h := range a.chain.ResponseHooks(Tail) {
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("chain: response hook panic recovered", "name", hookName(h), "panic", r, "stack", string(debug.Stack()))
			}
		}()
		if err := h.OnResponse(ctx); err != nil {
			log.Warn("response hook error", "name", hookName(h), "err", err)
		}
	}()
}
```

> 注意：ResponseHook panic **不写 500**（响应阶段可能已写回客户端，且 hook 是旁路观测，写 500 会污染已发出的响应）；仅记录日志继续。这是与 Head/Middle 阶段（未转发、可安全写 500）的本质区别，文档必须写明。

### P3.4 easyserver `ServeHTTP` 兜底（必须，最后防线）（easyserver/httpsvr/server.go:54-68）

最后防线：兜住 `Forward` 阶段 panic（`Adapter.Handler` 步骤 7 的 `a.forward` 不在 safeHandle 覆盖内）与未来新中间件漏包。

```go
func (s *EasyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.initOnce.Do(func() { s.listenPrepare() })
	dataFlow := NewDataFlow()
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("httpsvr: panic recovered: %v\n%s", rec, debug.Stack())
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	}()
	for _, m := range s.middles {
		if !m.Handler(w, r, dataFlow) {
			break
		}
	}
}
```

import 增补：`runtime/debug`。此步为**必须**（P3 主流程的一部分）：不实施则 Forward panic 仍会击穿到 net/http 导致连接中断，与任务卡"单请求健壮性"目标冲突。改动后必须跑 `go test ./easyserver/...`（含 server_test.go 回归）。

### P3.5 单测（internal/chain/chain_test.go 追加）

新增 panic 假中间件：

```go
// panicMiddleware 构造即 panic 的中间件（type 参数区分触发时机）。
type panicMiddleware struct{ name string }
func (m *panicMiddleware) Name() string { return m.name }
func (m *panicMiddleware) Handle(ctx *Context) bool { panic("boom-" + m.name) }

// panicHook 实现 ResponseHook 的 panic Tail 中间件。
type panicHook struct{ name string }
func (h *panicHook) Name() string { return h.name }
func (h *panicHook) Handle(ctx *Context) bool { return false }
func (h *panicHook) OnResponse(ctx *Context) error { panic("boom-hook-" + h.name) }
```

| 用例 | 场景 | 断言 |
|---|---|---|
| `TestExecuteHeadPanicRecovers` | Head 槽位挂 panicMiddleware | `Execute` 返回 false（不 panic 上抛）；`rec.Code == 500` |
| `TestExecuteMiddlePanicRecovers` | Middle 槽位挂 panicMiddleware | 同上 |
| `TestExecutePanicDoesNotKillChain` | 同一 Chain 上 panic 请求后，追加一个正常请求 | 正常请求仍返回 true / 正常执行（链未被污染） |
| `TestResponseHookPanicContinues` | Tail 挂两个 hook：**执行顺序第一个**为 panicHook、第二个为 tailHook（记录 gotCode）——注意 `ResponseHooks(Tail)` 返回注册逆序（impl.go:92），后注册者先执行；文档中"第一个"均指执行顺序 | Adapter.Handler 不 panic；第二个 hook 的 `OnResponse` 仍被调用且 `gotCode` 正确；客户端响应正常（rec.Code == 200） |
| `TestResponseHookPanicAfterWriteFinal` | **执行顺序第一个** hook 调 `WriteFinal` 后 panic，第二个 hook 正常 | 客户端收到改写后的响应，第二个 hook 仍执行 |

### P3.6 文档更新

- `ARCHITECTURE.md` §5.2 降级语义补充 panic 行为说明：「链中间件 panic 被 safeHandle 兜底：记录中间件名 + 堆栈、写 500、中断该请求链，其余请求零影响；ResponseHook panic 仅记录日志继续后续 hook（响应阶段不写 500）；recover 只兜底不吞错——完整堆栈必入日志」。

### P3 验收标准

- panic 中间件存在时：客户端收到 500（非连接重置），并发请求全部正常。
- 日志可定位：中间件名 + panic 堆栈。
- `go test ./internal/chain/...`、`go test ./easyserver/...`（P3.4 已改 server.go）与 `go test ./...` 全绿。

---

## P4：整体验证与收尾

### P4.1 全量回归

```bash
go build ./...
go vet ./...
go test ./...
```

### P4.2 手工验证清单

1. **P1**：启动 `rocksys`（默认配置）→ `sqlite3 rocksys.db "PRAGMA journal_mode;"` 输出 `wal`。**注意：`busy_timeout` 是连接级 PRAGMA、不持久化，sqlite3 CLI 新连接查询恒返回 0，无法用 CLI 验证——改用 P1.4 集成测试 `TestOpenSQLiteAutoPragma` 在应用内验证（查询返回 5000）。**
2. **P3**：临时在某个中间件注入 panic（或构造单测覆盖），压测并发请求 → 全部 500 且进程存活、日志含堆栈。
3. **P2**：模拟 DB 锁定（如 `chmod` 只读 / 长事务占锁）→ obs 日志出现重试 → 丢弃告警 → 连续失败升级 Error；`curl /admin/metrics` 可见 `drop_count`/`consecutive_fails`。

### P4.3 commit 与回填

- 每阶段独立 commit（P1/P2/P3 互不依赖、可单独 revert），message 用中文、无 AI 协作署名；**commit 前须经用户确认**。
- 每项完成后在 `docs/TODO_IMPROVEMENTS.md` 对应任务卡回填「完成情况」：验证命令与结果、改动文件清单。

### P4.4 回滚策略

- P1：revert `ensureSQLitePragma` + 默认值恢复裸 `rocksys.db`。
- P2：revert obs 插件（store.go / obs.go / admin.go / store_test.go）。
- P3：revert chain（impl.go / adapter.go / chain_test.go）与 easyserver/server.go（P3.4 为必须项，一并回滚）。

---

## 依赖顺序总览

```
P1（TODO-1 sqlite pragma）──先行──▶ P2（TODO-3 重试+指标）
                                        │
P3（TODO-2 recover）◀──逻辑独立，最后收尾──┘
```
- P1 先于 P2：P1 消除 SQLITE_BUSY 根源，P2 的重试触发频率随之大降，便于观察 P2 效果。
- P3 独立，可随时插入；作为架构级最后防线放最后，避免与 P1/P2 改动相互干扰回归定位。
- TODO-2b / TODO-3b 本期不做（见设计文档 §4 可选增强）。
