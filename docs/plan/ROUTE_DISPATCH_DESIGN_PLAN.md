# 路由分发宏观设计（ROUTE_DISPATCH DESIGN PLAN）

> 层级定位：本文是**宏观设计层（最权威最上层）**，以 OGSM 框架承载"为什么与做到什么程度"，指导同源实施文档 `ROUTE_DISPATCH_IMPL_PLAN.md`（实施指导层，下层）。冲突时本文优先；操作细节以下层为准。
> 状态：**定稿 v3（2026-09-21）**——人类已明确授权"问题收敛后直接定稿"，宪法 §2 设计定稿单关口视为已过；可据此建 STEP、落地实施。实施文档 `ROUTE_DISPATCH_IMPL_PLAN.md` 随本定稿自主细化，不需单独请示。

## 现状结论（带证据）

- **装配**：`plugins/dispatch` 为转发链 L2 中间件（`cmd/rocksys/main.go:282`，`chain.Middle` 槽位），`DISPATCH_ENABLED` 开关控制（默认 false，`dispatch.go:82`）。
- **匹配维度单一**：仅 URI 路径，不感知 Host。规则为 `DISPATCH_RULES` 单行字符串 DSL（`<Prefix>=<spec>`），内部 Radix Tree 最长前缀优先（`router.go`）；未命中不写 `DataFlow.Target`，回退 Adapter 默认 upstream（`internal/chain/adapter.go:74-78`，全局单值、无域名维度）；无默认 upstream 时不写响应直接放行、交 easyserver 链尾处理（`adapter.go:78-83`）。该 DSL **无真实存量数据**，处置 = 连同代码整体移除（见 G6），不存在迁移与兼容问题。
- **节点组能力已备但内聚于规则字符串**：多节点平滑加权轮询、高优/备份（priority）、规则级主动健康检查（interval/timeout/path，2xx/3xx 判健康）——`balancer.go` / `healthcheck.go`，`Select` 选点与探活生命周期可直接改造复用。局限：同一物理节点出现在多条规则即重复登记、重复探活，无单一状态事实源。
- **热更骨架已备**：路由表不可变快照经 `atomic.Value` 原子替换，`Start(nil)` 重建、`Stop()` 停旧探活，请求路径零锁。
- **响应链钩子可挂，但仅 Tail 槽位生效**：chain 提供响应阶段回调——`ResponseHook.OnResponse`（转发完成后、写回客户端前执行，转发成功与失败两类路径均覆盖；但**链中断与无 target 放行两类路径不经过**，`internal/chain/interface.go:45-50`、`adapter.go:73-84/109-123`）与 `DoneHook.OnDone`（写回完成后，同样不覆盖中断路径；obs 为消费先例）。★ Adapter 只收集 **Tail 槽位**实现的钩子（判定证据：`adapter.go:99` `chain.HasResponseHook(Tail)`；`internal/chain/impl.go:103-116` 为 `ResponseHooks(slot)` 通用收集机制）——dispatch 挂 Middle，其自身的 OnResponse 永不被调用，响应期收尾须经独立 Tail 收尾件（落地机制见 S4 双中间件）。缓冲代价口径：obs 启用时全站响应本就走缓冲路径，dispatch 收尾件增挂无新增代价；obs 停用而 dispatch 启用时由收尾件引入缓冲路径——收尾件只要 dispatch 启用即挂 Tail，缓冲代价覆盖 dispatch 启用期的全部命中流量，与所选策略无关（round_robin 亦然；这是 least_conn 递减的必要代价）。WebUI 侧对 least_conn 选项做 obs 门控与警告提示（见 WebUI 设计章节均衡器编辑表单），向用户透出该代价与边界。least_conn 递减与观测透出有现成挂载点。转发写回经 `copyHeader` 逐值 `Add` 合并上游响应头（`internal/chain/impl.go:140`）——Middle 槽位预设的响应头不会被上游覆盖，与上游同名头（如 Set-Cookie）共存。
- **数据层默认可用**：`DB_DRIVER` 默认 `sqlite` 零外部依赖；SQL 脚本按 `<表>_<动作>.sql` 三方言组织（`sql/<dbtype>/`），表结构检查/执行闭环已有产品化能力（adminapi `dbschema.go` + WebUI 数据库页）。
- **DB CRUD 管理全链路有成熟先例**：ip_blacklist（软删/恢复/行详情编辑弹层，`plugins/shield/admin.go` + WebUI），端点经 `adminapi.RegisterPlugin(path, handler)` 注入。
- **WebUI 规范体系完备**：`filterBar`/`dataTable`/`detailModal` 公共组件（pages.md §4.7）、toast 唯一提示组件（§4.10）、数据类资产不设功能开关（§4.14）、枚举字典经接口下发（先例：配置项类型接口）。

## O（Objective·目的）

将 dispatch 升级为 NGINX 同构的**三层路由与负载均衡体系**：**后端节点资产（登记与体检）→ 负载均衡器（策略与会话保持）→ 路由规则（匹配与引用）**。路由数据全部由数据库承载、WebUI 管理；`.env` 只保留组件开关。节点健康状态由健康检查中心统一生产（每节点一份、全局去重），均衡器作为状态消费者选点。为无共享存储 session 的后端提供 sticky cookie 会话保持。

## G（Goals·关键结果）

