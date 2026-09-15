# STEP7：文档同步 + 终验 + 验收回写

状态：已实施

## 目标
文档逐项同步（webui.md / webui-api.md / CONFIGURATION.md / README.md）；全量测试、vet、dev 与生产构建；跨方言真实双库迁移验收（MySQL/PG 环境变量门控）；浏览器全程实看截图；设计文档 §8 验收结论回填。

## 改动文件清单
- 修改 docs/webui.md、docs/webui-api.md、docs/CONFIGURATION.md、README.md
- 修改 docs/plan/DATA_MIGRATION_PLAN.md（§8 回填）
- 新增跨方言集成测试（环境变量门控，如 MYSQL_TEST_DSN/PG_TEST_DSN）

## 实施步骤（完成一项立即勾选保存）
- [x] 跨方言集成测试：SQLite→MySQL、MySQL→PG（行数一致、抽样字段、批次/策略/序列重置）
- [x] 文档同步四处
- [x] 全量 `go test ./... && go vet ./...` + dev/生产构建
- [x] 浏览器终验（方案 §5 验收标准 1–7 逐条，截图留证）
- [x] §8 验收结论回写 + 进度日志

## 验证
- 接手核实命令：`go test ./... && go vet ./...`
- 本步完整验证：
  - [x] 方案 §5 验收标准逐条核对（自动化 + 浏览器实测）+ 构建产物 --version 可用

## 完成标准
- §5 验收标准 1–8 全过（无法全绿项如实记入已知边界）。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增测试：migrate_integration_test.go（跨方言）
### 偏差与现场记录
- 文档同步范围在方案四处之外增加两份：docs/COMPONENTS.md（geoip 手动同步改为后台任务）、docs/PROJECT_STRUCTURE.md（新增 taskcenter 目录与数据迁移链路说明）——同属「文档同步红线」要求。
- 跨方言集成测试新增两用例：数据迁移（SQLite→MySQL、MySQL→PG，含宽表子批、序列重置、取消重跑）与结构对齐（MySQL/PG 临时库自建自清）。
- 实施期修复 6 个缺陷（拆句吞终止符、后台执行开关复位、MySQL AUTO_INCREMENT 占位符、PG $N 占位符、MySQL 索引前缀越界、概览页同步响应结构回归），详见设计文档 §8。
- 既有 `internal/db` 表同步集成用例要求目标库为空，本机外部测试库有历史残留表导致其 F 级差异失败：属环境残留，未擅自清理外部库数据，已记入 §8。
- 既有代码/文档中仍有多处对历史方案文档的引用（DB_SCHEMA_SYNC_PLAN / GEOIP_LIST_PLAN / IP_BLACKLIST_PLAN 等，涉 geoip、obs、shield、dbschema 等模块），按宪法「功能产出自包含」应清理，但超出本次任务范围，仅报告待用户安排。
