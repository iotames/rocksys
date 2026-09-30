# 流量大小统计（TRAFFIC_BYTES）宏观设计

> 层级定位：本文是**宏观设计层（最权威最上层）**，以 OGSM 框架承载“为什么与做到什么程度”，指导同源实施文档 `TRAFFIC_BYTES_IMPL_PLAN.md`（实施指导层，下层，随本文定稿自主细化）。冲突时本文优先，操作细节以下层为准。
> 状态：**待人类确认（设计定稿关口，宪法 §2）**——开放项（字节口径 / 端点形态 / Top-N 是否排除静态资源 / 拦截禁用降级 / WebSocket 边界）已拍板并入本文；经人类一句“确认”即定稿，随后须另下开工令方可实施（宪法 §2.2 两段式：定稿 ≠ 开工令）。
> 分层边界：本文只定**为什么、做到什么程度、口径与接口契约**；**文件级改动清单、影响面分析、实施切片顺序、风险与回退**归下层 `TRAFFIC_BYTES_IMPL_PLAN.md`（随本文定稿自主细化，不在此堆砌）。
> 前置文档：流量统计总览（summary/series/geo）见归档 `docs/done/TRAFFIC_ANALYSIS_PLAN.md`；本文在其基础上补齐“**流量大小（字节）**”维度，不改其计数/耗时口径。

## 现状结论（带证据）

全项目共两处承载 HTTP 流量的字节数据，均为**明细表存字节、读侧零字节聚合**：

| 位置 | 表 | 请求字节 | 响应字节 | 证据 |
|---|---|---|---|---|
| 入网数据（obs，放行请求） | `access_log` | ✅ `req_bytes` | ✅ `resp_bytes` | `plugins/obs/dim.go:56`、`sql/sqlite/access_log_create_table.sql:18` |
| WAF 防护（shield，拦截请求） | `shield_event` | ✅ `req_bytes` | ❌ 无（拦截请求网关自造响应，无转发响应体，语义如此） | `sql/sqlite/shield_event_create_table.sql:26` |

**采集口径现状（关键，决定方案）**：

- 请求字节两处均取请求头 `Content-Length`（**仅请求体大小**，不含请求行/请求头）；请求头未携带该值时驱动程序给出 `-1`（`plugins/obs/obs.go:356`、`plugins/shield/event_recorder.go:182`）。
- 响应字节取 `len(ctx.RespBody)`（**仅响应体**，`plugins/obs/obs.go:357`），而响应体经 Adapter 的 **4MB 缓冲**（`internal/chain/adapter.go:17` `respBufferLimit = 4<<20`）：超过 4MB 时缓冲被清空、改为直写，`ctx.RespBody` 变 `nil` → **大响应（>4MB）的 `resp_bytes` 实际落 0**。
- WAF 拦截事件在转发链中断前就地记录（`plugins/shield/shield.go` 拦截点），请求体未被读取，故只能用请求头声明值。
- **现有“只算体”口径的硬伤**：GET/无请求体请求的入网流入恒为 0——十万个 GET 洪泛会显示入站流量 ≈ 0，严重失实（GET 的入站流量几乎全在请求行与请求头）。

**统计聚合现状（字节维度 = 0 支持）**：

- obs 的 `traffic_summary.sql` / `traffic_series_*.sql` / `traffic_geo_*.sql` 只做 `COUNT` / `COUNT DISTINCT`（计数、UV）与耗时分位，**无一处 `SUM(bytes)`**。
- shield 的 `shield_event_stats_top_ip.sql` / `shield_event_stats_daily.sql` 只 `COUNT(*)`，**无字节**。

**UI 现状**：

- 日志明细表有“请求流量/响应流量”列（`webui/assets/js/views/logs.js:233`，`fmtBytes`）。
- 概览“流量统计”区（`webui/assets/js/views/overview.js`）指标卡/趋势/地理**均无字节指标**；WAF“攻击源 IP Top N”（`webui/assets/js/views/topIPs.js`）只有命中**次数**。

