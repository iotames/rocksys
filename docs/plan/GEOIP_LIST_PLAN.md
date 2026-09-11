# GEOIP_LIST 设计方案（IP 地理信息关联化 + 定时任务只读登记）

> 状态：**待人类定稿确认**（宪法 §2 关口；未获确认前不建 TODO/STEP、不动代码）
> 层级：设计文档（母文档），只答「为什么这么做」。
> 关联：`docs/plan/README.md`（宪法）、`docs/DATA_DICT.md`（数据层权威）、`docs/webui-api.md`、`docs/COMPONENTS.md`。

---

## 1. 真实痛点（现状 + 证据）

### 1.1 地理信息按行冗余存储

`country`/`city` 两列存于 2 张业务表（`access_log` 放行、`shield_event` 拦截），每行都重复存一份地理字符串——列定义见 `sql/*/access_log_create_table.sql`、`sql/*/shield_event_create_table.sql`，写入点见 `plugins/obs/obs.go:320-321`、`plugins/shield/event_recorder.go:324-325`。

**地理信息只是 IP 的函数，却按行重复存**（同一 IP 反复出现即反复存同一串）；`client_ip` 早已建索引（`sql/*/access_log_create_index.sql`、`sql/*/shield_event_create_index.sql`）。

> 量级参考（本机开发库 `bin/rocksys.db` 抽样；运行库不随仓库交付、接手者不可复核，故仅示意、不作立项依据）：

| 表 | 行数 | 唯一 `client_ip` | 唯一 `country\|city` 组合 |
|---|---|---|---|
| `access_log` | 649,138 | **178,246** | **200** |
| `shield_event` | 107,301 | 14,478 | 65 |

### 1.2 列名与值语义不符

- `country` 存的是 **ISO 码**（`CN`/`SG`），不是国名——写入取 `gi.Code`（`plugins/obs/obs.go:321`、`plugins/shield/event_recorder.go:325`；`internal/geoip/geoip.go:85`）。国名（`zh-CN` 优先）在 `GeoInfo.Country`，**未入库**。→ 列名「国家」与值「代码」不符，显示要另做映射。
- `city` 存的是 **「省/市」拼接串**（`geoip.go:87` `joinNames(subdivisions, city)` → `广东省/深圳市`）；新加坡恰因拼接退化为「新加坡」。→ 一列时而城市名、时而省+市拼接，语义不自洽。

### 1.3 省级聚合靠字符串切分（脆弱）

中国省级聚合用 `substr(city, 1, instr(city||'/','/')-1)` 取首个 `/` 前（`sql/*/traffic_geo_province_top.sql`）——依赖格式约定，城市名含 `/` 即错；且**缺独立 `province`（省/州）字段**，美国州（`California`）无语义位置。

### 1.4 定时任务入口分散、状态不可见

| 任务 | 位置 | 周期 | 开关 |
|---|---|---|---|
| obs 访问日志清理 | `plugins/obs/obs.go:386` | 24h（首延迟 1 分钟） | `OBS_LOG_PRUNE_ENABLED` |
| shield 拦截事件清理 | `plugins/shield/event_recorder.go:447` | 24h（首延迟 1 分钟） | `SHIELD_EVENT_PRUNE_ENABLED` |
| shield 自动拉黑引擎 | `plugins/shield/auto_ban.go` | `window/3`（≥1 分钟，**启动即执行**） | `SHIELD_AUTO_BAN_ENABLED` |

- **入口分散**：各自 `ticker` 循环、各自启停/日志；想知道"系统有哪些定时任务"要翻多个包。
- **状态不可见**：无「上次执行时间/上次结果」持久记录，页面无处展示（连 GeoIP 的「上次同步时间」都没有落脚点）。
- **新增任务要重造轮子**：GeoIP 同步得再写一套 ticker + 配置 + 状态。

### 1.5 地图/展示的字段硬约束

`webui/assets/js/components/geomap.js`：世界地图按 **ISO2 码**、中国地图按**中文省名**直连底图；Top 列表需**中文国名**。故聚合须产出 ISO2 码 + 中文省名，展示须有中文名。

---

## 2. 需求（本方案要达成的目标）