- **G1 规则结构化**：规则 = **可选域名** + 路径匹配（前缀/精确/模式三类型）+ 均衡器引用 + 序号（1–999 升序、命中即停）。域名是规则的可选正交维度，不是独立规则类型；"域名默认均衡器"即「域名 + 路径 `/` + 大序号」的普通规则。
- **G2 均衡器实体**：独立持久化实体（名称、策略、sticky 开关），与节点经关系表多对多关联；同一批节点可被多个均衡器引用（会话保持池与普通池共用后端的场景）。
- **G3 节点资产与关系分离**：节点基础表登记服务器资产与体检参数（登记一次、探活一次）；关系表承载均衡器视角的节点属性（权重、高优/备份）。
- **G4 健康检查中心**：节点级统一探活——只对"被启用均衡器引用的启用节点"探活，每节点恰好一个探活任务；健康状态单一事实源，供选点消费与 WebUI 透出。
- **G5 WebUI 管理**：「路由分发」页覆盖规则/均衡器/节点三实体管理 + 命中测试器；保存即热更（快照原子替换，请求路径零锁）。
- **G6 DSL 整体移除**：`DISPATCH_RULES` 配置项与全部 DSL 解析代码（`parseRules` 解析链、Radix Tree、chash）删除——无存量数据、无迁移、无兼容，当作从未存在；既有 DSL 语义测试随代码删除，测试资产按 v3 语义重建（`:param`/`*` 段匹配语义保留进 path_type=3，与新模型无关地延续）。DB 不可用时规则表为空 = 全部走默认 upstream（现状兜底语义），组件状态透出规则源。

## S（Strategies·策略）

- **S1 匹配引擎（扁平序号命中即停）**：规则快照按 `(match_order, id)` 稳定升序排列（仅含启用且未软删行）。引用有效性不做构建期剔除——停用均衡器/停用节点不吞规则（**fail-closed**：配置停用是管理动作，命中后显式 503 优于流量静默落到后续规则或默认后端；NGINX `down`、Traefik 服务不可用同款语义）。匹配流程零特判：
  1. `host = 剥端口(含 IPv6 `[::1]:80` 方括号形态) + 转小写`（空 Host 保持空）；
  2. 依序逐条判定：规则 `domain` 字段**为空（匹配任意域名）或与请求 Host 精确相等**，且路径按类型匹配（前缀=段对齐且**命中自身**——`/api` 命中 `/api` 与 `/api/x`、不匹配 `/apix`，`/` 命中一切路径含 `/` 本身（NGINX `location` 前缀同款语义，D13）；精确=全等、不做尾斜杠归一；模式=`:param`/`*` 段匹配，沿用现有捕获语义与 `X-Route-Param-*` 注入）。**路径匹配大小写敏感，域名匹配不敏感（归一小写比对）**——与 NGINX 同款语义；
  3. 命中 → 该规则引用的均衡器选点（均衡器停用或无可用节点 → 写 503 中断链，现状语义）→ 写 `Target`，终止；全未命中 → 不写 Target，Adapter 默认 upstream（无默认 upstream 时直接放行，现状分支 `adapter.go:78-83`）。
  Radix Tree 废弃（最长前缀的隐式优先级与序号模型冲突）；性能靠线性扫描在百级规则下的微秒量级 + benchmark 门槛，不达标才考虑加载期分桶（不预设优化）。
- **S2 均衡器与算法**：策略枚举 `algo`：**round_robin**（平滑加权轮询，权重默认 1，即纯轮询——与 NGINX 同款一体语义，不单设"加权轮询"枚举）/ **least_conn**（优先分配至在途请求最少的节点，平局回落轮询游标）。选点统一语义：高优节点（priority=0）健康集优先，全不健康回落备份节点（priority=1）健康集，再无 → 该规则回 503（现状语义不变）。均衡器停用 = 其节点视为不可用，效果同 503。
- **S3 sticky cookie 会话保持**：均衡器独立开关（与任意策略正交组合，Envoy/Traefik/HAProxy 同款模型，不作为第三种算法）：
  1. 请求携带本均衡器的 sticky Cookie 且**所指节点在当前关系内且健康** → 直路由该节点（优先于策略选点；不校验 priority——粘性优先于高优回落，节点探活判死即失效）；
  2. 无 Cookie 或失效 → 按策略选点，并经 `W.Header().Add("Set-Cookie", ...)` 在响应头种 Cookie（Middle 槽位设头合法，转发写回经 `copyHeader` Add 合并自然携带；**必须 `Add` 禁止 `Set`**——`Set` 会整键覆盖，抹掉上游应用自身的 Set-Cookie）；
  3. Cookie 值 = 节点 id 明文；会话级 Cookie（不设 Max-Age，浏览器会话内有效）；Cookie 名可配（默认 `rocksys_node`）；https 请求（经 X-Forwarded-Proto 判断）附加 `Secure` 属性。
