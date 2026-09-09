# 流量统计与 GeoIP（TRAFFIC_ANALYSIS）设计文档

> 状态：**待人类定稿确认**（宪法 §2 阶段 2 关口）。确认前不建总纲/STEP、不动代码。
> 需求来源：2026-09-08/09 会话——参照雷池 WAF 统计报表能力，做按需 SQL 聚合的流量统计，地理位置本期实施；运行指标与流量统计合并进首页概览，指标不重复。

## 1. 现状结论（带证据）

| 事实 | 证据 |
|---|---|
| 放行/拦截两表分离：`access_log`（16 列：time/method/client_ip/status_code/耗时四段/流量，**无 UA、无 host**）、`shield_event`（14 列：含 user_agent/host/block_type/status_code） | `sql/{sqlite,mysql,postgres}/*_create_table.sql`；`docs/DATA_DICT.md` §2 |
| 两表 INSERT 脚本均为**显式列清单**：加列不破坏现有写入，但新列落库必须同步扩充列清单与参数 | `sql/sqlite/access_log_insert.sql`、`shield_event_insert.sql` |
| **表结构同步机制**：`/admin/db/schema` 经 DiffSchema 对比实际 catalog、GenerateSQL **生成**补列/补索引 SQL；执行靠 `/admin/db/exec`（danger 级，WebUI 人工强确认）。启动时仅 `SetTableSpecs` 注入清单（两表在清单内），**无任何启动自动迁移**——"自动补列"实为"自动生成、人工执行" | `internal/db/schema_diff.go:236,381`、`internal/adminapi/dbschema.go:49,59,76`、`cmd/rocksys/main.go:406,613-623`、`docs/PROJECT_STRUCTURE.md:92-95` |
| obs 落库点可直接取请求头：OnDone 里已在用 `ctx.R.URL.Path/Method/ContentLength`，加 UA 仅需 `ctx.R.Header.Get("User-Agent")`（shield 侧已有同法先例） | `plugins/obs/obs.go:252-261`、`plugins/shield/event_recorder.go:162` |
| "累计落库/累计丢弃"是**本次运行以来**的内存 atomic 计数，重启清零，与 1 分钟窗口无关；WAF 页现标签未表达"重启清零"语义 | `plugins/shield/event_recorder.go:200-201,320`、`waf.js:200-202` |
| WAF 实时窗口固定 1 分钟（内存滑动窗口，`window_seconds:60` 硬编码），不可选时长 | `plugins/shield/admin.go:63-81` |
| 实时 QPS 已有内存实现（1 分钟滑动窗口）；概览页趋势图为**前端采样**：每次页面刷新采 1 点、最多 240 点，存浏览器内存，刷新/离开即丢，无历史可回溯 | `plugins/obs/obs.go`、`webui/assets/js/views/overview.js:93-103`、`state.js:23` |
| 按天聚合已有三方言范式（substr/DATE_FORMAT/to_char，统一 UTC 口径），**最细粒度只有天，无小时桶**；`shield_event_count.sql` 带条件参数可复用 | `sql/*/shield_event_stats_daily.sql`、`shield_event_count.sql` |
| WebUI 纯自绘图表（Canvas 折线），无第三方图表库；时间筛选组件 `dateRange`、Top 攻击 IP 子视图已留地区占位 TODO | `components/chart.js`、`dateRange.js`、`views/topIPs.js:6,69` |
| 全仓无任何 geoip/mmdb 代码与依赖 | go.mod 及全仓检索无命中 |

## 2. 已确认决策表（只追加不覆盖）

