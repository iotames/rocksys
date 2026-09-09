# STEP1：数据层三同步——两表加列（user_agent/country/city）+ 集成测试基建前置修复

状态：已实施

## 目标
按 PLAN §3.2：access_log 加 `user_agent`/`country`/`city`，shield_event 加 `country`/`city`（三方言均 `TEXT NOT NULL DEFAULT ''`）；完成数据字典红线三同步 + TableSpecs；前置修复 §5.1 集成测试基建两处编译缺陷。geo 解析值本步先写空串（STEP2 接 geo 后填充）。

## 改动文件清单
- sql/{sqlite,mysql,postgres}/access_log_create_table.sql、access_log_insert.sql（15→18 列）
- sql/{sqlite,mysql,postgres}/shield_event_create_table.sql、shield_event_insert.sql（13→15 列）
- docs/DATA_DICT.md 两表章节
- plugins/obs/dim.go（Dims 注册 DimUserAgent/DimCountry/DimCity + AccessRecord 字段 + ToFlatMap）
- plugins/obs/db_store.go（Write 参数扩充）
- plugins/obs/obs.go（OnDone 构造条目补 user_agent=ctx.R.UserAgent()，country/city 暂空串）
- plugins/shield/event_recorder.go（ShieldEvent 加 Country/City + flush 写入参数；newEvent 暂填空串）
- cmd/rocksys/main.go TableSpecs 两表列清单补列（若在清单内）
- internal/db/schema_sync_acceptance_integration_test.go（syscall.Flock 修 Windows 编译）
- internal/db/schema_sync_integration_test.go（缺 context 导入）
- 相关既有单测（obs_test、event_recorder_test）随列数断言更新

## 实施步骤（完成一项立即勾选保存）
- [x] 修 internal/db 集成测试两处编译缺陷（syscall.Flock 加 build 约束/替代、补 context 导入）✓（go vet -tags integration ./internal/db/，通过）
- [x] 三方言 access_log 建表/INSERT 脚本加列（含中文注释） ✓（go test ./... && go vet ./...，通过）
- [x] 三方言 shield_event 建表/INSERT 脚本加列 ✓（go test ./... && go vet ./...，通过）
- [x] docs/DATA_DICT.md 两表章节同步（字段/说明/三方言类型对照） ✓（go test ./... && go vet ./...，通过）
- [x] Go 权威定义与写入链路：Dims/AccessRecord/ToFlatMap/db_store.Write/obs OnDone；ShieldEvent/newEvent/flush 参数 ✓（go test ./... && go vet ./...，通过）
- [x] cmd/rocksys TableSpecs 补列 ✓（核实：TableSpec 仅存 CreateScript 文件名，列清单由建表 DDL 解析（internal/db/schema_parse.go），脚本已改即自动生效；main_test.go 防漏单测随 go test 通过）
- [x] 既有单测随改更新 ✓（go test ./... && go vet ./...，通过）
- [x] bin/hotscripts/sql 同步（cp -r sql/* bin/hotscripts/sql/） ✓（go test ./... && go vet ./...，通过）

## 验证
- 接手核实命令：`grep -c "user_agent" sql/sqlite/access_log_insert.sql sql/mysql/access_log_insert.sql sql/postgres/access_log_insert.sql && grep -c "country" sql/sqlite/shield_event_insert.sql && go vet ./plugins/obs/ ./plugins/shield/`
- 本步完整验证（勾选＝已真实执行且通过）：
  - [x] `go test ./plugins/obs/ ./plugins/shield/ ./internal/db/`（全过）
  - [x] `go vet ./...`（退出码 0）
  - [x] 真库门控（PLAN §5.1 DSN 经环境变量注入）：MYSQL_TEST_DSN / PG_TEST_DSN 全量 `-tags integration ./internal/db/`（32s 全过，含 schema_sync_acceptance 7 表 A 级闭环）
  - [x] `go test ./...` 全仓全过（含子 Agent 产出的 internal/geoip）
  - （老库升级运行时冒烟移至 STEP6 验证——其内容即 STEP6 的缺列检测，此处不重复）

## 完成标准
- 三处同步齐备且一致（列数：access_log 18+extra、shield_event 15+extra）；测试全绿；真库门控通过。

## 实施回填区
### 产物锚点清单（实施后核实）
- 新增维度：obs DimUserAgent/DimCountry/DimCity（dim.go 常量+Dims 注册+AccessRecord 字段+ToFlatMap）；shield ShieldEvent.Country/City
- 改动函数：obs.DBStore.Write（18 参数）、obs OnDone（补 UserAgent）、EventRecorder flush 写入（15 参数）；TableSpecs 无需改动（列清单由建表 DDL 解析）
- 新增测试基建文件：internal/db/testdevdblock_unix_test.go、testdevdblock_windows_test.go
- 集成测试修复：schema_sync_integration_test.go（补 context 导入；旧版 DDL 保留 updated_at）
### 偏差与现场记录
- 集成测试基建实际缺陷比 PLAN §5.1 记载多一处：schema_sync_integration_test 旧版 DDL 缺 updated_at（C 级时间列 DiffSchema 有意不自动补）→ 同步后零差异断言恒挂；修法为旧版 DDL 保留 updated_at（测试从未运行过，属设计期不可见缺陷）。
- 存量 PG 方言缺陷（非本步引入）：sql/postgres/access_log_query.sql 排序 CASE $12/$13 被 PG 消解为 text 比较（lib/pq 未知类型下发），日志页排序在 PG 必报「text = integer」；修法为 CAST($12/$13 AS INTEGER)（已随本步提交）。真库集成测试自该标签可编译起首次跑通。
- 三方言加列曾被中断脚本重复应用一次，已去重并以 git diff（+38/-18）核实列数正确（access_log 18+extra、shield_event 15+extra）。