- **S4 least_conn 在途计数**：每节点原子计数，选中时 +1（sticky 直路由同样真实占用节点，同样计入），递减挂 dispatch 的 **Tail 收尾件** `OnResponse`（写回客户端前，覆盖转发成功与失败两类路径）。递减做饱和处理（≥0）：节点失引用清 registry 记录后重新登记，旧在途请求的迟到递减不得把新记录打成负数；registry 中记录已不存在（节点失引用且未再登记）时递减为 no-op。**落地机制（双中间件）**：Adapter 只调用 Tail 槽位的响应钩子，而 dispatch 主件须挂 Middle（路由在转发前执行）——故 dispatch 组件注册两个链中间件：主件（Middle，Handle = 路由 + 选点 + 计数 +1）与收尾件（独立 Name，Tail，Handle 直通放行、OnResponse 递减），共用运行态；`DISPATCH_ENABLED` 经 autoEnableMap 同键联动两者（启停同步，装配注册顺序固定保证链序确定）。**递减定位**：Handle 选中节点后将节点 id 存入请求级 DataFlow（KV 先例：`rocksys:path_params` 同款），OnResponse 按 DF 中的节点 id 递减——节点 id 跨热更稳定，不经快照 url 反查，杜绝 Rebuild 替换快照后递减失配的泄漏。WebSocket 隧道：Adapter 在 forward 返回后才执行 Tail 钩子，而 WS 隧道 forward 阻塞至连接关闭（engine 双向对拷后返回），故在途计数**全程占用、隧道结束才递减**——与 NGINX least_conn 计入活跃连接的语义一致，无需特殊处理。已知边界：dispatch 之后的中间件中断链的请求不经过响应钩子，存在计数泄漏（低频、仅统计偏差不影响转发正确性），chain 补中断路径收尾回调后消除（本期不做，入已知边界）。
- **S5 健康检查中心（供应商-消费者）**：dispatch 内部运行态子模块，不落独立进程：
  - **生产**：探活任务集 = Rebuild 时计算"被启用均衡器经有效关系引用的启用节点"去重集合；每节点一个探活 goroutine（参数取节点表：`hc_interval_ms`/`hc_timeout_ms`/`hc_path`，path 为空 = 不探活、视为健康）；启动即探一次防窗口期，2xx/3xx 健康，其余判死。状态经节点 id 索引的 registry（atomic）读写。
  - **消费**：均衡器选点、WebUI 健康状态列、`/admin/dispatch/health` 快照端点统一读 registry，请求路径零 DB 查询。健康状态三态：健康（绿）/ 不健康（红）/ 未探活（灰）。**记录集口径**：registry 记录集 = 探活任务集 ∪ 免探活登记集（后者即 `hc_path` 为空的被引用节点，恒健康、不产生探活任务）——任务差量移除（节点失引用/停用）即清其记录、节点转灰；「跨热更保序」的保留不清零仅指任务仍保留的节点。`hc_path` 为空的被引用节点不产生探活任务，但在 registry 登记为健康态（选点可用、WebUI 显示绿）——避免"实际可用却显灰"误导运维。
  - **跨热更保序**：Rebuild 后任务集合做差量增减，保留节点的既有健康状态不清零（避免热更瞬间状态闪断）；节点探活参数（interval/timeout/path）变更视为差量重启该节点任务；探活 goroutine 随组件 `Stop` 排空退出，无泄漏。
- **S6 规则源与热更**：DB 为唯一规则源（不存文件、不读 .env 规则）；**路由数据四表**（规则/均衡器/节点/关系）任一变更 → adminapi 调 `Dispatch.Rebuild()`：拉全部四表 → 构建对象图（规则数组 + 均衡器含关系展开 + 探活任务集差量）→ 校验（均衡器引用存在、节点 URL 合法、值与类型匹配、序号范围、domain 构建期统一归一（转小写）放行、不做拒绝项——外部改库存入大写 domain 时 Rebuild 静默归一，不导致快照构建失败）→ 原子替换快照；**Rebuild 全程持互斥锁串行**（防并发保存下旧拉取结果覆盖新快照）；**标签两表仅影响管理展示（列表筛选/显示），变更不触发 Rebuild**。构建失败保留旧快照并报错回前端。多实例/他进程改库场景提供「重载」按钮，不做自动轮询（宁简勿繁）。**启动期恢复**：Start 时 DB 未就绪 → 空表快照 + 后台按固定间隔重试直至首次构建成功、成功即停（非常驻轮询）——否则进程重启赶上 DB 慢启（PG/MySQL 后启动），路由静默失效等人工点重载。`DISPATCH_ENABLED` 仍是总开关，false 时中间件不挂载、一切不生效。
- **S7 WebUI 页面**：侧边栏「路由分发」一项，页内三视图切换（规则/均衡器/节点），详见 WebUI 设计章节。

## M（Measures·度量）

| 度量项 | 口径 | 验证手段 |
|---|---|---|
| 匹配语义正确性 | 序号命中即停 / domain 可选维度（空=任意、精确匹配、剥端口、IPv6、小写）/ 三类型路径（前缀段对齐含命中自身、精确不归一尾斜杠、模式捕获）/ 路径大小写敏感·域名不敏感 / 空 Host | 表驱动单测（新 `match` 用例矩阵） |
| sticky 语义 | 带 Cookie 直路由 / 失效节点回落选点并重种 / 不跨均衡器误粘 / Secure 属性跟随协议 | 表驱动单测 + 实请求验证 |
| least_conn 语义 | 并发下选中分布偏向低在途节点 / 转发成败均正确递减 | 单测 + `-race` |
| 健康检查中心 | 同节点多均衡器引用只探一份 / 任务集随 Rebuild 差量增减（含参数变更重启） / goroutine 无泄漏 | 单测 + `go test -race` + 热更重建用例 |
| 匹配性能 | 100 与 1000 规则两档下单次匹配 ≤ 10µs（绝对门槛；线性扫描必然慢于 Radix 树，不与旧量级攀比，只看绝对值对代理场景是否够用——上游 RTT 毫秒级） | `go test -bench` 留档 |
| CRUD 全链路 | 规则/均衡器/节点/标签增删改查、引用约束拒绝（含启停/恢复时的引用校验）、命中测试端到端可用 | adminapi 表驱动测试 + 浏览器实看截图留证 |
| 文档同步 | DATA_DICT/README/CONFIGURATION/pages.md/配置项 title·usage 与实现一致 | 仓库法文档同步红线逐项核对 |

