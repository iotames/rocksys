# 路由分发宏观设计（ROUTE_DISPATCH DESIGN PLAN）

> 层级定位：本文是**宏观设计层（最权威最上层）**，以 OGSM 框架承载"为什么与做到什么程度"，指导同源实施文档 `ROUTE_DISPATCH_IMPL_PLAN.md`（实施指导层，下层）。冲突时本文优先；操作细节以下层为准。
> 状态：**草案 v2（待设计定稿关口）**——按宪法 §2.2，本文未经人类明确表态"确认/通过"前，不建 STEP、不动代码。

## 现状结论（带证据）

- **装配**：`plugins/dispatch` 为转发链 L2 中间件（`cmd/rocksys/main.go:282`），`DISPATCH_ENABLED` 开关控制（默认 false，`dispatch.go:82`）。
- **匹配维度单一**：仅 URI 路径，不感知 Host。规则格式 `DISPATCH_RULES = <Prefix>=<spec>`（`dispatch.go:75`），pattern 支持前缀/`:param`/`*`/根兜底；**未配置（空串）时为空路由表，全部走默认 upstream**（`dispatch.go:172-176`）。
- **匹配语义**：内部 Radix Tree（`router.go`），最长前缀优先，无显式优先级；命中写 `DataFlow.Target`，未命中回退 Adapter 默认 upstream（`internal/chain/adapter.go:76-78`）。
- **节点组能力已备且与匹配正交**：多节点平滑加权轮询/一致性哈希 + 主动健康检查 + 高优/备份标记（`dispatch.go:209-312` spec 解析成熟、测试覆盖）；`Select` 选节点逻辑与上游健康判定可整体复用。
- **热更骨架已备**：`RouteTable` 不可变快照经 `atomic.Value` 原子替换，`Start(nil)` 重建、`Stop()` 停旧表探活（`dispatch.go:95-113`）。
- **数据层默认可用**：`DB_DRIVER` 默认 `sqlite`（`main.go:300`），本地文件库零外部依赖；SQL 脚本按 `<表>_<动作>.sql` 三方言组织（`sql/<dbtype>/`），表结构差异检查/执行闭环已有产品化能力（adminapi `dbschema.go` + WebUI 数据库页）。
- **DB CRUD 管理全链路有成熟先例**：ip_blacklist（软删/恢复/批量导入/行详情可编辑弹层，`plugins/shield/admin.go` + WebUI 黑白名单页），端点经 `adminapi.RegisterPlugin(path, handler)` 注入（同 path 注册 GET+POST，插件自行校验方法）。
- **WebUI 规范体系完备**：数据列表公共组件 `filterBar`/`dataTable`/`detailModal`（pages.md §4.7）、toast 唯一提示组件与降级引导态豁免（§4.10）、开关设置原则"数据类资产有数据即生效、不设开关"（§4.14）、表单控件已统一、枚举字典可经接口下发（配置项类型接口下发先例）。

## O（Objective·目的）

将 `plugins/dispatch` 的路由规则从"单行字符串 DSL（DISPATCH_RULES）"升级为**结构化规则列表（数据库承载、WebUI 管理）**：每条规则是显式字段的结构体，**序号即优先级、命中即停**的单一匹配模型，域名精确匹配作默认兜底。不引入 NGINX/Envoy 式隐式优先级与层级模型，保持极简底座定位。

## G（Goals·关键结果）

