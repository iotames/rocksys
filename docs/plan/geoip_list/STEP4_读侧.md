# STEP4：读侧——明细端点 JOIN + 回退 Lookup + 聚合端点适配

状态：已实施

## 目标
/admin/logs 与 /admin/shield/events 明细经 JOIN geoip_list 返回 country_code/country_name/province/city，未命中回退实时 Lookup（保新 IP 可见）；「市→省→国名→未知」兜底只在读侧显示；traffic geo 聚合端点适配新 SQL 输出（country_code/country_name、province）。

## 改动文件清单
- plugins/obs/db_store.go（Query 后 geo 回填：JOIN 未命中逐行 Lookup）
- plugins/obs/obs.go（OnDone 去写时解析 country/city）
- plugins/obs/dim.go（Dims 去 country/city 注册，或改由读侧合成——以实现定，保持对外字段稳定）
- plugins/obs/traffic.go（TrafficGeo 输出列适配 + geoip_list 占位符替换）
- plugins/shield/event_recorder.go（去写时解析；QueryEvents 回填；StatsTopIP 维持现状）
- 相关 _test.go 适配

## 实施步骤
- [x] obs OnDone 去 geo 写入；dim 注册表去 country/city ✓（已随 STEP1 前置完成，go build 通过）
- [x] shield newGeoEvent 去解析；writeBatch 去 country/city 实参 ✓（已随 STEP1 前置完成）
- [x] obs Query / shield QueryEvents 行级 geo 回填（Lookup 注入，nil 安全）✓（fillGeoRows ×2，go test 通过）
- [x] TrafficGeo 适配 ✓（country 级附 country_name；go test TestTrafficScriptsSQLite 通过）
- [x] 测试更新全绿 ✓（go test ./plugins/... ./cmd/rocksys/ -count=1）

## 验证
- 接手核实命令：`go test ./plugins/obs/ ./plugins/shield/ -count=1`
- 本步完整验证：
  - [ ] `go build -tags dev -o bin/rocksys.exe ./cmd/rocksys`
  - [ ] `go test ./plugins/... -count=1`

## 完成标准
- 两明细端点行含 country_code/country_name/province/city；库列不再写入 geo；聚合端点输出与前端 geomap 输入约定一致（世界=ISO2、中国=中文省全称）。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增函数：obs 行级 geo 回填（fallbackGeoRows 之类）、shield QueryEvents 回填
### 偏差与现场记录
- 写路径去除已随 STEP1 前置（编译边界），本步实际交付=读侧回填与聚合端点适配。
- obs normalizeRowTypes 增加未注册 key 的 []byte→string 兜底（JOIN 出的 geo 列不在维度注册表，防 PG 驱动 []byte 变 JSON base64）。
- TrafficGeo country 级输出新增 country_name 字段（聚合 SQL MAX(country_name) 带回），前端 Top 列表展示中文名（STEP5 消费）。
- shield 包另有一份 fillGeoRows（*geoip.Resolver 为参，与 obs 各自包内实现，避免跨包依赖）。
