# EGRESS_TIMING_PLAN — 访问链路计时模型修正：补出网埋点 DoneAt、egress_ms 正式列、耗时语义修正

> 状态：**已定稿（2026-09-05 人类确认通过，宪法 §2 阶段 2 关口已过）**
> 进度承载：经人类确认本稿即**显式豁免**宪法 §1 的拆步建纲——四阶段规划即执行期唯一进度载体（理由见 §4）。
> 背景：用户审计访问链路计时点后发现三处语义/完整性问题（详见 §1），本方案给出修正设计。

## 1. 现状结论

### 1.1 计时点模型现状

DataFlow 目前埋 3 个计时点（`internal/dataflow/dataflow.go`）：

| # | 计时点 | 取点位置 | 语义 |
|---|---|---|---|
| ① | `BeginAt` | easyserver 进入（`df.BeginAt()` 委托 `inner.GetStartAt()`） | 请求到达网关 |
| ② | `BeginBizAt` | `internal/chain/adapter.go` 步骤 6（写一次） | 转发前一刻 = 前置链（Head→Middle 全部中间件）执行完 |
| ③ | `DoneBizAt` | `internal/engine/engine.go` forward 内部（成功/失败路径统一） | 转发完成（上游响应读毕） |

**缺 ④ 出网时刻（DoneAt）**：步骤 8 Tail 响应钩子（result 改写、obs 记录）＋ 步骤 9 缓冲响应写回客户端，这段没有任何计时点。obs 开启时必走缓冲路径（`HasResponseHook(Tail)` 为真），该段必然存在且无法度量。

### 1.2 落库口径与语义偏差（`plugins/obs/obs.go` OnResponse）

access_log 落库 `time` + 三个毫秒差：

| 列 | 现口径 | 问题 |
|---|---|---|
| `time` | `time.Now()`（OnResponse 执行时刻，在写回客户端**之前**） | DATA_DICT 标注「完成时刻」名不副实 |
| `shield_ms` = ②−① | **整个前置链耗时**（auth/dispatch/rewrite/trace/copy…全在内） | 名为「防护耗时」实为「入网耗时」，仅当中间链只挂 shield 时才成立 |
| `biz_ms` = ③−② | 转发耗时 | ✓ 真实 |
| `total_ms` = ③−① | 转发完成耗时 | 名为「总耗时」但不含出网段，非真·总耗时 |

### 1.3 可计算性

三段拆解中 ①②③ 可由 `time − total_ms` / `time − biz_ms` 间接反推，但 ③→④（出网段）无任何数据源，**不可计算**——这正是要补的。

## 2. 已确认决策表（用户对话拍板记录，只追加不覆盖）

| # | 决策点 | 结论 | 理由（用户原话摘要） |
|---|---|---|---|
| D1 | 是否补出网埋点 | **补 DoneAt**（写回客户端完成后取点） | 「少了 DoneAt 这个是出网关的那一刻……中间件处理的耗时完全无法计算」 |
| D2 | egress_ms 存储形态 | **正式列**（三方言 + 可索引 + 可排序），不走 extras 扩展维度 | 「用于排序你扩展维度就不方便了。耗时排序是现实运维经常用的排查场景」 |
| D3 | shield_ms 语义 | 展示层与文档改为**「入网耗时」**，存储列名 `shield_ms` 不动 | 「防护耗时只有在中间链仅仅只开防护组件的时候才成立」 |
| D4 | total_ms 语义 | 落库值改为 **④−①（真·总耗时）**，历史行不迁移、文档注一句旧口径 | 「总耗时更是不真实了，要把那三个耗时加起来才是对的」 |
| D5 | 三段模型 | 入网（①→②）＋ 业务（②→③）＋ 出网（③→④），总 = 三段之和 | 「实际能计算的是入网耗时，业务耗时，出网耗时。这几个才是真实的」 |
| D6 | biz_ms 展示名 | 改**「转发（业务）耗时」**（存储列名 `biz_ms` 不动）；说明点明「含网关↔上游网络往返，内网部署、网络稳定时约等于业务真实处理耗时」 | 「转发耗时就约等于业务服务处理数据的真实耗时，因网络原因不可控，这个约等于是相对准确的」 |
| D7 | time 与 DoneAt 关系 | **不新增 DoneAt 数据库列**；代码保留 DoneAt 语义埋点（写回客户端完成时刻），`time` 列复用其取值（单一数据源）。已按代码核实：迁移后 OnDone（步骤 9b）与 DoneAt 取点位置一致，满足统一取值前提 | 「如果是一致的，就必须统一取值。数据列不单独添加 DoneAt，但代码埋点要用 DoneAt 语义的埋点，time 转而复用它」 |

