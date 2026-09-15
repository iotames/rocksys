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
5. **GeoIP 同步**：手动增量 + 定时自动（间隔可配，默认每小时）；显示上次同步时间。
6. **数据不造假**：库列保持解析真实语义（该空则空），拼接/兜底只做在读侧显示。

---

## 3. 已确认决策表（现行有效决策清单：结论过时即删旧行、变更追加新行；取舍理由留 §6）

| # | 决策点 | 结论 | 出处 |
|---|---|---|---|
| D1 | 去冗余方向 | 新建 `geoip_list` 关联表，取代逐行存 geo | 用户 09-11 |
| D2 | 两表 4 列 | 全部删除（开发阶段） | 用户 09-11 |
| D3 | 关联键 | 用 `client_ip` JOIN（两方一致、复杂度低） | 用户 09-11 |
| D4 | IP 列类型 | 纯文本（三方言一致），**不引第三方 DB / DB 特有 IP 类型** | 用户 09-11 |
| D5 | `geoip_list` 维护 | 手动增量同步 + 定时自动（**写日志路径**不查该表；同步器读表求差不受影响） | 用户 09-11 |
| D7 | 上次同步时间 | 落在 **`schedule_list`** | 用户 09-11 |
| D8 | 1 IP 多归属 | 不做（`ip` 唯一键） | 用户 09-11 |
| D9 | `province` | 本期落地（`city` 只存城市名） | 用户 09-11 |
| D10 | `country` 语义 | 拆 `country_code`（聚合/地图）+ `country_name`（显示） | 用户 09-11 |
| D11 | 空值口径 | 数据层不造假，拼接/兜底只在读侧 | 用户 09-11 |
| D12 | 交付节奏 | 先出方案，定稿后实施 | 用户 09-11 |
| D13 | 定时任务登记 | 统一收敛到 `schedule_list`（含已有任务 + GeoIP 同步） | 用户 09-11 |
| D15 | 配置区新增页 | 「配置」组、「数据库」之后新增「定时任务」（`#/schedule`） | 用户 09-11 |
| D17 | 状态读取端点 | 新增统一 `GET /admin/schedule/list`；`/admin/db/geoip_sync` **维持 POST-only 现状不动**（防 GET 触发批量写，既有安全语义，本就无 GET） | 用户 09-11 |
| D18 | `schedule_list` 定位 | **只读登记 + 状态汇总（不驱动）**；不适配统一模式的定时任务不塞入，以**代码注释 + 页面说明**标注 | 用户 09-11 |
| D19 | 登记清单 | 可配型 4 个 + 系统级只读若干（见 §4.3） | 用户 09-11 |
| D20 | 系统级任务 | **整行只读**；被改则重启时**按 `name` 定位重置为系统值** | 用户 09-11 |
| D21 | 状态回写范围 | 本期**只回写 `geoip_sync`**；obs/shield/auto_ban 现有触发循环**原样不动**（零侵入） | 用户 09-11 |
| D22 | 手动触发 | 沿用现有 `POST /admin/db/geoip_sync`，**不新建统一 run** | 用户 09-11 |
| D23 | 增量算法 | 沿用 `id` 主键游标分块 + 内存去重（**禁无界 DISTINCT**，`geoip_sync.go:7` 已证其超时） | 架构审查 09-11 |
| D24 | JOIN 聚合性能 | 实施阶段用真实库实测；备选 Go 侧把 `geoip_list` 装内存 map，SQL 只按 `client_ip` 分组 | 架构审查 09-11 |
| D25 | 统计缓存 | geo 同步成功后清 traffic 缓存（`OBS_TRAFFIC_CACHE_TTL`），避免展示滞后 | 架构审查 09-11 |
| D27 | 模块边界 | 遵循「底座不依赖挂件」：登记与只读端点放装配层（`cmd/rocksys`）/无挂件原语，`internal` 不 import `plugins` | 架构审查 09-11 |
| D28 | README「变更记录」 | 改为「**验收结论 / 已知边界**」（去过程流水），宪法 §1/§5 相应修订 | 用户 09-11 |
| D29 | 自动同步频率 | 每小时增量：间隔可配 `GEOIP_SYNC_INTERVAL`（**int，单位分钟**，默认 60=1h，取值边界见 D34）；「同步间隔内新 IP 在实时聚合视图暂缺」入已知边界 | 用户 09-14 |
| D30 | 删列路径 | 建表脚本直接删 4 列，**不写迁移脚本**（开发阶段无存量生产数据；旧库残留列 schema 同步 F 级仅提示、无害） | 用户 09-14 |
| D31 | 聚合卡同步入口 | 概览页地理位置卡（唯一聚合 SQL 消费组件）旁：能力边界注记 + GeoIP 同步按钮（复用 `POST /admin/db/geoip_sync`；三触发源收敛 `geoSyncAll` 唯一入口） | 用户 09-14 |
| D33 | 存量解析刷新 | 本期不做：已入表 IP 不随 mmdb 更换自动重解析（暂不考虑）；需要时删 `geoip_list` 后全量同步重建 | 用户 09-14 |
| D34 | 自动同步控制 | **不设独立开关 `GEOIP_SYNC_AUTO_ENABLED`**，开关语义并入 `GEOIP_SYNC_INTERVAL`：**0 = 关闭**自动同步（手动不受影响）；有效最小 **10 分钟**（防设置过小耗尽系统资源），<10（非 0）或非法回落默认 60。**生效前置 = mmdb 已加载**：未加载则定时器不启动（此时配置任意值均不生效），`schedule_list` 登记行注明依赖未满足。运行中修改间隔**下一轮生效**（每轮触发时重读配置值，含 0=关闭的动态判定） | 用户 09-14 |

