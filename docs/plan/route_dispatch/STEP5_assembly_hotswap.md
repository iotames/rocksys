# STEP5：装配与热更改造 + DSL 整体移除

状态：已实施

## 目标

改造 dispatch 装配与生命周期并整体移除旧 DSL：
① `Start` 改造——DB 可用拉四表构建快照；DB 不可用 = 空表快照 + 状态透出 + 后台按固定间隔重试直至首次构建成功、成功即停（非常驻轮询）。
② `Rebuild()` 供端点触发——拉全部四表 → 构建对象图（含探活任务集差量）→ 校验 → 原子替换快照；全程持互斥锁串行（防并发保存下旧拉取覆盖新快照）；构建失败保留旧快照并报错。
③ 双中间件装配——主件（Middle：路由+选点+计数+1+sticky 种值）与收尾件（Tail，STEP4 产物）共用运行态；收尾件 Start/Stop 为 no-op；`DISPATCH_ENABLED` 经 autoEnableMap 两个名称同键联动启停。
④ Handle 改造——新匹配引擎 + 均衡器选点（均衡器停用或无可用节点 → 写 503 中断链返回 false）+ 写 Target + 参数注入 + sticky；返回 true（继续转发）时只设响应头、禁止 Write/WriteHeader。
⑤ 整体删除 `DISPATCH_RULES` 配置项与全部 DSL 解析代码（parseRules 解析链、Radix Tree、chash）及旧语义测试；`.env` 只留 `DISPATCH_ENABLED`。

## 改动文件清单

- 修改 `plugins/dispatch/dispatch.go`：New（删 DISPATCH_RULES 注册，注入 DB 读取能力与 registry/healthcenter）、Start/Stop、新增 `Rebuild()`（互斥锁）、Handle 全面改写（新引擎）
- 删除 `plugins/dispatch/router.go`、`chash.go`、`router_test.go`、`chash_test.go`（STEP3 已提取段匹配公共函数的除外——提取物迁入 match.go/snapshot.go 后删源文件）；`balancer.go` 中未被新引擎复用的部分一并删除；`dispatch_test.go` 旧 DSL 用例删除
- 修改 `cmd/rocksys/main.go`：装配双中间件（`mgr.RegisterMiddleware(dispatch.New(cfgMgr))` 主件 + 收尾件注册，约 :282 位置）；autoEnableMap 两个名称同键 `DISPATCH_ENABLED`（约 :673-684 位置）
- 修改 `plugins/registry/registry.go`：拆除 DISPATCH_RULES 联动（registry 服务发现本体保留）；其 dispatch 联动测试用例删除
- 修改配置注册：`DISPATCH_RULES` 消失后 default.env 由程序自动同步刷新（禁止手工编辑）

## 实施步骤（完成一项立即勾选保存；粒度到不可再分的动作）

- [x] dispatch.go：DB 读取依赖注入（经 internal/db 拉四表行，接口抽象便于测试注入内存行集）
- [x] dispatch.go：`Rebuild()`（互斥锁串行：拉四表 → ValidateGraph → 快照构建 → 原子替换 → 探活任务集差量；失败保留旧快照返回 error）
- [x] dispatch.go：`Start`（DB 可用同步构建一次；不可用空表快照 + 后台固定间隔重试至首次成功即停）与 `Stop`（停探活排空）
- [x] dispatch.go：Handle 改写（归一 Host → 快照匹配 → 均衡器选点/ sticky 直路由 → 计数 +1 → DF 存节点 id + 参数注入 + 写 Target；命中但均衡器停用/无可用节点 → 503 返回 false；未命中返回 true 不写 Target；true 路径只设头禁止写体）
- [x] cmd/rocksys/main.go：收尾件装配（共用主件运行态）+ autoEnableMap 补收尾件名同键联动（主件名 `dispatch` 保持，收尾件名与 STEP4 Name 一致）
- [x] 删除 DSL：`DISPATCH_RULES` 注册代码、parseRules 及解析链、Radix Tree、chash、旧语义测试用例；同步拆除 plugins/registry 的 DISPATCH_RULES 联动代码与对应测试用例
- [x] 全量回归修复：受删除影响的引用与测试

## 验证

- 接手核实命令（拆步时预写；无副作用、可重复执行；断点后一条命令自证已完成部分完好）：
  `go build ./... && go test ./plugins/dispatch/ ./cmd/... -race -count=1 && ! grep -rn "DISPATCH_RULES" --include="*.go" .`
  （预期：构建与测试全绿；全仓 .go 源码含 plugins/registry 零残留——docs 历史归档与 docs/plan 除外）
