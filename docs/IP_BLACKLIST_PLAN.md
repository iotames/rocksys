# IP 黑名单增强方案：语义化 block_type · 从文件同步 · 自动拉黑（计划文档）

> 状态：**已定稿，进入实施** · 设计决策全部拍板并已拆分为 STEP 文档；执行入口与进度见 `docs/plan/TODO.md`，细节冲突时以 STEP 文档为准。
> 最后更新：2026-08-29

## 1. 背景与现状排查结论

1. **列表数据来源**：黑白名单页表格数据纯数据库（`ip_blacklist` 表）；`rules/ip_blacklist.txt` 只参与运行时拦截快照（文件 ∪ DB 活跃条目，`plugins/shield/shield.go` Start 重建），不进列表展示——列表里看不到文件中的 IP。
2. **批量接口三条路**（不统一）：
   - 单条新增 `POST /admin/shield/blacklist`（JSON）；
   - 批量导入 `POST /admin/shield/blacklist/import`（纯文本每行一个 IP + query 传 title/block_type，幂等跳过重复）；
   - 攻击源 TOP 页「批量加入黑名单」：前端循环逐条调单条接口（`webui/assets/js/views/topIPs.js`），撞重复报"已存在"。
   - 校验双层：前端逐行 IP/CIDR 校验 + 后端 `validIPEntry` + DB 唯一约束兜底。统一存 DB，写库成功即重建拦截快照（立即生效）。
3. **block_type 语义问题**：枚举 1-10 本义是 `shield_event` 表拦截原因，被 `ip_blacklist` 表复用为"拉黑原因类别"；人工添加/批量导入默认 1（IP黑名单）在黑名单表内同义反复、无信息量；"人工收录/其他"这类来源语义现有枚举覆盖不了；自动拉黑（新功能）应写触发时的真实拦截类别。

## 2. 已确认决策（用户拍板）

| # | 决策点 | 结论 |
|---|--------|------|
| 1 | block_type 语义改造 | **新增两个枚举取值：0 = 其他（备用）、11 = 人工收录**。人工添加/批量导入/文件同步/攻击源页手动加黑默认写 11；0 备用兜底。不改表结构，只同步枚举定义三处 |
| 2 | 0 与既有"0=全部"语义冲突处理 | `shield_event` 查询过滤参数中 0 仍表示"全部"（现有行为不变）；0=其他 仅作为黑名单条目的存储取值（见 3.1 口径说明） |
| 3 | 自动拉黑参数 | **阈值 50 次 / 10 分钟窗口 → 拉黑 24 小时**，全部注册为可配置项，可关闭 |
| 4 | TOP 批量加黑接口 | **改走 import 批量接口**（一次请求、幂等跳过重复），替换前端逐条循环 |
| 5 | 列表标题消歧 | 黑/白名单数据表格标题改为 **「黑名单条目（DB表）」/「白名单条目（DB表）」**，明示数据来自数据库、不含 rules/ip_blacklist.txt 文件条目 |
| 6 | 自动拉黑入库形态 | **仅精确 IP 入库**（拦截事件来源是精确 IP，无 CIDR） |
| 7 | 软删/过期条目被自动拉黑命中 | **不跳过**：恢复条目（清除 deleted_at）拉回小黑屋，且解封时间延长为**默认 TTL 的 10 倍**（默认 24h → 240h） |
| 8 | 黑名单表新增字段 `warn_times` | 封禁次数计数；**自动限时封禁累计达 5 次 → 转永久封禁**（expires_at 置 NULL）。牵涉三方言表结构 + 数据字典同步 |
| 9 | 拦截明细行内封禁 | 拦截明细表格每行最右新增**「操作」列 → 「IP封禁」按钮**：弹窗选择 **24h 封禁 / 永久封禁**，确认后封禁该来源 IP 且 `warn_times` 自动 +1 |
| 10 | 人工封禁命中软删/过期条目 | **恢复条目并按弹窗所选时长落 `expires_at`**（24h/永久）+ `warn_times`+1；清 deleted_at。10 倍惩罚延长仅适用于无人值守的自动拉黑（决策 7），人工尊重弹窗显式选择。弹窗内提示"该 IP 有历史封禁记录，将恢复原条目" |
| 11 | 自动拉黑候选统计口径 | **排除 `block_type=1`（黑名单自我拦截事件）**：黑名单拦截在链路最前，被封 IP 后续请求全部记为 1，若计入则封禁惩罚自我生产续封证据（到期即续封直至永久的循环），且拉黑原因永远同义反复记"IP黑名单"；排除后解封若仍攻击会再产生真实类别事件自然续封（非循环） |
| 12 | 阈值统计粒度 | **按 IP 所有拦截类别合计**（窗口内合计 ≥ 阈值触发）；拉黑原因取该 IP 窗口内**次数最多的类别** |
| 13 | 首页「小黑屋」页签 | 首页（overview）引入页签结构：现有内容为默认页签「总览」，新增**「小黑屋」**页签——`dataTable` 列出**近期非永久封禁**（未过期、未软删的限时封禁条目），字段：封禁 IP / 封禁原因（block_type 枚举名）/ 命中次数 / 封禁次数 / 封禁时间（首次，created_at）/ 解封时间（expires_at） |
| 14 | 黑名单列表排序下拉 | 黑白名单页黑名单表格筛选栏新增**「排序」下拉框**，按可用数值/时间字段服务端排序：默认（最近添加）/ 命中次数 / 封禁次数 / 封禁时间 / 解封时间 / 最后更新 / 封禁原因类别，**固定倒序**（次数高/时间新在前）；字符串字段（ip/title）不参与排序 |

