# STEP4：读侧——明细端点 JOIN + 回退 Lookup + 聚合端点适配

状态：待实施

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
- [ ] obs OnDone 去 geo 写入；dim 注册表去 country/city
- [ ] shield newGeoEvent 去解析；writeBatch 去 country/city 实参
- [ ] obs Query / shield QueryEvents 行级 geo 回填（Lookup 注入，nil 安全）
- [ ] TrafficGeo 适配（country 级输出 country_code+country_name；province 级 g.province；空值兜底「未知/中国」）
- [ ] 测试更新全绿

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
- （无）