**一句话结论**：字节原始数据已具备（明细表），但口径偏窄（只算体，GET 恒 0）、响应字节在大响应上失真；统计层与展示层**完全没有按字节的汇总与排行**——“TOP IP / URL PATH 流量大小”整条链路未建。

## O（Objective·目的）

在**不影响正常转发业务、数据流改动最小**的前提下，为 RockSys 补齐“HTTP 流量大小”的采集与统计能力：以**报文大小**为口径，回答**谁在占用带宽、哪些路径最耗流量**，并区分**拦截流量**与**入网流量（流入 request / 流出 response）**三段。

## G（Goals·关键结果）

- **G1 原始数据可信**：`access_log.req_bytes/resp_bytes` 与 `shield_event.req_bytes` 口径统一为**报文字节**，无脏值（不再出现 `-1`；大响应不再归零；GET/无体请求不再恒 0）。
- **G2 三段字节可汇总**：拦截流入请求字节、入网流入请求字节、入网流出响应字节，可分别按时间范围求和。
- **G3 维度排行（本期核心）**：`Top IP` 与 `Top URL PATH` 两维度，按流量大小倒序 Top-N，支持“入网/拦截”两侧口径切换。
- **G4 业务无感**：新增采集对转发热路径仅增加一次整数累加；**请求体读取路径零改动**（不包裹 body）；原有计数与耗时口径不变。
- **G5 架构简洁**：不新增表、不新增列、不新增索引、不新增配置项；复用既有 SQL 三方言范式、TTL 结果缓存、obs 端点归属。

## S（Strategies·策略）

- **S1 字节口径 = 报文字节**（含行、头、体）：由统一助手按字段长度求和（行/头）+ 实测或声明（体）计算，三处（入网请求/入网响应/拦截请求）共用同一口径，详见「统计口径与计算方式」。
- **S2 数据源分离（与既有口径一致）**：`source=access` → `access_log`（`req_bytes` + `resp_bytes`）；`source=blocked` → `shield_event`（仅 `req_bytes`，响应字节恒 0）。两侧互斥、不混算，延续 TRAFFIC_ANALYSIS 的 req_ok/block 分离原则。
- **S3 聚合与端点**：新增单一端点 `GET /admin/obs/traffic/top`，参数 `dim=ip|path`、`source=access|blocked`、`limit`（默认 10），与既有 `/traffic/geo` 的 `level` 参数同构，避免端点数量膨胀。SQL 用“两表 UNION ALL + source 参数互斥分支”范式（照 `traffic_geo_top.sql`），每维度一份脚本、三方言齐平。
- **S4 汇总标量**：在既有 `traffic_summary.sql` 增加三段字节合计（入网请求/入网响应/拦截请求），随 `/traffic/summary` 一并返回，作为“总量”视图；不改既有计数/耗时口径。
- **S5 缓存与热更**：复用 `OBS_TRAFFIC_CACHE_TTL` 结果缓存与 `POST /admin/obs/traffic/cache_clear`，key 纳入 `dim/source/limit`；**不新增配置项**。
- **S6 UI（页面局部，遵守全局/局部解耦红线）**：概览“流量统计”区新增独立“流量排行”卡（`dim` / `source` / Top-N 切换 + 表格，字节经 `fmtBytes` 展示）；提示统一走 `Rock.ui.toast`，失败弹 error toast，降级态走行内引导卡。本期不改 WAF 页 topIPs（避免范围膨胀）。

## M（Measures·度量）