## 3. 设计方案（文件/字段级）

### 3.1 DataFlow 加第 4 埋点（`internal/dataflow/dataflow.go`）

- 新增 key `rocksys:done_at` → `SetDoneAt(t)`（写一次，重复调用忽略，与 SetBeginBizAt 同风格）/ `DoneAt()`（未记录返回零值）。
- `EgressMs()` 新方法：`DoneAt − DoneBizAt`（毫秒）；DoneAt 为零值时返回 0。
- `TotalMs()` 语义修正：DoneAt 非零 → `DoneAt − BeginAt`；零值（未取点，如单元测试构造的 DF）→ 回落现口径 `DoneBizAt − BeginAt`，保证既有测试与未埋点路径行为不变。

### 3.2 chain 取点与写回后回调（`internal/chain/`）

- `adapter.go` `Handle`：
  - 链中断路径（步骤 4 `!shouldForward` 返回前）与正常路径末端（步骤 9 写回完成后）统一 `df.SetDoneAt(time.Now())`——普通/WebSocket 7a/缓冲 7b/流式 7c/被拦截路径均有 DoneAt；**豁免路径**：步骤 5「未命中路由且无默认上游 → return true 交 easyserver」不走 Tail 钩子，DoneAt 与 access_log 同缺，语义自洽。**取点语义注记**：7a/7c 路径写回可横跨长时段（流式转发 DoneAt＝整条流写毕时刻），egress 语义即"响应写回客户端完成的时刻差"；obs 开启时必走缓冲路径（7b）不受影响，被拦截路径不产生 access_log，故该注记仅影响语义解读、不影响落库。
  - 新增步骤 9b：遍历 Tail 响应钩子，实现了新可选接口 `DoneHook`（`interface{ OnDone(ctx *Context) }`，定义在 `internal/chain/interface.go`）的，调用 `OnDone`。此时 DoneAt 已取点。
- **接口注释写明契约**：OnDone 在响应写回客户端之后、仅调用一次；panic recover 语义与步骤 8 相同。

### 3.3 obs 记录迁移（`plugins/obs/`）

- `obs.go`：
  - `OnResponse` 改为 no-op（保留方法以维持 ResponseHook 注册——obs 在 Tail 槽位是缓冲路径的前提，`RespBody`/`RespBytes` 依赖缓冲）。
  - 新增 `OnDone(ctx)`：实现 `chain.DoneHook`；把现 OnResponse 的记录构造 + 异步落盘 + 指标聚合整体迁入。取值变化：
    - `Time: ctx.DF.DoneAt()`（零值兜底 `time.Now()`，对齐「完成时刻」= 出网时刻语义；D7：与 DoneAt 同点统一取值，单一数据源，不新增 DoneAt 数据库列）；
    - `EgressMs: ctx.DF.EgressMs()`（新增字段；**语义含客户端网络传输时间**——慢客户端会把 egress 撑大，属"出网"的正确含义，DATA_DICT 说明须点明）；
    - `TotalMs: ctx.DF.TotalMs()`（新口径 ④−①，随 3.1 自动生效）；
    - `ShieldMs/BizMs` 不变（shield_ms 语义改为「入网耗时」仅展示层；biz_ms 展示名改「转发（业务）耗时」仅展示层，见 D6）；
    - `metrics.Add` 的时间取点由 `time.Now()` 对齐为 `ctx.DF.DoneAt()`（零值兜底 `time.Now()`），与记录 `Time` 同源，避免指标与落库两处时间源漂移。
    - **指标口径变化注记**：迁移后 `metrics.Add` 的 TotalMs 参数随 3.1 自动变为新口径 ④−①，即网关延迟指标从此**含客户端写回时间**——慢客户端会直接撑高延迟监控曲线/分位数。此为对告警基线有实际影响的行为变化，随本期一并生效并写入变更记录；若后续监控发现客户端网速污染明显，再评估指标侧回落 `DoneBizAt − BeginAt` 口径（本期不做）。
