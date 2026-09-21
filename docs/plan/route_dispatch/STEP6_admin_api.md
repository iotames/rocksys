# STEP6：adminapi 管理端点

状态：已实施

## 目标

落地路由分发管理端点（仿 shield admin 模式，`adminapi.RegisterPlugin` 注入）：rules（CRUD/update/delete/restore/match-test/meta）、reload（全局四表全量快照）、upstreams（CRUD/关系组整体替换/引用删除保护 409/restore 前查重 name 活跃行唯一性）、nodes（CRUD/引用保护/restore 前查重 url 活跃行唯一性）、tags（实体 CRUD、软删同步软删 dispatch_rule_tag 关系行、不触发 Rebuild、无 restore）、health（registry 快照 + 在途计数）。路由数据四表写端点成功后自动触发 `Rebuild()`（保存即热更）；match-test 只读无副作用（不推进轮询游标、不计在途），返回契约：命中结果含规则摘要、均衡器、所选节点或兜底链位置，所选节点为动态参考值、不要求与实请求一致。

## 改动文件清单

- 新增 `plugins/dispatch/admin.go`：端点常量 + handler 组 + 校验（表驱动风格仿 `plugins/shield/admin.go`）
- 新增 `plugins/dispatch/admin_test.go`：表驱动测试（内存/临时 DB）
- 修改 `cmd/rocksys/main.go`：`adminapi.RegisterPlugin(dispatch.NewAdminHandler(...))` 装配（一处）
- 端点清单（全部路径前缀 `/admin/dispatch/`）：
  - `rules` GET/POST、`rules/update`、`rules/delete`、`rules/restore`、`rules/match-test`、`rules/meta`（GET 枚举字典：路径类型/策略/优先级说明/默认序号建议）
  - `reload` POST
  - `upstreams` GET/POST、`upstreams/update`、`upstreams/delete`、`upstreams/restore`
  - `nodes` GET/POST、`nodes/update`、`nodes/delete`、`nodes/restore`
  - `tags` GET/POST、`tags/update`、`tags/delete`
  - `health` GET

## 实施步骤（完成一项立即勾选保存；粒度到不可再分的动作）

- [x] admin.go：DB 行存取层（消费 STEP1 SQL 文件组；列表分页 + 筛选 + `X-Total-Count`，仿 ip_blacklist query_list）
- [x] rules 端点：新增/更新校验（match_order 1–999、path_value 以 `/` 开头且按类型合法、domain 保存时归一落库并拒绝端口与通配、upstream_id 引用存在、同序号非阻断提示并列出）；软删/恢复（恢复时校验均衡器引用仍存在，悬空拒绝）；meta 枚举字典
- [x] rules/match-test：读当前快照执行匹配 + 选点（只读：不动游标不计在途），返回规则摘要/均衡器/所选节点或兜底链位置
- [x] upstreams 端点：CRUD + 节点关系组整体替换（先 upsert 关系行：软删旧行插新行）+ 引用删除保护（被未软删规则引用 → 409 文案三要素）+ restore 前查重 name 活跃行唯一性（冲突拒绝）
- [x] nodes 端点：CRUD + URL 唯一性与 `http(s)://` 校验 + 引用删除保护（被未软删关系引用 → 409）+ restore 前查重 url 活跃行唯一性
- [x] tags 端点：实体 CRUD（name 归一小写、全局唯一）；delete 同步软删其 dispatch_rule_tag 关系行；不触发 Rebuild；无 restore
- [x] health 端点：registry 三态 + 在途计数快照
- [x] reload 端点：调 `Rebuild()`（多实例/外部改库出口）
- [x] 四表写端点成功后触发 `Rebuild()`；Rebuild 失败报错回前端（保留旧快照）
- [x] cmd/rocksys/main.go 装配 RegisterPlugin
- [x] 表驱动测试：CRUD 回路 / 校验拒绝 / 引用保护（含规则恢复时均衡器悬空拒绝）/ restore 活跃行查重冲突拒绝（均衡器 name、节点 url）/ match-test 前后轮询游标与在途计数不变 / 命中测试一致性（与快照行为对照）/ 标签随规则表单整体保存（先 upsert 标签实体再整体替换关系行）/ 写端点触发 Rebuild 断言