- **G1 结构化规则模型**：规则为显式字段结构体——类型（枚举）、标题、值、序号、标签 + 上游节点组、启用状态、备注；抛弃字符串 DSL 的解析歧义。
- **G2 序号命中即停**：全部规则（含域名规则）同列表，按序号 1–999 升序逐条尝试，命中即确定转发目标、不再读取后续；无任何跨规则的隐式优先级。
- **G3 域名精确兜底**：域名规则仅支持精确匹配（Host 剥端口 + 小写归一后比对），**默认序号 999**——路径规则全未命中后按域名兜底，域名也未命中则走默认 upstream（现状语义）。用户可改小序号让域名规则提前参与裁决（模型单一自洽，无硬编码层级）。
- **G4 DB 承载 + WebUI 管理**：规则表进统一数据访问层，WebUI 完成增删改查、启停、标签筛选、命中测试（输入 Host+Path 预演匹配结果）、从旧配置一键导入；保存即热更生效（快照原子替换，请求路径零锁）。
- **G5 旧 DSL 降级兼容**：DB 未配置/不可用时回退现有 `DISPATCH_RULES` DSL（现状语义不变）；现有 dispatch 测试回归零改动全绿。

## S（Strategies·策略）

- **S1 扁平有序匹配引擎**：`RouteTable` 快照 = 按 `(order_no, id)` 稳定升序排列的规则数组（仅含启用且未软删行）。匹配流程单一、零特判：
  1. `host = 规范化(R.Host)`（剥端口——含 IPv6 `[::1]:80` 方括号形态、转小写；空 Host 保持空）；
  2. 依序逐条尝试，按规则类型解释值：路径前缀=段对齐前缀匹配（沿用现状补 `/` 归一语义，`/api` 匹配 `/api/x` 不匹配 `/apix`）、路径精确=全等、路径模式=段匹配（复用现有 `:param`/`*` 语法与捕获逻辑）、域名=非空 Host 全等；
  3. 命中 → `Select` 选节点 → 写 `Target`，终止；全未命中 → 不写 Target，Adapter 默认 upstream。
  Radix Tree 废弃（其"最长前缀优先"隐式语义与序号模型冲突）；性能靠线性扫描在百级规则下的微秒量级 + benchmark 留证，不达标才考虑加载期分桶（不预设优化）。
- **S2 规则字段（Go 权威定义）**：`Rule{ID, Order(1–999), Type(枚举), Title, Value, Tags, Upstream, Enabled, Remark}` + 运行态（轮询状态）。类型枚举 `rule_type`：**1 路径前缀 / 2 路径精确 / 3 路径模式 / 4 域名**（Go 侧权威定义 + DATA_DICT 枚举映射，三处同步）。加载期校验：值与类型匹配（前缀值不得含 `:`/`*`、模式值段语法合法、域名值合法且小写无端口）、序号范围合法；同类型同值重复与同序号碰撞告警但不禁（前者先到先得、后者按 id 稳定序），WebUI 保存时前端提示。
- **S3 上游节点组结构化承载**：节点组/负载均衡/健康检查不再拼 DSL 字符串，以 JSON 列存储（结构见数据结构章节），Go 侧 `json.Unmarshal` 直接构建；现有 `parseRule/parseNodes/parseHealthCheck` 保留两途——旧 DSL 降级路径 + WebUI「从配置导入」的解析源。多节点能力（权重/高优备份/rr/chash/探活）语义不变，`Select` 与健康检查生命周期零改动。
- **S4 规则源降级链（单一事实源原则）**：DB 数据层可用 → `dispatch_rule` 表为**唯一**规则源（表空 = 无规则 = 全走默认 upstream，不回看旧 DSL，避免双源合成歧义）；DB 未配置/降级 → 回退 `DISPATCH_RULES` 旧 DSL（现状语义）。`DISPATCH_ENABLED`（默认 false）仍是总开关，false 时中间件不挂载、一切不生效。规则源当前形态经组件状态与规则页头部透出。
- **S5 保存即热更**：WebUI 增/改/删/恢复/导入/启停成功 → adminapi 调 `Dispatch.Rebuild()`（拉库 → 构建 → 校验 → 原子替换快照 → 新旧健康检查启停，复用现有 Start 生命周期）；构建失败保留旧快照并报错回前端（表单前后端双重校验）。多实例与他进程改库场景提供「重载」按钮手动触发，不做自动轮询（宁简勿繁，写入已知边界）。
- **S6 WebUI 页面「路由分发」**：侧边栏独立顶级项（性质同「脚本」页——内容管理页，组件状态/配置仍在 dispatch 组件详情页，组件页放「管理路由规则 →」链接，先例：script 组件去脚本页）。页面 = 状态区 + 筛选表格 + 表单弹层 + 命中测试器（详见 WebUI 设计章节）。