| # | 决策点 | 结论 | 理由 / 来源 |
|---|---|---|---|
| D1 | 计算策略 | 打开页面/切时间范围才做 SQL 聚合；服务端 singleflight + TTL 结果缓存，TTL 默认 10 分钟（0=禁用），配置项 `OBS_TRAFFIC_CACHE_TTL` 注册进配置中心 | 用户拍板（09-08）；防大表重复聚合拖库 |
| D2 | 页面落点 | ~~新增独立「流量分析」页~~ **被 D10 取代** | — |
| D3 | PV/UV 口径 | ~~首版近似：UV≈独立 IP~~ **被 D11 取代** | — |
| D4 | 地理位置 | ~~本期 TODO~~ **被 D12 取代** | — |
| D5 | UA/host 列 | ~~后续批次~~ **user_agent 列随 D11 本期实施**；host 列（按域名筛选）仍留后续 | 衍生自 D3/D4 |
| D6 | 接口归属 | 读侧统计端点放 obs 挂件（观测/报表归属地）；geo 解析独立小包 `internal/geoip`，写入链路（obs/shield）与读侧共用 | 智能体建议，已确认范围 |
| D7 | 索引 | 本期不加新索引 | 两表 time 索引已够范围扫描；性能以真实流量为依据，缓存兜底 |
| D8 | 实时 QPS | 复用 `GET /admin/metrics` 内存窗口，不缓存不查库 | 已有能力 |
| D9 | 图表 | 复用自绘 Canvas 组件（`chart.js`），不引入图表库 | 外部依赖最小化红线 |
| D10 | 页面落点（定稿） | **流量统计并入首页概览**：概览页 = 实时运行区（现状不动）+ 新增「流量统计」区（时间范围筛选 + 指标卡 + 趋势 + 地理位置），**不新增独立页** | 用户拍板（09-09）：一进首页即心中有数；避免与运行指标重复建页 |
| D11 | UV 口径（定稿） | **UV = COUNT(DISTINCT client_ip + user_agent)**（access 侧）；`access_log` 本期加 `user_agent` 列（写入侧 ctx.R 现成可得）；补列 SQL 经既有 schema_sync 机制生成，**WebUI 人工确认执行**（见 D16） | 用户拍板（09-09）：局域网出口 IP 后有多个用户，IP+UA 更准确 |
| D12 | 地理位置（定稿） | **本期实施**：`GEOIP_MMDB_DIR` 查找链加载 City/Country 两个 mmdb；`access_log`/`shield_event` 各加 `country`/`city` 列，**写时解析**入库（入库时解析 mmdb 存列，国家聚合走 SQL）；Top IP 表格地区列用**查询时解析**（读出结果后逐行解析，行数少免加列）；缺失时 warning 日志 + 页面警告（形态见 D17），功能降级不阻断 | 用户拍板（09-09） |
| D13 | WAF 实时窗口 | 内存滑动窗口延长且桶宽可选（1m/5m/15m/1h，纯内存无查库）；"累计落库"标签更正为「本次运行落库（重启清零）」，另新增查库口径的「落库总数」瓦片（受保留期影响） | 用户拍板（09-09）：现标签不清晰 |
| D14 | 实时趋势归属 | 概览页运行指标的前端采样趋势保持现状（心跳观感、零成本）；按时间桶的历史回溯由流量统计区承担，二者语义不同不互替 | 用户问询 09-09 的结论方案，关口确认 |
| D15 | 第三方依赖 | 引入 `github.com/oschwald/maxminddb-golang/v2`（全项目唯一新增第三方库） | v2 为当前主线（v1 已停功能维护）；mmdb 二进制格式自解析成本过高，符合依赖最小化例外论证 |
| D16 | 老库升级同步处置 | 保持"执行人工确认"安全设计不变；**启动时检测两表缺列打 warning 日志**（提示管理员去 WebUI `/admin/db/schema`→`/admin/db/exec` 执行同步）；升级风险写入已知边界 | 用户拍板（09-09） |
| D17 | geo 缺失警告形态 | **会话内首次进入依赖 geo 的页面弹一次统一警告 toast**（sessionStorage 标记，不重复刷屏）+ **页内常驻警告引导卡**（含下载放置指引）；符合体验红线豁免②降级引导态 | 用户拍板（09-09） |

## 3. 设计方案

### 3.1 指标/功能归属（去重原则：一个指标只有一处"权威完整视图"，其余位置只做不同口径切片且标注口径）

