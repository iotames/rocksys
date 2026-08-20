# WAF 防护监控与拦截统计改造方案

> 状态：方案设计（代码引用已复核，开放问题已定论，待评审 → 分批实施）
> 起草：2026-08-20　|　参考：`D:\projects\golang\cdnguard\main\sql\init_tables.sql`
> 维护：随实施进度更新本文"实施进度"与"开放问题"两节，作为持续改进锚点。

---

## 1. 背景与目标

当前 WAF 组件（`plugins/shield`）能拦截异常流量，但**拦截之后没有任何记录、没有统计**：命中了多少次、被哪种规则拦的、来自哪些 IP、拦截趋势如何——一概不知。防护开了等于"黑箱"，无法度量防护效果，也无法据此调优规则。

同时，obs 的 access_log 表记录全量放行请求，但**没有任何清理机制**——DBStore 无 prune 逻辑（仅已弃用的 file_store 有按天清理），数据库无限膨胀。

**目标**：让 shield 的每一次拦截都可追溯、可统计、可观测，并建立全量 HTTP 请求记录的清理机制：

1. **拦截事件落库**——每次拦截留一条明细（含规则类型、IP、UA、路径、命中特征）。
2. **查询时聚合统计**——看板一眼看出"今天拦了多少、哪类攻击最多、Top 攻击源 IP"。
3. **admin API + WebUI**——管理侧可查明细、看统计趋势。
4. **数据保留与清理**——拦截明细保留 90 天、常规日志保留 7 天，默认不开启，登录管理后台时未开启则警告提示。
5. **不破坏现有架构**——遵循配置中心红线、ScriptHub 热更、SQL 三方言外置等铁律。

---

## 2. 现状分析（Gap）

经代码核查（`plugins/shield/shield.go`、`internal/chain/adapter.go`、`plugins/obs/`、`sql/`、`internal/adminapi/`）确认：

| 维度 | 现状 | 问题 |
|---|---|---|
| shield 拦截后 | `http.Error` + `return false`（`shield.go:359-380`、`shield.go:464-506`） | **零计数、零持久化** |
| 拦截请求流向 | `adapter.go:70-71` 链中断直接 `return`，不进步骤8 ResponseHooks | **obs 收不到被拦请求**，access_log 对攻击完全盲区 |
| access_log 表 | 14 维度列 + extra（`access_log_create_table.sql`） | **无 WAF 命中/拦截/规则类型字段**（不补，各记各的） |
| access_log 清理 | DBStore 无 prune 逻辑，`retentionDays` 仅用于已弃用的 file_store | **DB 日志无限膨胀** |
| 统计表/接口 | 全项目 grep `shield_event/block_log/waf_stats/blocked_count` 均无匹配 | **无任何 WAF 统计** |
| shield 规则类型 | IP 黑名单 / 限流 / 方法 / 体积 / 风险路径 / 路径遍历 / SQL 注入 / XSS / 爬虫 UA / 路径规则 deny（10 类） | 类型已齐，可直接映射为 `block_type` 枚举 |

**核心架构约束（决定方案走向）**：拦截请求被链短路，走不到 obs——所以**拦截事件必须在 shield 拦截点就地记录**，不能依赖 obs。obs 负责放行侧全量日志，shield_event 负责拦截侧明细，两者**各记各的，互不关联**。

---

## 3. 参考设计（cdnguard 借鉴点）

`cdnguard/init_tables.sql` 的可借鉴结构（丢弃其 CDN 文件/七牛相关表）：

- **`block_requests` 表**：拦截明细 + `block_type SMALLINT`（0=IP黑名单 1=漏洞扫描 2=爬虫 3=异常UA）。
- **`statis` 表**：按日聚合——`statis_date` + `request_count` + `blocked_count` + 各分类 `blocked_*_count` + `request_size/blocked_size`。
- **`ip_white_list/ip_black_list` 表**：可管理名单，唯一约束防重复，`black_type` 区分名单类型。

