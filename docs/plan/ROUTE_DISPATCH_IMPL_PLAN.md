# 路由分发实施指导（ROUTE_DISPATCH IMPL PLAN）

> 层级定位：本文是**实施指导层（下层）**，受 `ROUTE_DISPATCH_DESIGN_PLAN.md`（宏观设计层，OGSM，最权威最上层）指导，把 S 策略翻译为可执行、可验证的实施切片。冲突时上层优先。
> 授权与冻结：本文**不需单独请示**——设计定稿关口对象唯一为 DESIGN_PLAN，本文随其定稿自主细化；**定稿即冻结**，执行状态一律不回写本文（现场记录只落 STEP 回填区，状态唯一权威 = 总纲）。
> 状态：**已随 DESIGN_PLAN 定稿（2026-09-21）**；定稿即冻结，可据此建总纲与 STEP、动代码。

## 1. 范围（Scope）

- **做**：六表数据层（核心路由四表 + 标签两表，G3）+ 均衡器实体与策略（round_robin/least_conn，G2）+ sticky cookie 会话保持（S3）+ 健康检查中心（G4）+ 规则匹配引擎（domain 可选维度 + 三类型 + 序号命中即停，G1/S1）+ adminapi 三组端点与标签端点、热更装配（S6）+ WebUI 路由分发页（G5）+ DSL 与 `DISPATCH_RULES` 整体移除（G6）。
- **不做**：上层"明确不做"清单全项（通配域名、正则、Header/QueryParam 维度、被动健康检查、请求级 failover（转发失败换节点重试）、加权 least_conn、sticky TTL、多实例自动同步、版本历史、文件存储）；不新增第三方依赖。

## 2. 前置与依赖

- 开工条件：DESIGN_PLAN 过定稿关口，并在关口确认 git 破例授权与否（宪法 §3.9）。
- 过关后以本文为蓝本建正式总纲（状态总表 + 断点续传协议）与 `docs/plan/route_dispatch/STEPn_*.md`，按宪法 §2.3 自主进行。
- 依赖顺序：数据层脚本先行（下游模型与端点依赖表存在）→ 引擎与中心（无 DB 依赖，纯内存可测）→ 装配 → 端点 → WebUI（依赖端点契约冻结）。

## 3. 实施切片（建议顺序，被依赖者先行）