---

## 4. 设计方案

### 4.0 数据结构与数据流（数据设计总览，宪法 §7）

> 数据模型：**地理信息是 IP 的函数**——日志表只存事实（`client_ip`），地理与登记各自归一成独立实体表；展示语义（拼接/兜底）全部移到读侧，库列只承载真实解析结果。

**实体与关系**

| 实体 | 粒度 | 关系 |
|---|---|---|
| `geoip_list` | 一 IP 一行 | `geoip_list.ip` ← `access_log.client_ip` / `shield_event.client_ip`（LEFT JOIN 等值关联，无外键，纯文本 IP 同格式无端口）；1 : N（一行地理对应两表多行日志） |
| `schedule_list` | 一任务一行 | `name` ↔ 代码常量绑定（装配期 upsert 登记）；与业务表零外键，运行期仅 `geoip_sync` 行回写状态 |

**数据流转（四条路径）**

1. **写路径**：请求放行 / WAF 拦截 → 日志表只写 `client_ip`（不再逐行落地理信息）。
2. **同步路径**：同步器扫两表 `client_ip` 与 `geoip_list` 求差 → mmdb `Lookup` → upsert `geoip_list`（`created_at` 首次入库、`updated_at` 每次刷新）。
3. **读路径**：明细/Top 查询 JOIN `geoip_list`，未命中回退实时 `Lookup`；聚合查询按 `g.country_code` / `g.province` 分组（世界地图 ISO2、中国地图 zh-CN 全省名直连）。
4. **登记路径**：装配期 `schedule_list` upsert 登记（`kind=system` 行重启重置）；`geoip_sync` 执行结束回写 `last_run_at` / `last_status`。

新表完整结构见 §4.1/§4.3；删列与索引影响见 §4.2/§4.7；同步细节见 §4.5。

### 4.1 新表 `geoip_list`（一 IP 一行）

```sql
CREATE TABLE IF NOT EXISTS {table} (
    ip           VARCHAR(64)  PRIMARY KEY,          -- 纯 IP 文本（与日志表 client_ip 同格式，无端口）
    country_code VARCHAR(8)   NOT NULL DEFAULT '',  -- ISO alpha-2（聚合口径 + 世界地图着色）
    country_name VARCHAR(64)  NOT NULL DEFAULT '',  -- 本地化国名 zh-CN 优先（显示）
    province     VARCHAR(128) NOT NULL DEFAULT '',  -- 一级行政区（省/州；中国地图着色）
    city         VARCHAR(128) NOT NULL DEFAULT '',  -- 城市名（仅市）
    created_at   DATETIME     NOT NULL,             -- 首次解析入库时间（UTC）
    updated_at   DATETIME     NOT NULL              -- 该 IP 最近解析时间（UTC）；冲突更新时保持 created_at 不变
)
```
方言：SQLite `DATETIME` / MySQL `DATETIME(3)` / PG `TIMESTAMPTZ`（照 `sql_exec_log_create_table.sql` 风格）。索引：主键 `ip`；可选 `idx_{table}_country(country_code)`。