| 度量项 | 口径 | 验证手段 |
|---|---|---|
| 请求字节非零保障 | 无请求体的请求（如 GET）入网流入 > 0（计入请求行/头） | 单测：构造 GET 断言 `access_log.req_bytes > 0` |
| 响应字节准确性 | >4MB 响应落库值 = 实际响应体字节数（非 0）+ 行/头 | 单测：构造 >4MB 上游响应，断言 `access_log.resp_bytes` |
| 请求字节无脏值 | 请求头未携带长度时体部分记 0（行/头仍计），无负数 | 单测：构造无 `Content-Length` 的请求断言落库值 |
| 排行正确性 | Top-N 按 `req_bytes+resp_bytes` 倒序；`source=blocked` 时 resp=0 | 小范围造数精确断言；大范围 Σtop ≤ 全量 SUM（对账） |
| 三方言齐平 | 新增脚本在 `sql/{sqlite,postgres,mysql}/` 文件集一致 | `internal/db` `TestScriptParity` |
| 业务无感 | 转发热路径仅增一次整数累加，请求体读取路径零改动 | `go test -race ./...` + 现有转发 benchmark 不劣化 |
| 文档同步 | 口径变更的全部承载处一致：`docs/api/obs.md`、`docs/DATA_DICT.md`、三方言建表脚本列注释（含 PG `COMMENT ON COLUMN`）、Go 侧字段/维度注释 | 仓库法数据字典红线逐项核对（见 §3.4 清单） |

## 决策表（现行有效结论，只承载现行项）

| # | 决策点 | 结论 | 理由 / 来源 |
|---|---|---|---|
| D1 | 字节口径 | **报文字节**（行 + 头 + 体），非“仅体” | 用户拍板；仅体口径使 GET/无体请求入站恒 0，不可接受 |
| D2 | 请求字节来源 | 请求行 + 请求头（字段长度求和）+ 请求体（`Content-Length` 声明值，未携带记 0） | 无感优先；不包裹 body 读实际值 |
| D3 | 响应字节来源 | 状态行 + 响应头（字段长度求和）+ 响应体（**实际写出字节数**） | 体实测兼容分块/gzip/超大响应；行/头求和补全口径 |
| D4 | 大响应归零修复 | 弃用 `len(ctx.RespBody)`（4MB 截断归零），改 Adapter 累计计数 | 现方案在 >4MB 时落 0，是数据质量硬伤 |
| D5 | WAF 响应字节 | **不新增** `resp_bytes`（拦截请求无转发响应体） | 语义如此，非缺陷 |
| D6 | 本期维度 | 先做 `Top IP`、`Top URL PATH` 两个维度 | 用户拍板（先 top_ip/top_path） |
| D7 | 端点形态 | 单一 `/admin/obs/traffic/top` + `dim`/`source`/`limit` | 用户拍板；与 geo 的 level 参数同构，避免端点膨胀 |
| D8 | 数据源分离 | access=入网（req+resp）；blocked=拦截（req only） | 延续 req_ok/block 分离原则 |
| D9 | 缓存 | 复用 `OBS_TRAFFIC_CACHE_TTL` 与 cache_clear | 已有能力，不重复建设 |
| D10 | schema 变更 | **无**：不新增表/列/索引，不新增配置项 | 既有列足够；宁简勿繁 |
| D11 | 历史数据 | 修复仅对新增行生效；历史行按旧“仅体”口径，为已知边界 | 不做回填（无历史原始大小可依） |
| D12 | 展示单位 | 存储字节，前端 `fmtBytes` | 与现有日志明细列一致 |
| D13 | Top-N 是否排除静态资源 | 默认**不排除**，保持全量 | 用户拍板（先看全量） |
| D14 | 请求体是否读实际值 | **否**：用 `Content-Length` 声明值，不包裹 body | 用户拍板（不追求极致精确，业务无感优先）；代价为分块传输的体记 0（行/头仍计，不显失实） |
| D15 | 聚合侧负值兜底 | `SUM` 对非正字节按 0 计（历史残留 `-1` 兜底） | 采集修复仅对新增行生效，历史需聚合层兜底 |
| D16 | 拦截禁用降级 | `SHIELD_EVENT_LOG_ENABLED=false`：summary 拦截字节字段输出 `null`；`/traffic/top?source=blocked` 输出 `rows:null` + `block_available:false`（不执行聚合查询、降级响应不入缓存） | 用户拍板（字段值 NULL）；与既有 `trafficBlockField` null 约定、geo `*_enabled` 降级范式同构；不入缓存防热开后 TTL 内返回陈旧 null |
| D17 | WebSocket 隧道 | 隧道流量**不统计**（直写路径不经响应缓冲，隧道行 `resp_bytes` 仅计状态行；升级后帧流量完全不计），列入已知边界 | 用户拍板（可以不统计）；WS 带宽非本期“HTTP 报文流量”口径 |