## M（Measures·度量）

| 度量项 | 口径 | 验证手段 |
|---|---|---|
| 匹配语义正确性 | 序号命中即停/四类类型语义/域名兜底/空 Host/IPv6/大小写/尾斜杠归一 | 表驱动单测（`dispatch_test.go` 扩展 + 新 `match` 用例矩阵） |
| 向后兼容 | 旧 DSL 降级路径与现有测试不改一行全绿 | 既有 dispatch 测试回归 + 降级路径集成用例 |
| 热更并发安全 | 保存重建期间在途请求不受影响、探活 goroutine 无泄漏 | 既有热更测试 + `go test -race` |
| 匹配性能 | ≥100 规则下单次匹配不劣于现状量级 | `go test -bench` 结果留档 |
| CRUD 全链路 | 增删改查/启停/导入/恢复/命中测试端到端可用 | adminapi 测试 + 浏览器实看截图留证 |
| 文档同步 | DATA_DICT/README/docs/配置项 title·usage 与实现一致 | 仓库法文档同步红线逐项核对 |

## 数据结构（宪法 §7 强制章节）

**表 `dispatch_rule`**（三方言建表脚本 `sql/{sqlite,postgres,mysql}/dispatch_rule_*.sql`，注释与 DATA_DICT 一一对应）：

| 字段 | 标题 | 说明 | 三方言类型 |
|---|---|---|---|
| `id` | 主键 | 自增主键 | INTEGER PK AUTOINCREMENT / BIGSERIAL / BIGINT AUTO_INCREMENT PK |
| `order_no` | 序号 | 匹配顺序，1–999 升序、命中即停；域名规则默认 999（兜底） | INTEGER NOT NULL |
| `rule_type` | 规则类型 | 枚举：1 路径前缀 / 2 路径精确 / 3 路径模式（`:param`/`*`）/ 4 域名（仅精确） | INTEGER NOT NULL |
| `title` | 规则标题 | 人类可读名称，空允许 | TEXT / TEXT / VARCHAR(255) |
| `rule_value` | 规则值 | 按 rule_type 解释：路径（`/` 开头）或精确域名（小写、无端口、无通配） | TEXT NOT NULL |
| `tags` | 标签 | 逗号分隔小写标签，供人类筛选 | TEXT NOT NULL DEFAULT '' |
| `upstream` | 上游节点组 | JSON（结构见下），节点/权重/高优备份/算法/健康检查 | TEXT / JSONB / JSON |
| `enabled` | 启用 | 1 启用 / 0 停用；停用行不参与匹配，保留配置 | INTEGER NOT NULL DEFAULT 1 |
| `remark` | 备注 | 人类备注 | TEXT NOT NULL DEFAULT '' |
| `created_at`/`updated_at`/`deleted_at` | 公共字段 | UTC，`deleted_at` 非 NULL 软删不参与匹配（与 ip_blacklist 惯例一致） | DATETIME / TIMESTAMPTZ / DATETIME(3) |

索引：`(enabled, deleted_at, order_no)` 列表查询索引。SQL 文件组：`create_table` / `create_index` / `query_list`（分页+筛选）/ `insert_returning_id` / `update` / `soft_delete` / `restore` / `query_active`（快照构建拉启用行）。

**`upstream` JSON 结构（Go 侧权威定义 + json tag）**：

```json
{"nodes":[{"url":"http://o1:9001","weight":1,"priority":0}],
 "algo":"roundrobin","chash_key":"$remote_addr",
 "health_check":{"interval":"10s","timeout":"2s","path":"/healthz"}}
```

