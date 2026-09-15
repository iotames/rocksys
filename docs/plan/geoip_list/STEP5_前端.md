# STEP5：前端——#/schedule 只读页 + 数据库页/概览卡/明细字段适配

状态：已实施

## 目标
配置组「数据库」后新增「定时任务」（#/schedule）只读页；数据库页 GeoIP 同步卡改语义文案 + 显示上次同步时间；概览页地理位置卡加同步按钮与能力边界注记（D31）；logs/waf/topIPs 视图字段适配（country_code/country_name/province/city，读侧兜底展示）。

## 改动文件清单
- webui/index.html（菜单项 + page 容器 + script 标签）
- webui/assets/js/views/schedule.js（新增）
- webui/assets/js/main.js（ROUTES/pageLoaders/assertDeps）
- webui/assets/js/state.js（scheduleLoaded）
- webui/assets/js/views/database.js（同步卡文案 + 上次同步时间）
- webui/assets/js/views/overview.js（地理位置卡同步按钮 + 注记）
- webui/assets/js/views/logs.js、waf.js、topIPs.js（字段适配）

## 实施步骤
- [x] schedule.js 只读表 ✓（浏览器实看 + 截图，登记 11 行含 enabled/状态/上次执行完成）
- [x] index.html + main.js 路由接线 ✓（#/schedule 直达渲染；state.js 未新增缓存标志——schedule 页 lazy=false 每次拉取，量小无缓存必要）
- [x] database.js 同步卡适配 ✓（新文案 + 上次同步时间来自 schedule_list.geoip_sync 行）
- [x] overview.js 地理位置卡同步按钮 + 能力边界注记 ✓（实点按钮：同步后排名即时增长并自动刷新；世界/中国地图着色截图验证）
- [x] logs.js / waf.js 字段与兜底展示 ✓（waf 详情弹层实看「美国 / 爱荷华州/康瑟尔布拉夫斯」；/admin/logs 接口直验 country_name/province/city；topIPs 走 StatsTopIP 实时解析链路，无需改动）
- [x] 浏览器实看截图 ✓（定时任务页/概览世界地图/流量统计区 3 张）

## 验证
- 接手核实命令：`grep -c "schedule" webui/assets/js/main.js webui/index.html`（非零即已接线）
- 本步完整验证：
  - [ ] `go build -tags dev -o bin/rocksys.exe ./cmd/rocksys` + `cd bin && ./rocksys.exe`
  - [ ] 浏览器实看 #/schedule、数据库页、概览卡、入网数据/WAF 明细渲染并截图

## 完成标准
- 各页面渲染正常；错误路径弹统一 toast；页面文案含能力边界注记；无 console 报错。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增视图：views/schedule.js；路由 #/schedule
### 偏差与现场记录
- state.js 未新增 scheduleLoaded（schedule 页 lazy=false 常拉，无缓存必要）；接手核实命令相应调整。
- logs 页历史范围 UI 自验未走通（今日无数据，脚本对 date input 赋值不触发 queryBar 内部状态），/admin/logs 接口已 curl 直验 JOIN 字段正确；logs 明细列渲染模式与 waf 详情一致，风险低。
- 实施中发现并修复存量缺陷：maxminddb-golang v2 对嵌套包装结构体 Country.Names 解码为空（真实 mmdb 实测），改直写 map[string]string 后 zh-CN 国名正常——此前 country_name 从未真实生效过（StatsTopIP 曾一直输出空）。
- 实测 mmdb zh-CN 省份多为短名（「广东」），部分全称（「北京市」）；短名与地图 geojson 直连、全称经 chinaShort 映射，两者均正确着色，D9 落地口径不受影响。