## 验证

- 接手核实命令（拆步时预写；无副作用、可重复执行；断点后一条命令自证已完成部分完好）：
  `go test ./plugins/dispatch/ -run 'TestAdmin' -count=1`
- 本步完整验证（勾选＝该项已真实执行且通过，凭意图不得勾选；按需含受影响面的全量回归）：
  - [x] `go test ./... -count=1` 全绿（集成用例按环境变量门控跳过属正常）
  - [x] `go vet ./...` 通过
  - [x] `go build -tags dev -o bin/rocksys ./cmd/rocksys` 后 `cd bin && ./rocksys` 启动，curl 实测端点回路：新增节点 → 建均衡器 → 建规则 → GET 列表 → match-test 命中（截图或响应体留证）

## 完成标准

- DESIGN「接口设计」表全部端点可用，行为与口径一致（409 保护、restore 查重、match-test 无副作用、四表写触发 Rebuild、标签不触发）
- 校验失败 400 带原因、文案三要素（发生了什么 + 为什么 + 下一步怎么办）
- 表驱动测试覆盖 IMPL 切片 6 验证列全项

## 实施回填区

### 产物锚点清单（拆步时预写，实施中随手更正；改名同步更新）
- 新增文件：`plugins/dispatch/admin.go`、`admin_test.go`
- 修改文件：`cmd/rocksys/main.go`（RegisterPlugin 装配一处）
- 新增端点：上列 19 个（rules 6 + reload 1 + upstreams 4 + nodes 4 + tags 3 + health 1，以实际清单为准）
- 新增测试：TestAdmin* 用例组

### 偏差与现场记录

1. **STEP2 已落 `DBSource` 存在两处潜藏缺陷，本步修复（未改语义，属缺陷修复）**：
   ① 脚本名缺 `.sql` 后缀（`GetMany("dispatch_rule_query_active", ...)`）——STEP5 只用 stubSource 注入未暴露，真实 DB 路径全断；② `db.DB` 不做 `{table}` 占位符替换（mq/shield 均由消费方替换），且 easydb 结构体扫描要求逐列 `db` tag 而 model.go 行结构无 tag——改为 map 行手工映射到 RuleRow 等（`DBSource.loadRows` + `dbRowInt64/dbRowString`）。STEP5 既有测试（stubSource）零改动全绿。
2. **PG 方言适配**：STEP1 PG 脚本关键词占位符复用（`$2` 出现 4 次仅传 1 参），规则列表/计数参数个数随方言（10/8 与 8/6）；原生校验查询（存在性/查重/引用计数）经 `sqlPh(i)` 按驱动生成 `?`/`$n`。sqlite 单测 + PG 实测双向验证。
3. **均衡器 delete 级联软删关系行、restore 按批次（deleted_at 精确相等）级联恢复**：设计未明示，但不级联则 Rebuild 校验「关系引用不存在的均衡器」必失败——属端点行为补全，关系表仍无单行 update/restore 脚本（级联恢复用可移植原生 UPDATE）。
4. **`sticky_enabled` 请求缺省值修正为停用（0，建表默认同源）**：`boolToInt` 缺省启用仅适用于 enabled 类字段，sticky 单独 `stickyToInt`。
5. **同序号非阻断提示**：以响应体 `warnings` 数组回传（HTTP 仍 200），前端 toast/表单侧自行展示。
6. **本步验证环境**：`bin/.env` 配置的是 PG——curl 实测回路在 PG 上完成（建节点→建均衡器→建规则→列表（X-Total-Count）→match-test 命中/未命中→引用保护 409→域名带端口 400→delete/restore 级联回路→reload ready:true），验证后测试数据已软删清理。表驱动测试走临时 sqlite。

### 端点落点清单（19 个，实际落点均在 `plugins/dispatch/admin.go`，路径常量前缀 `PathDispatch*`）