1. **去冗余**：地理信息独立成表，与日志表按 IP 关联；两表删掉冗余的 4 列。
2. **名实相符**：代码与名称分离（`country_code` + `country_name`）、省与市分离（`province` + `city`）。
3. **定时任务统一登记**：系统已有定时任务 + 新增 GeoIP 同步，统一登记到一张表，**入口一致、可查**；不搞复杂调度器，**只读兜底展示**。
4. **配置区新增「定时任务」页**（只读）：看得到有哪些任务、关联开关、上次执行时间（获取难度高的可以不用）。
5. **GeoIP 同步**：手动增量 + 每天 05:00 自动；显示上次同步时间。
6. **数据不造假**：库列保持解析真实语义（该空则空），拼接/兜底只做在读侧显示。

---

## 3. 已确认决策表（只追加不覆盖）

| # | 决策点 | 结论 | 出处 |
|---|---|---|---|
| D1 | 去冗余方向 | 新建 `geoip_list` 关联表，取代逐行存 geo | 用户 09-11 |
| D2 | 两表 4 列 | 全部删除（开发阶段） | 用户 09-11 |
| D3 | 关联键 | 用 `client_ip` JOIN（两方一致、复杂度低） | 用户 09-11 |
| D4 | IP 列类型 | 纯文本（三方言一致），**不引第三方 DB / DB 特有 IP 类型** | 用户 09-11 |
| D5 | `geoip_list` 维护 | 手动增量同步 + 定时自动（写日志不查该表） | 用户 09-11 |
| D6 | 定时同步时刻 | 每天 **05:00**（服务器本地时间） | 用户 09-11 |
| D7 | 上次同步时间 | 落在 **`schedule_list`** | 用户 09-11 |
| D8 | 1 IP 多归属 | 不做（`ip` 唯一键） | 用户 09-11 |
| D9 | `province` | 本期落地（`city` 只存城市名） | 用户 09-11 |
| D10 | `country` 语义 | 拆 `country_code`（聚合/地图）+ `country_name`（显示） | 用户 09-11 |
| D11 | 空值口径 | 数据层不造假，拼接/兜底只在读侧 | 用户 09-11 |
| D12 | 交付节奏 | 先出方案，定稿后实施 | 用户 09-11 |
| D13 | 定时任务登记 | 统一收敛到 `schedule_list`（含已有任务 + GeoIP 同步） | 用户 09-11 |
| D14 | `enabled` 字段 | ~~与 .env 同步、页面可切换~~ **被取代（D18）**：取消 `enabled`，开关只读 easyconf | 用户 09-11 |
| D15 | 配置区新增页 | 「配置」组、「数据库」之后新增「定时任务」（`#/schedule`） | 用户 09-11 |
| D16 | `GEOIP_SYNC_AUTO_ENABLED` 默认 | **false** | 用户 09-11 |
| D17 | 状态读取端点 | 新增统一 `GET /admin/schedule/list`；不改 `/admin/db/geoip_sync` 的 GET | 用户 09-11 |
| D18 | `schedule_list` 定位 | **只读登记 + 状态汇总（不驱动）**；不适配统一模式的定时任务不塞入，以**代码注释 + 页面说明**标注 | 用户 09-11 |
| D19 | 登记清单 | 可配型 4 个 + 系统级只读若干（见 §4.3） | 用户 09-11 |
| D20 | 系统级任务 | **整行只读**；被改则重启时**按 `name` 定位重置为系统值** | 用户 09-11 |
| D21 | 状态回写范围 | 本期**只回写 `geoip_sync`**；obs/shield/auto_ban 现有触发循环**原样不动**（零侵入） | 用户 09-11 |
| D22 | 手动触发 | 沿用现有 `POST /admin/db/geoip_sync`，**不新建统一 run** | 用户 09-11 |
| D23 | 增量算法 | 沿用 `id` 主键游标分块 + 内存去重（**禁无界 DISTINCT**，`geoip_sync.go:7` 已证其超时） | 审查修正 |
| D24 | JOIN 聚合性能 | 实施阶段用真实库实测；备选 Go 侧把 `geoip_list` 装内存 map，SQL 只按 `client_ip` 分组 | 审查 |
| D25 | 统计缓存 | geo 同步成功后清 traffic 缓存（`OBS_TRAFFIC_CACHE_TTL`），避免展示滞后 | 审查 |
| D26 | 删列迁移 | 两步走：旧列先改名 `*_legacy` 留存一轮 → 验证 → 物理删；提供核实命令与备份前置 | 审查 |
| D27 | 模块边界 | 遵循「底座不依赖挂件」：登记与只读端点放装配层（`cmd/rocksys`）/无挂件原语，`internal` 不 import `plugins` | 审查 |
| D28 | README「变更记录」 | 改为「**验收结论 / 已知边界**」（去过程流水），宪法 §1/§5 相应修订 | 用户 09-11 |

