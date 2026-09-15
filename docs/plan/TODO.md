# 执行总纲

> 执行依据：docs/plan/README.md（计划目录工作宪法）——记号语义、状态机、关口与裁决以宪法为准。

项目状态：进行中（DATA_MIGRATION，设计已定稿，git 破例授权：一个 STEP 验证通过即提交）

## §0 断点续传（接手者从这里开始）
1. 读项目状态行与 §1 总表；
2. 定位第一个非「已实施」的步骤；受阻按宪法铁律 4 处理；
3. 「实施中」先跑该 STEP 验证节预写的接手核实命令，通过则从首个未勾选项继续；
4. 每次接手核实后、每次状态迁移收口时，跑 §4.1 附状态一致性自检；
5. 严禁凭会话记忆做任何实施判断。

## §1 状态总表（状态列可带标注）
| # | STEP | 内容 | 依赖 | 状态 |
|---|------|------|------|------|
| 1 | data_migration/STEP1_任务执行中心.md | internal/taskcenter 新包 + /admin/tasks 三端点 | — | 已实施 |
| 2 | data_migration/STEP2_数据源管理.md | CONF_DIR 注册 + dsn.go CRUD/测试端点 + dsn.json 持久化 | 1 | 已实施 |
| 3 | data_migration/STEP3_表结构对齐.md | migrate/schema diff 预览 + schema_apply 任务化 | 1,2 | 已实施 |
| 4 | data_migration/STEP4_数据迁移执行器.md | migrate.go 执行器（流式读/攒批/子批/冲突策略/序列重置）+ 三端点 | 3 | 已实施 |
| 5 | data_migration/STEP5_GeoIP与SQL任务化.md | geoip_sync 后台任务化 + exec 后台执行开关 | 1 | 已实施 |
| 6 | data_migration/STEP6_前端表数据页签.md | database.js 页签 + 四卡 + 共用提交→轮询组件（含页面恢复） | 1–5 | 待实施 |
| 7 | data_migration/STEP7_文档与终验.md | 文档同步 + 全量测试/vet/构建 + 浏览器实看 + 终验回写 | 6 | 待实施 |

## §2 执行期红线（自仓库法摘录，冲突时以仓库法原文为准）
- 构建/测试用原生命令行：`go build -tags dev -o bin/rocksys ./cmd/rocksys` / `go test ./...` / `go vet ./...`；
- 运行必须在 bin/ 目录（`cd bin && ./rocksys`），禁止项目根目录执行；
- WebUI 提示统一 `Rock.ui.toast`；错误 toast 不自动消失；文案三要素；
- 文档同步：webui.md / webui-api.md / CONFIGURATION.md / README.md；DATA_DICT 无新表不涉；
- 执行期新红线：代码注释与正式文档不得引用 _PLAN/TODO/STEP 文档（宪法新增「功能产出自包含」），叙述自包含
- git 提交：本次任务**适用**破例授权（用户明示「自行完成开发，测试，验收，提交Git全流程」），范围 = 本项目 STEP 逐个提交至 master 本地；禁止 force push；推送仍待用户确认。

## §3 进度日志（一行一事）
| 日期 | 执行者 | STEP | 结果 |
|---|---|---|---|
| 2026-09-15 | ZCode | — | 建纲完成，方案定稿（14b9361）后拆步 |
| 2026-09-15 | ZCode | STEP1 | 已实施：taskcenter 单测+端点测试全绿（互斥/panic收口/淘汰/取消竞态/404） |
| 2026-09-15 | ZCode | STEP2 | 已实施：dsn CRUD/脱敏/重复拦截/懒创建单测全绿，CONF_DIR 注册于 adminapi.New |
| 2026-09-15 | ZCode | 宪法 | 用户新增「功能产出自包含」条款；STEP1/2 已落地注释同步去方案引用 |
| 2026-09-15 | ZCode | STEP3 | 已实施：sqlite 目标真库 diff→apply→复检零差异全绿；拆句改用 SplitStatements |
| 2026-09-15 | ZCode | STEP4 | 已实施：SQLite 双库真跑（行数/抽样/序列重置/宽表子批/clamp/整任务取消）全绿 |
| 2026-09-15 | ZCode | STEP5 | 已实施：geoip_sync 任务化 + exec background 分支，构建/vet/单测全绿 |