## 统计口径与计算方式

> 三段流量统一按 **HTTP 报文字节数**计（含行、头、体）。行/头按解析后的字段长度求和（近似），体按实测或声明取值。三处计算共用同一实现，保证口径一致。
> **聚合兜底**：任意范围聚合求和时，对 `req_bytes`/`resp_bytes` 的非正值（含历史残留的 -1）统一按 0 计，保证 `SUM` 不被负值污染。

**① 入网流入（access_log.req_bytes）= 请求行 + 请求头 + 请求体**

- 请求行 = `len(方法) + 1 + len(URL) + 1 + len(协议) + 2`（CRLF）
- 请求头 = `Σ_每条头(len(头名) + 2 + len(头值) + 2)`（含 `Host`），末尾 + `2`
- 请求体 = `max(Content-Length, 0)`（`Content-Length` 未携带时记 0；**不读 body 数实际值**）

**② 拦截流入（shield_event.req_bytes）= 请求行 + 请求头 + 请求体**

- 与 ① 同式；体取 `Content-Length` 声明值（拦截发生在读体之前，只能取声明值）

**③ 入网流出（access_log.resp_bytes）= 状态行 + 响应头 + 响应体**

- 状态行 = `len(协议) + 1 + len(状态码) + 2`（协议近似取 `HTTP/1.1`）
- 响应头 = `Σ_每条头(len(头名) + 2 + len(头值) + 2)`，末尾 + `2`
- 响应体 = **上游响应体实际字节数**（在网关缓冲写路径累计，非缓冲长度，故 >4MB 亦准；经 result/rewrite 改写场景反映改写前，见「已知边界」）

> 结果均为整数（字节）。三者之和即“网关口径的 HTTP 报文字节总量”；三段可分别按时间范围聚合、按维度（IP/PATH）排行。

## 设计方案

> 本节只定**接口契约与设计落点（模块级）**；文件级改动清单、影响面分析与实施顺序见下层 `TRAFFIC_BYTES_IMPL_PLAN.md`。

### 3.1 采集（设计落点）

- **口径计算助手**：请求/响应报文字节的计算收敛为共享纯函数（放 `internal/netutil`，与其他网络工具同处），供 obs 与 shield 共用，保证口径单源。
- **响应体实测**：在转发内核的响应写回路径累计“实际写出的字节数”，经链上下文透出给 obs（一次整数累加，零拷贝无锁）。
- **接入**：obs 落库时以助手计算 `req_bytes`/`resp_bytes`；shield 拦截落库时以助手计算 `req_bytes`。请求体读取路径不改动。

### 3.2 聚合与端点契约

**新增端点**（三方言 SQL 每维度一份，照 `traffic_geo_top.sql` 的“两表 UNION ALL + source 互斥分支”范式，参数同构：sqlite/mysql 7 参、PG 5 参）：