## 3. 设计方案

### 3.1 block_type 枚举扩展：0=其他（备用）+ 11=人工收录（数据层变更，红线三处同步）

- Go 权威定义 `plugins/shield/block_type.go`：
  - 新增 `BlockOther BlockType = 0`（其他，现有无法归类取值的兜底）、`BlockManual BlockType = 11`（人工收录）；
  - 注释明确口径：**`shield_event` 拦截事件永远只写 1-10**；0 与 11 仅出现在 `ip_blacklist` 表（拉黑原因归类）。`BlockType.String()` 对 0 返回"其他"。
- ★ 口径冲突说明（写入代码注释与数据字典）：`block_type=0` 在**拦截明细查询参数**语境 =「全部」（现有行为不变，校验仍 0-10）；在**黑名单条目存储**语境 =「其他」。两语境分离，查询过滤不改。
- 三方言建表脚本注释同步：`sql/{sqlite,mysql,postgres}/shield_event_create_table.sql`（block_type 注释追加：0 仅黑名单表=其他；11=人工收录（仅黑名单表））与 `ip_blacklist_create_table.sql` 同步。
- `docs/DATA_DICT.md` 枚举章节同步（block_type 取值表补 0/11 及语境说明）。
- 校验口径调整：黑名单新增/导入的 block_type 参数校验放宽为 **0-11**（0=其他、11=人工收录均合法），**默认值 1 → 11**；拦截明细查询过滤保持 0-10 不变。
- 默认值切换点：`addIPList` 后端缺省、前端新增表单/导入下拉默认选项、`ImportIPList` 缺省、攻击源 TOP 批量加黑缺省。

### 3.2 黑名单列表展示优化

- 表格标题改为「黑名单条目（DB表）」/「白名单条目（DB表）」，标题旁 data-tip：数据来自数据库 ip_blacklist 表；外挂 `rules/ip_blacklist.txt` 仅参与拦截判定、不在此展示，可经「从文件同步」入库。
- 类别列：人工行显示"人工收录"，自动拉黑行显示真实触发类别（如"爬虫UA"），0 显示"其他"。★ 前端枚举映射是 `state.js` 硬编码数组 `BLOCK_TYPES`（`[[1,'IP黑名单'],…]`，现仅 1-10；经 `Rock.state.blockTypeName` 查名，未命中返回「未知」），**必须手工补 `[0,'其他']`、`[11,'人工收录']` 两项**——否则类别列显示「未知」、新增/导入下拉无新选项。★ 筛选下拉（filterBar `block_type` select，已存在，仅黑名单有）选项若直接展开 `BLOCK_TYPES` 会把 0 带入——查询语境 0=全部（决策 2），**筛选下拉必须排除 0**（0 仅作存储兜底值，不提供筛选）；补数组后其余下拉选项自动带出。

