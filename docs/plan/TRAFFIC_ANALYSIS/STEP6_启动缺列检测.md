# STEP6：启动检测两表缺列打 warning（D16）

状态：已实施

## 目标
按 PLAN D16：新二进制启动时检测 access_log/shield_event 实际表缺新列（user_agent/country/city）→ warning 日志：缺哪些列、去 WebUI /admin/db/schema 查看 diff、经 /admin/db/exec 人工确认执行；不自动迁移、不阻断启动。

## 改动文件清单
- cmd/rocksys/main.go（启动序列装配处，SetTableSpecs 附近）
- 或 internal/db 新增检测辅助 + 单测（实施时按最小改动定位）

## 实施步骤（完成一项立即勾选保存）
- [x] 缺列检测 + warning 文案（三要素）✓（go test ./cmd/rocksys/ -run TestMissingLogColumns，通过）
- [x] 单测：旧表 schema → 触发（报 user_agent/country/city 与 country/city）；新表 → 不触发；缺表 → 不误报 ✓（TestMissingLogColumns 三子用例）

## 验证
- 接手核实命令：`go test ./cmd/rocksys/ -run TestMissingLogColumns -count=1`
- 本步完整验证：
  - [x] `go test ./... && go vet ./...`（全过）
  - [ ] 手工：旧两表 sqlite 库启动新二进制 → 日志出现 warning；新库无 warning（留待 STEP7 dev 运行环节一并实测——需重启用户既有实例）

## 完成标准
- 缺列 warning 准确触发且不阻断；验收 §3 老库升级路径可走通。

## 实施回填区
### 产物锚点清单（实施后核实）
- 新增函数：cmd/rocksys/main.go missingLogColumns(d, specs) map[string][]string；调用点 adminSrv.SetTableSpecs 之后
- 新增测试：cmd/rocksys/missing_columns_test.go TestMissingLogColumns（三子用例）
### 偏差与现场记录
- 首版漏传 DiffTable 的 ActualCols（空 catalog 被当缺表）→ 单测抓住，补 d.CatalogColumns 查询。
- 运行时手工冒烟延后至 STEP7（与 dev 实测合并执行）。