- `dim.go`：注册 `DimEgressMs = "egress_ms"`（DimIndexed 索引维度，**注册顺序插在 DimTotalMs 之后**，标题「出网耗时（毫秒）」）；`DimShieldMs` 标题「防护耗时（毫秒）」→「入网耗时（毫秒）」；`DimBizMs` 说明「业务/转发耗时（毫秒）」→「转发（业务）耗时（毫秒）」（D6）；`AccessRecord` 加 `EgressMs int64`（DimTotalMs 之后）并入 `ToFlatMap`。注：`db_store.go` 的 `normalizeRowTypes` 由 dimIndex 注册表驱动，注册后类型归一自动生效，查询后处理零改动。`obs.go` 编译期断言加 `_ chain.DoneHook`。
- `db_store.go` `Write`：insert 参数序加 `r.EgressMs`（total_ms 之后）。

### 3.4 数据层脚本（`sql/{sqlite,postgres,mysql}/`，红线三处同步之一）

- `access_log_create_table.sql`：`total_ms` 后新增列 `egress_ms`（注释「出网耗时（ms）＝响应写回客户端完成 − 转发完成；历史行为 0」）；postgres 补 `COMMENT ON COLUMN`；列数 15→16。**既有注释随 D3/D4/D6 同步改**（脚本注释与 DATA_DICT 一一对应红线）：`shield_ms`「L1 防护（shield）环节耗时」→ 入网耗时口径、`biz_ms`「业务（上游处理）耗时」→「转发（业务）耗时（含网关↔上游网络往返）」、`total_ms`「请求总耗时」→「到达→出网总耗时（历史行为旧口径：到转发完成）」。
- `access_log_insert.sql`：列清单与占位符加 `egress_ms`（注释「14 个索引列」→「15 个索引列」）。
- `access_log_query.sql`：SELECT 加 `egress_ms`；`ORDER BY` 改写为 **simple CASE**（占位符确为 2 个不变；searched CASE `WHEN ? = 3` 会新增占位符，禁用）：
  `CASE ? WHEN 1 THEN total_ms WHEN 3 THEN egress_ms ELSE -1 END DESC, CASE ? WHEN 2 THEN total_ms WHEN 4 THEN egress_ms ELSE -1 END ASC, id DESC`（PG 对应 `$12`/`$13` 不变）；头部注释 sort_code 说明扩为 `0=时间倒序 1=总耗时降序 2=总耗时升序 3=出网耗时降序 4=出网耗时升序`。
- **不给 egress_ms 新增索引**：现有三个 ms 列均无索引（`access_log_create_index.sql` 仅 time/path/status），ORDER BY 内存排序，保持一致；D2「可索引」指列形态。
- 同步 `bin/hotscripts/sql/`（外挂优先红线）。

### 3.5 后端排序映射（`plugins/obs/store.go`）

- `sortCode()` 增加 `"egress_desc" → 3`、`"egress_asc" → 4`。

### 3.6 前端（`webui/assets/js/views/logs.js`）

- `SORT_OPTIONS` 增加「出网耗时 降/升」两项。
- `normalizeLogRow` 加 `egress_ms`；**`KNOWN` 集合（logs.js）必须加 `'egress_ms'`**——否则详情弹层在核心字段区之外、末尾 extras 遍历会重复裸显一次 `egress_ms`（用户可见缺陷）。
- `logDetailFields`：「防护耗时」→「入网耗时」；「业务耗时」→「转发（业务）耗时」；新增「出网耗时：X ms」；展示顺序 入网/转发（业务）/出网/总。
- 「耗时排序是现实运维场景」→ 列表页是否加出网列：**不加**（列表保持现有 8 列密度，详情弹层 + 排序已满足排查；如需可后续迭代）。

### 3.7 文档同步（红线清单）

- `docs/DATA_DICT.md`：access_log 节列数 15→16；`shield_ms` 标题「防护耗时」→「入网耗时」、说明改「请求到达→转发前（全部前置中间件）耗时；仅中间链只挂 shield 时等价防护耗时」；`biz_ms` 标题改「转发（业务）耗时」、说明补「含网关↔上游网络往返，内网部署、网络稳定时约等于业务真实处理耗时（D6）」；新增 `egress_ms` 行（说明点明**含客户端网络传输时间**：＝响应写回客户端完成−转发完成，慢客户端会撑大该值）；`total_ms` 说明改「到达→出网（含出网段）」；`time` 说明确认「完成时刻 = DoneAt 出网时刻（D7）」；历史行差异注记。
- `docs/HTTP_DATAFLOW.md`：计时点模型 ①→④ 图示与各段语义。
- `docs/webui.md`：入网数据详情弹层字段描述、排序选项。
- 代码注释：`dataflow.go`/`adapter.go`/`obs.go` 相关注释随改随新。

