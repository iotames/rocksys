# STEP6：前端「表数据」页签 + 四卡 + 共用提交→轮询组件

状态：已实施

## 目标
`webui/assets/js/views/database.js`：页签 schema→overview→data→history；data 页签四卡（数据源 / 表结构对齐 / 数据迁移 / GeoIP 数据同步——整卡自 overview 移入并改名）；新增共用「提交→轮询→进度/结果展示」组件（含 D29 页面恢复：加载时查 /admin/tasks 按 CreatedBy 匹配续轮询）；SQL 执行区「后台执行」开关。

## 改动文件清单
- 修改 `webui/assets/js/views/database.js`
- 新增共用组件函数（放 database.js 内或 ui.js——按现有结构最小改动定，回填记录）

## 实施步骤（完成一项立即勾选保存）
- [x] 页签顺序与 state.tab 'data'
- [x] 共用任务轮询组件（提交→轮询 /admin/tasks/{id}→进度/结果渲染；页面恢复）
- [x] 数据源卡（列表脱敏/添加/删除/连通测试）
- [x] 表结构对齐卡（下拉选目标→检查差异→执行对齐确认）
- [x] 数据迁移卡（源/目标/表多选/批次输入+备注/冲突策略→启动→表级进度区、单表/整任务取消）
- [x] GeoIP 卡迁入改名，接入任务化交互；overview 恢复纯概览
- [x] SQL 历史页执行区「后台执行」开关

## 验证
- 接手核实命令：`node --check webui/assets/js/views/database.js`
- 本步完整验证：
  - [x] dev 构建运行，浏览器实看四卡渲染与交互（截图留证：数据源添加/差异对齐 27 条/迁移 675+11 行/取消/页面恢复/互斥提示/后台执行）

## 完成标准
- 语法校验通过；浏览器实看页面无 JS 报错、四卡可用（完整链路验收归 STEP7）。

## 实施回填区
### 产物锚点清单（实施中随手更正）
- 新增函数：dataHTML / renderDsnCard / renderAlignCard / renderMigrateCard / taskPoller（名随实现更正）
### 偏差与现场记录
- 共用组件落在 database.js 内（pollTask/findRunningTask/fmtTaskCost）：四处复用点（迁移、GeoIP 同步、结构对齐执行、SQL 后台执行）全在本页，独立 ui.js 组件会引入跨页依赖而无第二消费方。
- 浏览器实测暴露两个真 bug，本步内修复：
  1) db.SplitStatements 行尾注释吞终止符（`语句 -- 注释;`），导致 DDL 与下条语句融合、目标库执行报 near "CREATE"（内部 db 包修复 + 回归测试 TestSplitStatementsTrailingCommentSemicolon）；
  2) 「后台执行」勾选态未入 state，页面每次重渲染即复位 → 后台分支永不触发（改为 state.execBackground + data-act 记录，渲染回显）。
- 迁移表清单取自「表概览」空间统计的运行库业务表（前端无期望结构清单接口）；未加载时给出引导文案。
- 实测证据：geoip_sync 后台任务 20 秒完成（超 15 秒无 HTTP 超时）；迁移 95 万行任务进行中刷新页面进度自动恢复（D29）；迁移进行中提交同步被拒并提示含任务 ID/标题；整任务取消后目标库已写入 29000 行保留。