rocksys 改造时**不照搬 cdnguard 的 `timestamp DEFAULT CURRENT_TIMESTAMP`**，改用各方言原生时间类型（PG `TIMESTAMPTZ`、MySQL `DATETIME(3)`、SQLite `DATETIME`，Go 侧传 `time.Time`）。SQL 已按方言拆分目录（`sql/postgres/`、`sql/mysql/`、`sql/sqlite/`），各方言各写各的建表脚本和聚合查询，充分发挥原生时间函数优势。block_type 从 4 类细化为 rocksys 的 10 类。

**不建物化聚合表**：借鉴 cdnguard statis 表的思路，但 rocksys 采用查询时聚合（`GROUP BY block_type, date(time)`），不建 daily_stats 物化表——实现简单、免维护聚合任务、明细量在 90 天保留期内索引足够支撑。

---

## 4. 架构设计

### 4.1 数据流

```
请求 → adapter.Handler
       → chain.Execute (Head=shield.Handle)
            ├─ 拦截命中 → recordEvent(blockType) ─┬→ 内存计数器(atomic, 滑动窗口, 供实时看板)
            │   http.Error + return false            └→ 异步落库(buffered chan → 后台批量 INSERT shield_event)
            └─ 放行 → 后续中间件 → 转发 → obs.OnResponse(Tail) → access_log
```

- **拦截侧**（shield_event）：shield 拦截点就地记录，异步入库。obs 收不到（架构短路），由 shield 自管。
- **放行侧**（access_log）：obs 照常记录全量访问，**不补 WAF 标记**——放行了就是符合规则，不越界，下一层的事。
- **互不关联**：shield_event 和 access_log 各记各的，不做 trace_id 串联、不做跨表关联。

### 4.2 记录器：EventRecorder

新增 `plugins/shield/event_recorder.go`，复用 `obs.DBStore` 范式（注入 `*db.DB`、SQL 外置、`{table}` 占位符）：

```
EventRecorder
├─ edb      *easydb.EasyDb    // 复用统一数据访问层
├─ sqls     db.SQLSource      // SQL 脚本源（shield_event_*.sql）
├─ counter  *eventCounter     // 内存计数：无锁 atomic 分桶滑动窗口（见下方实现说明）
└─ ch       chan ShieldEvent  // 异步落库缓冲通道
```

- **内存计数器**：**无锁 atomic 分桶**——固定 60 桶数组，桶内 `slot/total/counts[10]` 全部 `atomic.Int64`，`Add` 热路径零锁：CAS 抢占过期桶（`slot` 变更后清零）后原子自增，`Snapshot` 无锁遍历。滑动窗口 1 分钟（仿 `obs.metrics` 窗口语义），供 `/admin/shield/metrics` 实时读取，无需查库。
- **无锁边界（可容忍）**：同一桶被相距 60s 整倍数的两个 slot 并发抢占时，CAS 保证 slot 唯一，被清空侧的并发自增可能丢失 1-2 条计数——统计类指标可容忍，与 channel 满丢弃同哲学（§12 风险表）。
- **异步落库**：拦截热路径只做 `counter.Add` + 非阻塞 `ch <- event`（满则丢弃并记 `dropped` 计数，不阻塞转发）；后台 goroutine 每 N 条或每 T 秒批量 `INSERT`。
- **优雅停机**：`Stop()` 时 flush channel 剩余事件（防丢），随组件生命周期（`Manager.Shutdown`）。
- **注入方式：setter**（Q1 决策）：`shield.New` 签名不变（仍为 `New(cfgMgr, hubs...)`），DB 就绪后由 `main.go` 调 `shieldMw.SetEventRecorder(recorder)` 注入。shield 不依赖 DB——未注入时仍正常拦截，只不记录。

### 4.3 block_type 枚举

`plugins/shield/block_type.go` 定义常量（SMALLINT 存储，数值稳定可排序）：