- `nodes[].weight` 正整数默认 1；`nodes[].priority` 0=高优/1=备份（**节点级**高优备份，与规则级 `order_no` 是两个概念，文档与表单文案必须消歧）；`algo`：roundrobin（默认）/chash；`health_check` 缺省 = 不探活全节点视为健康；时长为 `time.ParseDuration` 字符串。
- **数据关系**：单表自包含，无外键（节点组内聚于规则的 JSON 列，不另建节点表——宁简勿繁）。
- **数据流转**：WebUI 表单 → adminapi（结构化 JSON 校验落库）→ `Rebuild` 拉库构建快照 → 请求路径只读快照；读写两端同构（JSON ↔ Go 结构体），旧 DSL 仅在降级与导入时解析。

## WebUI 设计（路由分发页）

**页面骨架**（`#/dispatch`，侧边栏顶级项，位于「入网数据」与「组件」之间）：

```
┌─ 状态区：[规则源: 数据库●/配置项(降级)] [当前规则 N 条] [⟳ 重载] [从配置导入] ┐
│  规则按序号升序逐条匹配，命中即停；全未命中走域名兜底，仍未命中走默认后端。
│  （DISPATCH_ENABLED 未开启时整页显示引导卡：开关位置 + 开启路径，豁免 toast）
├─ 筛选栏（filterBar）：类型▾ 标签▾ 状态▾（启用/停用/已删除） 关键词  ──────┤
├─ 规则表格（dataTable，服务端分页）                                     │
│ 序号│类型│标题│规则值│标签│上游摘要│[启用开关]│操作(编辑/恢复·删除)        │
├─ 命中测试器（右侧常驻卡）：Host [___] Path [___] [测试]                 │
│ → 命中：序号 #12 路径前缀 /api/ → o1:9001,o2:9001                      │
│ → 未命中：域名兜底 a.com → o3:9003 ／ 全未命中 → 默认后端                │
└──────────────────────────────────────────────────────────────────────┘
```

**新增/编辑弹层**（表单控件按现有统一组件）：

1. 类型下拉（选项与说明文字来自 meta 接口枚举字典）：路径前缀 / 路径精确 / 路径模式 / 域名；
2. 规则值输入：按类型切换占位与校验（前缀/精确/模式须 `/` 开头，模式允许 `:param`/`*`；域名须合法域名，失焦自动转小写、拒绝端口与通配）；
3. 序号数字输入（1–999）：默认值 = 类型为域名时 999，否则当前最大序号 +10（前端建议值，可改）；
4. 标题、标签（输入回车成 chip、去重小写）、备注（折叠）；
5. **上游节点组编辑器**：节点行列表（URL、权重、高优/备份下拉、行删除、添加行），算法下拉（roundrobin/chash，选 chash 展开 key 输入），健康检查折叠区（interval/timeout/path 三输入，留空 = 不探活）——节点行校验 URL 须 `http(s)://` 开头；
6. 保存 → POST → 成功 toast「已保存并生效」自动消失 + 列表刷新；校验失败/后端拒绝常驻 error toast（文案三要素）；启停开关行内切换即保存即生效；删除走 `confirmDialog` 确认（软删，可在状态=已删除筛出后恢复）。

**提示与降级红线**：全站 toast 唯一组件（§4.10）；DB 数据访问层未就绪的 503 按普通错误弹 toast（非"功能未开启"引导态，先例：obs 降级口径辨析）；页面不设"路由规则功能开关"——规则是数据资产，有数据即生效（§4.14），唯一开关是组件级 `DISPATCH_ENABLED`。

## 接口设计（adminapi，经 RegisterPlugin 注入，仿 shield 三端点模式）