- 本步完整验证（勾选＝该项已真实执行且通过，凭意图不得勾选；按需含受影响面的全量回归）：
  - [x] `go test ./... -race -count=1` 全绿
  - [x] `go vet ./...` 通过
  - [x] 降级集成用例：DB 不可用（注入失败 DB）→ 空表快照全走默认 upstream、无 panic；恢复后后台重试自动完成首次构建（用例断言重试即停）
  - [x] 热更重建用例：Rebuild 并发调用串行安全（`-race`）；构建失败保留旧快照
  - [x] `cd bin && go run ../cmd/rocksys --version` 级冒烟（或等价）：程序可启动、default.env 刷新后无 DISPATCH_RULES 条目、DISPATCH_ENABLED 仍在

## 完成标准

- G6 完成：DSL 与 DISPATCH_RULES 当作从未存在（无迁移、无兼容层），全量测试全绿
- 双中间件装配生效：启停联动、链序确定；请求路径零锁（快照原子替换）
- 降级与启动期恢复语义符合 S6（空表快照、后台重试成功即停）
- Rebuild 互斥串行、失败保旧快照

## 实施回填区

### 产物锚点清单（拆步时预写，实施中随手更正；改名同步更新）
- 修改文件：`plugins/dispatch/dispatch.go`（整体重写：RowSource/DBSource 注入、Rebuild、Start 降级重试、Handle 新引擎、Ready 状态透出）、`plugins/dispatch/model.go`（UpstreamRT 补 Enabled 字段；AlgoKind 常量归一 AlgoRoundRobin/AlgoLeastConn）、`plugins/dispatch/select.go`、`plugins/dispatch/match.go`（收编 matchSegments/splitSegments）、`plugins/dispatch/doc.go`、`plugins/registry/registry.go`、`plugins/registry/doc.go`、`cmd/rocksys/main.go`（db.Open 块前移至挂件注册之前；dispatch 双中间件装配；autoEnableMap 补 dispatch-tail 同键）
- 删除文件：`plugins/dispatch/router.go`、`chash.go`、`balancer.go`、`healthcheck.go`、`router_test.go`、`chash_test.go`、`plugins/registry/multi_publish_test.go`；`dispatch_test.go` 旧 DSL 用例整体重写
- 新增函数：`RowSource`/`NewDBSource`、`Rebuild`、`retryUntilBuilt`（后台重试循环）、`Ready`、新 Handle 主体
- 配置项：删除 `DISPATCH_RULES`；`DISPATCH_ENABLED` 语义不变
- 测试：`plugins/dispatch/dispatch_test.go` 重写（Handle 契约/降级重试即停/Rebuild 串行/失败保旧快照/Stop 排空）

### 偏差与现场记录
- UpstreamRT 补 `Enabled` 字段（model.go）：fail-closed 需在 Handle 区分「均衡器停用」与「无健康节点」503；STEP2 未落该字段，属必要最小补充。
- 关系表无 enabled 列（软删即移除、整组替换语义），query_all_active 不返回该列 → DBSource.LoadRelations 对有效行恒置 Enabled=true（HealthCenter.Rebuild 关系过滤口径依赖），已注释说明。
- AlgoKind 常量归一顺带完成（STEP2 遗留待办）：`AlgoKindRoundRobin→AlgoRoundRobin`、`AlgoKindLeastConn→AlgoLeastConn`，全仓引用已同步。
- main.go 的 DB_DRIVER/DB_DSN 注册与 db.Open 块整体前移至挂件注册之前（原在挂件注册之后）：dispatch 主件装配（约原 :282）需要 dataDB 注入，链序（Middle: dispatch 在 rewrite 之前）不变。
- Registry 联动拆除面：rulesKey 常量、buildRules、Server.cfgMgr/rulesKey 字段、SetConfMgr/SetRulesKey、publish 的 conf.Set 分支；TestBuildRules、TestServer_Register_TriggersConfSet、multi_publish_test.go 删除，组件生命周期用例去掉联动断言后保留。
- New 签名变更为 `New(cfg, src RowSource, reg *Registry)`；reg 传 nil 时内部自建 registry（独立测试便利），src 为 nil 时视为 DB 不可用走降级。
- 全量 `-race` 中 `internal/adminapi/TestMigrateCancelWholeTask` 偶发失败（计时敏感）：经 stash 在改动前 HEAD 复现（count=3 必挂），确认与本次改动无关的存量问题；`plugins/shield/TestEventCounterConcurrent` 同类偶发，单跑必过。