| 值 | 常量 | 对应拦截点 | HTTP 状态 |
|---|---|---|---|
| 1 | BlockIPBlacklist | `shield.go:360` IP 黑名单 | 403 |
| 2 | BlockRateLimit | `shield.go:378` 令牌桶限流 | 429 |
| 3 | BlockMethodNotAllowed | `shield.go:471` 方法白名单 | 403 |
| 4 | BlockBodyTooLarge | `shield.go:477` 请求体超限 | 413 |
| 5 | BlockRiskPath | `shield.go:482` 风险路径 | 403 |
| 6 | BlockPathTraversal | `shield.go:487` 路径遍历 | 403 |
| 7 | BlockSQLInjection | `shield.go:492` SQL 注入 | 403 |
| 8 | BlockXSS | `shield.go:497` XSS | 403 |
| 9 | BlockCrawlerUA | `shield.go:502` 爬虫/扫描器 UA | 403 |
| 10 | BlockPathRuleDeny | `shield.go:369` 路径/UA 规则 deny | 403 |

> 拦截点覆盖 `Handle` 中 3 个（IP 黑名单、路径规则 deny、限流）+ `runWAF` 中 7 个（方法/体积/风险路径/路径遍历/SQL注入/XSS/爬虫UA），共 10 个点。

新增类型改代码常量即可，SMALLINT 足够扩展。

---

## 5. 数据库表设计

SQL 脚本外置 `sql/<sqlite|mysql|postgres>/shield_event_*.sql` 与 `access_log_prune.sql`，走 ScriptHub 热更（`sql/` 子目录已注册进 hub，改 SQL ≤3s 生效）。`{table}` 为运行时表名占位符（非用户输入，安全）。

### 5.1 shield_event（拦截明细表）

`sql/postgres/shield_event_create_table.sql`：

```sql
-- WAF 拦截事件明细表（幂等建表，PostgreSQL 方言）。{table} 为运行时表名占位符。
CREATE TABLE IF NOT EXISTS {table} (
    id          BIGSERIAL PRIMARY KEY,
    time        TIMESTAMPTZ NOT NULL,        -- 拦截时刻（Go 侧传 time.Time，非格式化字符串）
    trace_id    TEXT NOT NULL DEFAULT '',   -- 链路 ID（仅 shield_event 内部追溯用，不与 access_log 关联）
    block_type  SMALLINT NOT NULL,          -- 拦截类别，见 §4.3 枚举
    client_ip   TEXT NOT NULL DEFAULT '',
    method      TEXT NOT NULL DEFAULT '',
    path        TEXT NOT NULL DEFAULT '',   -- URL 路径
    raw_url     TEXT NOT NULL DEFAULT '',   -- 含查询串的原始 URL（攻击特征常在此）
    user_agent  TEXT NOT NULL DEFAULT '',
    host        TEXT NOT NULL DEFAULT '',
    status_code INT NOT NULL DEFAULT 0,     -- 拦截响应码（403/413/429）
    rule_hit    TEXT NOT NULL DEFAULT '',   -- 命中的规则/特征名（如 sql_pattern / 具体 crawler_ua）
    req_bytes   BIGINT NOT NULL DEFAULT 0,
    extra       TEXT NOT NULL DEFAULT '{}' -- 扩展（referer / x_forwarded_for 等，JSON）
)
```

> MySQL 方言：`BIGSERIAL` 换 `BIGINT AUTO_INCREMENT`、`TIMESTAMPTZ` 换 `DATETIME(3)`、`SMALLINT` 换 `SMALLINT`、`TEXT` 换 `VARCHAR(n)`/`TEXT`。
> SQLite 方言：`BIGSERIAL` 换 `INTEGER PRIMARY KEY AUTOINCREMENT`、`TIMESTAMPTZ` 换 `DATETIME`、`SMALLINT` 换 `INTEGER`。
> 聚合查询可用各方言原生时间函数：PG `date_trunc('hour', time)`、MySQL `DATE_FORMAT(time, '%Y-%m-%d %H')`、SQLite `strftime('%Y-%m-%d %H', time)`。

`shield_event_create_index.sql`（逐条执行，幂等容错）：