| # | 端点 | handler |
|---|---|---|
| 1 | GET/POST `/admin/dispatch/rules` | `Rules`（`listRules`/`addRule`） |
| 2 | POST `/admin/dispatch/rules/update` | `RulesUpdate` → `updateRule`（含标签整组替换） |
| 3 | POST `/admin/dispatch/rules/delete` | `RulesDelete`（软删，触发 Rebuild） |
| 4 | POST `/admin/dispatch/rules/restore` | `RulesRestore`（均衡器悬空拒绝 400） |
| 5 | POST `/admin/dispatch/rules/match-test` | `RulesMatchTest` → `matchTest`（只读：`pickHealthy` 不经 `count()`，游标不动、在途不计） |
| 6 | GET `/admin/dispatch/rules/meta` | `RulesMeta`（路径类型/策略/优先级/序号建议/domain 规则） |
| 7 | POST `/admin/dispatch/reload` | `Reload`（Rebuild，失败 500 报错回前端保留旧快照） |
| 8 | GET/POST `/admin/dispatch/upstreams` | `Upstreams`（`listUpstreams` 附关系组+rule_refs+实时健康 / `addUpstreams` 含关系组） |
| 9 | POST `/admin/dispatch/upstreams/update` | `UpstreamsUpdate`（关系组整体替换：软删旧行插新行） |
| 10 | POST `/admin/dispatch/upstreams/delete` | `UpstreamsDelete`（规则引用 409；级联软删关系行） |
| 11 | POST `/admin/dispatch/upstreams/restore` | `UpstreamsRestore`（name 活跃查重 409；按批次级联恢复关系行） |
| 12 | GET/POST `/admin/dispatch/nodes` | `Nodes`（`listNodes` 附 rel_refs+实时健康 / `addNode`） |
| 13 | POST `/admin/dispatch/nodes/update` | `NodesUpdate`（URL http(s):// 校验+活跃行查重） |
| 14 | POST `/admin/dispatch/nodes/delete` | `NodesDelete`（关系引用 409） |
| 15 | POST `/admin/dispatch/nodes/restore` | `NodesRestore`（url 活跃查重 409） |
| 16 | GET/POST `/admin/dispatch/tags` | `Tags`（query_all 全量 / `addTag`，name 归一小写+活跃唯一 409） |
| 17 | POST `/admin/dispatch/tags/update` | `TagsUpdate`（重命名，查重 409；不触发 Rebuild） |
| 18 | POST `/admin/dispatch/tags/delete` | `TagsDelete`（同步软删 dispatch_rule_tag 关系行；无 restore；不触发 Rebuild） |
| 19 | GET `/admin/dispatch/health` | `Health`（活跃节点 × registry 三态 ok/bad/unknown + inflight + dispatch.ready） |

装配：`cmd/rocksys/main.go` 装配一处——`dispatchMain` 变量捕获主件后 `dispatch.NewAdminHandler(dispatchMain, dataDB)`，19 端点循环 `RegisterPlugin`。

### 测试落点（`plugins/dispatch/admin_test.go`，TestAdmin* 共 10 例）

- `TestAdminNodeCRUD`：回路 + URL 校验矩阵 + url 查重（新增 400 / restore 409）
- `TestAdminUpstreamCRUD`：回路 + 关系组整体替换 + name 查重 + 无关系 Rebuild 失败 500（旧快照文案）
- `TestAdminUpstreamDeleteRefProtection`：停用规则引用亦 409（D11）
- `TestAdminRuleCRUD`：domain 归一落库 + 同序号 warnings + 校验拒绝矩阵 + 悬空恢复 400 + 恢复回路
- `TestAdminMatchTestReadOnly`：命中/未命中 + 双节点游标零推进序列断言 + 在途恒 0 + 与快照 `Match` 一致性
- `TestAdminTags` / `TestAdminRuleTagsWholeSave`：标签实体 CRUD + 随规则整组保存（upsert 实体→关系替换）+ 不触发 Rebuild
- `TestAdminWriteTriggersRebuild`：写端点后快照即含新规则 + reload ready
- `TestAdminMetaAndHealth` / `TestAdminPostOnly`
