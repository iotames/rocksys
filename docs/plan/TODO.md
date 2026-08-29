# 实施总纲（TODO）：数据库表结构同步 + IP 黑名单增强

> **唯一执行入口**：任何 Agent（含中断后接手的）从本文件开始，按 §1 顺序逐个执行 STEP 文档，**全自动完成，无需向用户逐项请示**；仅 git 提交必须等用户明确确认（AGENTS.md 红线）。
> 设计依据（只读参考）：`docs/DB_SCHEMA_SYNC_PLAN.md`、`docs/IP_BLACKLIST_PLAN.md`（决策与设计理由的权威来源；执行细节以 STEP 文档为准，冲突时顺序以本总纲为准）。
> 建立：2026-08-29

## 0. 断点续传协议（每一步都照此执行）

1. **开始**：读本文件 §1 状态总表，定位第一个状态非「已实施」的 STEP（顺序固定：不跳步、不并行、不挑活）。
2. **接手「实施中」**：先读该 STEP 文档的勾选清单与「实施回填区」，再**对照代码现状核实**已勾选项是否真实存在（防中断后重复实施），从首个未勾选项继续。
3. **实施中**：每完成清单中一项，**立即编辑 STEP 文档勾选保存**（这是进度持久化的最小单位，宁可多存不可少存）；发现与设计偏差或现场新决策，先写入回填区再继续。
4. **验证**：全部勾选后执行该 STEP「验证」节的命令/浏览器操作，全部通过才能把状态改「已实施」；验证失败就修复；修不动则在回填区记录阻塞原因与已尝试方案，状态保持「实施中」，继续下一个不受阻的 STEP（若后续 STEP 依赖被阻塞项则一并跳过并在总表标注）。
5. **收尾**：状态改「已实施」后，回本文件更新 §1 总表，并在 §3 进度日志追加一行（日期 + STEP + 结果摘要）。
6. **全部完成**：两个项目全部「已实施」后，向用户汇报总结并等待指示（含 git 提交确认），**不得自行执行任何 git 写操作**。

## 1. 执行顺序与状态总表

状态取值：`待实施` / `实施中` / `已实施`。

### 项目一：数据库表结构同步（`docs/plan/db_schema_sync/`，先行）

> 先行理由：独立基础设施、零依赖；且为项目二的存量库加列（`warn_times`）提供「服务 → 数据库 → 表结构」升级通道。

| 序 | STEP 文档 | 内容 | 状态 |
|---|---|---|---|
| B1 | [STEP1_ddl_parser.md](db_schema_sync/STEP1_ddl_parser.md) | TableSpec + DDL 解析器 + 拆句器 + 真实脚本 fixtures 单测 | 已实施 |
| B2 | [STEP2_catalog_diff.md](db_schema_sync/STEP2_catalog_diff.md) | 三方言 catalog 脚本 + diff 引擎（A-F 分级）+ SQL 生成 + 单测 | 已实施 |
| B3 | [STEP3_endpoints_assembly.md](db_schema_sync/STEP3_endpoints_assembly.md) | schema/exec 端点 + 表清单注册装配 + 端点单测 | 已实施 |
| B4 | [STEP4_frontend.md](db_schema_sync/STEP4_frontend.md) | 服务→数据库 新页/页签/两按钮/codeEditor sql 高亮 | 已实施 |
| B5 | [STEP5_verify_docs.md](db_schema_sync/STEP5_verify_docs.md) | 全量测试回归 + 文档同步收尾 | 已实施 |

### 项目二：IP 黑名单增强（`docs/plan/ip_blacklist/`，B5 完成后开始）