---

## 4. 设计方案

### 4.1 新表 `geoip_list`（一 IP 一行）

```sql
CREATE TABLE IF NOT EXISTS {table} (
    ip           VARCHAR(64)  PRIMARY KEY,          -- 纯 IP 文本（与日志表 client_ip 同格式，无端口）
    country_code VARCHAR(8)   NOT NULL DEFAULT '',  -- ISO alpha-2（聚合口径 + 世界地图着色）
    country_name VARCHAR(64)  NOT NULL DEFAULT '',  -- 本地化国名 zh-CN 优先（显示）
    province     VARCHAR(128) NOT NULL DEFAULT '',  -- 一级行政区（省/州；中国地图着色）
    city         VARCHAR(128) NOT NULL DEFAULT '',  -- 城市名（仅市）
    updated_at   DATETIME     NOT NULL              -- 该 IP 最近解析时间（UTC）
)
```
方言：SQLite `DATETIME` / MySQL `DATETIME(3)` / PG `TIMESTAMPTZ`（照 `sql_exec_log_create_table.sql` 风格）。索引：主键 `ip`；可选 `idx_{table}_country(country_code)`。

### 4.2 关联与删列

- 两表保留 `client_ip`，查询 `LEFT JOIN geoip_list g ON g.ip = {table}.client_ip`；读侧统一「市→省→国名→未知」兜底。
- 删 `access_log`/`shield_event` 的 `country`/`city`，并移除 `idx_*_time_country`。
- 聚合改造：`traffic_geo_top.sql` → `GROUP BY g.country_code`（带回 `country_name`）；`traffic_geo_province_top.sql` → `GROUP BY g.province WHERE g.country_code='CN'`（**去掉字符串切分**）。
- 性能：实施阶段实测 JOIN 聚合；不达标则改 Go 侧内存映射（D24）。

### 4.3 `schedule_list`：只读登记表（不驱动）

```sql
CREATE TABLE IF NOT EXISTS {table} (
    id           INTEGER PRIMARY KEY AUTOINCREMENT, -- 自增主键
    name         VARCHAR(64)  NOT NULL,             -- 任务唯一标识（UNIQUE；代码常量绑定）
    title        VARCHAR(128) NOT NULL DEFAULT '',  -- 中文名（展示）
    kind         VARCHAR(16)  NOT NULL DEFAULT '',  -- configurable（可配型）/ system（系统级只读）
    config_key   VARCHAR(64)  NOT NULL DEFAULT '',  -- 关联 easyconf 开关名（可配型；系统级空）
    plan         VARCHAR(64)  NOT NULL DEFAULT '',  -- 计划描述（仅展示：daily@05:00 / every@24h / window/3）
    last_run_at  DATETIME,                          -- 上次执行时间（UTC）；仅调度器回写者有意义
    last_status  VARCHAR(16)  NOT NULL DEFAULT '',  -- success / failed / skipped
    last_message VARCHAR(255) NOT NULL DEFAULT '',  -- 结果摘要
    remark       VARCHAR(255) NOT NULL DEFAULT '',  -- 说明（含"不纳入原因/只读"）
    updated_at   DATETIME     NOT NULL
)
+ UNIQUE 索引：idx_{table}_name(name)
```

- **定位**：只做**登记 + 状态汇总**，**不驱动任何任务**（D18）。无真实触发逻辑，故「检查粒度/精度」不适用；需要实时/精确触发的任务**不纳入**，以代码注释 + 页面说明标注。
- **登记方式**：装配期由代码按 `name` **upsert（新增或更新）**；`kind=system` 行**整行只读**，被改则重启按 `name` 重置为系统值（D20）。
- **开关**：**无 `enabled` 列**；启用状态由页面**只读**读取 easyconf（`config_key` 对应配置项）。
- **纳入清单（D19）**：
  - `configurable`（4）：`obs_log_prune`(`OBS_LOG_PRUNE_ENABLED`)、`shield_event_prune`(`SHIELD_EVENT_PRUNE_ENABLED`)、`shield_auto_ban`(`SHIELD_AUTO_BAN_ENABLED`)、`geoip_sync`(`GEOIP_SYNC_AUTO_ENABLED`)。
  - `system`（只读，若干）：hotswap 文件监控、conf 配置轮询、registry 心跳扫描、dispatch 健康检查、mq 投递轮询、shield IP 快照 TTL 重建、shield 事件 flush。
  - **不纳入**（精确/实时类或非任务语义）：workpool 重试/worker 检查等；在代码注释与页面注明"该类不纳入统一登记"。