### 3.3 从文件同步（新端点 + 前端按钮）

- 后端：`POST /admin/shield/blacklist/sync_file`（`admin.go` 新路由常量 `PathBlacklistSyncFile`，`cmd/rocksys/main.go` 装配）。实现：经 ScriptHub/ScriptDir 读 `rules/ip_blacklist.txt`（外挂优先、内嵌兜底），复用导入解析（`#` 注释/空行忽略、逐行 IP/CIDR 校验），走 `ImportIPList(true, lines, title="来自 ip_blacklist.txt 同步", BlockManual)`；响应 `{imported, skipped}`；文件缺失/为空返回 400 明确文案（遵循文案三要素）。
- 前端：黑白名单页签操作区新增「从文件同步」按钮，hover tooltip（data-tip）：**"从外挂规则文件 rules/ip_blacklist.txt 同步 IP 入库。因文件无过期时间/备注等维护字段，同步入数据库后便于统一管理、统计与自动拉黑"**。结果 toast：成功展示 imported/skipped、异常常驻（遵循提示分级规范）。
- 幂等：重复同步 skipped 递增，不报错。

### 3.4 自动拉黑（风控规则引擎，配置驱动）

- 新配置项（`conf.Manager.Register` 注册，default.env 自动同步）：

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `SHIELD_AUTO_BAN_ENABLED` | false | 自动拉黑开关（显式开启） |
| `SHIELD_AUTO_BAN_THRESHOLD` | 50 | 统计窗口内拦截次数阈值 |
| `SHIELD_AUTO_BAN_WINDOW` | 10m | 统计窗口 |
| `SHIELD_AUTO_BAN_TTL` | 24h | 默认拉黑时长（0=永久；续封 10 倍基数即取此值） |

- 数据层新增：`ip_blacklist` 表新增列 **`warn_times INTEGER NOT NULL DEFAULT 0`（封禁次数）**——三方言建表脚本 + `docs/DATA_DICT.md` 同步（数据字典红线三处）；`ip_list_store.go`（Insert/List/Update/Import 语句）与前端列表新增「封禁次数」列同步。
- 实现：新文件 `plugins/shield/auto_ban.go`。后台 goroutine（运行周期 = window/3、下限 1 分钟）：
  1. 三方言新 SQL 脚本 `shield_event_auto_ban_candidates.sql`：近窗口内、**`block_type >= 2`**（排除黑名单自我拦截事件，决策 11）`GROUP BY client_ip, block_type` 返回 `(client_ip, block_type, cnt)`；**Go 侧按 IP 跨类别合计判阈值**（决策 12），拉黑原因取该 IP 次数最多的类别——SQL 不用窗口函数（老 MySQL 无 ROW_NUMBER，三方言保持同构），聚合逻辑在 Go 单处实现可测；
  2. 仅精确 IP 入库（拦截事件来源为精确 IP，无 CIDR）；
  3. 命中 IP 分情况处理（均重建快照）：
     - **无记录**：新增入库，`block_type`=触发类别（1-10 真实枚举）、`title`=`自动拉黑：{window}内拦截≥{threshold}次`、`expires_at`=now+TTL、`warn_times`=1；
     - **活跃条目**（未删未过期）：跳过；
     - **软删/已过期条目**：恢复条目（清 deleted_at）拉回小黑屋，解封时间延长为**默认 TTL 的 10 倍**（如 24h → 240h），`warn_times`+1；
     - **限时封禁前 `warn_times`+1 后 ≥5**：转为**永久封禁**（expires_at 置 NULL），title 追加"（累计封禁达 5 次转永久）"。
- 生命周期：随 EventRecorder 模式（Start 启动、Stop 退出），`main.go` 装配处按配置开启；每轮循环开始时经配置中心读最新值（开关/阈值/窗口/TTL 均支持热更，无需重启）。入库前用拦截快照白名单过滤——**复用快照的 CIDR 匹配语义**（白名单可含网段，不能只做精确 IP 比对）。拦截热路径零改动（纯后台批处理，性能红线不触碰）。
- 候选 SQL 性能：窗口过滤走 `time` 列（`shield_event` 已有 `idx_time` 索引，默认 10 分钟窗口扫描量可控）；`GROUP BY client_ip` 可用已有 `idx_client_ip`，无需新增索引。