## 数据结构（宪法 §7 强制章节）

六张表（核心路由四表 + 标签两表），三方言建表脚本 `sql/{sqlite,postgres,mysql}/dispatch_*.sql`，注释与 `docs/DATA_DICT.md` 一一对应；公共字段 `created_at`/`updated_at`/`deleted_at`（UTC，软删惯例与 ip_blacklist 一致，软删行不参与任何运行期构建）。

### dispatch_node — 后端服务器节点基础表

登记节点资产与体检参数；登记一次、全局去重（探活去重的前提）。

| 字段 | 标题 | 说明 | 三方言类型 |
|---|---|---|---|
| `id` | 主键 | 自增主键 | INTEGER PK AUTOINCREMENT / BIGSERIAL / BIGINT AUTO_INCREMENT PK |
| `name` | 节点名称 | 人类可读名称，空允许 | TEXT / TEXT / VARCHAR(255) |
| `url` | 节点地址 | `http(s)://host[:port]`，唯一（软删行除外） | TEXT NOT NULL（三方言统一） |
| `hc_interval_ms` | 探活周期 | 毫秒，默认 20000；`hc_path` 为空时不参与探活 | INTEGER NOT NULL DEFAULT 20000 |
| `hc_timeout_ms` | 探活超时 | 毫秒，默认 5000 | INTEGER NOT NULL DEFAULT 5000 |
| `hc_path` | 探活路径 | 以 `/` 开头；**空 = 不主动探活，视为健康**（现状语义） | TEXT NOT NULL DEFAULT '' |
| `enabled` | 启用 | 1/0；停用 = 不参与任何均衡器构建与探活 | INTEGER NOT NULL DEFAULT 1 |
| `remark` | 备注 | 人类备注 | TEXT NOT NULL DEFAULT '' |
| 公共字段 | — | created_at / updated_at / deleted_at | DATETIME / TIMESTAMPTZ / DATETIME(3) |

### dispatch_upstream — 负载均衡器表

| 字段 | 标题 | 说明 | 三方言类型 |
|---|---|---|---|
| `id` | 主键 | 自增主键 | 同上惯例 |
| `name` | 均衡器名称 | 人类可读、唯一（软删行除外；如「订单服务-会话保持池」） | TEXT NOT NULL |
| `algo` | 均衡策略 | 枚举：1 round_robin（平滑加权轮询，默认）/ 2 least_conn（最小连接优先） | INTEGER NOT NULL DEFAULT 1 |
| `sticky_enabled` | 会话保持 | 1/0，默认 0；开启后网关自种 Cookie 粘性（见 S3） | INTEGER NOT NULL DEFAULT 0 |
| `sticky_cookie` | Cookie 名 | 默认 `rocksys_node`；仅 sticky_enabled=1 时生效 | TEXT NOT NULL DEFAULT 'rocksys_node' |
| `enabled` | 启用 | 1/0；停用 = 引用它的规则全部 503（WebUI 停用时提示引用数） | INTEGER NOT NULL DEFAULT 1 |
| `remark` | 备注 | 人类备注 | TEXT NOT NULL DEFAULT '' |
| 公共字段 | — | 同上 | 同上 |

### dispatch_upstream_node — 节点与均衡器关系表

| 字段 | 标题 | 说明 | 三方言类型 |
|---|---|---|---|
| `id` | 主键 | 自增主键 | 同上惯例 |
| `upstream_id` | 均衡器 | → dispatch_upstream.id | INTEGER NOT NULL |
| `node_id` | 节点 | → dispatch_node.id | INTEGER NOT NULL |
| `weight` | 权重 | 正整数默认 1；round_robin 平滑加权用 | INTEGER NOT NULL DEFAULT 1 |
| `priority` | 优先级 | 0=高优（默认）/ 1=备份（高优全挂才启用，即 NGINX backup） | INTEGER NOT NULL DEFAULT 0 |
| 公共字段 | — | 同上；唯一约束 `(upstream_id, node_id)`（软删行除外） | 同上 |

### dispatch_rule — 路由规则表

| 字段 | 标题 | 说明 | 三方言类型 |
|---|---|---|---|
| `id` | 主键 | 自增主键 | 同上惯例 |
| `match_order` | 匹配序号 | 匹配顺序 1–999 升序、命中即停；域名默认兜底规则建议 999 | INTEGER NOT NULL |
| `domain` | 域名（可选） | 空 = 匹配任意域名；非空 = 精确匹配、剥端口、转小写（无通配）；**保存时归一落库；Rebuild 构建期归一进快照（放行不做拒绝项，见 S6）**（防外部改库存入未归一值永不命中） | TEXT NOT NULL DEFAULT '' |
| `path_type` | 路径类型 | 枚举：1 前缀（段对齐）/ 2 精确 / 3 模式（`:param`/`*`） | INTEGER NOT NULL DEFAULT 1 |
| `path_value` | 路径值 | 以 `/` 开头，按 path_type 解释；`/` + 前缀 = 全路径兜底 | TEXT NOT NULL |
| `title` | 规则标题 | 人类可读，空允许 | TEXT / TEXT / VARCHAR(255) |
| `upstream_id` | 均衡器 | → dispatch_upstream.id，命中规则的转发目标 | INTEGER NOT NULL |
| `enabled` | 启用 | 1/0；停用行不参与匹配，保留配置 | INTEGER NOT NULL DEFAULT 1 |
| `remark` | 备注 | 人类备注 | TEXT NOT NULL DEFAULT '' |
| 公共字段 | — | 同上 | 同上 |