| # | 切片 | 内容（上层策略映射） | 验证 |
|---|---|---|---|
| 1 | 数据层六表 | 三方言 `dispatch_rule/upstream/node/upstream_node/tag/rule_tag` SQL 文件组 + `docs/DATA_DICT.md` 六表与三枚举（path_type/algo/priority）章节 | `go test ./internal/db/...`；WebUI 数据库页表结构检查零差异（截图） |
| 2 | 领域模型与选点引擎 | 运行时对象图（Rule/Upstream/Node/关系展开）——**旁路新建文件/类型**，旧 DSL 链（`dispatch.go` Handle→`parseRules`）保持可编译至切片 5 整体移除；策略选点（round_robin 复用 + least_conn 在途计数——选中 +1 及 sticky 直路由计入在本片落地，递减挂 Tail 收尾件归切片 4；本片先以注入 registry 桩完成选点分布与 sticky 失效回落测试——sticky 失效回落同样依赖健康态判定，切片 4 真实 registry 就绪后替换该桩并回归本片单测）、sticky cookie（直路由/种值 `Add`/失效回落）、加载期校验（引用存在、节点 URL 合法、值与类型匹配、序号范围、domain 构建期归一放行——转小写不拒绝） | 选点与 sticky 表驱动单测（`-race`）：策略分布/粘性/失效/不跨均衡器误粘/Secure 属性跟随协议/优先级回落 |
| 3 | 匹配引擎与快照 | `(match_order, id)` 稳定序数组 + Host 归一化（剥端口/IPv6/小写）+ 三类型路径匹配（前缀=段对齐且命中自身——`/api` 命中 `/api` 与 `/api/x`，`/` 命中一切含 `/` 本身（D13）、精确=全等不归一尾斜杠、模式复用 `:param`/`*` 段语义与参数注入；路径匹配大小写敏感、域名匹配不敏感——S1）+ host 空=任意、非空=精确 | 匹配语义表驱动测试（M 表口径全项） |
| 4 | 健康检查中心 | 节点级探活任务管理器（任务集=被引用启用节点去重集、差量增减、启动即探、2xx/3xx 判据；registry 记录集=探活任务集 ∪ 免探活登记集（hc_path 为空的被引用节点登记为健康态、显绿），任务差量移除即清记录转灰；跨热更保序仅指任务仍保留的节点）+ 内存 registry（健康态三态+在途计数）+ **Tail 收尾件**（独立 Name 挂 Tail、Handle 直通、OnResponse 按 DF 节点 id 递减——双中间件机制见上层 S4：Adapter 只调用 Tail 槽位钩子，Middle 主件的 OnResponse 不被调用；链中断与无 target 放行路径 Tail 亦不执行——后者无选中节点、无递减需求，覆盖面口径见上层） | 单测：多均衡器引用同节点只探一份 / Rebuild 差量 / goroutine 无泄漏（`-race`）/ 收尾件递减跨 Rebuild 不失配 |
| 5 | 装配与热更 | `Start` 改造：DB 可用拉四表构建（不可用=空表快照+状态透出+后台按固定间隔重试直至首次构建成功、成功即停（非常驻轮询））；`Rebuild()` 供端点触发（内部持互斥锁串行，S6）；装配注册**双中间件**（Middle 主件 + Tail 收尾件，autoEnableMap 两个名称同键 `DISPATCH_ENABLED` 联动启停；**收尾件与主件共用运行态，其 Start/Stop 为 no-op**——生命周期由主件统一驱动，防双份 Rebuild/探活）；删除 `DISPATCH_RULES` 配置项与全部 DSL 解析代码（`parseRules` 解析链、Radix Tree、chash）及其旧语义测试，`.env` 只留 `DISPATCH_ENABLED` | 降级集成用例（DB 不可用全走默认 upstream；恢复后自动重建）+ 热更重建用例 + `go test -race`；删除后全量测试全绿 |
| 6 | adminapi 端点 | rules（CRUD/match-test/meta）+ 独立 reload 端点（/admin/dispatch/reload，全局四表全量快照）+ upstreams（CRUD/关系组整体替换/引用删除保护 409/**restore 前查重均衡器 name 活跃行唯一性**）+ nodes（CRUD/引用保护/**restore 前查重节点 url 活跃行唯一性**，restore 冲突拒绝并按文案三要素提示——防 PG 部分唯一索引裸 DB 错，与规则 restore 口径一致）+ match-test **只读无副作用（不推进轮询游标、不计在途）**，返回契约转写上层：命中结果含规则摘要、均衡器、所选节点或兜底链位置，所选节点为动态参考值、不要求与实请求一致（上层验收标准 3） + tags（实体 CRUD，软删同步软删其 dispatch_rule_tag 关系行——防残留关系行与重建同名实体的唯一约束冲突；不触发 Rebuild；无 restore）+ health 快照；路由数据四表写端点触发 Rebuild（标签端点除外） | adminapi 表驱动测试：CRUD 回路/校验拒绝/引用保护（含规则恢复时均衡器悬空拒绝）/**restore 活跃行查重冲突拒绝（均衡器 name、节点 url）**/match-test 前后轮询游标与在途计数不变/命中测试一致性/标签随规则表单整体保存 |
| 7 | WebUI 页面 | 路由分发页三视图（规则/均衡器/节点）+ 表单弹层（含序号默认值超界钳 999 与同序号非阻断提示、节点关系编辑器、权重"仅 round_robin 生效"注记、均衡器表单选中 least_conn 弹非阻断警告（可继续：least_conn 在途递减依赖 Tail 收尾件、dispatch 启用期命中流量走缓冲路径，该代价与策略选择无关；含 dispatch 后中间件中断链的在途计数泄漏边界，DESIGN 均衡器编辑表单口径）、sticky 开关开启弹非阻断警告（浏览器 WS 首连握手响应拿不到种值 Cookie 边界，DESIGN 表单口径））+ 命中测试器 + 引导态与降级文案 + 健康三态（绿红灰，灰=未探活）+ pages.md 章节 | 浏览器实看 + 截图留证（三视图/弹层/测试器/引导态/错误 toast） |
| 8 | 性能与终验 | benchmark（100/1000 规则两档、单次匹配 ≤10µs 门槛）留档；全量测试/vet/生产构建；文档同步清单逐项核对（含 DISPATCH_RULES 删除的配置文档清理）；实请求终验：登记 3 节点+均衡器（round_robin+sticky 与 least_conn）+三类规则（域名+路径、纯路径、域名兜底）后，`curl -H "Host: a.com"` 实请求验证——分流正确、sticky Cookie 种植与直路由生效、域名兜底链正确、停一节点探活摘除后流量切走（上层验收标准 2） | `go test -bench` 结果 + 终验清单全绿 |