| 指标/功能 | 权威位置 | 口径/数据源 |
|---|---|---|
| 实时 QPS、P95、错误率 + 前端采样趋势 | 首页·运行指标区（现状不动） | `/admin/metrics` 内存窗口 |
| 运行时长、CPU/内存资源 | 首页·资源监控卡（现状不动） | `/admin/system` |
| 请求次数、PV、UV、独立 IP、4xx/5xx 数与率 + 访问趋势 | 首页·流量统计区（新） | SQL 聚合 + 缓存 |
| 拦截次数、攻击 IP、4xx 拦截数率 + 拦截趋势 | 首页·流量统计区（新） | SQL 聚合 + 缓存 |
| 地理位置（Top 国家/地区，访问/拦截切换） | 首页·流量统计区（新） | 写时解析列 GROUP BY |
| 实时拦截（桶宽可切 1m/5m/15m/1h）、本次运行落库/丢弃 | WAF 页·实时拦截卡（改造） | 内存滑动窗口 |
| 落库总数 | WAF 页·新增瓦片 | 查库 COUNT（受保留期影响） |
| 按日×拦截类别分布、Top 攻击源 IP（+地区列）、拦截明细 | WAF 页（现状保留；Top IP 接 geo） | SQL；地区列查询时解析 |

> 口径辨析：首页"拦截次数"（所选时间范围的落库值）与 WAF"实时拦截"（内存窗口）是不同口径的不同切片，标签写明即不算重复；WAF 按日图保留**类别维度**（首页趋势仅总量），维度不同不重复。

### 3.2 数据层（本批 schema 变更，走数据字典红线三同步）

- **加列**：`access_log` 加 `user_agent`/`country`/`city`，`shield_event` 加 `country`/`city`；三方言均为 `TEXT NOT NULL DEFAULT ''`（老行加列后为空串，UV/geo 自然退化，见下）。
- **三处同步（红线，缺一即违规，编号对应仓库法数据字典红线）**：
  1. 三方言建表脚本（列定义+注释）及 INSERT 脚本（现 `access_log_insert.sql` 15 列 / `shield_event_insert.sql` 13 列，同步扩充列清单与参数）；
  2. `docs/DATA_DICT.md` 两表章节（字段/说明/三方言类型）；
  3. Go 权威定义：obs `AccessRecord`（dim.go）及 OnDone 条目构造、shield `ShieldEvent`（event_recorder.go newEvent）补新字段。
- **老库升级路径（D16）**：`/admin/db/schema` 生成两表补列 diff 与 SQL → 管理员在 WebUI 经 `/admin/db/exec` **人工确认执行**（无启动自动迁移）；新二进制启动时检测两表缺列打 **warning 日志**提示管理员执行；同步前两表 INSERT 失败、落库暂停（转发不受影响，仅丢弃计数爬升），写入 §5 已知边界。
- **回退安全**：加列带 DEFAULT + INSERT 为显式列清单 ⇒ **旧二进制兼容新表**，可独立回滚二进制、无需回滚表。
- **老数据兼容**：新列 `DEFAULT ''`，UV 在 ua 为空时退化为 IP 口径（SQL 侧再以 COALESCE 兜底 NULL，防拼接少计）；geo 为空计「未知」（见 §3.4），前端不特判、文档标注。

### 3.3 GeoIP 模块（`internal/geoip`，新包）

- 配置项 `GEOIP_MMDB_DIR`（easyconf 注册，默认 `geoip`，中文 title/usage）。
- **查找链（逐文件解析，找到即止）**：`GeoLite2-City.mmdb` 与 `GeoLite2-Country.mmdb` **各自独立**按 `$GEOIP_MMDB_DIR` → 当前工作目录 → `$HOME/geoip` 定位，首个含该文件的目录胜出（Country 判国内外，City 取国内省市）；warning 精确列出缺失文件名。
- **加载与生效**：首次有 geo 请求时惰性定位加载（含"缺失"结论缓存）；文件补放后**重启生效**（首版不做热加载，宁简勿繁，文案写明）。
- **缺失降级（D17）**：惰性定位失败时打 warning 日志（缺哪个文件、去哪下载、放搜寻目录任一处、重启生效）；前端会话内首次进入依赖 geo 的页面弹一次统一警告 toast + 页内常驻警告引导卡（文案三要素）；geo 未加载时列写空串，不阻断主流程（基础 Proxy 永远是最后一道保障）。
- 解析：Country 库出国家码/名，City 库出国家/省/市；mmdb 本身为内存查询，首版不加 LRU（宁简勿繁）。
- 写时解析接入：obs OnDone、shield newEvent 各解析一次填列。

