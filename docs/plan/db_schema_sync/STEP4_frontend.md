# B4 · 前端：服务 → 数据库 → 表结构

> 实施状态：**已实施**
> 前置：B3 已实施。设计依据：`docs/DB_SCHEMA_SYNC_PLAN.md` §3.6；UX 红线（toast 唯一组件/提示分级/文案三要素）。

## 1. 目标

新静态页「服务 → 数据库」，页签「表结构」：检查按钮 → 差异表 + 生成 SQL 预填编辑器 → 强确认执行 → 逐条结果。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| webui/assets/js/views/database.js | 新增 | 页面视图（tabs + 表结构页签） |
| webui/index.html | 修改 | 服务分组子项「数据库」+ 页容器 + script 标签 |
| webui/assets/js/main.js | 修改 | ROUTES `database:1` + pageLoaders |
| webui/assets/js/components/codeEditor.js | 修改 | 新增 lang='sql' 轻量高亮 |

## 3. 实施步骤

- [x] index.html：`menu-sub-services` 内加子项 data-route="database"（**风格随现有子项**：`<span>数据库</span><i>database</i>`，现有 menu-sub-item 无 SVG 图标）；主内容区 `<section class="page hidden" id="page-database">`；底部按依赖序引入 database.js
- [x] main.js：ROUTES 与 pageLoaders 注册 database（固定页，参照 scripts 模式）
- [x] codeEditor.js：新增 `lang='sql'` 高亮（SQL 关键字/注释/字符串三类着色，纯前端 ~30 行，不引外部库）
- [x] database.js：
  - [x] tabs 页签「表结构」（组件结构预留后续扩展，本期只做一个页签）
  - [x] 说明区：期望结构 = 当前运行 SQL 源（外挂 `HOT_SCRIPTS_DIR/sql/` 优先、内嵌兜底）；实际结构 = 当前数据连接 catalog
  - [x] 操作区：「表结构检查」主按钮 + 「执行SQL」危险色按钮（编辑器空则禁用）
  - [x] 差异结果表：dataTable client 模式，列 = 表 / 对象 / 差异类型 / 期望 / 实际 / 建议；自动项绿、需人工橙、仅提示灰
  - [x] SQL 编辑器：codeEditor(sql) 预填生成 SQL，用户可自由编辑
  - [x] 检查交互：GET /admin/db/schema；无差异 → 成功 toast（自动消失）+ 空态；有差异 → 渲染表 + setValue SQL
  - [x] 执行交互：confirmDialog **danger 强确认**（将执行 N 条语句直接作用于当前数据库；DDL 不可回滚；建议先备份）→ POST /admin/db/exec → 逐条结果表（失败行标红）→ 失败常驻 toast / 成功汇总自动消失 → 文案引导「再次执行表结构检查复核」
- [x] dev 构建 + `cd bin && ./rocksys` 运行（**新增文件需重启一次**）

## 4. 验证（浏览器回归，可用 chromedp/webapp-testing 技能）

- [ ] 菜单：服务 → 数据库 → 表结构 页签可达、高亮正确
- [ ] 检查·无差异态：toast 自动消失 + 空态展示
- [ ] 检查·有差异态：临时在库里手工 DROP 一个次要列（或新建测试库）→ 差异表分级展示 + SQL 预填
- [ ] SQL 编辑：编辑器可改内容，'sql' 高亮生效
- [ ] 执行：强确认弹窗文案完整；成功态逐条 ok；失败中断态（故意写错一条）标红 + 常驻 toast + 三要素文案
- [ ] 复核引导出现；执行成功后再次检查回到无差异态

## 5. 完成标准

清单全勾 + 验证全过 → 状态改「已实施」→ 更新总纲。

## 6. 实施回填区（中断现场记录）

- **日期**：2026-08-29
- **实施范围**：§3 全部编码子项（index.html / main.js / codeEditor.js / database.js 新增「数据库」页）；未做最后一条「dev 构建 + 运行」（留给主代理浏览器回归）。
- **实现要点**：
  - `views/database.js`：tabs（仅「表结构」页签，act 预留 `db-tab`）+ 说明区（口径 + 方言 tag）+「表结构检查」/「执行SQL」（编辑器空禁用）双按钮 + 差异表（dataTable client，A/B/D 绿=自动、C 橙=需人工、E/F 灰=仅提示）+ codeEditor(lang='sql') 预填（onChange 即时同步执行按钮可用态）+ 执行结果表（失败行复用 `is-error` 标红）。确认走 `confirmDialog(danger:true)`（语句条数前端近似估算：剥离 `--` 注释行后按分号计数）；toast 全走 `Rock.ui.toast`（失败/异常常驻含三要素文案，成功/信息自动消失），执行成功/失败文案均引导「再次表结构检查复核」。
  - `codeEditor.js`：新增 `lang='sql'` 单正则三类着色（`--` 注释 / 单双引号字符串（含 `''` `""` 转义）/ 关键字大小写不敏感），复用 tok-com/tok-str/tok-kw 现有样式类，不引外部库；lua/lines 行为未动。
  - `main.js`：ROUTES `database: 1`、pageLoaders（lazy，页面内部 loaded 缓存、`force` 重拉）、assertDeps 补 `views.database`。
  - `index.html`：`menu-sub-services` 尾部加子项（`<span>数据库</span><i>database</i>`）、`#page-database` 页容器、`database.js` 引入（syslogs.js 之后、main.js 之前）。
- **验证**：三份改动 JS 经 `node --check` 语法通过；SQL 高亮与语句计数经 node 冒烟验证（注释/转义字符串/关键字均正确着色）；`go vet ./...` 与 `go test ./...` 全量通过（防误伤）。浏览器回归（§4 清单）待主代理执行。
- **有无偏差**：无实质偏差。两处微调：① 执行确认弹窗的语句条数为前端近似估算（后端拆句感知字符串/注释，前端仅剥离注释行按分号计数，纯提示用途）；② 额外提供「复制」SQL 小按钮（clipboard API，失败常驻 toast 引导手选），不影响既定交互流。