## 4. 红线摘录（执行期必守，来源见上层决策表与仓库法）

- 请求路径零锁零阻塞：快照经 `atomic.Value` 原子替换；探活状态经节点级 atomic registry 读；健康状态不落库。
- DB 为唯一规则源（D4）：不读 `.env` 规则、不存文件；DB 不可用 = 空表快照全走默认 upstream，不得 panic。
- 停用语义 fail-closed：停用均衡器/节点不把规则剔除出快照（不吞规则），命中后无可用节点即 503；禁止构建期按引用启停状态过滤规则（防流量静默落到后续规则或默认后端，S1）。
- 引用完整性：均衡器被未软删（含停用）规则引用、节点被未软删关系引用时删除拒绝（D11）；规则启停/恢复同样校验引用存在；restore 活跃行查重——均衡器 restore 前查 name、节点 restore 前查 url 的活跃行唯一性，冲突拒绝并按文案三要素提示（防 PG 部分唯一索引裸 DB 错）；Rebuild 校验失败保留旧快照服务并报错。
- 续链契约：Middle 主件**返回 true（继续转发）时只设响应头、禁止 Write/WriteHeader**（sticky 种 Cookie 依赖此契约，写体会在 Forward 时二次写头 panic）；命中但无可用节点时写 503 并**返回 false 中断链**（现状语义，属已自行响应）。响应期收尾必须经独立 Tail 收尾件——Adapter 只调用 Tail 槽位的 ResponseHook，Middle 中间件实现的 OnResponse 不会被调用（双中间件机制见上层 S4）。
- 数据层红线：六表三方言脚本 + DATA_DICT + Go 枚举三处同步，缺一即违规；公共字段 UTC 惯例与既有表一致。
- WebUI 红线：toast 唯一组件（§4.10）、服务端报错必弹 error toast、DB 未就绪 503 属普通错误非引导态、数据资产不设功能开关（§4.14）。
- 纯标准库，不新增第三方依赖。

## 5. 风险与回退

- **构建失败保旧快照**：`Rebuild` 校验失败保留旧快照服务并向前端报错，转发不中断（S6）。
- **性能回退**：切片 8 benchmark 为硬门槛，不达标回退做加载期按 host 有无/首段分桶（语义不变）并回填 STEP 分析。
- **least_conn 计数泄漏**：已知边界（后继中间件中断链不经过响应钩子），监控递减/选中比率告警辅助，不为本期阻塞项。
- **探活任务生命周期**：差量增减与排空必须有泄漏测试兜底（切片 4），否则热更高频场景 goroutine 累积。