### 3.5 拦截明细行内「IP封禁」操作（新交互）

- 入口两处（同一段封禁弹窗逻辑复用，避免双份实现）：
  1. **拦截明细表格**（WAF 页·拦截统计页签下方明细表）新增最右**「操作」列**，每行一个「IP封禁」按钮（取该行 `client_ip`）；
  2. **行详情弹层**（点击明细行弹出的键值详情）追加「IP封禁」操作按钮入口——用户在详情里核对完 UA/原始 URL 等证据后可直接就地封禁，不必关弹层回表格找按钮。
- 封禁弹窗（复用 `openModal` 风格，**字段尽量齐全**）：

| 字段 | 控件 | 默认值 | 说明 |
|------|------|--------|------|
| 来源 IP | 只读文本 | 该行 `client_ip` | 不可改；旁边展示该 IP 当前 `warn_times`（已有封禁次数）与是否已在黑名单 |
| 封禁理由（title） | 文本输入 | 预填 `人工封禁：{该行拦截类别名}拦截`（可改） | 自由文本，落库到 `title` |
| 拉黑原因类别（block_type） | 下拉选择 | **11 人工收录** | 复用「拉黑原因类别」枚举下拉（1-11）；通常无需改动，保留改选能力（如明确因 SQL 注入拉黑可选 7） |
| 封禁时长 | 单选 | **封禁 24 小时** / 永久封禁 | 落库 `expires_at`：now+24h / NULL |
| 确认效果提示 | 弹窗内说明文字 | — | 提交后该 IP `warn_times` +1；累计达 5 次的限时封禁自动转永久（与自动拉黑同规则） |

- 提交：**新增专用端点 `POST /admin/shield/blacklist/ban`**（body：`ip`/`title`/`block_type`/`duration`=`24h`\|`permanent`，服务端换算 `expires_at`、`warn_times` 计数），弹窗提交改调此端点。**不复用/不过载 `POST /admin/shield/blacklist` 新增端点**：`addIPList` 撞已存在（含软删未生效）时报"已存在但未生效并指引恢复路径"是刚精修的 UX（2bdeb1b），决策 10 的自动恢复语义塞进去会悄悄抹掉该报错路径、污染新增语义；新增表单行为保持不变。
- ban 端点三态处理（同自动拉黑复用的 store 辅助方法）：无记录 → insert（`warn_times`=1）；**活跃条目** → 报"已在黑名单"（按钮本应置灰，双保险，文案含条目跳转指引）；**软删/过期条目** → 决策 10（恢复 + 弹窗所选时长 + `warn_times`+1）。
- 成功 toast 自动消失、异常常驻（提示分级规范），弹层关闭并刷新明细。
- 已在黑名单的 IP：按钮置灰 + tooltip 提示（与攻击源 TOP「在黑名单不可选」同语义）。数据来源：拦截明细 events 接口按当前页 IP **遍历内存拦截快照 `InBlacklist`** 附 `in_blacklist` 标记（与 stats TOP 页同源、零 DB 查询）；`warn_times` 与是否软删/过期在**弹窗打开时单查**——复用列表接口 `GET /admin/shield/blacklist?ip={精确IP}` 取完全匹配行（deleted_at/expires_at 判状态、warn_times 随 A2 补列可得，不新增端点）。
- 组件扩展：`detailModal` 现仅支持 copy 按钮，需新增**可选 actions 配置**（视图注入「IP封禁」按钮与回调，保持组件通用、不耦合 shield 语义）；「操作」列经 `dataTable` 的 `col.render` 回调实现——该回调属组件约定的**显式信任边界**，`client_ip` 写入 data 属性须 `esc` 转义。
- 说明：`warn_times`+1 的语义为"该 IP 被人工/风控封禁的累计次数"，人工封禁与自动拉黑共用同一计数（决策 8 的 5 次转永久同样适用于人工限时封禁）。

### 3.6 前端交互修缮