```sql
CREATE INDEX IF NOT EXISTS "idx_{table}_time" ON {table} (time);
CREATE INDEX IF NOT EXISTS "idx_{table}_block_type" ON {table} (block_type);
CREATE INDEX IF NOT EXISTS "idx_{table}_client_ip" ON {table} (client_ip);
CREATE INDEX IF NOT EXISTS "idx_{table}_time_type" ON {table} (time, block_type);
```

> `idx_{table}_time_type` 复合索引支撑查询时聚合（按时间范围 + block_type GROUP BY）。

配套脚本（仿 access_log 全套）：`shield_event_insert.sql`、`shield_event_query.sql`、`shield_event_size.sql`、`shield_event_prune.sql`（按保留天数清理）。

### 5.2 统计策略：查询时聚合（不建物化表）

**不建 daily_stats 物化表**（Q4 决策延伸）。统计全部通过查询时 `GROUP BY` 实时聚合完成：

- **按日/按类型统计**：`SELECT date(time) AS d, block_type, COUNT(*) FROM {table} WHERE time >= ? GROUP BY d, block_type`
- **Top 攻击源 IP**：`SELECT client_ip, COUNT(*) FROM {table} WHERE time >= ? GROUP BY client_ip ORDER BY COUNT(*) DESC LIMIT ?`
- **按小时趋势**：各方言原生时间函数截断到小时（见上方方言注释）

> **★ 三方言统一"按 UTC 日聚合"口径**：事件写入统一存 UTC 时刻（Go 侧 `time.UTC()`），故"日"边界必须以 UTC 为准，避免跨库迁移统计漂移：
> - sqlite：驱动将 UTC `time.Time` 存为 ISO 字符串，`substr(time,1,10)` 取 UTC 日；
> - MySQL：`DATETIME(3)` 无时区语义，存 UTC 墙上时间字面值，`DATE_FORMAT(time,'%Y-%m-%d')` 取 UTC 日；
> - PostgreSQL：`TIMESTAMPTZ` 存绝对时刻，**必须 `to_char(time AT TIME ZONE 'UTC','YYYY-MM-DD')`**，否则随数据库服务器时区漂移。
>
> 时间点过滤（events 明细、prune 的 `time >= ?`）为绝对时刻比较，不受时区影响。

90 天保留期内，`idx_{table}_time_type` 复合索引足以支撑聚合查询性能。如未来明细量极大（DDoS 洪流场景），可再评估物化表方案。

### 5.3 access_log 清理（补齐 obs DBStore prune）

当前 obs DBStore **无任何清理逻辑**，access_log 表无限膨胀。本次同步补齐：

- 新增 `sql/<三方言>/access_log_prune.sql`：`DELETE FROM {table} WHERE time < ?`（参数为 `time.Time` 截止时刻）。
- DBStore 新增 `Prune(retentionDays int) error` 方法，后台 goroutine 每日执行（仿 file_store `cleanupOld` 的调度模式）。
- 受 `OBS_LOG_PRUNE_ENABLED` 配置项控制（默认 `false`，不开启）。

---

## 6. 代码改造点

| 位置 | 改动 | 说明 |
|---|---|---|
| `plugins/shield/event_recorder.go`（新） | EventRecorder + eventCounter + Prune 任务 | 内存计数 + 异步落库 + 拦截明细清理，复用 obs.DBStore 范式 |
| `plugins/shield/block_type.go`（新） | block_type 常量 + String() | 10 类枚举 |
| `plugins/shield/shield.go` | 新增 `SetEventRecorder` setter；10 个拦截点调 `Record`；`New` 注册配置项 | setter 注入，`New` 签名不变 |
| `sql/<三方言>/shield_event_*.sql`（新） | 建表/索引/插入/查询/清理/大小 | 三方言齐平，{table} 占位符 |
| `sql/<三方言>/access_log_prune.sql`（新） | 常规日志清理 | 补齐 obs DBStore prune，三方言齐平 |
| `plugins/obs/db_store.go` | 新增 `Prune` 方法 + 后台清理 goroutine | 仿 file_store `cleanupOld` 调度，受 `OBS_LOG_PRUNE_ENABLED` 控制 |
| `plugins/obs/obs.go` | 注册 prune 相关配置项 | `OBS_LOG_PRUNE_ENABLED` / `OBS_LOG_RETENTION_DAYS` |
| `internal/adminapi/` 或 `plugins/shield/admin.go`（新） | admin 路由 | events/stats/metrics/prune |
| `internal/adminapi/` 登录响应 | 追加 `warnings` 字段 | prune 未开启时返回警告，WebUI 展示横幅 |
| `cmd/rocksys/main.go` | 装配 EventRecorder（setter 注入） | shield 启用且 DB 组件就绪时：`shieldMw.SetEventRecorder(recorder)` |
| `webui/` | 拦截统计看板页 + prune 未开启警告横幅 | 批次3 |