### 3.4 后端读侧（obs 挂件 + WAF 页改造）

**指标口径（闭合定义，验收"数字互洽"以此为准）**：

- `req_ok` = 放行请求总数（**含静态资源、含放行后 4xx/5xx 响应**）；**请求次数 = req_ok + block_total**；4xx/5xx 错误率分母 = 请求次数。
- `req_pv` = req_ok 去静态资源（后缀清单 `.js .css .map .ico .png .jpg .jpeg .gif .svg .webp .woff .woff2 .ttf .eot` **硬编码于 SQL 过滤条件**，注释说明清单内容）。
- `uv` = access 侧 `COUNT(DISTINCT client_ip, user_agent)`，三方言写法：sqlite `COUNT(DISTINCT client_ip||'|'||COALESCE(user_agent,''))`、MySQL `COUNT(DISTINCT client_ip, COALESCE(user_agent,''))`、PG `COUNT(DISTINCT ROW(client_ip, COALESCE(user_agent,'')))`。
- series `ok_count` 口径同 `req_ok`（含静态资源）；`blocked_count` = shield_event 计数。
- geo 分组：`country` 空串计「未知」、**参与排序**（与 §3.2 口径一致，不悄悄丢量）。

**新增 SQL 脚本**（三方言各 4 个，照 `stats_daily` 日期范式与 UTC 口径）：

- `traffic_summary.sql`：参数 from/to；标量子查询一次出 `req_ok, req_pv, uv, ip_all, block_total, attack_ips, err4xx, err5xx, block4xx`。
- `traffic_series_hour.sql` / `traffic_series_day.sql`：两表 UNION ALL（带来源标记）按桶 GROUP BY，条件聚合出 `bucket, ok_count, blocked_count`。
- `traffic_geo_top.sql`：参数 from/to/source（access|blocked）；按 country GROUP BY 计数倒序 Top N。

**时间边界与时区**：summary/series/geo 统一 `time >= from AND time <= to`（沿 `shield_event_count` 现状），**保证趋势各桶之和 = 总数可对账**；新统计区前端将本地时间换算为 UTC 传参（既有 logs 页时区口径沿袭现状，本批不动）；桶标签以 UTC 返回、前端原样展示并标注 UTC；首尾不完整桶计入。

**端点**（`plugins/obs/` 新增 `traffic.go`、`traffic_cache.go`，`admin.go` 注册）：

- `GET /admin/obs/traffic/summary?from=&to=` → 标量 + 率（Go 算，分母 0 防护）+ `{computed_at, cache_ttl}`。
- `GET /admin/obs/traffic/series?from=&to=&bucket=hour|day` → 桶宽缺省自适应（跨度 ≤48h 用 hour）。
- `GET /admin/obs/traffic/geo?from=&to=&source=` → Top 国家列表。
- 缓存：纯标准库 singleflight（mutex+map）+ 过期时刻表，key=端点+from+to+bucket/source；预设范围有限、自定义低频，条目量实际有界，首版不设上限与淘汰（边界在此标注）。实时 QPS 不参与缓存。
- 降级：obs 未启用 → 503（前端引导态，豁免 toast 红线②）；`SHIELD_EVENT_LOG_ENABLED=false` → 拦截侧字段 null，前端显示"—"。
- 新增配置项：`OBS_TRAFFIC_CACHE_TTL`（时长，默认 10m，0=禁用）。

**WAF 实时窗口改造**（`plugins/shield`）：

- 内存滑动窗口 1 分钟 → **1 小时 = 60 个 1 分钟桶**；`/admin/shield/metrics?window=1m|5m|15m|1h` 由整分钟桶聚合返回（零误差，缺省 1m 兼容现状）。
- WAF 页卡片：窗口瓦片可切桶宽；「累计落库」标签改「本次运行落库（重启清零）」；新增「落库总数」瓦片 = 查库 `shield_event_count.sql` 全范围（进页查一次，不跟随窗口刷新）；卡片副标注「内存窗口数据重启后从零重新累计」。

