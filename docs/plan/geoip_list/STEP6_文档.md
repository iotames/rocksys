# STEP6：文档同步 + 终验收口

状态：待实施

## 目标
按仓库法文档同步红线更新全部受影响文档；终验（build/vet/test + 浏览器实看截图）；回写 PLAN §8 验收结论与已知边界。

## 改动文件清单
- docs/DATA_DICT.md（geoip_list/schedule_list 新表章节 + 两表删列 + 枚举 schedule_list.kind/last_status）
- docs/webui-api.md（GET /admin/schedule/list；/admin/logs、/admin/shield/events 字段变更；client_ip 带端口示例修正）
- docs/COMPONENTS.md、docs/webui.md、docs/CONFIGURATION.md、docs/PROJECT_STRUCTURE.md
- sql/{sqlite,postgres,mysql}/README.md
- docs/plan/GEOIP_LIST_PLAN.md §8

## 实施步骤
- [ ] 上述文档逐项同步
- [ ] 终验全绿 + 截图留证
- [ ] PLAN §8 回写验收结论与已知边界

## 验证
- 接手核实命令：`grep -l "geoip_list" docs/DATA_DICT.md docs/webui-api.md`
- 本步完整验证：
  - [ ] `go build -tags dev -o bin/rocksys.exe ./cmd/rocksys && go vet ./... && go test ./... -count=1`
  - [ ] 浏览器实看 + 截图

## 完成标准
- 验收标准（PLAN §5）8 条逐条核对通过；文档与代码一致。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- （无）
### 偏差与现场记录
- （无）