### 6.1 shield 拦截点接入（示意）

`Handle`/`runWAF` 每个 `return false` 前统一调用（不改拦截逻辑，仅追加记录）：

```go
// 旧：http.Error(ctx.W, "forbidden", http.StatusForbidden); return false
// 新：
http.Error(ctx.W, "forbidden", http.StatusForbidden)
s.recorder.Record(ctx, BlockSQLInjection, ruleHit) // 非阻塞，热路径安全
return false
```

`Record` 内部：`counter.Add(blockType)` + 非阻塞 `select { case ch <- ev: default: dropped++ }`，绝不阻塞转发。`recorder` 为 nil 时 `Record` 静默 no-op（DB 未配置降级）。

### 6.2 setter 注入（main.go 装配）

```go
// shield.New 签名不变，DB 就绪后 setter 注入
shieldMw, _ := shield.New(cfgMgr, scriptHub)
mgr.RegisterMiddleware(shieldMw)
// ... dataDB 就绪后 ...
if dataDB != nil {
    recorder := shield.NewEventRecorder(dataDB, cfgMgr)
    shieldMw.SetEventRecorder(recorder) // 未调用时 recorder=nil，shield 仍正常拦截
}
```

### 6.3 登录警告机制（常驻置顶横幅）

admin API 登录成功响应追加 `warnings` 数组字段，并提供独立只读端点供页面刷新后重拉：

```json
// POST /admin/auth/login 响应（登录后即时渲染横幅）
{
  "token": "...",
  "warnings": [
    "拦截记录清理未开启，shield_event 表可能持续膨胀",
    "访问日志清理未开启，access_log 表可能持续膨胀"
  ]
}
// GET /admin/warnings（鉴权，与登录响应同源：pruneWarnings()）
{ "warnings": ["..."] }
```

- `SHIELD_EVENT_PRUNE_ENABLED=false` → 追加 shield_event 警告。
- `OBS_LOG_PRUNE_ENABLED=false` → 追加 access_log 警告。
- **WebUI 常驻置顶横幅**：登录成功即时渲染 + 应用启动（`boot`）经 `GET /admin/warnings` 拉取渲染（登录态为 localStorage token、无会话内缓存，刷新页面横幅不丢失、配置变更实时反映）；横幅支持关闭（仅本次会话，刷新后重拉显示）。

---

## 7. admin API 设计

