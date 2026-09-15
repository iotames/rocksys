# STEP7：文档同步 + 终验 + 验收回写

状态：待实施

## 目标
文档逐项同步（webui.md / webui-api.md / CONFIGURATION.md / README.md）；全量测试、vet、dev 与生产构建；跨方言真实双库迁移验收（MySQL/PG 环境变量门控）；浏览器全程实看截图；设计文档 §8 验收结论回填。

## 改动文件清单
- 修改 docs/webui.md、docs/webui-api.md、docs/CONFIGURATION.md、README.md
- 修改 docs/plan/DATA_MIGRATION_PLAN.md（§8 回填）
- 新增跨方言集成测试（环境变量门控，如 MYSQL_TEST_DSN/PG_TEST_DSN）

## 实施步骤（完成一项立即勾选保存）
- [ ] 跨方言集成测试：SQLite→MySQL、MySQL→PG（行数一致、抽样字段、批次/策略/序列重置）
- [ ] 文档同步四处
- [ ] 全量 `go test ./... && go vet ./...` + dev/生产构建
- [ ] 浏览器终验（方案 §5 验收标准 1–7 逐条，截图留证）
- [ ] §8 验收结论回写 + 进度日志

## 验证
- 接手核实命令：`go test ./... && go vet ./...`
- 本步完整验证：
  - [ ] 方案 §5 验收标准逐条核对 + 构建产物 --version 可用

## 完成标准
- §5 验收标准 1–8 全过（无法全绿项如实记入已知边界）。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增测试：migrate_integration_test.go（跨方言）
### 偏差与现场记录
- （无）
