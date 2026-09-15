# STEP5：前端——#/schedule 只读页 + 数据库页/概览卡/明细字段适配

状态：待实施

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
- [ ] schedule.js 只读表（名称/说明/类型/关联开关/上次执行时间；未登记标注；opts 透传；失败 toast error）
- [ ] index.html + main.js + state.js 路由接线
- [ ] database.js 同步卡适配
- [ ] overview.js 地理位置卡同步按钮（复用 POST /admin/db/geoip_sync，30s 超时，成功后刷新卡）
- [ ] logs.js / waf.js / topIPs.js 字段与兜底展示
- [ ] 浏览器实看截图（终验统一，本步先自验）

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
- （无）