- **状态回写范围**：本期仅 `geoip_sync`（D21）——老任务零侵入；其 `last_run_at` 即「上次同步时间」。

### 4.4 只读页与端点

- 页面：`index.html` 配置组内、「数据库」之后新增 `#/schedule`「定时任务」；新增 `views/schedule.js`（表：名称/说明/类型/关联开关/**上次执行时间**）；`main.js` 路由 + `state.js`。**页面只读**（无切换、无触发按钮）。**上次执行时间**仅对本期回写者（`geoip_sync`）有值，其余行须页面标注「未登记」并加说明——否则会被读成"任务从未执行"。
- 端点：新增 **`GET /admin/schedule/list`**（只读，供该页与数据库页读取）。
- 配置项新增：`GEOIP_SYNC_AUTO_ENABLED`(默认 false)、`GEOIP_SYNC_HOUR`(默认 5)。

### 4.5 GeoIP 同步（D5/D6/D23/D25）

- **手动**：沿用 `POST /admin/db/geoip_sync`（D22），语义改为「增量构建/刷新 `geoip_list`」。
- **自动**：每天 05:00 触发（配置项 `GEOIP_SYNC_AUTO_ENABLED` 为 true 时）；执行后回写 `schedule_list` 的 `geoip_sync` 行（`last_run_at`）。
- **增量算法**（D23）：沿用 **`id` 主键游标分块**扫两表 `client_ip`、内存去重后与 `geoip_list` 求差，逐 IP `geo.Lookup` 后 upsert；沿用分块 + 断点游标 + 时间预算 + 互斥。**禁止无界 `DISTINCT` 全表扫描**。
- 新增三方言 `geoip_list_upsert.sql`（SQLite/PG `ON CONFLICT`、MySQL `ON DUPLICATE KEY`）。
- 同步成功后清 traffic 统计缓存（D25）。
- 写日志（obs/shield）：**只写 `client_ip`**，去掉逐行 geo 写入；**读侧 JOIN 未命中时回退实时 `Lookup`**（详情/Top 结果集小，保新 IP 及时可见，沿用 `event_recorder.go:581` 既有做法）。

### 4.6 影响面清单

- **SQL（三方言）**：新增 `geoip_list_create_table.sql`/`_create_index.sql`/`_upsert.sql`、`schedule_list_create_table.sql`/`_create_index.sql`；改 `access_log_{create_table,insert,query,create_index}.sql`、`shield_event_{...}.sql`、`traffic_geo_top.sql`、`traffic_geo_province_top.sql`；新增删列迁移脚本；`sql/*/README.md`。
- **Go**：`internal/geoip/geoip.go`（+`Province`）；`plugins/obs/{obs.go,db_store.go,dim.go,traffic.go}`、`plugins/shield/{event_recorder.go,admin.go}`（去两列、查询改 JOIN）；`cmd/rocksys/geoip_sync.go`（改增量构建）、`cmd/rocksys/main.go`（登记/端点/配置/每日触发）；相关 `_test.go`。
- **端点**：`GET /admin/schedule/list`（新）；`/admin/logs`、`/admin/shield/events`、`/admin/obs/traffic/geo`、`/admin/shield/stats`（改 JOIN）。
- **前端**：`index.html`（菜单）、`views/schedule.js`（新）、`main.js`、`state.js`；`views/database.js`（同步卡文案 + 上次同步时间）、`views/overview.js` + `components/geomap.js`（地理位置卡）、`views/logs.js`/`waf.js`（详情）、`views/topIPs.js`（地区列）。
- **文档**：`docs/DATA_DICT.md`、`docs/webui-api.md`、`docs/COMPONENTS.md`、`docs/webui.md`、`sql/*/README.md`；顺带修正 `webui-api.md` 中 `client_ip` 示例带端口与现状不符。

### 4.7 迁移与回退（D26）

顺序：① 建 `geoip_list` + `schedule_list`（schema 同步 A 级）→ ② 全量构建并校验覆盖率 → ③ 端点/前端切 JOIN → ④ 旧列改名 `*_legacy` 留存一轮 → ⑤ 验证后物理删列（人工脚本）。
回退：任一步失败可回退到上一步；物理删列前须有备份。不可逆点：物理删列 + 数据迁移。

### 4.8 明确不做（边界）

- 不做通用"调度器驱动"（D18）；不新建统一手动触发端点（D22）；
- 不纳入实时/精确触发的内部机制（D18）；
- 不做 1 IP 多归属（D8）；本期不清理 `geoip_list` 长期未引用 IP；
- 隐私/回环等无 geo 的 IP 不入 `geoip_list`（读侧显示「未知」）。

---

## 5. 验收标准

1. `geoip_list`、`schedule_list` 三方言建表/索引/upsert 脚本齐备，经 schema 同步可在 WebUI 确认执行；`docs/DATA_DICT.md` 同步登记。
2. `geoip_list` 行数 ≈ **可解析 geo 的唯一 IP 并集**（私网/无 geo IP 不入表、读侧显示「未知」）。
3. 两表 4 列按 D26 两步迁移可执行、可回退；备份前置到位。
4. `/admin/logs`、`/admin/shield/events` 经 JOIN 正确返回 `country_code/country_name/province/city`；详情「市→省→国名→未知」兜底正确。
5. `/admin/obs/traffic/geo` 聚合正确且**不再字符串切分**；概览页热力图（世界=ISO2、中国=中文省名）与 Top 排名正常；实测 JOIN 聚合性能达标（否则走 D24 备选）。
6. **定时任务**：`schedule_list` 登记可配型 4 + 系统级若干；装配期 upsert；系统级行重启重置；`#/schedule` 只读页展示；`geoip_sync` 每天 05:00 自动 + 手动可触发；数据库页 GeoIP 同步卡显示**上次同步时间**；geo 同步后统计缓存已失效。
7. `go build -tags dev` / `go vet ./...` / `go test ./...` 全绿；涉及前端页面**开浏览器实看并截图**留证。
8. 全部相关文档同步（DATA_DICT / webui-api / COMPONENTS / webui / README）。

---

## 6. 决策取舍与备选（讨论留痕）

> 目的：把「为什么这么选」的备选与理由留档，避免只存在于会话（宪法 §0：会话记忆易失）。

| 议题 | 曾评估的备选 | 选定 | 理由 |
|---|---|---|---|
| 去冗余 | 拆列 / 建 IP 关联表 | 建 `geoip_list` 关联 | 地理信息是 IP 的函数，曾按 75 万行重复存 |
| `country` 值 | 拆 code+name / 只更名 / 不动 | 拆 `country_code`+`country_name` | 名实相符；聚合用码、显示用名 |
| `city` | 拆 `province`+`city` / 不动 / 加冗余展示列 | 拆 `province`+`city` | 去字符串切分、承载美国州 |
| 关联键 | `client_ip` JOIN / `geo_id` 外键 | `client_ip` JOIN | 两方一致、复杂度低 |
| 定时任务模型 | A 统一调度器驱动 / B 统一登记+各自触发 / C 折中 | **只读登记（不驱动）** | 不搞复杂调度器、零侵入、只做兜底可见性 |
| 启用开关 | 表内 `enabled` 与 .env 双向同步 / 取消 `enabled` 只读消费 easyconf | 取消 `enabled`、只读 easyconf | 配置中心为唯一真源，消除"谁说了算" |
| 增量发现 | 日志唯一 IP 差集（DISTINCT）/ `id` 游标分块 | `id` 游标分块 | 全表 DISTINCT 百万行必超时（`geoip_sync.go:7` 已证） |
| JOIN 聚合性能 | DB JOIN / Go 侧内存映射 | 待实施实测 | 大表 JOIN 小表可能劣化 |
| 手动触发 | 统一 `POST /admin/schedule/run` / 沿用 `/admin/db/geoip_sync` | 沿用 | 仅 1 个手动任务，统一 run 收益小 |
| 调度模块归属 | `internal/schedule` / `cmd/rocksys` 装配 | 装配层 + 无挂件原语 | 底座不依赖挂件（架构红线） |

## 7. 独立架构审查意见（落档）

> 来源：独立只读架构审查（omo-oracle），15 条，按严重度；每条标注处置去向。

**阻断（定稿前必须解决）**
- B1 文档与最新决策冲突（`schedule_list.enabled` 已被取消）→ D18/D20。
- B2 `internal/schedule` 违反"底座不依赖挂件"→ D27。
- B3 增量"日志唯一 IP 差集"会重蹈无界 DISTINCT 全表扫描（已证超时）→ D23。

**高**
- H1 JOIN 聚合与删索引的性能风险（`access_log` 649k 行 JOIN ~180k 行小表）→ D24（实测，备选 Go 内存映射）。
- H2 geo 同步后统计缓存不失效、展示最长滞后 15 分钟 → D25。
- H3 `geoip_list` upsert 三方言未定义 + 并发写 → §4.5（三方言 upsert 脚本 + 互斥）。
- H4 "统一调度器"对异构周期任务（`auto_ban` 为 `window/3` 动态、启动即执行）可能过度设计 → D18（只读登记、不驱动）。

**中**
- M1 `last_run_at` 并发写与语义（开始 vs 结束）→ §4.3（定义语义 + 单条原子 UPDATE + 互斥）。
- M2 写日志不查表 → 新 IP geo 实时性下降 → §4.5（读侧 JOIN 未命中回退实时 `Lookup`）。
- M3 验收"覆盖率 100%"不可达（私网/无 geo IP 被跳过）→ §5 验收 2 已改口径为"可解析 IP 并集"。
- M4 删列不可逆 → D26（legacy 改名两步走 + 备份前置）。
- M5 `schedule_list` 表结构/方言一致性 → §4.3（照 `sql_exec_log` 范式）。

**低**
- L1 定稿前四维质量自查缺失 → §8。
- L2 §4 待定项与已定决策混放 → 已收敛进决策表。
- L3 分工与宪法"单写者假设"、STEP 级锚点/核实命令 → 留待定稿后拆步落实（本期不建 STEP）。

## 7.1 二次审查补充（仓库基准，定稿前待人类取舍）

> 来源：二次审查（只读）。基准为**仓库内可复核内容**（代码、SQL、仓库文档），不以本机运行库为依据。仅列两条需人类定稿时决定的项，不擅自改动已拍板决策。

- **R1 聚合侧无新 IP 回退路径**：`traffic_geo_top.sql`、`traffic_geo_province_top.sql` 是 SQL `GROUP BY` 聚合，§4.5 的「JOIN 未命中回退实时 `Lookup`」只覆盖详情/Top（结果集小）。写日志不再落 geo 后，未进入 `geoip_list` 的 IP 在聚合结果里必然落空桶——库列允许空（需求 6），但热力图/省级榜会掉数据（需求 6 不支持）。候选取舍（**需在 D6 上补一行**）：① 提高 `geoip_sync` 频率；② 聚合端点被请求时先补一次增量；③ 空桶在 Go 侧对未入表 IP 补算 `Lookup`。
- **R2 删列迁移口径冲突（D2 vs D26）**：D2「4 列全部删除（开发阶段）」与 §4.7/§5.3 的「改名 `*_legacy` 留存一轮 → 物理删 + 备份前置」并存，实施者无法判定走哪条。若确无存量生产数据，取 D2 简单路径（直接删，免迁移脚本）；反之取 D26。**定稿时择一并删去另一表述**。

## 8. 定稿前四维质量自查（宪法 §2）

- **工程化**：沿用 `id` 游标增量、三方言脚本齐平、走 schema 同步分级、遵守配置中心红线；无新增第三方依赖。
- **用户体验**：数据名实相符、省级聚合不再脆弱、地图/列表兜底可用；新增只读「定时任务」页与「上次同步时间」；新 IP 实时性由读侧回退保住。
- **改动量与复杂度（是否过度设计）**：**主动裁掉**通用调度器驱动（D18），只做只读登记；定时任务页只读无写操作；避免为 4 个异构任务造调度中枢。
- **架构统一性**：遵循"底座不依赖挂件"（D27）、配置中心唯一真源（D18）、数据字典红线（DATA_DICT 同步）。

## 9. 验收结论与已知边界

> 本节按 D28 取代原「变更记录」：只记**验收结论**与**已知边界**，不记过程性改动流水。

（待实施与终验后回填）