- 攻击源 TOP「批量加入黑名单」：改为一次 `POST /import?title=攻击源TOP批量加黑&block_type=11`，toast 展示"已导入 X 条，跳过 Y 条"；消除逐条循环与"已存在"报错。
- 批量导入卡片左下角下拉框加 label：**「拉黑原因类别」**（select 前置 label 或 data-tip：入库记录的 block_type 枚举，默认人工收录）。
- 新增表单类别选择同样补 label 与默认值 11。

### 3.7 首页「小黑屋」页签（决策 13）

- 页面结构：首页 overview 现无页签（纯 metrics/组件卡片仪表盘），引入 `tabs` 组件——现有内容归入默认页签**「总览」**（自动刷新等既有行为不变），新增页签**「小黑屋」**。
- 数据口径：**当前在押的限时封禁条目**——`expires_at` 非 NULL 且 > now（未过期）、`deleted_at` 为 NULL（未软删）；即"非永久封禁且正在生效"。`created_at` 天然满足"首次封禁时间"（恢复续封只改 `expires_at`/`warn_times`，不动 `created_at`）。
- 后端：新端点 `GET /admin/shield/jail`（query：`limit`，默认 20，上限 100）。三方言新脚本：
  - `ip_blacklist_query_jail.sql`：上述过滤条件，`ORDER BY expires_at ASC`（临近解封的在前，便于关注即将放出的 IP），`LIMIT ?`；
  - `ip_blacklist_jail_count.sql`：同条件计数（前端展示"共 N 条在押"，超出 limit 提示还有更多）。
  - 响应 `{total, rows}`，rows 字段：`ip / block_type / hit_count / warn_times / created_at / expires_at`（`block_type` 中文名前端经既有 `typeName` 映射）。
- 前端（小黑屋页签）：
  - `dataTable` **client 模式**（首页轻量预览，不分页，拉 limit 条）；列：封禁 IP / 封禁原因 / 命中次数 / 封禁次数 / 封禁时间（首次）/ 解封时间；空态用 empty 组件（"小黑屋空空如也"）；
  - 表格下方「管理全部黑名单 →」链接跳黑白名单页（在押超出预览条数时的完整出口）；
  - 数据拉取时机：切到该页签时拉取 + 随首页自动刷新周期联动刷新（「总览」页签行为不变）；
  - 依赖：`warn_times` 列（决策 8）先落地——实施顺序上本节在数据层步骤之后。

### 3.8 黑名单列表排序下拉（决策 14）

- 筛选栏（filterBar）新增「排序」select，**改动即查**（与既有筛选控件同语义），排序变更回第 1 页：
  - 选项与映射（服务端排序，`sort` 参数）：默认（最近添加，`id DESC`）/ 命中次数（`hit_count DESC`）/ 封禁次数（`warn_times DESC`）/ 封禁时间（`created_at DESC`）/ 解封时间（`expires_at DESC`）/ 最后更新（`updated_at DESC`）/ 封禁原因类别（`block_type DESC`）；
  - **字符串字段（ip/title）不提供**（用户拍板）；非法/缺省 `sort` 值回默认。
- SQL 实现：`ip_blacklist_query_list.sql` 的 `ORDER BY id DESC` 改为 `ORDER BY {order}` **占位符**（复用 `{table}` 占位符先例与替换机制）；Go 侧将 `sort` 参数经**白名单映射表**（枚举值 → 列名表达式）替换注入，杜绝拼接注入面；`ip_blacklist_count.sql` 不受影响。
- 白名单页（whitelist）无 block_type/warn_times/expires_at 语义，排序下拉仅上黑名单表格；白名单沿用默认排序（不过度设计）。

## 4. 实施步骤

实施已逐一拆分为 `docs/plan/ip_blacklist/STEP1..STEP8`（A1-A8，每份含勾选清单/验证标准/实施状态/中断回填区），执行顺序与进度总纲见 `docs/plan/TODO.md`——**从总纲开始，按序全自动**，前置为项目一（表结构同步）B5 完成。本节不再重复步骤明细，与 STEP 文档冲突时：顺序以总纲为准、细节以 STEP 文档为准。

