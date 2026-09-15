# 执行总纲

> 执行依据：docs/plan/README.md（计划目录工作宪法）——记号语义、状态机、关口与裁决以宪法为准。

项目状态：待人类验收（DATA_MIGRATION 全部 7 步已实施，终验通过，等待人类验收与归档确认）

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
| 6 | data_migration/STEP6_前端表数据页签.md | database.js 页签 + 四卡 + 共用提交→轮询组件（含页面恢复） | 1–5 | 已实施 |
| 7 | data_migration/STEP7_文档与终验.md | 文档同步 + 全量测试/vet/构建 + 浏览器实看 + 终验回写 | 6 | 已实施 |

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
| 2026-09-15 | ZCode | STEP6 | 已实施：四卡+轮询组件浏览器实测通过；修复拆句吞终止符与后台执行开关复位两个真 bug |
| 2026-09-15 | ZCode | STEP7 | 已实施：跨方言集成测试（迁移+对齐）真库通过、四份文档同步、全量测试/vet/构建全绿、浏览器终验、§8 回写 |
| 2026-09-15 | ZCode | 验收后 | 用户实测提三问：①database.js 曾被误删已恢复 ②表单未统一（已抽公共组件）③schedule 与 taskcenter 域被混用（见 §4） |

## §4 待决事项（阻塞于人类决策，未决前不动相关代码）

### P1 schedule 登记语义被 taskcenter 终态词汇污染（用户 09-15 提出）
- **现象**：`schedule_list.last_status` 现被写入 `cancelled`——该值只有「人手动提交的同步任务又被人取消」这一条路径才会产生（定时器路径不经 taskcenter、永不取消），等于一个字段承载两个来源的语义；标签「已取消」在「状态」列易被读成"该定时任务被停用"（旁边还有「关联开关/启用」列）。
- **证据**（已机器核实）：`startGeoSyncTimer` 用 `context.WithTimeout(context.Background(), geoSyncBudget)` 直调 `geoSyncAll`，不提交任务中心、不响应取消；手动入口经 `adminSrv.SubmitTask` 可取消；两条路径同写 `schedule_list.geoip_sync` 一行（`geoSyncOnDone` 单点回写）。实测：取消 → 任务终态 `cancelled` + 登记行 `cancelled`；正常到点 → 任务 `done` + 登记行 `success`。
- **域边界**：schedule = 只读登记 + 执行结果汇总（DB 持久、由各业务定时循环驱动、无进度/无取消/无互斥）；taskcenter = 长任务运行时（内存、人触发、有进度/取消/全局互斥）。`cancelled` 属后者。
- **候选**（用户未定，**未决前不提交相关改动**）：
  - A 保守：登记行回到 success/failed/skipped；手动取消记 success，message 写明"收工原因：任务被取消"；"被取消"由任务中心列表承载。
  - B 最干净：登记行引入执行域词（如 `partial` = 本轮未跑完），定时到点与手动取消**统一**记它，success 只留给真跑完；需改归档方案"到点记 success"的明文约定。
  - C 妥协：保留 `cancelled`，标签改「上轮已取消」+ 文档写明"指上一轮执行被中断，非任务停用"，接受同字段双来源。
- **已落地但待定**（工作区未提交）：三方言脚本注释 + `docs/DATA_DICT.md` §3.5 + `cmd/rocksys/schedule.go` 权威常量 `ScheduleStatus*` + 前端 `schedule.js` 标签 + 收口按 ctx 区分到点/取消/失败（`geoipSyncOutcome`）。若选 A/B 需按新语义回改。

### P2 定时 GeoIP 同步绕过任务中心「全局单任务」约束（同源问题）
- **现象**：设计决策为「全局同一时刻仅 1 个长任务（含 GeoIP 同步）」，但**定时**同步不走 taskcenter，仅受 `geoSyncAll` 自身的 sync 级互斥保护 → 定时同步可与人工提交的数据迁移/结构对齐/SQL 后台执行**并发**（迁移读运行库、定时同步写 `geoip_list`、SQL 后台执行亦写运行库，存在锁竞争与"全局单任务"承诺落空）。
- **候选**：① 让定时路径也经 taskcenter 提交（受全局互斥；但定时任务被人工长任务挤占时会跳过该轮，需定义跳过策略）；② 明确写下边界"定时任务不受全局互斥约束、只受任务级互斥"，并评估并发风险可接受。
- **待办**：用户定方向后再改（涉及 D20 决策表述修订）。

### P3 未提交的工作区改动（等用户允许后提交）
- 恢复 `database.js` 被误删的 547 行（HEAD 上「表同步/表概览/SQL历史」页签曾失效）；
- 新增公共表单组件 `webui/assets/js/components/form.js` + `style.css` 语义类，重构「表数据」三卡去内联样式，全局配置搜索框改走同一组件；
- 上文 P1 相关实现（`geoip_sync.go` 收口/文案、`schedule.go` 常量与按字符截断、三方言脚本、DATA_DICT、前端标签）与 P2 的取证结论。
- 修复既有缺陷 `msg[:255]` 按字节截断中文致非法 UTF-8（已改按字符截断 + 回归测试）。
- 质量门：`go vet ./...` 干净、`go test ./...` 全绿、生产构建通过；测试残留已清理。