经 `adminapi.RegisterPlugin` 注入（仿 obs 三端点）：

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/admin/shield/events` | 拦截明细查询（支持 `from/to/block_type/client_ip/limit` 过滤） |
| GET | `/admin/shield/stats` | 聚合统计（查询时 GROUP BY：按日/按类型/Top IP） |
| GET | `/admin/shield/metrics` | 实时计数（读内存滑动窗口，秒级，无需查库） |
| POST | `/admin/shield/prune` | 手动触发拦截明细清理（按保留天数删旧记录） |

复用 `obs.admin.go` 的查询参数风格与响应归一化（`normalizeRowTypes`）。

---

## 8. 配置项（经 conf.Manager.Register）

遵循配置中心红线（禁止绕过 Register 另开读取入口）：

### shield 侧（EventRecorder）

| 配置项 | 默认 | 说明 |
|---|---|---|
| `SHIELD_EVENT_LOG_ENABLED` | true | 是否记录拦截事件（关闭则只内存计数不落库） |
| `SHIELD_EVENT_TABLE` | shield_event | 事件表名 |
| `SHIELD_EVENT_RETENTION_DAYS` | 90 | 拦截明细保留天数 |
| `SHIELD_EVENT_PRUNE_ENABLED` | false | 是否开启自动清理（默认不开启） |
| `SHIELD_EVENT_BUFFER` | 1024 | 异步落库缓冲通道大小 |
| `SHIELD_EVENT_FLUSH_ROWS` | 200 | 批量 INSERT 行数阈值 |
| `SHIELD_EVENT_FLUSH_INTERVAL` | 5 | flush 间隔秒 |

### obs 侧（access_log 清理，补齐）

| 配置项 | 默认 | 说明 |
|---|---|---|
| `OBS_LOG_PRUNE_ENABLED` | false | 是否开启 access_log 自动清理（默认不开启） |
| `OBS_LOG_RETENTION_DAYS` | 7 | 常规访问日志保留天数 |

> 两个 prune 各自独立开关，默认均不开启。未开启时登录管理后台展示警告横幅（见 §6.3）。

---

## 9. WebUI 看板（批次3）

`webui/` 新增页面（纯静态，经 `go:embed`/dev 热重载）：

- **prune 警告横幅**：登录后若 prune 未开启（shield_event 和/或 access_log），顶部展示黄色横幅引导开启。
- **概览卡片**：今日拦截总数、Top 拦截类型、Top 攻击源 IP、环比。
- **趋势图**：按日/按小时的拦截量折线（按 block_type 分色）。
- **拦截类型分布**：饼图（10 类占比）。
- **拦截明细表**：分页列表 + 过滤（类型/IP/时间）。
- 复用 `webui/assets/` 现有图表组件风格。

---

## 10. 数据保留与清理

| 数据 | 保留天数 | 清理方式 | 默认 | 警告 |
|---|---|---|---|---|
| `shield_event`（拦截明细） | 90 天 | 后台 goroutine 每日执行 `shield_event_prune.sql` | **不开启** | 未开启时登录警告 |
| `access_log`（常规日志） | 7 天 | 后台 goroutine 每日执行 `access_log_prune.sql` | **不开启** | 未开启时登录警告 |

- **清理任务归属**：shield_event 清理由 EventRecorder 自管（随 shield 生命周期）；access_log 清理由 DBStore/Obs 自管（随 obs 生命周期）。各管各的，不交叉。
- **手动清理**：`POST /admin/shield/prune` 手动触发 shield_event 清理；obs 侧可复用现有 admin 接口或新增 `POST /admin/logs/prune`。
- **清理 SQL**：`DELETE FROM {table} WHERE time < ?`，参数为截止时刻 `time.Time`，幂等可重复执行。
- 借鉴 cdnguard 的 requests(7d) / block_requests(90d) 分级保留思路（原 cdnguard 为 30d，rocksys 拦截明细保留 90d）。

---

## 11. 分批实施计划

| 批次 | 内容 | 依赖 | 验证 |
|---|---|---|---|
| **1** | block_type 枚举 + EventRecorder（内存计数+异步落库+setter 注入）+ shield_event 表三方言 SQL + 10 拦截点接入 + main 装配 | DB_DRIVER 已配 | go test + pg 集成测试（建表/插入/查询）+ 拦截后查库有记录 |
| **2** | admin API（events/stats/metrics/prune）+ adminapi 路由 + 登录 warnings 字段 | 批次1 | curl 四接口返回正确 + 登录响应含 warnings |
| **3** | WebUI 看板页 + prune 警告横幅 | 批次2 | 浏览器刷新即见（dev 模式免编译） |
| **4** | access_log_prune.sql 三方言 + DBStore Prune 方法 + obs 配置注册 | 批次1 | 旧 access_log 被清理 + 登录警告正确 |

每批独立可交付、可回滚；批次1 即解决"拦截零记录"核心痛点；批次4 补齐 access_log 清理（与 shield_event 清理独立，不互相阻塞）。

---

## 12. 风险与权衡

| 风险 | 评估 | 对策 |
|---|---|---|
| 异步落库进程崩溃丢数据 | 拦截统计可容忍少量丢失 | buffered chan + 优雅停机 flush |
| 高频拦截压库 | DDoS 下拦截洪流 | 异步批量 INSERT + channel 满丢弃计 `dropped` |
| 查询时聚合慢 | 90 天明细量大时 GROUP BY 偏慢 | `idx_{table}_time_type` 复合索引支撑；极端场景可再评估物化表 |
| block_type 扩展 | 新增检测项 | SMALLINT + 代码常量，新增类型改代码 + DDL 不动 |
| shield_event 与 access_log 边界 | 各记各的，互不关联 | 拦截侧=shield_event，放行侧=access_log，不跨表 JOIN |
| DB 未配置时 shield 降级 | sqlite/pg 未就绪 | recorder=nil 时 Record 静默 no-op，shield 仍拦截，不阻断防护 |
| prune 未开启导致库膨胀 | 默认不开启 | 登录管理后台警告提示，引导用户按需开启 |

---

## 13. 验证计划

- **单元测试**：`event_recorder_test.go`（计数器、channel 满丢弃、flush、prune）。
- **pg 集成测试**（仿 `pg_integration_test.go`，`PG_TEST_DSN` 门控）：建表幂等、插入、查询、聚合、清理。
- **obs DBStore prune 测试**：插入旧数据 → prune → 确认删除。
- **登录警告测试**：prune 未开启时登录响应含 warnings。
- **回归**：现有 shield 测试不破坏（记录是追加逻辑，不改拦截判定）；现有 obs 测试不破坏（prune 是新增逻辑）。
- **端到端**：构造各类攻击请求（SQL 注入 URL、爬虫 UA、超限 QPS），确认 shield_event 有对应记录、看板可见。

---

## 14. 实施进度

| 批次 | 状态 | 备注 |
|---|---|---|
| 1 | ✅ 已完成 | block_type 枚举 + EventRecorder（无锁 atomic 计数 + 异步落库 + setter 注入）+ shield_event 三方言 SQL + 10 拦截点接入 + main 装配 |
| 2 | ✅ 已完成 | admin API（events/stats/metrics/prune）+ adminapi 路由 + 登录 warnings + GET /admin/warnings 端点 |
| 3 | ✅ 已完成 | WebUI 拦截统计页 + prune 未开启警告**常驻置顶横幅**（可关闭，刷新重拉） |
| 4 | ✅ 已完成 | access_log_prune.sql 三方言 + DBStore Prune 方法 + obs 配置注册 |

> 追加修订：三方言按日聚合统一 UTC 口径（PG `AT TIME ZONE 'UTC'`）；eventCounter 无锁 atomic 分桶化（零锁热路径）；警告横幅常驻置顶（经 /admin/warnings 重拉）。

---

## 15. 开放问题（已定论）

- [x] **Q1**：EventRecorder 注入方式 → **setter**（`shield.New` 签名不变，`main.go` 调 `SetEventRecorder` 注入；不使用 mq outbox，用自带异步 buffered channel）。
- [x] **Q2**：数据清理机制 → **统一 prune**：shield_event 保留 90 天、access_log 保留 7 天，各自独立开关、默认不开启，登录管理后台未开启则警告提示。
- [x] **Q3**：放行请求是否补 WAF 标记 → **不补**。放行了就是符合规则，不越界，不碰 obs/access_log。
- [x] **Q4**：Top IP 统计 → **查询时聚合**，不建独立表。所有统计（按日/按类型/Top IP）均走查询时 GROUP BY。
- [x] **Q5**：拦截与放行关联 → **不关联**。shield_event 和 access_log 各记各的，不串联 trace_id，不跨表 JOIN。

---

> 本文档是 WAF 监控统计改造的唯一上下文锚点，实施前必读；每批完成后更新"实施进度"与"开放问题"。
