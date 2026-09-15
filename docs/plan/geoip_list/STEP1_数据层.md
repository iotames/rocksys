# STEP1：数据层——geoip_list / schedule_list 三方言脚本 + 两表删 country/city

状态：已实施

## 目标
按 GEOIP_LIST_PLAN §4.1/§4.3/§4.2 落数据层：新表三方言脚本齐备；access_log/shield_event 删 country/city 4 列与 idx_*_time_country；geo 聚合 SQL 改 JOIN geoip_list（去字符串切分）；db 层表常量与 buildTableSpecs 登记。

## 改动文件清单
- 新增 sql/{sqlite,postgres,mysql}/geoip_list_create_table.sql、geoip_list_create_index.sql、geoip_list_upsert.sql
- 新增 sql/{sqlite,postgres,mysql}/schedule_list_create_table.sql、schedule_list_create_index.sql、schedule_list_upsert.sql
- 修改 sql/*/access_log_{create_table,insert,query,create_index}.sql、shield_event_{create_table,insert,query,create_index}.sql
- 修改 sql/*/traffic_geo_top.sql、traffic_geo_province_top.sql（JOIN geoip_list）；traffic_summary.sql sqlite 版头注释清理
- 修改 internal/db/db.go（+TableGeoipList/TableScheduleList 常量）、cmd/rocksys/main.go buildTableSpecs
- 修改 cmd/rocksys/main_test.go（三方言脚本集合一致性若有断言）

## 实施步骤
- [x] 6 个新表脚本 ✓（go test ./cmd/rocksys/ -run TestTableSpecsMatchScripts，通过） × 3 方言（geoip_list 含 upsert；schedule_list 含 upsert；唯一约束照仓库惯例）
- [x] access_log / shield_event 两表脚本删列 ✓（grep country|city 三方言脚本仅注释残留已清，通过）（create/insert/query/index ×3 方言）
- [x] traffic_geo_top / traffic_geo_province_top 改 JOIN ✓（go test ./plugins/obs/ -run TestTrafficScriptsSQLite，通过）（{geo} 占位符）
- [x] internal/db 常量 + buildTableSpecs + 脚本集合单测核对 ✓（go test ./cmd/rocksys/ -count=1，通过）
- [x] go vet + go build 通过 ✓（go vet ./... 无输出；go build -tags dev 成功）

## 验证
- 接手核实命令：`go test ./cmd/rocksys/ -run TestBuildTableSpecs -count=1 && go vet ./...`
- 本步完整验证：
  - [x] `go build -tags dev -o bin/rocksys.exe ./cmd/rocksys` ✓
  - [x] `go test ./... -count=1` 全绿 ✓
  - [x] 脚本集合一致性（TestTableSpecsMatchScripts 覆盖三方言文件集合）✓

## 完成标准
- 新表脚本三方言齐备且字段/类型/注释与 PLAN §4.1/§4.3 一致；两表 4 列及 idx_*_time_country 在全部脚本中不存在；聚合 SQL 无 substr/instr 字符串切分。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增表常量：db.TableGeoipList="geoip_list"、db.TableScheduleList="schedule_list"
- 新增占位符：{geo} = geoip_list 表名（query/聚合脚本）
### 偏差与现场记录
- 写路径去 geo 列（原属 STEP4）被迫前置到本步：删列后编译即断，写侧实参必须同步删；同因 geoip_sync 旧行为测试失效，其重写归 STEP2，两步合并为一次提交（编译边界交织，拆分提交无法各自全绿）。
- 新库未走 schema 同步时明细查询会因缺 geoip_list 失败——obs/shield 的 EnsureTable 已随建表幂等确保 geoip_list（比设计多一层兜底）。
- access_log_query/shield_event_query 新增 {geo} 占位符（geoip_list 表名），obs db_store / shield sqlText / obs trafficScript 三处替换已接线。