- `GET /admin/obs/traffic/top?from=&to=&dim=ip|path&source=access|blocked&limit=10`
  - `dim`（缺省 ip）、`source`（缺省 access）、`limit`（缺省 10，上限 100）；非法值 400
  - 响应：`{dim, source, limit, rows:[{key, req_bytes, resp_bytes, total_bytes, cnt}], cache_hit, cache_ttl_sec}`
  - `key` = `client_ip`（dim=ip）或 `path`（dim=path）；`source=blocked` 时 `resp_bytes` 恒 0
  - 排序：`total_bytes` 倒序取 Top-N；**不排除静态资源**（D13）
  - 复用 TTL 结果缓存（key 含 `dim/source/limit`）与 `POST /admin/obs/traffic/cache_clear`
  - **拦截禁用降级（D16）**：响应恒带 `block_available`（取 `SHIELD_EVENT_LOG_ENABLED` 实值）；`source=blocked` 且 `block_available=false` 时**不执行聚合查询**，`rows` 输出 `null`（非空数组），且**该降级响应不入缓存**（防止热开后 TTL 内仍返回陈旧 null）；前端据此渲染「拦截明细未开启」行内引导（红线②豁免②，不弹 error toast）

**汇总标量**：`/admin/obs/traffic/summary` 增 `req_bytes_ok`、`resp_bytes_ok`、`req_bytes_blocked`（`SHIELD_EVENT_LOG_ENABLED=false` 时 `req_bytes_blocked` 输出 `null`，沿用既有 `trafficBlockField` 范式）。

### 3.3 展示侧（WebUI）

- 概览页“流量统计”区新增**独立“流量排行”卡**（页面局部，不触碰全局区域）：`dim`（IP/路径）×`source`（入网/拦截）×Top-N 下拉；表格列 = 排名 / 维度值 / 请求字节 / 响应字节 / 合计 / 次数；空/降级走行内引导，失败弹统一 error toast。
- 概览“指标卡”增入网请求字节、入网响应字节、拦截请求字节三项（`fmtBytes`）。

### 3.4 文档同步（红线）

> 字节口径由“体字节”改为“报文字节”属**字段语义变更**（无字段增删），按数据字典红线，字段说明的**全部承载处**必须同步——列注释与 `DATA_DICT.md` 一一对应，遗漏即视为未完成。

- `docs/api/obs.md`：新增 `/traffic/top` 端点说明（含 `block_available` 降级契约）；`/traffic/summary` 响应新增字节字段。
- `docs/DATA_DICT.md`：更新 `access_log.req_bytes/resp_bytes`、`shield_event.req_bytes` 的**口径说明**（由“请求/响应体字节数”改为“报文字节数：行+头+体”）。
- **三方言建表脚本列注释（共 6 处）**：`sql/{sqlite,postgres,mysql}/access_log_create_table.sql` 的 `req_bytes`/`resp_bytes` 注释、`sql/{sqlite,postgres,mysql}/shield_event_create_table.sql` 的 `req_bytes` 注释；PG 侧另有 `COMMENT ON COLUMN` 语句需同步（sqlite 无独立注释语句）。
- **Go 侧字段注释**：`plugins/obs/dim.go` 维度标题（“请求流量（字节）”等）与结构体字段注释、`plugins/shield/event_recorder.go` `ShieldEvent.ReqBytes` 注释，同步为新口径表述。
- `plugins/obs/traffic.go` 文件头注释补 `top` 端点与字节口径。

## 数据结构（宪法 §7 强制章节）

**本批无 DDL 变更**：不新增表、不新增列、不新增索引，不新增配置项。字节数据复用既有列，仅修正其**采集口径（体 → 报文）**与**读侧聚合**。

| 表.列 | 语义（本批明确后） | 组成 | 三方言类型（现状） |
|---|---|---|---|
| `access_log.req_bytes` | 入网请求报文字节数 | 请求行 + 请求头 + 请求体（`Content-Length`，未携带记 0） | INTEGER / BIGINT / BIGINT |
| `access_log.resp_bytes` | 入网响应报文字节数 | 状态行 + 响应头 + 响应体（实测写出） | INTEGER / BIGINT / BIGINT |
| `shield_event.req_bytes` | 拦截请求报文字节数 | 请求行 + 请求头 + 请求体（`Content-Length`，未携带记 0） | INTEGER / BIGINT / BIGINT |

