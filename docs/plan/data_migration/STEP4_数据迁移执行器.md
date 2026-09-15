# STEP4：数据迁移执行器（migrate start/status/cancel）

状态：待实施

## 目标
`internal/adminapi/migrate.go` 增补执行器：POST /admin/db/migrate/start（校验目标≠运行库、表清单非空、批次 clamp 100–10000、冲突策略 replace/skip）→ 经任务中心 Submit；流式 SELECT → 攒批（占位符预算 30000 切子批、子批共享事务）→ 同名列批量 INSERT；每表迁完自增序列重置（D18）；表级进度/单表取消/整任务取消；GET /admin/db/migrate/status 转发中心快照表级明细；POST /admin/db/migrate/cancel 转发中心 Cancel（带 table= 取消 pending 表）。

## 改动文件清单
- 修改 `internal/adminapi/migrate.go` + `migrate_test.go`

## 实施步骤（完成一项立即勾选保存）
- [ ] 请求校验与 clamp；表清单取自期望结构业务表（tableSpecs）与源库现存交集
- [ ] 迁移 run 函数：逐表流式读、同名列对齐、子批 INSERT（预算切分、共享事务）、进度 setProgress
- [ ] 冲突策略三方言分写（TRUNCATE/DELETE、INSERT IGNORE/ON CONFLICT）
- [ ] 自增序列重置（sqlite_sequence / AUTO_INCREMENT / setval；无自增主键跳过）
- [ ] 单表取消（pending 移除）与整任务取消（当前批事务完成后停）
- [ ] 单测（SQLite 双库真跑）：迁移行数一致、批次 7 小批行为、宽表 30 列×10000 不撞占位符、replace/skip、序列重置、单表失败不阻塞、整任务取消

## 验证
- 接手核实命令：`go test ./internal/adminapi/ -run TestMigrate`
- 本步完整验证：
  - [ ] `go build ./... && go vet ./... && go test ./internal/adminapi/`

## 完成标准
- SQLite 双库全用例绿；跨方言（SQLite→MySQL、MySQL→PG）真实用例归 STEP7 终验（环境变量门控集成测试）。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增函数：handleMigrateStart/Status/Cancel、migrateRun、resetAutoIncrement 等
- 新增常量：migrateDefaultBatch=1000、migratePlaceholderBudget=30000
- 新增端点：POST /admin/db/migrate/start、GET /admin/db/migrate/status、POST /admin/db/migrate/cancel
- 新增测试：TestMigrateRun*
### 偏差与现场记录
- （无）
