# STEP3：目标库表结构对齐端点（schema diff / schema_apply 任务化）

状态：已实施

## 目标
`internal/adminapi/migrate.go`：GET /admin/db/migrate/schema?code= 对目标库（数据源）diff 预览（复用 DiffSchema/GenerateSQL，目标库 db.Open 不传 hub）；POST /admin/db/migrate/schema_apply?code= 经任务中心后台化执行 DDL（拆句、遇错停、逐条结果归 Result，不写 sql_exec_log）。

## 改动文件清单
- 新增 `internal/adminapi/migrate.go`（本步仅 schema 部分）+ `migrate_test.go`
- 修改 `internal/adminapi/adminapi.go`（路由注册）

## 实施步骤（完成一项立即勾选保存）
- [x] openTargetDB(code) 帮助函数：按 code 从 dsn 存储取数据源 → db.Open(driver, dsn)（不传 hub）
- [x] handleMigrateSchema：diff 预览（items + DDL 文本）
- [x] handleMigrateSchemaApply：向任务中心 Submit（CreatedBy=schema_apply），Run 内拆句逐条执行、遇错停，结果 JSON 归 Progress/Result
- [x] 单测：sqlite 目标库 diff 空态与建表后零 diff；apply 任务终态 done 且目标表存在；重复提交被互斥拒绝

## 验证
- 接手核实命令：`go test ./internal/adminapi/ -run TestMigrateSchema`
- 本步完整验证：
  - [x] `go build ./... && go vet ./... && go test ./internal/adminapi/`（全绿，2026-09-15）

## 完成标准
- 单测全绿（sqlite 真库）；三方言完整链路验证归 STEP7 终验（依赖本地 MySQL/PG 时走集成测试约定）。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增函数：openTargetDB / handleMigrateSchema / handleMigrateSchemaApply
- 新增端点：GET /admin/db/migrate/schema、POST /admin/db/migrate/schema_apply
- 新增测试：TestMigrateSchema*
### 偏差与现场记录
- apply 前预读差异：零差异直接 noop 返回不建任务；DDL 文本带进任务执行（任务生命周期与 HTTP 请求解耦，执行期重开目标库连接）。
- 拆句用 db.SplitStatements（分号感知、支持多语句行内注释），不能用按行拆的 SplitSQLStatements——生成的 CREATE TABLE 为多行文本，按行拆必炸「incomplete input」。