三段流量口径：**拦截流入** = `SUM(shield_event.req_bytes)`；**入网流入** = `SUM(access_log.req_bytes)`；**入网流出** = `SUM(access_log.resp_bytes)`。

## 已知边界

- 请求行/头、状态行/头按**解析后的字段长度求和**近似，与实际原始字节有小差（串接/空白/大小写/顺序、Go 写回时隐式补充的头）；近似按 **HTTP/1.x 报文口径**（协议固定视作 `HTTP/1.1`，本项目当前仅跑 HTTP/1.1，无 h2/HPACK 场景），对“流量大小”是充分的（数百字节级）。
- 请求体取 `Content-Length` 声明值：分块传输（未携带长度）时体部分记 0，但请求行/头仍计，故“入站流量不为 0”，仅体量偏小。
- 经响应改写挂件（result/rewrite）改写 body 时，`resp_bytes` 的体部分反映**改写前**的上游响应体大小——低频、可选挂件场景，本期接受。
- 历史存量行按旧“仅体”口径（且 >4MB 落 0），**不回填**（无历史原始大小可依）。
- **WebSocket 隧道流量不统计（D17）**：WS 走 Adapter 直写路径（`respBufferWriter` 不支持 Hijack，隧道必须直写底层连接），不经响应缓冲计数点——升级成功的隧道行 `resp_bytes` 仅计状态行，升级后的双向帧流量完全不计入三段字节。WS 带宽非本期“HTTP 报文流量”口径，本期接受。
- 排行聚合为“查询时扫描 + 结果缓存”，大时间范围首次查询为全范围扫描（与既有 summary/series 同代价，缓存兜底）。

## 验收标准

1. **采集合理**：无请求体的请求（如 GET）`access_log.req_bytes > 0`（请求行/头计入）；>4MB 响应 `access_log.resp_bytes` 落库 = 实际响应体字节数 + 行/头；请求头未携带长度时体部分记 0、无负数。（单测断言）
2. **排行正确**：`/admin/obs/traffic/top` 按 `total_bytes` 倒序返回 Top-N；`source=blocked` 时 `resp_bytes` 恒 0；小范围造数可精确断言，大范围 Σtop ≤ 全量 SUM。**降级契约（D16）**：`SHIELD_EVENT_LOG_ENABLED=false` 时 `source=blocked` 返回 `rows:null` + `block_available:false`，不执行聚合查询，且降级结果不入缓存（热开后立即可见真实数据）。
3. **汇总正确**：`/traffic/summary` 新增三段字节字段与明细 SUM 对账一致；**含历史 `-1` 行时字节和不受污染（非正值按 0 计）**；`SHIELD_EVENT_LOG_ENABLED=false` 时拦截字节字段输出 `null`。
4. **三方言齐平**：`go test ./internal/db` `TestScriptParity` 通过（新增两脚本三方言文件集一致）。
5. **业务无感**：`go test -race ./...` 通过；转发 benchmark 无劣化；请求体读取路径未改动。
6. **UI 可用**：浏览器实看“流量排行”卡渲染正确并截图留证；失败弹统一 error toast、降级走行内引导。
7. **文档同步**：按 §3.4 清单逐项核对——`docs/api/obs.md`、`docs/DATA_DICT.md`、三方言建表脚本列注释（含 PG `COMMENT ON COLUMN`）、Go 侧字段/维度注释均改为“报文字节”口径，与实现一致。

## 明确不做（本期范围边界）

- 不做 `resp_bytes` 历史回填；不包裹 request body 去数实际字节（分块传输的体精确化）。
- 不做 method / upstream / status / 地区 等更多维度的字节排行（架构已预留，后续按需扩）。
- 不新增独立“流量分析”页；不改 WAF 页 topIPs 卡片。
- 不新增配置项、不引入第三方依赖。