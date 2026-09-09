# STEP6：启动检测两表缺列打 warning（D16）

状态：待实施

## 目标
按 PLAN D16：新二进制启动时检测 access_log/shield_event 实际表缺新列（user_agent/country/city）→ warning 日志：缺哪些列、去 WebUI /admin/db/schema 查看 diff、经 /admin/db/exec 人工确认执行；不自动迁移、不阻断启动。

## 改动文件清单
- cmd/rocksys/main.go（启动序列装配处，SetTableSpecs 附近）
- 或 internal/db 新增检测辅助 + 单测（实施时按最小改动定位）

## 实施步骤（完成一项立即勾选保存）
- [ ] 缺列检测 + warning 文案（三要素）✓（go test，通过）
- [ ] 单测：旧表 schema → 触发 warning；新表 → 不触发

## 验证
- 接手核实命令：`go test ./... -run MissingColumn 2>/dev/null; grep -rn "缺列\|missing column\|MissingColumn" cmd/ internal/ | head`
- 本步完整验证：
  - [ ] `go test ./... && go vet ./...`
  - [ ] 手工：旧两表 sqlite 库启动新二进制 → 日志出现 warning；新库无 warning

## 完成标准
- 缺列 warning 准确触发且不阻断；验收 §3 老库升级路径可走通。

## 实施回填区
### 产物锚点清单（拆步时预写）
- 新增函数：<实施时定名>
### 偏差与现场记录
- （无）