### 3.8 不做的事（边界）

- 不迁移历史数据（旧行 `egress_ms=0`、`total_ms` 为旧口径，查询时按 `id`/`time` 甄别即可）。**注意排序表现**：历史行 egress_ms=0 在降序（sort_code 3）时沉底、在**升序（sort_code 4）时置顶**（0 是最小值），均属预期，验收与运维解读按此口径。
- 不改 `shield_event`（被拦请求不产生 access_log，`time`=拦截时刻语义无缺）。
- 不改列名 `shield_ms`（数据层列名稳定红线，D3 只改展示与文档）。
- **biz_ms 内部不拆分**（上游建连 / TTFB / 响应传输）：本期三段模型内不算缺口；未来若需排查「转发慢是建连慢还是上游处理慢」，需在 engine forward 内再加埋点，留作演进方向。
- **列表页不加出网列但开放出网排序**（§3.6 既定决策）：排序项命名保留「出网耗时 降/升」，接受「按不可见列排序」的轻量折中；若上线后用户反馈困惑，后续迭代可让列表「耗时」列 hover 展示三段拆解（本期不做）。

## 4. 整体规划（四阶段，按依赖排序；不拆步骤文档，进度记录于本文档变更记录）

> 依宪法 §1：本规划即执行期的唯一进度载体——每阶段完成后在 §5 变更记录追加一行（做了什么/验证结果），中断续传时以「变更记录 + 改到一半的代码」为现场依据。

### 阶段一：DataFlow 第 4 埋点（纯内部，无依赖）

- `dataflow.go`：`rocksys:done_at` key、`SetDoneAt`（写一次）/`DoneAt()`、`EgressMs()`、`TotalMs()` 口径修正（DoneAt 零值回落现口径）。
- 单测：DoneAt 写一次语义、EgressMs/TotalMs 新旧口径（零值回落）。
- 验证：`go test ./internal/dataflow/` + `go vet ./...`。

### 阶段二：chain 取点与写回后回调

- `interface.go`：`DoneHook` 可选接口（`OnDone(ctx)`，契约：写回客户端完成后、仅一次、panic recover 同步骤 8）。
- `adapter.go`：链中断路径与正常路径末端统一 `SetDoneAt`；步骤 9b 调用实现了 DoneHook 的 Tail 钩子。
- 验证：`go test ./internal/chain/ ./internal/engine/`（既有回归不破）。

### 阶段三：obs 记录迁移 + 数据层 egress_ms 列（依赖阶段一、二）

- `obs.go`：OnResponse → no-op；新增 OnDone 实现并整体迁入记录构造/落盘/指标聚合（Time=DoneAt、EgressMs、TotalMs 新口径）。
- `dim.go`：注册 DimEgressMs（索引维度）＋ AccessRecord.EgressMs ＋ ToFlatMap；DimShieldMs 标题改「入网耗时」。
- `db_store.go`：Write 参数序加 EgressMs。
- 三方言 SQL：create_table 加列（15→16）、insert 加列、query SELECT/ORDER BY 加 egress_ms 与 sort_code 3/4；`store.go` sortCode 映射 egress_desc/asc。
- 同步 `bin/hotscripts/sql/`。
- 验证：`go test ./plugins/obs/ ./plugins/shield/`（真库集成测试按环境变量门控跑 access_log 新列读写与排序）。

### 阶段四：前端展示 + 文档红线收口（依赖阶段三）

- `logs.js`：SORT_OPTIONS 加出网耗时降/升；normalizeLogRow 加 egress_ms；logDetailFields 改「入网耗时」「转发（业务）耗时」+ 加「出网耗时」，展示序 入网/转发（业务）/出网/总。
- 文档：DATA_DICT（15→16 列、shield_ms 标题改入网耗时、total_ms 说明、历史行注记）、HTTP_DATAFLOW（①→④ 模型）、webui.md（详情字段/排序项）。
- 验证：dev 实机——放行请求详情弹层四段拆解（总=入网+业务+出网±1ms）、出网耗时排序生效；`node --check` 相关 JS；文档与实现字段一致性核对。

### 终验（宪法 §5）

- `go test ./...` + `go vet ./...` + 生产构建；前端实看渲染；DATA_DICT 与三方言字段数核对；已知边界（历史行 egress_ms=0、total_ms 旧口径）写入变更记录。