## 5. 枚举系统审计（2026-08-29 全量排查）

### 5.1 项目内全部数据枚举清单

| 枚举 | 权威定义 | 使用表/场景 | 审计结论 |
|------|----------|-------------|----------|
| `block_type`（1-10） | `plugins/shield/block_type.go` | `shield_event` / `ip_blacklist` / `attack_archive` 三表复用 | **核心问题枚举**，见 5.2 |
| `mq.status`（pending/failed/done/dead） | `plugins/mq/mq.go` | `outbox.status` | ✅ 语义清晰，字符串枚举覆盖完整生命周期（含 dead 兜底），无需改动 |
| `rule_hit`（特征名字符串） | `plugins/shield/waf.go` 等拦截点 | `shield_event.rule_hit` | ✅ 自由字符串特征名（如 `waf:sql_union`），非受限枚举，扩展无需迁移，无需改动 |
| `block_type` 过滤参数语境（0=全部） | `admin.go` Events/listIPList | 拦截明细/黑名单列表查询 | ✅ 查询语境合理；但黑名单列表过滤校验 0-10 与存储口径（将新增 0/11）需同步为 0-11，已纳入 §3.1 |

### 5.2 block_type 语义问题详单（本方案 §3.1/§3.4 已覆盖）

1. **三表复用、语义漂移**：枚举本义是 `shield_event` 的"拦截原因"（运行时真实发生），但 `ip_blacklist` 把它用作"拉黑原因/来源"（管理面归类），`attack_archive` 又用作"攻击类别"。三者语境不同，唯一权威注释只按"拦截类别"描述——`docs/DATA_DICT.md` §3.1 需在实施时补"语境列"（拦截事件 / 黑名单条目 / 攻击归档）。
2. **人工来源语义缺失**：人工添加/批量导入默认 `block_type=1`（IP黑名单），在黑名单表内同义反复、无信息量（用户实际发现：列表类别列全部显示"IP黑名单"）→ 决策 1：新增 0（其他，备用）/ 11（人工收录）。
3. **自动拉黑未来语义**：后台风控拉黑应写触发时的真实拦截类别（1-10），不需要新枚举表达攻击类型 → §3.4 已按此设计。
4. **前端展示**：映射为 `webui/assets/js/state.js` 硬编码数组 `BLOCK_TYPES`（1-10，经 `blockTypeName` 查名、未命中返回「未知」，非服务端驱动）——新增 0/11 后须同步补数组两行，类别列与新增/导入下拉才出现新值；筛选下拉须排除 0（见 §3.2）。
5. **攻击归档表 `attack_archive.block_type` 默认值 1**：与黑名单表同样的语义问题（建表脚本默认 `1`），本期该表仅建表未启用；实施时仅同步注释口径，不改默认值（留归档功能实装时一并处理，避免过度设计）。

### 5.3 「批量导入」左下角下拉框答疑与改造

**答疑**：是的——那个下拉框的选项（黑名单/限流/方法不允许/…/爬虫UA）就是 `block_type` 枚举值 1-10 的中文名，含义是"**这批导入的 IP 之所以被拉黑的原因归类**"（例如导入一批扫描器 IP 可选"爬虫UA"）。迷惑点有三：① 无 label，光秃秃一个 select；② 第一个选项"黑名单"与所在功能（加黑名单）同义反复，最易误导（根因即 5.2-2，导入默认落 1）；③ 与"备注（title）"的关系没说明（类别是枚举归类、备注是自由文本，互补非重复）。

**改造**（§3.6 已列，此处补充细化）：
- select 前加 label **「拉黑原因类别」**，默认选项改为「人工收录」（11）；
- data-tip：`入库记录的拉黑原因归类（block_type 枚举），用于黑名单列表过滤与拦截统计；自由文字请填备注栏`；
- "黑名单"（1）选项保留但不做默认——它专指"被 IP 黑名单拦截规则自动拉黑"的场景，人工导入不该选它。

## 6. 变更记录

- 2026-08-29 定稿：初稿至定稿共八轮评审（枚举系统审计、专用 ban 端点、决策 6-14 拍板、state.js 断言修正等）的结论已全部合并进正文，过程记录不再保留；开放问题清零。