标签经 `dispatch_tag` / `dispatch_rule_tag` 两表关联（见下），不在本表存列。

### dispatch_tag — 路由规则标签表

标签实体化（GitLab label 同款模型）：全局唯一、重命名一次生效、筛选下拉数据源干净。chip 颜色由前端按名称哈希自动分配，不落库。

| 字段 | 标题 | 说明 | 三方言类型 |
|---|---|---|---|
| `id` | 主键 | 自增主键 | 同上惯例 |
| `name` | 标签名 | 全局唯一、小写（保存时归一；软删行除外） | TEXT NOT NULL |
| 公共字段 | — | 同上 | 同上 |

### dispatch_rule_tag — 规则与标签关系表

| 字段 | 标题 | 说明 | 三方言类型 |
|---|---|---|---|
| `id` | 主键 | 自增主键 | 同上惯例 |
| `rule_id` | 规则 | → dispatch_rule.id | INTEGER NOT NULL |
| `tag_id` | 标签 | → dispatch_tag.id | INTEGER NOT NULL |
| 公共字段 | — | 同上；唯一约束 `(rule_id, tag_id)`（软删行除外） | 同上 |

标签随规则表单整体保存（先 upsert 标签实体、再整体替换该规则的关系行）；无引用的标签保留（可复用），不自动清理。

索引：`dispatch_rule (enabled, deleted_at, match_order)`；`dispatch_upstream_node (upstream_id)`、`(node_id)`、唯一 `(upstream_id, node_id)`（软删行除外）；`dispatch_rule_tag (rule_id)`、`(tag_id)`；唯一索引 `dispatch_node (url)`、`dispatch_upstream (name)`、`dispatch_tag (name)`（均软删行除外）。**「软删行除外」三方言实现口径**：MySQL/SQLite 经复合唯一键 `(列, deleted_at)` 借 NULL 不判重实现（活跃行重复由 adminapi 保存查重兜底，与引用完整性应用层校验惯例一致）；PG 经部分唯一索引 `WHERE deleted_at IS NULL`。

SQL 文件组（每表）：`create_table` / `create_index` / `query_list`（分页+筛选）/ `insert_returning_id` / `update` / `soft_delete` / `restore`；规则表另加 `query_active`（快照构建拉启用行）、均衡器/节点/关系表另加 `query_all_active`（Rebuild 全量拉取）；标签表加 `query_all`（下拉全量）。

**数据关系**：`dispatch_rule.upstream_id → dispatch_upstream`（多对一）；`dispatch_upstream ←→ dispatch_node` 经 `dispatch_upstream_node`（多对多，关系表带属性 weight/priority）；`dispatch_rule ←→ dispatch_tag` 经 `dispatch_rule_tag`（多对多，纯关联无属性）。删除约束：均衡器存在未软删规则引用时拒绝删除（含停用规则，理由见 D11；前端文案三要素提示）；节点存在未软删关系引用时同理；标签删除时同步软删其关系行（标签是纯管理辅助，无运行态影响）。引用完整性由应用层（adminapi）校验，不建数据库外键（项目惯例）。

**数据流转**：WebUI 表单 → adminapi（校验落库）→ `Rebuild` 拉路由四表构建运行时对象图 → 请求路径只读内存快照与探活 registry；健康状态不落库（内存单一事实源，经接口透出）——探活翻转不产生 DB 写放大，管理态（enabled/软删）与运行态（健康）职责分离；标签数据仅在列表/筛选时经 SQL join 读取，不进运行时快照。

## WebUI 设计（「路由分发」页，`#/dispatch`）

侧边栏独立顶级项（性质同「脚本」页——内容管理页；组件开关仍在 dispatch 组件详情页，组件页放「管理路由 →」链接，先例：script 组件）。

```
┌─ 状态区：[规则源: 数据库●/数据库未就绪(降级)] [规则 N 条·均衡器 M 个·节点 K 个] [⟳ 重载] ┐
│  规则按序号升序逐条匹配（域名维度可选参与），命中即停；全未命中走默认后端。
│  （DISPATCH_ENABLED 未开启时整页引导卡：开关位置 + 开启路径，豁免 toast）
├─ 视图切换：[路由规则] [负载均衡器] [上游节点] ─────────────────────────────┤
│ 路由规则视图：filterBar（类型▾ 域名 标签▾ 状态▾ 关键词）+ dataTable（服务端分页）│
│   序号│域名│路径类型│路径值│标签│均衡器│[启用开关]│操作(编辑/恢复·删除)          │
│ 负载均衡器视图：名称│策略│sticky│节点数(高优/备份)│被引用规则数│[启用]│操作      │
│ 上游节点视图：名称│地址│探活│实时健康●●◐（绿红灰三态，灰=未探活，/health 接口）│被引用│[启用]│操作 │
├─ 命中测试器（规则视图右侧常驻卡）：Host [___] Path [___] [测试]              │
│   → 命中：#12 [a.com]/api/ 前缀 → 均衡器「订单池」→ 选中节点 o2:9001        │
│   → 未命中：走默认后端                                                       │
└──────────────────────────────────────────────────────────────────────────┘
```