| 序 | STEP 文档 | 内容 | 状态 |
|---|---|---|---|
| A1 | [STEP1_schema_enum.md](ip_blacklist/STEP1_schema_enum.md) | warn_times 列 + block_type 枚举 0/11（三处红线同步） | 待实施 |
| A2 | [STEP2_store_sql.md](ip_blacklist/STEP2_store_sql.md) | store 覆盖 warn_times/续封转永久/排序 {order}/jail SQL | 待实施 |
| A3 | [STEP3_endpoints.md](ip_blacklist/STEP3_endpoints.md) | sync_file / ban / jail / sort 四组端点 | 待实施 |
| A4 | [STEP4_auto_ban.md](ip_blacklist/STEP4_auto_ban.md) | 自动拉黑引擎 + 四配置项 + 装配 | 待实施 |
| A5 | [STEP5_frontend_lists.md](ip_blacklist/STEP5_frontend_lists.md) | 黑白名单页/TOP 批量/state.js 枚举数组 | 待实施 |
| A6 | [STEP6_frontend_ban_ui.md](ip_blacklist/STEP6_frontend_ban_ui.md) | 拦截明细「IP封禁」垂直切片（含 events in_blacklist） | 待实施 |
| A7 | [STEP7_frontend_jail.md](ip_blacklist/STEP7_frontend_jail.md) | 首页 tabs + 小黑屋页签 | 待实施 |
| A8 | [STEP8_verify_docs.md](ip_blacklist/STEP8_verify_docs.md) | 全量回归 + 文档同步收尾 | 待实施 |

## 2. 全局红线（每个 STEP 都适用）

- 命令一律原生 `go build` / `go test` / `go vet`，不用 make；运行程序必须 `cd bin && ./rocksys`（严禁项目根目录执行，运行时文件落 bin/）。
- 前端验证：`go build -tags dev -o bin/rocksys ./cmd/rocksys` 后 `cd bin && ./rocksys`，浏览器 http://127.0.0.1:19527/ ；改已有 webui 文件刷新即见，**新增**前端文件需重启一次（路由启动时注册）。
- 提示统一 `Rock.ui.toast`（成功/信息自动消失，错误/警告常驻需手动关）、确认统一 `confirmDialog`；异常文案三要素（发生了什么/为什么/怎么办）。
- 数据层/枚举变更三处同步：三方言建表脚本、`docs/DATA_DICT.md`、Go 权威定义；接口/配置变更同步 webui-api.md / webui.md / CONFIGURATION.md（正式同步集中在各项目最后一个 STEP，中途可先记回填区）。
- 提交信息中文单行摘要；**绝不自行 git commit/push**。
- 每个 STEP 完成：STEP 文档状态改「已实施」+ 本总表 §1 更新 + §3 日志一行。

## 3. 进度日志

| 日期 | STEP | 结果 |
|---|---|---|
| 2026-08-29 | — | 总纲与 13 份 STEP 文档建立（B1-B5 + A1-A8），全部待实施 |
| 2026-08-29 | B1 | 解析器/拆句器/索引名提取落地 internal/db/schema_parse.go，fixtures 全三方言真实脚本，单测全绿 + vet 通过（转义引号 '' 状态机 bug 已修） |
| 2026-08-29 | B2 | 9 份 catalog 脚本 + schema_catalog（归一读取）+ schema_diff（A-F 分类/归一化/GenerateSQL）落地，sqlite 内存库真实脚本零差异闭环单测全绿 + vet 通过 |
| 2026-08-29 | B3 | /admin/db/schema + /admin/db/exec 端点、SetTableSpecs 装配注入、buildTableSpecs 7 表清单（含 SHIELD_EVENT_TABLE 实值）、一致性单测（cmd/rocksys/main_test.go）；全量测试+vet 通过，curl 冒烟过（旧进程占端口教训见 STEP3 回填区） |
| 2026-08-29 | B4 | 子代理实施前端（database.js 新页/codeEditor sql 高亮/路由菜单），主代理浏览器回归 §4 全清单通过（A→执行→D→执行→无差异闭环 + 失败中断态 + 常驻 toast） |
| 2026-08-29 | B5 | 全量 go test + vet + 生产构建通过；文档同步五份（webui-api §3.19/webui §4.11/DATA_DICT §5/PROJECT_STRUCTURE/母文件状态行）；项目一完成，git 提交，项目二解锁 |