### 3.5 前端（首页概览扩展，遵守全局/局部解耦红线）

- `views/overview.js` 新增「流量统计」卡（页面局部区块，不动顶栏/菜单）：
  - 时间范围：预设 近 24 小时/今日/近 7 天/近 30 天 + 复用 `dateRange` 自定义；默认近 24 小时；传参按 §3.4 时区口径换算 UTC；
  - 指标卡 grid（请求/PV/UV/独立IP、拦截/攻击IP、4xx 数率/4xx 拦截数率、5xx 数率）；
  - 访问情况/拦截情况折线（`Rock.comp.chart`）+ 峰值（前端取序列 max）；
  - 地理位置卡：Top 国家列表 + 访问/仅拦截切换；geo 缺失时页内常驻警告引导卡（D17）；
  - 运行指标卡现状不动（D14：前端采样趋势保留）。
- `views/waf.js`：实时拦截卡按 §3.4 改造。
- `views/topIPs.js`：地区占位列接 geo（查询时解析），缺失时占位 + 页内引导（同 D17 形态）。
- geo 缺失 toast：会话内首次进入依赖 geo 的页面时由 geo 状态检测统一弹一次（sessionStorage 标记）。
- toast/silent/文案三要素遵守红线；`pageLoaders` 透传 opts。

### 3.6 文档同步清单（验收必查）

`docs/webui-api.md`（新端点 + window 参数）、`docs/webui.md`（概览页流量统计区/WAF 卡）、`docs/CONFIGURATION.md`（GEOIP_MMDB_DIR、OBS_TRAFFIC_CACHE_TTL）、`docs/COMPONENTS.md`/`README.md`（geo 能力与统计报表）、`docs/PROJECT_STRUCTURE.md`（新增 `internal/geoip` 包与调用链路）、`docs/DATA_DICT.md`（两表加列三同步）；三方言脚本同步 `bin/hotscripts/sql/`。

## 4. 实施组织（子 Agent 分工备注）

> 本节为**非规范性备注**：仅记录拆步思路与子 Agent 适用性供建纲参考；实施顺序、依赖执行以定稿后拆步建的总纲为准（宪法 §2.3，不在此双头维护）。

- **适合派子 Agent（相对独立、接口契约已在 §3 定义）**：三方言 SQL 脚本编写+sqlite 单测；`internal/geoip` 包；shield 窗口改造；DATA_DICT 与建表/INSERT 脚本三同步核对。
- **主会话亲自做**：装配接线（conf 注册、admin 注册、依赖注入、启动缺列检测）、WebUI 三个视图改动（必须实看渲染）、端到端验收统筹（终验 + 视觉验收关口）。
- 依赖脉络：数据层加列 → 写入链路补字段 → geo 写时解析生效 → 读侧聚合端点 → 前端区块；geoip 包与 SQL 脚本、WAF 窗口改造三者互不依赖，可并行派发。

## 5. 验收标准

1. `go test ./...`、`go vet ./...` 全绿；新增单测覆盖：traffic SQL 脚本（sqlite 内存库；UV 断言：同 IP 不同 UA 计 2、ua 空串退化计 1）、geoip 查找链逐文件解析与缺失降级、缓存命中/过期、WAF window 参数、启动缺列 warning 触发。**三方言脚本沿既有门控真库集成测试**跑通语法与聚合（构建标签 `-tags integration`，本机环境见下方「本机真库测试环境」）。
2. 浏览器实测（`-tags dev`）：首页流量统计区四预设切换出数且按 §3.4 口径互洽（请求次数=访问+拦截趋势各桶之和可对账）；同范围二次请求 `computed_at` 不变（缓存命中）、`OBS_TRAFFIC_CACHE_TTL=0` 时每次变化；WAF 桶宽切换与「落库总数」瓦片；topIPs 地区列；geo 缺失环境（无 mmdb）：warning 日志 + 会话内首次进入时弹一次 toast + 页内常驻引导卡，放置文件重启后恢复。
3. 老库升级路径（可判定步骤）：带旧两表的库启动新二进制 → 日志出现缺列 warning → `/admin/db/schema` 返回两表补列 diff → `/admin/db/exec` 人工确认执行生成 SQL → 列存在、新写入含 UA/geo、统计出数。
4. §3.6 文档清单逐项核对。
5. **已知边界**：升级后未人工执行表结构同步前，两表落库暂停（转发不受影响）；schema 为向后兼容加列，可独立回滚二进制；host 列（按域名筛选）不做；geo 精度取决于 mmdb 数据本身；统计范围受日志保留期限制；老数据无 UA/geo 时口径退化（已标注）。