## 5. 验收标准

1. **单元/回归**：`go test ./...` 全过（含新增：DataFlow DoneAt 写一次与 EgressMs/TotalMs 口径的单测；obs 记录迁移后既有 obs 测试适配通过）。
2. **真库集成**（环境变量门控）：sqlite 真库跑 `access_log` insert/query 含 `egress_ms` 与新 sort_code 3/4 的查询排序。
3. **静态检查**：`go vet ./...` 通过；生产构建通过。
4. **实机功能验收**：dev 服务造一条放行请求 → 日志详情弹层展示 入网/业务/出网/总 四段，且 出网 ≥ 0、总 = 入网+业务+出网（±1ms 取整误差）；按出网耗时降序排序生效。
5. **文档核对**：DATA_DICT（16 列）与三方言脚本字段数一致；HTTP_DATAFLOW/webui.md 与实现一致。
6. **已知边界**：升级后首条新数据之前的历史行 egress_ms=0、total_ms 旧口径——降序排序时沉底、升序排序时置顶，均属预期。

## 变更记录

- 2026-08-31：定稿前打磨——① §3.2 补 7a/7c 路径 DoneAt 取点语义注记（流式＝整条流写毕时刻，落库不受影响）；② §3.3 `EgressMs` 语义点明含客户端网络传输时间，`metrics.Add` 取点对齐 DoneAt 与落库同源；③ §3.7 DATA_DICT 的 egress_ms 说明同步点明客户端传输时间；④ 头部显式声明经确认豁免拆步建纲（§4 单文档承载进度的宪法依据）。
- 2026-09-05：用户确认两项——D6：biz_ms 展示名「转发（业务）耗时」（含上游网络往返、内网部署时约等于业务真实处理耗时的说明入 DATA_DICT）；D7：time 列复用 DoneAt 取值、不新增 DoneAt 数据库列（经代码核实 OnDone 与 DoneAt 取点位置一致，满足统一前提）。§3.6/§3.7 措辞同步。
- 2026-09-05：实施前复审修订五处——① §3.4 query.sql 排序改写为 simple CASE（原 searched CASE 写法与「参数个数不变」矛盾）；② §3.4 补三方言 create_table 既有注释随 D3/D4/D6 同步（脚本注释↔DATA_DICT 一一对应红线）；③ §3.3 dim.go 补 DimBizMs 说明改「转发（业务）耗时」与 DimEgressMs 注册位置（DimTotalMs 之后），确认 normalizeRowTypes 零改动、obs 加 DoneHook 编译断言；④ §3.2 补步骤 5 直通路径 DoneAt 豁免注记；⑤ §3.6 补前端 KNOWN 集合加 egress_ms（漏加则详情弹层重复裸显）。另审一项不采纳：不给 egress_ms 建索引已写入 §3.4 边界（现有 ms 列均无索引，保持一致）。
- 2026-09-05：**设计定稿（人类确认通过，宪法 §2 阶段 2 关口过）**。定稿前终审修订三处——① §3.3 补指标口径变化注记（metrics TotalMs 迁移后含客户端写回时间，影响告警基线，本期生效、回落方案留待后续评估）；② §3.8/§5 修正历史行排序表述（egress_ms=0 升序置顶、降序沉底，均属预期）；③ §3.8 补两条边界（biz_ms 不拆建连/TTFB 留作演进；列表无出网列但开放排序的折中注记）。同关口 git 破例授权未授予，提交照常等用户确认。
- 2026-09-05：**终验通过，四阶段全部完成**。实施要点与发现：① 实机验证发现方案遗漏一处——`plugins/obs/admin.go` 的 /admin/logs sort 参数白名单未列入 egress_desc/asc（请求 400 拒绝），已补齐（含注释与报错文案）；② obs 既有测试适配为 OnDone 调用（newCtx 补 DoneAt，出网段固定 10ms），新增 TestQuerySortByEgress 真库（sqlite）覆盖 sort_code 3/4；③ 终验实机：dev 服务 + 6MB 大响应造流量，详情弹层四段拆解（0+64+135=199 ✓）、出网降序生效且与总耗时序不同（证明按 egress 排序）；`go test ./...`/`go vet`/生产构建全过；④ 已知边界确认：本地快响应 egress_ms 可为 0（<1ms 取整）；>4MB 响应截断直写时 resp_bytes=0 系既有缓冲截断行为（respBufferWriter.Body 截断后返回空），非本期引入，不做处理。