### 4.2 关联与删列

- 两表保留 `client_ip`，查询 `LEFT JOIN geoip_list g ON g.ip = {table}.client_ip`；读侧统一「市→省→国名→未知」兜底。
- 删 `access_log`/`shield_event` 的 `country`/`city`，并移除 `idx_*_time_country`。
- 聚合改造：`traffic_geo_top.sql` → `GROUP BY g.country_code`（带回 `country_name`）；`traffic_geo_province_top.sql` → `GROUP BY g.province WHERE g.country_code='CN'`（**去掉字符串切分**）。`province` 存 **zh-CN 全称**（如「广东省」）——与 `geomap.js` 既有「全称→短名」前端映射链兼容（其输入一直是全称），实施**不得存短名**（否则中国地图着色静默错位）。
- 性能：实施阶段实测 JOIN 聚合；不达标则改 Go 侧内存映射（D24）。

### 4.3 `schedule_list`：只读登记表（不驱动）

```sql
CREATE TABLE IF NOT EXISTS {table} (
    id           INTEGER PRIMARY KEY AUTOINCREMENT, -- 自增主键
    name         VARCHAR(64)  NOT NULL UNIQUE,       -- 任务唯一标识（唯一约束照仓库惯例，如 ip_blacklist.ip：SQLite/PG 内联、MySQL 表级；代码常量绑定）
    title        VARCHAR(128) NOT NULL DEFAULT '',  -- 中文名（展示）
    kind         VARCHAR(16)  NOT NULL DEFAULT '',  -- configurable（可配型）/ system（系统级只读）
    config_key   VARCHAR(64)  NOT NULL DEFAULT '',  -- 关联 easyconf 开关名（可配型；系统级空）
    plan         VARCHAR(64)  NOT NULL DEFAULT '',  -- 计划描述（仅展示：every@1h（geoip_sync，可配）/ every@24h / window/3）
    last_run_at  DATETIME,                          -- 上次执行完成时刻（UTC；语义=执行结束而非开始）；仅回写者有意义（本期=geoip_sync）
    last_status  VARCHAR(16)  NOT NULL DEFAULT '',  -- success / failed / skipped
    last_message VARCHAR(255) NOT NULL DEFAULT '',  -- 结果摘要
    remark       VARCHAR(255) NOT NULL DEFAULT '',  -- 说明（含"不纳入原因/只读"）
    updated_at   DATETIME     NOT NULL
)
```

方言：`id` 自增主键照仓库惯例（SQLite `INTEGER PRIMARY KEY AUTOINCREMENT` / MySQL `BIGINT AUTO_INCREMENT PRIMARY KEY` / PG `BIGSERIAL PRIMARY KEY`，照 `mq_create_table.sql` 等既有表）；时间列同 §4.1。

- **定位**：只做**登记 + 状态汇总**，**不驱动任何任务**（D18）。无真实触发逻辑，故「检查粒度/精度」不适用；需要实时/精确触发的任务**不纳入**，以代码注释 + 页面说明标注。
- **登记方式**：装配期由代码按 `name` **upsert（新增或更新）**；`kind=system` 行**整行只读**，被改则重启按 `name` 重置为系统值（D20）。
- **开关**：**无 `enabled` 列**；启用状态由 `GET /admin/schedule/list` 响应行内附带 `enabled`（bool，服务端读 `config_key` 对应 easyconf 项当前值；`config_key` 为空的系统级行恒为 true），页面一次取全、无需另查配置端点。
- **纳入清单（D19）**：
  - `configurable`（4）：`obs_log_prune`(`OBS_LOG_PRUNE_ENABLED`)、`shield_event_prune`(`SHIELD_EVENT_PRUNE_ENABLED`)、`shield_auto_ban`(`SHIELD_AUTO_BAN_ENABLED`)、`geoip_sync`(`GEOIP_SYNC_INTERVAL`)。
  - `system`（只读，若干）：hotswap 文件监控、conf 配置轮询、registry 心跳扫描、dispatch 健康检查、mq 投递轮询、shield IP 快照 TTL 重建、shield 事件 flush。
  - **不纳入**（精确/实时类或非任务语义）：workpool 重试/worker 检查等；在代码注释与页面注明"该类不纳入统一登记"。