> 节点视图健康列附说明文字：「健康状态由探活按周期巡检得出。节点刚发生故障时，最长要等约一个探活周期（默认 20s）才会转为不健康并摘除，窗口期内新请求仍会转发到该节点并失败——属正常现象，不是网关故障。」

**表单弹层**：

1. 规则编辑：域名输入（可空，失焦转小写、拒绝端口与通配，占位"留空=任意域名"）、类型下拉（meta 枚举）、路径值（按类型切换占位与校验）、序号（1–999，默认 = 当前最大序号+10，超 999 上界时钳到 999 并提示；域名+路径`/`组合默认 999；**保存时同序号已存在其他规则则非阻断提示并列出**——同序号按 id 升序先后、先建者先匹配，防全局兜底静默吞掉域名专属规则）、均衡器下拉（显示名称+节点数）、标签多选（下拉选已有标签或回车新建实体，来源 `/admin/dispatch/tags`）、标题/备注；
2. 均衡器编辑：名称、策略下拉（round_robin/least_conn + 说明文字；**least_conn 可用性跟随 obs**——obs 组件未开启时该选项显示为不可用置灰，点击选中无效并弹 toast 说明原因："least_conn 需要响应收尾件引入缓冲路径，建议先开启观测（obs）组件以复用其缓冲路径，避免额外代价"；obs 开启时可选，**选中 least_conn 即弹出警告说明**（非阻断，可继续）：least_conn 依赖 Tail 收尾件做在途递减，dispatch 启用期命中流量走缓冲路径；已知边界——dispatch 之后的中间件中断链的请求不经过收尾回调，存在在途计数泄漏（低频统计偏差不影响转发正确性））、sticky 开关（开启展开 Cookie 名输入，默认值；**开启即弹警告说明**：会话保持对普通 HTTP 请求完全生效；浏览器 WebSocket 首次连接的握手响应拿不到粘性 Cookie（隧道直写、不经响应头），需先有过一次普通 HTTP 请求完成种值，否则 WS 连接不保证粘住原节点）、备注、**节点关系编辑器**（行列表：节点下拉（已登记节点，显示实时健康点）+ 权重数字（注记"仅 round_robin 生效"）+ 高优/备份下拉 + 行删除/添加；提交时整组保存关系）；停用/删除时展示引用计数与影响提示；
3. 节点编辑：名称、URL（`http(s)://` 校验 + 唯一性）、探活折叠区（周期/超时毫秒 + 路径；周期注记"节点故障到被自动摘除，最长存在约一个探活周期的窗口期，期间该节点的新请求仍会被转发并失败"；路径留空时显式提示"留空 = 不做探活，该节点将**始终被视为健康**——节点宕机后流量不会被自动摘除，将持续转发失败，建议生产环境配置探活路径"）、备注；删除时被引用则拒绝（文案三要素）。

**提示与降级红线**：全站 toast 唯一组件（§4.10）；DB 未就绪 503 按普通错误弹 toast；页面不设"路由功能开关"——路由数据是数据资产，有数据即生效（§4.14），唯一开关是组件级 `DISPATCH_ENABLED`。

## 接口设计（adminapi，经 RegisterPlugin 注入，仿 shield 模式）

| 端点 | 方法 | 说明 |
|---|---|---|
| `/admin/dispatch/rules` | GET / POST | 规则列表（分页+筛选，`X-Total-Count`）/ 新增（校验失败 400 带原因） |
| `/admin/dispatch/rules/update` | POST | 规则整行更新（含启停） |
| `/admin/dispatch/rules/delete` · `/restore` | POST | 规则软删 / 恢复（恢复时校验均衡器引用仍存在，悬空则拒绝并提示） |
| `/admin/dispatch/reload` | POST | 手动重载全局快照（Rebuild 四表全量快照，非规则单实体；多实例/外部改库出口） |
| `/admin/dispatch/rules/match-test` | POST | `{host,path}` → 命中结果（规则摘要、均衡器、所选节点或兜底链位置），只读无副作用（不推进轮询游标、不计在途） |
| `/admin/dispatch/rules/meta` | GET | 枚举字典（路径类型/策略/优先级说明/默认序号建议） |
| `/admin/dispatch/tags` | GET / POST | 标签实体管理：全量列表（供下拉与筛选）/ 新建 |
| `/admin/dispatch/tags/update` · `/delete` | POST | 标签重命名 / 软删（同步软删其关系行）；**不做恢复**——软删即隐藏，需要时重建同名实体（恢复会与新同名实体唯一冲突）；标签变更不触发 Rebuild |
| `/admin/dispatch/upstreams` | GET / POST | 均衡器列表（含节点关系、被引用规则数）/ 新增（含关系组） |
| `/admin/dispatch/upstreams/update` · `/delete` · `/restore` | POST | 更新（含关系组整体替换）/ 软删（有未删规则引用则 409）/ 恢复（恢复前查重均衡器 name 活跃行唯一性，冲突拒绝并按文案三要素提示，与规则 restore 口径一致——防 PG 部分唯一索引裸 DB 错） |
| `/admin/dispatch/nodes` | GET / POST | 节点列表（含被引用数、实时健康）/ 新增 |
| `/admin/dispatch/nodes/update` · `/delete` · `/restore` | POST | 更新 / 软删（有未删关系引用则 409）/ 恢复（恢复前查重节点 url 活跃行唯一性，冲突拒绝并按文案三要素提示，与规则 restore 口径一致——防 PG 部分唯一索引裸 DB 错） |
| `/admin/dispatch/health` | GET | 节点实时健康快照 + 在途计数（读内存 registry，供节点视图与均衡器编辑器） |

