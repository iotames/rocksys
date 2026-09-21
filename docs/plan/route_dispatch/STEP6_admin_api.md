# STEP6：adminapi 管理端点

状态：待实施

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

- [ ] admin.go：DB 行存取层（消费 STEP1 SQL 文件组；列表分页 + 筛选 + `X-Total-Count`，仿 ip_blacklist query_list）
- [ ] rules 端点：新增/更新校验（match_order 1–999、path_value 以 `/` 开头且按类型合法、domain 保存时归一落库并拒绝端口与通配、upstream_id 引用存在、同序号非阻断提示并列出）；软删/恢复（恢复时校验均衡器引用仍存在，悬空拒绝）；meta 枚举字典
- [ ] rules/match-test：读当前快照执行匹配 + 选点（只读：不动游标不计在途），返回规则摘要/均衡器/所选节点或兜底链位置
- [ ] upstreams 端点：CRUD + 节点关系组整体替换（先 upsert 关系行：软删旧行插新行）+ 引用删除保护（被未软删规则引用 → 409 文案三要素）+ restore 前查重 name 活跃行唯一性（冲突拒绝）
- [ ] nodes 端点：CRUD + URL 唯一性与 `http(s)://` 校验 + 引用删除保护（被未软删关系引用 → 409）+ restore 前查重 url 活跃行唯一性
- [ ] tags 端点：实体 CRUD（name 归一小写、全局唯一）；delete 同步软删其 dispatch_rule_tag 关系行；不触发 Rebuild；无 restore
- [ ] health 端点：registry 三态 + 在途计数快照
- [ ] reload 端点：调 `Rebuild()`（多实例/外部改库出口）
- [ ] 四表写端点成功后触发 `Rebuild()`；Rebuild 失败报错回前端（保留旧快照）
- [ ] cmd/rocksys/main.go 装配 RegisterPlugin
- [ ] 表驱动测试：CRUD 回路 / 校验拒绝 / 引用保护（含规则恢复时均衡器悬空拒绝）/ restore 活跃行查重冲突拒绝（均衡器 name、节点 url）/ match-test 前后轮询游标与在途计数不变 / 命中测试一致性（与快照行为对照）/ 标签随规则表单整体保存（先 upsert 标签实体再整体替换关系行）/ 写端点触发 Rebuild 断言

## 验证

- 接手核实命令（拆步时预写；无副作用、可重复执行；断点后一条命令自证已完成部分完好）：
  `go test ./plugins/dispatch/ -run 'TestAdmin' -count=1`
- 本步完整验证（勾选＝该项已真实执行且通过，凭意图不得勾选；按需含受影响面的全量回归）：
  - [ ] `go test ./... -count=1` 全绿（集成用例按环境变量门控跳过属正常）
  - [ ] `go vet ./...` 通过
  - [ ] `go build -tags dev -o bin/rocksys ./cmd/rocksys` 后 `cd bin && ./rocksys` 启动，curl 实测端点回路：新增节点 → 建均衡器 → 建规则 → GET 列表 → match-test 命中（截图或响应体留证）

## 完成标准

- DESIGN「接口设计」表全部端点可用，行为与口径一致（409 保护、restore 查重、match-test 无副作用、四表写触发 Rebuild、标签不触发）
- 校验失败 400 带原因、文案三要素（发生了什么 + 为什么 + 下一步怎么办）
- 表驱动测试覆盖 IMPL 切片 6 验证列全项

## 实施回填区

### 产物锚点清单（拆步时预写，实施中随手更正；改名同步更新）
- 新增文件：`plugins/dispatch/admin.go`、`admin_test.go`
- 修改文件：`cmd/rocksys/main.go`（RegisterPlugin 装配一处）
- 新增端点：上列 19 个（rules 7 + reload 1 + upstreams 4 + nodes 4 + tags 3... 以实际清单为准）
- 新增测试：TestAdmin* 用例组

### 偏差与现场记录
- <设计与现实的偏差、试过放弃的方案、阻塞原因——先写这里再继续动手>