- **状态回写范围**：本期仅 `geoip_sync`（D21）——老任务零侵入；其 `last_run_at` 即「上次同步时间」。回写为任务结束时单条原子 UPDATE，并发由同步器互斥保护（同一时刻仅一个同步在跑，见 §4.5）。

### 4.4 只读页与端点

- 页面：`index.html` 配置组内、「数据库」之后新增 `#/schedule`「定时任务」；新增 `views/schedule.js`（表：名称/说明/类型/关联开关/**上次执行时间**）；`main.js` 路由 + `state.js`。**页面只读**（无切换、无触发按钮）。**上次执行时间**仅对本期回写者（`geoip_sync`）有值，其余行须页面标注「未登记」并加说明——否则会被读成"任务从未执行"。**UX 红线**：页面 load 的 fetch 必须透传 `refreshPage` 的 `opts`（含 `silent`）；非引导态的加载失败弹 `Rock.ui.toast(msg, 'error')`（不自动消失），行内灰字可同时保留。
- 端点：新增 **`GET /admin/schedule/list`**（只读，供该页与数据库页读取）。
- 配置项新增（经 `conf.Manager.Register` 注册进配置中心，title/usage/备注填全注意点，`default.env` 随装配自动同步）：
  - `GEOIP_SYNC_INTERVAL`：**int，单位分钟（不用字符串/duration）**，是 GeoIP 自动同步的**唯一控制项**（不设独立开关，D34）。**0 = 关闭**自动同步（手动同步不受影响）；有效最小 **10**（防设置过小耗尽系统资源），默认 **60**（=1h）；<10（非 0）或非法值回落默认 60。**生效前置 = mmdb 已加载**：未加载时该配置不生效（定时器不启动），usage 与 `schedule_list` 登记行 remark 均注明。

### 4.5 GeoIP 同步（D5/D29/D23/D25/D31）

- **唯一入口收敛**：同步核心 = `geoSyncAll` 单函数（现有互斥 `geoSyncRunning` 沿用：执行中再触发直接返回「进行中」，不排队）；触发方三处——数据库页同步卡、概览页地理位置卡同步按钮（D31）、定时器（装配层 ticker，D27）——全部调它，不另起炉灶。
- **手动**：沿用 `POST /admin/db/geoip_sync`（D22），语义改为「增量构建/刷新 `geoip_list`」；数据库页与概览聚合卡两按钮共用该端点（沿用 30s 客户端超时与加载态防重复；结果走 `Rock.ui.toast`；成功后刷新聚合卡——同步已清缓存（D25），刷新即见新数据）。
- **自动**：每 `GEOIP_SYNC_INTERVAL` 分钟（int，默认 60；**0=关闭**，最小 10，D34）触发。**启动前置 = mmdb 已加载**（装配期判定，mmdb 不支持热加载、放置后须重启）：未加载则不启动定时器——此时无论间隔配多少均不生效（同步无意义），`schedule_list` 的 `geoip_sync` 行 remark 注明「mmdb 未加载，自动同步未启动」。执行结束在 `geoSyncAll` 入口内回写 `last_run_at`/`last_status`（**手动两处与定时触发皆回写**，回写点单一）；运行中修改间隔**下一轮生效**（每轮触发时重读配置值，含 0=关闭的动态判定）。单趟未追平（单趟 IP 上限/时间预算）由下一轮继续——增量求差天然幂等，重扫已入表 IP 无害。
- **增量算法**（D23）：**沿用现有 `geoip_sync.go` 骨架（`id` 游标分块 + 断点 + 时间预算 + 互斥），重写发现与回填两段**——发现谓词从「`country` 为空的行」（绑定旧库列，删列后失效）改为「`client_ip` 未入 `geoip_list`」（内存求差，**禁无界 `DISTINCT` 全表扫描**，`geoip_sync.go:7` 已证其超时）；回填动作从 CASE WHEN 批量 UPDATE 两表列改为逐 IP `geo.Lookup` 后 upsert `geoip_list`。
- 新增三方言 `geoip_list_upsert.sql`（SQLite/PG `ON CONFLICT`、MySQL `ON DUPLICATE KEY`）；冲突更新仅刷新 geo 相关列与 `updated_at`，`created_at` 保持首次入库值。
- 同步成功后清 traffic 统计缓存（D25）。
- **能力边界注记（D31）**：概览页地理位置卡（`overview.js`，唯一聚合 SQL 消费组件）旁注明「数据按同步间隔更新，最近一个间隔内新访问的 IP 暂不计入地图/省级榜；点击同步可立即补齐」。
- 写日志（obs/shield）：**只写 `client_ip`**，去掉逐行 geo 写入；**读侧 JOIN 未命中时回退实时 `Lookup`**（详情/Top 结果集小，保新 IP 及时可见；借鉴 `event_recorder.go:581` 逐行 Lookup 模式——彼处为 Top IP 全行实时解析、SQL 不查 geo 列，此处为 JOIN 命中用存量、未命中才回退）。

### 4.6 影响面清单

- **SQL（三方言）**：新增 `geoip_list_create_table.sql`/`_create_index.sql`/`_upsert.sql`、`schedule_list_create_table.sql`/`_create_index.sql`；改 `access_log_{create_table,insert,query,create_index}.sql`、`shield_event_{...}.sql`、`traffic_geo_top.sql`、`traffic_geo_province_top.sql`；顺带清理 `traffic_summary.sql` sqlite 版头注释对 country 的提及（三方言注释保持一致）；`sql/*/README.md`。
- **Go**：`internal/geoip/geoip.go`（+`Province`）；`plugins/obs/{obs.go,db_store.go,dim.go,traffic.go}`、`plugins/shield/{event_recorder.go,admin.go}`（去两列、明细查询改 JOIN；stats Top IP 维持查询时实时解析）；`cmd/rocksys/geoip_sync.go`（骨架复用：重写发现/回填段为 `geoip_list` 增量构建，+ 间隔定时器）、`cmd/rocksys/main.go`（登记/端点/配置/间隔触发）；相关 `_test.go`。
- **端点**：`GET /admin/schedule/list`（新）；`/admin/logs`、`/admin/shield/events`、`/admin/obs/traffic/geo`（改 JOIN）；`/admin/shield/stats` **维持现状不动**——其 Top IP 地理信息本就查询时实时解析、不读库列（`webui-api.md` 已明示），删列零影响，无列可 JOIN。
- **前端**：`index.html`（菜单）、`views/schedule.js`（新）、`main.js`、`state.js`；`views/database.js`（同步卡文案 + 上次同步时间）、`views/overview.js` + `components/geomap.js`（地理位置卡：+ 同步按钮与能力边界注记 D31）、`views/logs.js`/`waf.js`（详情）、`views/topIPs.js`（地区列）。
- **文档**：`docs/DATA_DICT.md`、`docs/webui-api.md`、`docs/COMPONENTS.md`、`docs/webui.md`、`docs/CONFIGURATION.md` 与 `docs/PROJECT_STRUCTURE.md`（两处均描述 country/city 写时落库链路，删列后必同步）、`sql/*/README.md`；顺带修正 `webui-api.md` 中 `client_ip` 示例带端口与现状不符。

### 4.7 建表与删列顺序（D30，无迁移脚本）

> 本节为**数据安全边界约束**（删列必须发生在端点切换之后），非实施排期——执行排期归总纲。

顺序：① 建 `geoip_list` + `schedule_list`（schema 同步 A 级）→ ② GeoIP 同步全量构建 `geoip_list` 并校验 → ③ 端点/前端切 JOIN → ④ 建表脚本删两表 4 列及 `idx_*_time_country`（D2/D30）。

**不写迁移脚本、不做回退设计**（开发阶段、无存量生产数据须保全；`geoip_list` 可由同步随时重建）。旧开发库（如 `bin/rocksys.db`）的残留 `country`/`city` 列由 schema 同步按 **F 级仅提示、不自动 DROP**；残留的 `idx_*_time_country` 索引同理无害（schema 同步不检测多余索引），可与残留列一并在人工清理时 DROP。

### 4.8 明确不做（边界）

- 不做通用"调度器驱动"（D18）；不新建统一手动触发端点（D22）；
- 不纳入实时/精确触发的内部机制（D18）；
- 不做 1 IP 多归属（D8）；本期不清理 `geoip_list` 长期未引用 IP；
- 隐私/回环等无 geo 的 IP 不入 `geoip_list`（读侧显示「未知」）。

---

## 5. 验收标准

1. `geoip_list`、`schedule_list` 三方言建表/索引/upsert 脚本齐备，经 schema 同步可在 WebUI 确认执行；`docs/DATA_DICT.md` 同步登记。
2. `geoip_list` 覆盖两表全部「可解析 geo 的唯一 IP」（私网/无 geo IP 不入表、读侧显示「未知」）：同步完成后抽查「可解析而未入表」差集为空（同步间隔内新增行除外）。
3. 两表建表脚本已删 4 列及 `idx_*_time_country`（D30）：新建库经 schema 同步确认无该列；旧库残留列仅 F 级提示、不自动 DROP。
4. `/admin/logs`、`/admin/shield/events` 经 JOIN 正确返回 `country_code/country_name/province/city`；详情「市→省→国名→未知」兜底正确。
5. `/admin/obs/traffic/geo` 聚合正确且**不再字符串切分**；概览页热力图（世界=ISO2、中国=中文省名）与 Top 排名正常；实测 JOIN 聚合性能达标（否则走 D24 备选）；聚合对已同步 IP 完整，同步间隔内新 IP 暂缺属已知边界（D29，页面已注记 D31）。
6. **定时任务**：`schedule_list` 登记可配型 4 + 系统级若干；装配期 upsert；系统级行重启重置；`#/schedule` 只读页展示；`geoip_sync` 按可配间隔（`GEOIP_SYNC_INTERVAL`，int 分钟，默认 60、0=关闭、最小 10，D34）自动（**mmdb 未加载时不启动定时器**且登记行注明依赖未满足）+ 手动两入口（数据库页、概览聚合卡）可触发；数据库页 GeoIP 同步卡显示**上次同步时间**；geo 同步后统计缓存已失效。
7. `go build -tags dev` / `go vet ./...` / `go test ./...` 全绿；涉及前端页面**开浏览器实看并截图**留证。
8. 全部相关文档同步（DATA_DICT / webui-api / COMPONENTS / webui / CONFIGURATION / PROJECT_STRUCTURE / sql README）。

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
| 空桶窗口（新 IP 聚合暂缺） | 维持每日 05:00 / 每小时增量 / 请求时补扫 / Go 侧补算 | 每小时增量（D29） | 增量骨架已备、单趟代价小；暂缺窗口 24h→1h；更重方案（补扫/补算）复杂度不值 |
| 删列路径 | 两步迁移（legacy 改名 + 备份）/ 建表脚本直接删 | 直接删、无迁移脚本（D30） | 开发阶段无存量生产数据；旧列残留 F 级提示无害；数据可由 GeoIP 同步重建 |
| 同步触发入口 | 各触发方自实现 / 收敛 `geoSyncAll` 单入口 | 收敛单入口（D31） | 现状已单端点单函数且有互斥；新触发方（概览卡按钮、定时器）只加调用不加逻辑 |
| 自动同步控制 | 独立开关 `GEOIP_SYNC_AUTO_ENABLED` / 间隔值内置开关语义 | 间隔内置：0=关闭、最小 10 分钟（D34） | 少一个配置项、职责不纠缠；0=关闭是直觉语义；下限防过小耗尽资源；mmdb 未加载为生效前置 |
| 存量解析刷新 | 手动全量重解析按钮 / 自动定期刷新 / 本期不做 | 本期不做（D33） | 月度漂移量级小、影响可忽略；需要时删表全量重建，不加常驻机制 |

## 7. 定稿前四维质量自查（宪法 §2）

- **工程化**：沿用 `id` 游标增量、三方言脚本齐平、走 schema 同步分级、遵守配置中心红线；无新增第三方依赖。
- **用户体验**：数据名实相符、省级聚合不再脆弱、地图/列表兜底可用；新增只读「定时任务」页与「上次同步时间」；新 IP 实时性分层保障——详情/Top 由读侧回退保住、聚合视图由每小时增量（D29）+ 边界注记（D31）覆盖。
- **改动量与复杂度（是否过度设计）**：**主动裁掉**通用调度器驱动（D18），只做只读登记；定时任务页只读无写操作；避免为 4 个异构任务造调度中枢。
- **架构统一性**：遵循"底座不依赖挂件"（D27）、配置中心唯一真源（D18）、数据字典红线（DATA_DICT 同步）。

## 8. 验收结论与已知边界

> 本节按 D28 取代原「变更记录」：只记**验收结论**与**已知边界**，不记过程性改动流水。

**已知边界（设计期已定，终验核对）**

- 同步间隔（默认 1 小时）内新出现的 IP，在实时聚合视图（概览页地理位置卡）中暂不计入；手动点击同步按钮可立即补齐（D29/D31，页面已注记）。
- 已入表 IP 的解析结果不随 mmdb 数据文件更换自动刷新（增量只补新 IP）；本期不做刷新机制（D33）——需要时删 `geoip_list` 后全量同步重建（本库验收时已按此口径重建过一次）。
- 冷启动多轮追平：单趟受 5000 IP 上限与 20 秒时间预算限制（沿用现有 `geoip_sync.go` 常量），存量唯一 IP 多的库首次构建需多轮（每小时一轮）逐步追平；期间 `last_status` 显示 success 但地图数据仍在分批补齐，属预期（手动连点可加速）。
- **实施期新增**：mmdb zh-CN 省份名实测多为短名（「广东」），部分为全称（「北京市」）——非「恒为全称」（原假设过强）。短名与中国地图 geojson 短名直连、全称经前端 chinaShort 映射，两者均正确着色，D9 意图（地图不错位）不受影响；`province` 列口径 = mmdb zh-CN 实际返回（数据不造假）。
- **实施期修复存量缺陷**：maxminddb-golang v2 对嵌套包装结构体承接 `Country.Names` 解码为空（省/市与数组形态正常）——`internal/geoip` 改直写 `map[string]string` 后 zh-CN 国名正常；此前 `country_name` 从未真实生效过。

**验收结论**（2026-09-15 终验通过）

1. ✅ `geoip_list`（7 列）/`schedule_list`（11 列）三方言建表/索引/upsert/重置/清单/状态回写脚本齐备并入 `buildTableSpecs`（数据库页 schema 同步可见，旧库残留 country/city 列为 F 级仅提示）；`docs/DATA_DICT.md` §2.9/§2.10/§3.5 同步登记。
2. ✅ 同步器对开发库（access_log 65 万行）实测：单趟预算内 upsert 745~758 个新 IP、私网/回环不入表、幂等重跑 0 行；`/admin/logs` 抽样「可解析而未入表」差集随同步轮次收敛（间隔内新行除外，属 D29 已知边界）。
3. ✅ 两表建表脚本已删 4 列与 `idx_*_time_country`；新库经 EnsureTable 建表即无该列；旧库残留列 F 级提示、不自动 DROP（数据库页实看确认）。
4. ✅ `/admin/logs`、`/admin/shield/events` 经 JOIN 返回 `country_code/country_name/province/city`（接口直验 + WAF 详情弹层实看「美国 / 爱荷华州/康瑟尔布拉夫斯」）；「市→省→国名→未知」兜底在读侧生效。
5. ✅ `/admin/obs/traffic/geo` 聚合改经 geoip_list（`substr/instr` 字符串切分已从三方言脚本移除，单测覆盖）；概览页世界地图（ISO2 着色）与中国地图（省名直连/映射）实看正常、Top 榜中文国名；JOIN 聚合在开发库实测响应正常（未触发 D24 备选）；同步间隔内新 IP 暂缺已注记（D31）。
6. ✅ `schedule_list` 登记可配型 4 + 系统级 7；装配期 upsert（系统级整行重置，reset 脚本实施期修为全列覆写 upsert 以支持全新库首登）；`#/schedule` 只读页实看正常；`GEOIP_SYNC_INTERVAL` 语义 0=关闭/最小 10/默认 60（纯函数单测覆盖），mmdb 未加载不启动定时器且登记行注明；定时器实测启动约 1 分钟后自动执行并回写 `last_run_at/last_status`；手动两入口（数据库页同步卡、概览卡「立即同步」实点验证）可触发并显示上次同步时间；geo 同步成功后统计缓存自动失效（同步后排名即时增长验证）。
7. ✅ `go build`（dev 与生产）/`go vet ./...`/`go test ./... -count=1` 全绿；前端实看并截图留证（定时任务页、概览世界/中国地图、数据库页同步卡）。
8. ✅ 文档同步完成：DATA_DICT / webui-api（含 1.11 变更记录与 client_ip 带端口示例修正）/ COMPONENTS / webui（新增 §4.16）/ CONFIGURATION / PROJECT_STRUCTURE / sql README ×3。

**临时决策**：无（`geoip_list_DECISIONS.md` 未创建——实施中的偏差均为设计文档内的实现细节取舍，已记录于各 STEP 回填区）。