路由数据四表（规则/均衡器/节点/关系）的全部写端点成功后自动触发 `Rebuild()`（保存即热更）；标签端点不触发（S6，标签不进运行时快照）。

## 会话保持设计（sticky cookie，行业正交模型）

粘性是均衡器**特性开关**而非策略枚举，可与 round_robin / least_conn 任意组合（Envoy `lb_policy` + session cookie、Traefik `loadBalancer.sticky.cookie`、HAProxy `balance` + `cookie insert` 同构）：

- **路由**：请求携带本均衡器 Cookie 名的值（节点 id），且该节点在**当前均衡器关系内且健康** → 直路由；否则按策略选点。
- **种值**：选点经策略产生（非 Cookie 直路由）时，`Handle` 内 `W.Header().Add("Set-Cookie", "name=<node_id>; Path=/; HttpOnly; SameSite=Lax")`——Middle 槽位设头合法（未写响应体），Adapter 写回时携带；会话级（无 Max-Age），浏览器关闭即失效，贴合"无共享 session 存储、粘性只需会话级"的场景。
- **失效**：节点被移出关系、软删、停用、探活判死 → Cookie 失效回落策略选点并重种。
- **不跨均衡器**：Cookie 值仅在本均衡器关系内解释；同域名下多个均衡器用不同 Cookie 名互不干扰（默认名相同则后种覆盖——多均衡器共用同一后端池时可配同名实现跨规则粘性，属用户自由组合，不特殊处理）。

## 健康检查中心设计（供应商-消费者）

- **生产者**：探活任务管理器持有节点探活 goroutine 池；任务集 = "被启用均衡器经有效关系引用的启用节点"去重集（未被引用的登记节点不探活，省无谓探测）；参数读节点表，启动即探一次，随后按周期；判据 2xx/3xx 健康。
- **消费者**：均衡器选点（读 registry 过滤健康集）、规则命中测试器、WebUI 健康列、`/admin/dispatch/health`——全部读同一 registry（节点 id → atomic 状态 + 在途计数）。
- **生命周期**：任务集随 Rebuild 差量增减（新增启探、移除停探并等待退出）；组件 Stop 全量排空；探活 HTTP 客户端复用（连接池），超时独立于转发链。

## 行业实践依据

| 实践来源 | 对本设计的支撑 |
|---|---|
| NGINX `upstream` 块 + `server` 指令（`weight`/`backup`/`max_fails`/`fail_timeout`） | 均衡器独立实体、关系表字段（weight/priority=backup）同构；`max_fails`/`fail_timeout` 为被动健康检查，本期不做（见决策 D9） |
| Envoy cluster/endpoint 分离、主动 health check 与被动 outlier detection 分离、session affinity（cookie 优先于策略、TTL） | 节点资产与均衡器关系分离；健康检查中心与转发解耦；sticky cookie 直路由优先语义（TTL 简化为会话级） |
| Kong / APISIX（路由、upstream、节点全部存控制面数据库/etcd，数据面只读快照） | "配置即数据"：路由全部 DB 承载、WebUI 管理、`.env` 只留开关；运行期内存快照热更同构 |
| Traefik router 规则 `Host(...) && PathPrefix(...)`、`loadBalancer.sticky.cookie` | 域名作为规则的可选正交维度（host 空即退化纯路径规则）；sticky 开关正交于策略 |
| HAProxy `balance leastconn` + `cookie insert` | least_conn 策略与 Cookie 粘性各自独立配置的组合范式 |
| Caddy `lb_policy`（round_robin/least_conn/cookie/ip_hash…） | 策略枚举参考；ip_hash 因 IP 不稳定与出口聚集缺陷被 sticky cookie 取代（决策 D6） |

## 决策表（现行有效）

