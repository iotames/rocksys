# STEP6：文档同步 + 终验收口

状态：已实施

## 目标
按仓库法文档同步红线更新全部受影响文档；终验（build/vet/test + 浏览器实看截图）；回写 PLAN §8 验收结论与已知边界。

## 改动文件清单
- docs/DATA_DICT.md（geoip_list/schedule_list 新表章节 + 两表删列 + 枚举 schedule_list.kind/last_status）
- docs/webui-api.md（GET /admin/schedule/list；/admin/logs、/admin/shield/events 字段变更；client_ip 带端口示例修正）
- docs/COMPONENTS.md、docs/webui.md、docs/CONFIGURATION.md、docs/PROJECT_STRUCTURE.md
- sql/{sqlite,postgres,mysql}/README.md
- docs/plan/GEOIP_LIST_PLAN.md §8

## 实施步骤
- [x] 上述文档逐项同步 ✓（DATA_DICT/webui-api/COMPONENTS/webui/CONFIGURATION/PROJECT_STRUCTURE/sql README ×3，grep 抽查一致）
- [x] 终验全绿 + 截图留证 ✓（生产构建 + go vet + go test ./... 全绿；截图见 STEP5）
- [x] PLAN §8 回写验收结论与已知边界 ✓

## 验证
- 接手核实命令：`grep -l "geoip_list" docs/DATA_DICT.md docs/webui-api.md`
- 本步完整验证：
  - [ ] `go build -tags dev -o bin/rocksys.exe ./cmd/rocksys && go vet ./... && go test ./... -count=1`
  - [ ] 浏览器实看 + 截图

## 完成标准
- 验收标准（PLAN §5）8 条逐条核对通过；文档与代码一致。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- PLAN §8 验收结论按 §5 八条逐条回填（全部 ✅）
### 偏差与现场记录
- 无新增；实施期发现（reset upsert / Country.Names 解码 / 省份短名）已分别记录于 STEP3/STEP2/PLAN §8。
- **真库补验（2026-09-15，本地默认凭据 root:root / postgres:postgres）**：初版终验仅覆盖 SQLite，经用户指正补跑——① MySQL/PG 真库集成套件全量绿（internal/db、adminapi、shield、obs traffic MySQL/PG 冒烟，连跑两轮）；② 端到端冒烟：真实服务分别挂 MySQL/PG 启动 → schedule/list 登记 11 行 → 手动同步 + 状态回写 success → PG 二次启动登记幂等且运行态保留；③ 顺带修复 obs traffic 真库测试的 geoip_list 清理逻辑（固定表名未入 DROP 清单，二跑撞主键）。