### 5.1 本机真库测试环境（2026-09-09 实测可用）

| 库 | 地址 | 账号 / 口令 | 实测版本 |
|---|---|---|---|
| MySQL | `127.0.0.1:3306` | `root` / `root` | 8.0.28 |
| PostgreSQL | `127.0.0.1:5432` | `postgres` / `postgres` | 18.4 |

- 门控运行示例（凭据仅经环境变量注入）：
  - `MYSQL_TEST_DSN='root:root@tcp(127.0.0.1:3306)/rocksys_itest?parseTime=true' go test -tags integration ./internal/db/ -run 'TestMySQLDialect|TestSchemaSyncMySQL'`
  - `PG_TEST_DSN='postgresql://postgres:postgres@127.0.0.1:5432/rocksys_itest?sslmode=disable' go test -tags integration ./internal/db/ -run 'TestPostgresDialect|TestSchemaSyncPostgres'`
- 临时库 `rocksys_itest` 已建好（空库，仅供集成测试；缺失时 `CREATE DATABASE` 即可）。MySQL 方言测试只用 `_mysqltest` 后缀临时表并自清理；schema_sync 测试会在目标库 DROP/CREATE `ip_blacklist`——**勿把 DSN 指向业务库**。
- 已知缺陷（实施前置小修，仅测试基建、不涉产品代码）：`-tags integration` 套件当前编译失败——`schema_sync_acceptance_integration_test.go` 用 `syscall.Flock`（Unix 专属，Windows 无此 API）；`schema_sync_integration_test.go` 缺 `context` 导入（任何平台均编译不过，说明该标签自该文件加入后从未被编译过）。首步先修此两处再跑真库验收。

## 6. 变更记录

- 2026-09-09 终验通过并实施完成（STEP1–9）：全量 `go test ./...`/`go vet ./...` 全绿；MySQL 8.0.28 / PG 18.4 真库门控（`-tags integration`）全过；生产构建通过；浏览器实测截图核验（概览流量统计区真实数据出数且口径互洽、缓存命中/禁用语义、WAF 桶宽切换与落库总数、geo 缺失引导卡与会话内一次 toast）。实施期新增工作：① 修复 integration 测试基建三处存量缺陷（syscall.Flock Windows 编译、缺 context 导入、旧版 DDL 缺 updated_at 致零差异断言恒挂）；② 修复 PG 存量缺陷 `access_log_query.sql` 排序参数类型消解为 text（显式 CAST）；③ 真库冒烟暴露 traffic 测试夹具三处缺陷（extra/path 无默认值、PG geo 占位符复用、临时表清理时序）。实施期临时决策 D-T1~D-T3 见 TRAFFIC_ANALYSIS_DECISIONS.md（时区传参口径沿 logs 页现状、{table2} 占位符约定、老数据 UV 退化确认），待人类审查。同日人类审查：D-T1/D-T3 维持；D-T2 根因处置——取消 `SHIELD_EVENT_TABLE` 配置项（表名固定 `shield_event`，不开放配置），相关注册/校验/注入与文档口径全部移除改写。

- 2026-09-08 初稿。
- 2026-09-09 按用户意见重写（D10–D15：并入首页概览、UV=ip+UA、geo 本期实施、WAF 窗口桶宽化、实时趋势保留采样、依赖 v2）；同日独立审核修订（D16/D17：表结构同步表述与验收改写、三同步清单补全、指标口径闭合、geo 警告形态统一）。修订细节已并入正文与决策表，此处不再重复。
