# STEP6：前端「表数据」页签 + 四卡 + 共用提交→轮询组件

状态：待实施

## 目标
`webui/assets/js/views/database.js`：页签 schema→overview→data→history；data 页签四卡（数据源 / 表结构对齐 / 数据迁移 / GeoIP 数据同步——整卡自 overview 移入并改名）；新增共用「提交→轮询→进度/结果展示」组件（含 D29 页面恢复：加载时查 /admin/tasks 按 CreatedBy 匹配续轮询）；SQL 执行区「后台执行」开关。

## 改动文件清单
- 修改 `webui/assets/js/views/database.js`
- 新增共用组件函数（放 database.js 内或 ui.js——按现有结构最小改动定，回填记录）

## 实施步骤（完成一项立即勾选保存）
- [ ] 页签顺序与 state.tab 'data'
- [ ] 共用任务轮询组件（提交→轮询 /admin/tasks/{id}→进度/结果渲染；页面恢复）
- [ ] 数据源卡（列表脱敏/添加/删除/连通测试）
- [ ] 表结构对齐卡（下拉选目标→检查差异→执行对齐确认）
- [ ] 数据迁移卡（源/目标/表多选/批次输入+备注/冲突策略→启动→表级进度区、单表/整任务取消）
- [ ] GeoIP 卡迁入改名，接入任务化交互；overview 恢复纯概览
- [ ] SQL 历史页执行区「后台执行」开关

## 验证
- 接手核实命令：`node --check webui/assets/js/views/database.js`
- 本步完整验证：
  - [ ] dev 构建运行，浏览器实看四卡渲染与交互（截图留证）

## 完成标准
- 语法校验通过；浏览器实看页面无 JS 报错、四卡可用（完整链路验收归 STEP7）。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增函数：dataHTML / renderDsnCard / renderAlignCard / renderMigrateCard / taskPoller（名随实现更正）
### 偏差与现场记录
- （无）