| 端点 | 方法 | 说明 |
|---|---|---|
| `/admin/dispatch/rules` | GET / POST | 列表（分页 + 类型/标签/状态/关键词筛选，`X-Total-Count` 回传）/ 新增（结构化 JSON，校验失败 400 带原因） |
| `/admin/dispatch/rules/update` | POST | 整行更新（含启停） |
| `/admin/dispatch/rules/delete` · `/restore` | POST | 软删 / 恢复 |
| `/admin/dispatch/rules/import` | POST | 解析 `DISPATCH_RULES` 旧 DSL 为结构化规则批量入库（幂等跳过同类型同值，返回导入/跳过摘要），用于一次性迁移 |
| `/admin/dispatch/rules/reload` | POST | 手动重载快照（多实例/外部改库场景的出口） |
| `/admin/dispatch/match/test` | POST | `{host,path}` → 命中结果（规则摘要或兜底链位置），只读无副作用 |
| `/admin/dispatch/rules/meta` | GET | 枚举字典（类型/算法/节点优先级说明/默认序号），表单下拉与校验提示的数据源 |

## 决策表（现行有效）

| # | 决策点 | 结论 | 出处 |
|---|---|---|---|
| D1 | 路由模型 | 扁平结构化规则列表，序号 1–999 升序命中即停，无隐式优先级（推翻此前 Envoy 两级模型） | 2026-09-19 用户指示 |
| D2 | 域名匹配 | 仅精确匹配，默认序号 999 兜底；不做通配域名 | 同上 |
| D3 | 兜底链 | 路径规则未命中 → 域名精确兜底 → 默认 upstream；全部由序号实现，无硬编码层级 | 同上 |
| D4 | 规则承载 | DB 表 + 结构化字段 + upstream JSON，WebUI 管理；不照抄 NGINX/不扩字符串 DSL | 同上 |
| D5 | 规则源降级链 | DB 可用即唯一源（空表=无规则）；DB 不可用回退旧 DSL；提供从配置导入 | 设计推荐，随关口确认 |
| D6 | 旧 DSL 去留 | 保留为降级路径与导入解析源，不再发展 | 设计推荐，随关口确认 |

## 验收标准（怎么算做完）

1. `go test ./...`、`go vet ./...` 全绿（含 `-race`）；匹配语义表驱动测试与 benchmark（≥100 规则）结果留档 STEP 回填区。
2. 实操复现：WebUI 建规则（前缀/精确/模式/域名兜底各至少一条）→ 浏览器实看渲染并截图留证 → `curl -H "Host: a.com" http://…/path` 实请求验证分流与兜底链正确。
3. 命中测试器可用：任意 Host+Path 输入返回命中规则或兜底位置，与实请求行为一致。
4. 降级路径验证：关闭 DB 配置 → 旧 `DISPATCH_RULES` 生效、组件状态标注规则源；既有 dispatch 测试零改动全绿。
5. 文档同步完成：`docs/DATA_DICT.md`（新表+枚举）、三方言建表脚本、README、`docs/CONFIGURATION.md`、`docs/webui/pages.md`（新增路由分发页章节）、接口文档、配置项 title/usage。
6. 明确不做：通配域名、正则匹配、请求头/查询参数维度、多实例自动同步与轮询重载、规则版本历史。

## 讨论留痕（防横跳，不承载现行结论）

- **Envoy 式两级模型（域名收敛 → 域内路径裁决）+ Radix 优先级分层 + `$` 精确标记 + `@` 语法扩展**：2026-09-19 上午会话曾获用户认可（"非常喜欢"），同日用户明示推翻——要求"不要照抄 NGINX"、取消两级模型、改为显式结构化字段 + 序号命中即停。教训：隐式优先级与字符串 DSL 扩展不如结构化字段直白，`@`/`$` 等字符在 URI 合法字符集内的歧义是 DSL 路线的先天缺陷。
- **map O(1) 精确匹配加速、通配域名有序列表**：随两级模型一并废弃；线性扫描 + benchmark 门槛足够，不为预设性能引入索引复杂度。
- **规则表 vs easyconf 存大 JSON 串 vs hotscripts 规则文件**：配置中心管静态配置、数据层管运营数据（含标签筛选/分页/软删审计），规则属后者；文件方案与 WebUI 写入存在双写冲突。定 DB 表。