| # | 决策点 | 结论 | 出处 |
|---|---|---|---|
| D1 | 规则模型 | 扁平结构化规则列表，序号 1–999 升序命中即停，无隐式优先级；不引入层级模型 | 2026-09-19 用户拍板，v3 沿用 |
| D2 | 域名维度 | 规则级**可选字段**（空=任意域名，非空=精确匹配小写归一剥端口）；域名默认均衡器 = 「域名+路径`/`+大序号」普通规则；不做通配域名 | 2026-09-21 用户拍板（分组形态）+ 设计定稿 |
| D3 | 数据模型 | 六表分层：规则 / 均衡器 / 节点 / 节点-均衡器关系（weight/priority）/ 标签 / 规则-标签关系；节点-均衡器关系表不建 max_fails/fail_timeout 字段 | 2026-09-21 用户拍板（两张表 + 均衡器自身属性；标签拆表同日拍板） |
| D4 | 规则存储 | **数据库唯一承载**，不存文件；`.env` 只留 `DISPATCH_ENABLED` 总开关；DB 不可用 = 空规则全走默认 upstream（可接受降级，状态透出） | 2026-09-21 用户拍板 |
| D5 | 旧 DSL | `DISPATCH_RULES` 配置项与 DSL 解析代码**整体删除**——无存量数据、无迁移、无兼容；`:param`/`*` 段匹配语义保留进 path_type=3 | 2026-09-21 用户拍板 |
| D6 | 会话保持 | sticky cookie（网关自种会话 Cookie）替代 ip_hash；**ip_hash/chash 不保留兼容**，直接切换 | 2026-09-21 用户拍板 |
| D7 | 策略枚举 | round_robin（平滑加权，权重默认 1 即纯轮询）/ least_conn；sticky 为均衡器独立开关与策略正交组合 | 2026-09-21 用户拍板 + 行业正交模型 |
| D8 | 健康检查 | 节点级中心化探活（每节点一份、全局去重、单一事实源、内存承载不落库）；探活配置在节点表 | 2026-09-21 用户拍板（健康检查中心 + 供应商-消费者） |
| D9 | 被动健康检查 | max_fails/fail_timeout（按真实请求失败计数摘除）本期不做，需要时随关系表加列演进；请求级 failover（转发失败换下一健康节点重试，NGINX `proxy_next_upstream` 同构）亦不在本期——探活窗口期（默认 20s）内死节点流量直接透传失败，属显式接受边界 | 2026-09-21 用户拍板；请求级 failover 边界 2026-09-21 设计评审显式化 |
| D10 | 分组形态 | 多条单值规则引用同一均衡器（标签筛选辅助管理），不做"规则内 URI 列表"组合值 | 2026-09-21 用户拍板 |
| D11 | 引用删除保护 | 均衡器被**未软删（含停用）**规则引用、节点被未软删关系引用时，删除拒绝并提示；规则恢复/启用时校验其均衡器仍存在，悬空则拒绝（管理约束，消灭悬空引用；停用行也是配置资产，删除其引用物会造成启用时悬空） | 设计推荐，随关口确认 |
| D12 | 字段命名 | 规则表匹配序号字段名 `match_order`、域名维度字段名 `domain`（语义即名，避免与 HTTP Host 头混淆） | 2026-09-21 用户拍板 |
| D13 | 前缀命中自身 | 前缀匹配段对齐且命中自身：`/api` 命中 `/api` 与 `/api/x`、不匹配 `/apix`；`/` 命中一切路径含 `/` 本身（NGINX `location` 前缀同款语义） | 2026-09-21 定稿审核拍板 |

## 验收标准（怎么算做完）

1. `go test ./...`、`go vet ./...` 全绿（含 `-race`）；匹配/sticky/least_conn/健康检查中心语义表驱动测试与 benchmark（100/1000 规则两档、≤10µs 门槛，口径见 M 表）结果留档 STEP 回填区。
2. 实操复现：登记 3 节点 → 建均衡器 A（round_robin+sticky）与 B（least_conn）引用同批节点 → 建规则（域名+路径、纯路径、域名默认兜底各至少一条）→ 浏览器实看截图留证 → `curl -H "Host: a.com"` 实请求验证：分流正确、sticky Cookie 种植与直路由生效、域名兜底链正确、停一节点探活摘除后流量切走。
3. 命中测试器结果与实请求行为一致：规则命中与均衡器判定（命中/未命中、命中哪条规则、引用哪个均衡器）必须一致；节点选点为动态参考值（round_robin/least_conn 选点随运行态变化），不要求一致。
4. 降级与恢复验证：DB 不可用 → 组件状态透出、全部请求走默认 upstream、无 5xx 崩溃；DB 就绪后经后台按固定间隔重试自动完成首次构建、成功即停（非常驻轮询），规则生效无需人工点「重载」（S6 启动期恢复）。
5. 文档同步：`docs/DATA_DICT.md`（六表+三枚举）、三方言脚本、README、`docs/CONFIGURATION.md`（DISPATCH_RULES 删除）、`docs/COMPONENTS.md` §3.2、`docs/webui/pages.md`（路由分发页章节）、接口文档、配置项注册清单。
6. 明确不做：通配域名、正则匹配、Header/查询参数匹配维度、被动健康检查（max_fails/fail_timeout）、请求级 failover（转发失败换节点重试）、加权 least_conn、sticky TTL、多实例自动同步、规则版本历史、路由数据文件存储。

## 已知边界

- 请求级 failover 不做（D9）：探活判死前窗口期（默认 20s）内被选中死节点的请求直接透传失败，不换节点重试；后续随被动健康检查一并演进。

- least_conn 计数在「选点后、后继中间件中断链」路径存在泄漏（低频统计偏差，不影响转发正确性）；chain 补中断路径收尾回调后消除，本期不做。
- WebSocket 隧道在途计数全程占用至连接结束（S4，与 NGINX least_conn 计入活跃连接一致）；sticky 读取与直路由对升级请求有效，但 **101 隧道响应经 `resp.Write(clientConn)` 直写劫持连接、绕过 `w.Header()`，网关新种的 Set-Cookie 不会出现在 101 握手响应上**——客户端首次 WS 连接拿不到粘性 Cookie（通常由先前普通 HTTP 请求种好），属可接受边界。
- 多实例部署下他实例/他进程改库不自动同步，经「重载」按钮或 POST `/admin/dispatch/reload` 手动触发（重载四表全量快照）。
- sticky Cookie 明文节点 id：内网网关场景可接受（HAProxy SERVERID 同款），不引入签名加密。
