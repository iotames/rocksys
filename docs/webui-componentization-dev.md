# RockSys WebUI 组件化重构 — 实施文档

> 供落地区域 Agent 直接自主开发，无需人工决策。
> 交付物同时含：每阶段函数签名、数据结构、集成点（文件:行）、改动前后对照、验收标准（含浏览器 QA）。

**总设计见** `docs/webui-componentization.md`。本文件是它的落地展开，阶段 P1→P5 串行依赖，请勿并发改同一视图文件。

---

## 0. 全局约定（每阶段都必须遵守）

1. **不改行为**：输出 HTML/文本与重构前逐字符等价（测通过后截图/结构比对）。
2. **不碰后端**：`cmd/ targets/ cmd/ plugins/` 等目录一律不动。只改 `webui/`。
3. **命名**：新组件一律 `window.Rock.comp.*`；文件放 `webui/assets/js/components/*.js`。
4. **样式**：一律走 CSS 变量，禁止新硬编码色值。
5. **data-act**：所有 `[data-act="…"]` 属性 `name` 与目标动作**保持不变**。
6. **验证**：每个 P 结尾执行
   - `cd /persistent/home/hankin/projects/rocksys && go build ./...`（校验 embed 打包）
   - 重启测试服务器：`/tmp/opencode/rocksys-test --config /tmp/opencode/rocksys-e2e/test.env`
   - Chrome CDP 加载 7 个路由（#/overview #/components #/config #/scripts #/metrics #/logs #/syslogs），控制台 **无 error**。
   - 按对应阶段回归清单交互。

---

## P1 — 组件基座：page-head / 空态 / 下拉

**目标**：交付最小可用组件层并让 7 个视图的重复样板收敛到它们。零行为风险。

### P1.1 新增文件（3 个）

**`webui/assets/js/components/head.js`** → 命名空间 `Rock.comp.head`
```js
(NEU) function headHTML({ title, desc, actions }) { /* 返回 <div class="page-head">…</div> */ }
window.Rock.comp = window.Rock.comp || {};
window.Rock.comp.head = { headHTML };
```
- `actions`：字符串（已含完整按钮 HTML），未传则省略 `<button>` 区。
- 输出结构还原原 `page-head`（`.page-title` `.page-desc` 在左侧子 div，`actions` 在其后）。esc 处理 title/desc。

**`webui/assets/js/components/empty.js`** → `Rock.comp.empty`
```js
(message 函数) function message({ text, action, br }) { return '<div class="empty">' + esc(text) + (br?'<br>':'') + (action||'') + '</div>'; }
function emptyCard(opt) { return '<div class="card">' + message(opt) + '</div>'; }
window.Rock.comp.empty = { message, emptyCard };
```
- `br` 仅用于还原既有 `<br>` 布局（概览/组件/配置/指标的空卡里）。logs 的「重试」在 item 后有 `<br>`。

**`webui/assets/js/components/select.js`** → `Rock.comp.selectOptions`
```js
function options(list, selected) { return (list||[]).map(o=> '<option value="'+esc(o[0])+'"'+(selected===o[0]?' selected':'')+'>'+esc(o[1])+'</option>').join(''); }
window.Rock.comp.selectOptions = options; // 直接导出函数，或挂 { options } 均可，选定一种：挂 Rock.comp.select.options
```

> **推荐统一**：`Rock.comp.select.options(list, selected)`。下方集成点皆按此引用。

### P1.2 加载顺序（`webui/index.html`）
在 `state.js`（第 224 行 `<script src="./assets/js/state.js">`）之后、`views/overview.js`（第 233行）之前插入三行：
```html
<script src="./assets/js/components/head.js"></script>
<script src="./assets/js/components/empty.js"></script>
<script src="./assets/js/components/select.js"></script>
```
（实际行号以文件为准，做「state.js 后 + views 前」即可，不必刻意依赖绝对行号。）

### P1.3 视图改造点（文件:行 —— 改成什么）

**`views/overview.js`**
- 行 133-135 失败空卡：替换为 `host.innerHTML = Rock.comp.empty.emptyCard({ text:'管理接口不可达，无法加载概览数据。', action:'<button class="btn btn-sm btn-primary" data-act="overview-reload">重试</button>', br:true });`
- 行 166-167 `暂无指标数据` 空卡：`Rock.comp.empty.message({ text:'暂无指标数据' })`（替换其原 `<div class="empty" style=...>`，注意还原 padding 差异——原行内 `style="padding:24px 8px"`，新 helper 需保留空态默认 `.empty` 样式，确认与默认无冲突；若需行内 padding 由调用透传）。
- 行 211-216 的 `page-head`：`headHTML({ title:'概览', desc:'…', actions:'<button class="btn btn-sm" data-act="overview-reload">⟳ 刷新</button>' })`。
- 行 195「暂无组件数据」：`empty.message({text:'暂无组件数据'})`。

2. `views/components.js`
- 行 96-99 失败空卡 → `emptyCard({text:'管理接口不可达，无法加载组件列表。', action: retry 按钮, br:true})`。
- 行 126-129 `page-head` → `headHTML({title:'组件', desc:'…'', actions: 刷新按钮})`。
- 行 118-123 `kindOpts` 构建 → `Rock.comp.select.options([['all','全部'],['middleware','链中间件'],['component','独立组件']], compFilter.kind)`，供 `<select id="comp-kind">`。
- 行 139 空态 → `empty.message({text:'没有符合条件的组件'})`。

**3. `views/config.js`**
- 行 123-127 失败头 + 空卡头：`headHTML({title:'配置', desc:'…', actions:用手刷新按钮})` + `emptyCard({text:'管理接口不可达…', action:重试, br:true})`。
- 行 139-142 正常 `page-head` → `headHTML({..., actions: config-reload 按钮})`。
- （可选，P1 做或 P2）行 192：枚举下拉 option 用 `Rock.comp.select.options`。

**4. `views/scripts.js`**
- 行 76-78 与 116-119 两个 `page-head` → `headHTML`。
- 行 94 / 124 空态 → `empty.message`。

**5. `views/metrics.js`**
- 行 112-113 与 121-124 两个 `page-head` → `headHTML`。
- 行 114-115 失败空卡 → `emptyCard`。

**6. `views/logs.js`**
- 行 284-287 `page-head` → `headHTML`。
- 行 130-138 storage 提示暂不动（不是空态卡）。
- 行 240/248/253/257 若干空态块 → `empty.message`（还原各自 action/br：240 无 br 有 go-obs；248 有 br 有 logs-reload；253 无按钮；257 无按钮）。
- 行 270-275 删掉本地 `optionsHTML` 函数，调用处（行 307/308）改为 `Rock.comp.select.options(STATUS_OPTIONS, logsFilter.status)` 等；`STATUS_OPTIONS`/`SORT_OPTIONS` 保留定义。

**7. `views/syslogs.js`**
- 行 261? `page-head`（262-268）→ `headHTML({title:'运行日志', …, actions: 清空按钮+载入历史按钮})`。注意该页 head 有 **2 个按钮**，actions 传两段拼接 HTML 字符串。
- 行 328-330 `levelOptions()` 改为 `Rock.comp.select.options(LEVEL_OPTIONS, null)`（无选中）。

### P1 验收
- `go build` 通过。
- 七个路由 Chrome 加载无 console error。
- 各页 page-head、空态、下拉在深/浅/绿主题下外观与重构前一致（截图比对可选）。
- 题/组件/config 下拉筛选功能正常；syslogs 的级别下拉、logs 的状态/排序下拉 options 数量与选中态不变。

---

## P2 — 指标去耦合 + 组件状态元数据

**目标**：移除 `overview→views.metrics` 依赖；统一指标卡；组件状态 meta 复用。

### P2.1 新增文件（2 个）

**`webui/assets/js/components/componentState.js`** → `Rock.comp.componentState`
```js
stateMeta(state)  // enabled→{text:'已启用',dot:'dot-ok',tag:'tag-green'}; draining→{text:'切换中',dot:'dot-warn',tag:'tag-orange'}; 其余→{text:'已关闭',dot:'dot-off',tag:'tag-gray'}
meta(name)        // COMPONENT_META[name] 或 {title:name, desc:'', slotLabel: kind==='component'?'独立组件':'链中间件'}
```

**`webui/assets/js/components/metrics.js`** → `Rock.comp.metrics`
```js
pushSample(m)              // 写 store.metricsHistory：push {t, qps, p50,p95,p99, err}；>240 shift()
delta(history?)            // 默认读 store.metricsHistory；len<2 或首条 qps<=0 → {delta:null}；否则 pct=((last-first)/first)*100; {delta:{txt,cls}}
fmtQps(qps)                // qps>=1→fmtInt; else toFixed(2)
metricTiles({ obsOff })    // obsOff→5 em-dash 卡；!store.metrics→<div class="empty">暂无指标数据</div>；否则 5 卡(每秒请求带 delta)
```

### P2.2 视图改造（集成点）
1. `views/metrics.js`
- 行 49-60 `pushSample`、行 63-72 `delta`、行 76-80 `fmtQps`、行 82-105 `metricTilesHTML`：整体**移除**，改为内部函数转发/直接使用 `Rock.comp.metrics`（`load()` 内 `pushSample` → `comp.metrics.pushSample`；`render()` 内 `metricTilesHTML(obsOff)` → `comp.metrics.metricTiles({obsOff})`）。
- 导出清单（行 231-241）移除 `delta`/`fmtQps`/`metricTilesHTML`（保留 `load/render/skeleton/pushSample/drawChart`。pushSample 作为 view 级 api 保留给 overview 兼容，内部只是转发。若坚持无 legacy 可连 pushSample 也去掉，改 overview 直调 comp——本文档**决策 D5** 选：overview 直调 comp，不再经 views.metrics）。

2. `views/overview.js`
- 行 159 `pushSample` 调用改 `Rock.comp.metrics.pushSample(store.metrics)`。
- 行 170-176 指标区：用 `comp.metrics.metricTiles({obsOff: store.metricsError==='obs'})` 生成，替换 local delta/fmtQps 及 map（确保 overview 的 `obsOff` 分支原逻辑保留：overview 那时显示「去组件页开启」空卡而非 em-dash；调整：`metricTiles` 仅供**有数据分支**；overview 的 `metricsOff` 分支维持其原来自有空卡 HTML，不改为 em-dash。因此在 overview 中调用 `comp.metrics.metricTiles({obsOff:false})` 以仅取得数据卡）。
- 行 199-205（`compBody`）：状态 dot 类改为 `comp.componentState.stateMeta(c.state).dot`，name/slot 用 `comp.componentState.meta(c.name)`。

3. `views/components.js`
- 行 52-56 `compStateMeta` 删除，`compCardHTML` 内改用 `comp.componentState.stateMeta(s.state)` 与 `comp.componentState.meta(s.name)`。

4.（净）确认 `overview.js` 顶部对 `Rock.views.metrics` 的引用全部移除。

### P2 验收
- overview/metrics 两页指标卡 DOM 与重构前等价（数值、delta 箭号/色彩、缺数据文案）。
- overview 组件总览与 components 组件页状态颜色一致（enabled/draining/disabled 三态）。
- 全路由无 console error；自动刷新 5s 下 metrics 历史箭头 delta 正常更新。

---

## P3 — 图表组件（Canvas 折线）

**目标**：把 `views/metrics.js` 的 ~85 行 drawChart 抽成可复用 `comp.chart` 并转为主题变量色。

### P3.1 新增文件 `webui/assets/js/components/chart.js` → `Rock.comp.chart`
```js
line(canvas, { data, value })   // data: [{t,..}], value:(p)=>number，用 store.metricsHistory 实参；
                                // Canvas 坐标布局逐字还原原 drawChart；X 轴用 fmtClock(p.t)；Y 轴 max*1.15
fmtClock(ts)                    // toDate→HH:MM:SS
cssVar(name)                    // getComputedStyle(document.documentElement).getPropertyValue(name)
```
- 颜色：描边/渐变取 `cssVar('--primary')`，网格/轴文字取 `cssVar('--text-2')`/透明化；`.5 alpha` 网格用 8 位 hex + alpha 或 `rgba()`——建议用变量基础上做 40% alpha（如 `color-mix` 或直接在 rgba 字符串拼接十六进制），落地时锁定与 `--primary` 一致的色相。
- 还原所有绘制细节：padL=46 padR=12 padT=14 padB=26、4 档 Y 网格、X 三档时间刻度、渐变面积、1.8px 折线、最新点圆点，dpr 处理，尺寸 0 保护。

### P3.2 改造点 `views/metrics.js`
- 行 139-224 `drawChart` 主体删除；`render()` 中的 `drawChart()` 调用改为 `comp.chart.line($('#metrics-chart'), { data: store.metricsHistory, value: p=>p.qps })`。
- 保留一个薄壳导出 `drawChart(canvasEl)`（转调 `comp.chart.line`）以不破坏 `main.js` 的 resize 钩子行 147-149（`views.metrics.drawChart()`）。
- `fmtClock` 从 views/metrics 移入 chart.js（或保留 `comp.chart.fmtClock`）。

### P3.3 加载
index.html 在 components/ select 之后追加 `<script src="./assets/js/components/chart.js">`（P1 已有现成插入点顺序）。

### P3 验收
- 指标页趋势图渲染正确（数据点≥2 时折线+渐变；<2 显示「等待采样」）。
- **切换浅色/深色/护眼绿**后再次进入指标页/监听 resize，折线主色/文字色随主题一致（紫色→深/绿范式），无硬编码蓝残留。
- 切换页面离开再回，resize 触发重绘无 console error。

---

## P4 — 配置渲染器解耦（components→config）

**目标**：把配置项渲染/编辑从 `views/config.js` 下沉 `comp.configEditor`，components 展开区不再引用 `Rock.views.config`。

### P4.0 注意（红线）
`configContainers` 集合、`configEditing/configMask` 状态、`saveEdit` 里 ROCKSYS_ 前缀刷新 `store.base`、404 容错、compact 语义**必须 1:1 迁移**。寨见 §5.4 主文档。

### P4.1 新增文件 `webui/assets/js/components/configEditor.js` → `Rock.comp.configEditor`
- 内部模块级状态：`configEditing {key,value}`、`configMask`、`configContainers = new Set()`。
- 迁移（源=views/config.js:177 至 :330）：
  - `configRowHTML`（含掩码/枚举/restart/编辑态）
  - `renderConfigItems`→`render(container, items, opts)`（注册 container、compact、绑定 input/select、焦点）
  - `refreshAllConfigContainers`→`refresh()`
  - `startEdit(cancelEdit→saveEdit(key)`、`resetItem(key)`、`toggleMask(key)`（内部 `findConfig/updateConfig/mask/configTip` 一并迁来）
- 依赖 `Rock.api`(PUT /config)、`Rock.ui.toast`、`Rock.state`（isSensitiveKey/RESTART_KEYS/ENUM_KEYS）。

### P4.2 改造 `views/config.js`
- 删除上面迁走的核心函数；引入 `const ce = Rock.comp.configEditor`。
- `render()` 内 `renderConfigItems(panel, items)`→`ce.render(panel, items,{compact:false})`。
- 保留对外薄壳（因 `main.js` data-act 现调 `views.config.startEdit/cancelEdit/saveEdit/resetItem/toggleMask`，行 230-245）：
```js
startEdit(key){ce.startEdit(key)} 等 5 个薄壳；以及 renderConfigItems→ce.render
```
（或由 main.js 直接改调 `Rock.comp.configEditor.*` — 二选一，**建议改 main.js 委托**，删除 views.config 薄壳，更彻底去噪；但需同步改 main.js 行 230-245 的 5 处 `views.config.*`。）

### P4.3 改造 `views/components.js`
- 行 190 `Rock.views.config.renderConfigItems(panel, items, {compact:true})` → `Rock.comp.configEditor.render(panel, items, {compact:true})`；行 183 `Rock.views.config.loadList()` 改为 `Rock.comp.configEditor.loadList&&ensureConfigList()`（在 configEditor 内实现懒加载 `api.get('/admin/config/list')`，统一两页）。由此 `views/components` 对 `Rock.views.config` 的 import 归零。

### P4.4 加载
在 components/ 清单追加 `configEditor.js`（需在 metrics/之后、views/*之前）。

### P4 验收
- 配置页整体可用：切页签、改值保存、恢复默认、掩码切换、敏感/restart 标记样式。
- **组件页展开某组件配置区**：与配置页同 key 同当前值；在配置页改一处后返回组件页展开，值已同步（验证 configContainers 共享语义）；组件页保存亦生效并更新配置页。
- `ROCKSYS_` key 保存后概览页 `store.base` 同步更新（saveEdit 那行逻辑保留）。
- main.js 委托 5 动作仍工作；`go build` 通过。

---

## P5 — 收尾回归

1. **全量构建**：`go build ./...`，重启测试服务器，`//go:embed` 打包新 `components/` 生效确认（f12 能看到 components 脚本）。
2. **全路由无错**：7 页面逐一打开，console 无 error、无404。
3. **主题回归**：浅/深/绿三态切换后：page 样式、指标卡、图表、下拉、配置行均正常且用变量（无硬编码）。
4. **交互回归**：
   - 概览：一次刷新/失败重试/降级链渲染。
   - 组件：筛选（类型+搜索）、开关二次确认、失败态还原、展开/收起配置、配置编辑。
   - 配置：分页、恢复默认二次确认、掩码、保存即时生效。
   - 脚本：列表选中、新建、语法校验（通过/未通过）、发布、版本回滚、移除。
   - 指标：自动刷新历史累积、趋势流畅、window resize 重绘。
   - 日志：时间/路径查询、状态/排序/只看异常、展开行详情、导出下载、存储占用。
   - 运行日志：开始/暂停实时、自动滚动、载入历史、级别热切、文件存档开关、**离开页面后后台 stream 关闭**（无泄漏）。
   - 顶栏：访问凭证 dialog 保存/清除后刷新；自动刷新时间档；手动刷新按钮。
5. **静态检查**：`grep -nE '#[0-9a-fA-F]{3,6}|rgb\\(' webui/assets/js/components/`（应只剩 token 常量具名、无裸颜色——chart 用变量）。
6. 记录一次全量截图留档，与重构前对照（无感知差异）。

---

## 附录 A — 落地任务拆分（给 Atlas 派发）

| 任务 | 范围 | 派发子代理 | 依赖 | 验收 |
|---|---|---|---|---|
| T1 | P1 组件基座 + 7 视图 head/空态/下拉 | exec | 无 | 无 console error、视觉等价 |
| T2 | P2 指标解耦 + 状态元数据 | exec | T1 | metrics/overview 去耦合、指标卡等价 |
| T3 | P3 图表组件 | exec | T2(T2改overview/metrics, T3改metrics—需 T2 后) | 主题自适应折线 |
| T4 | P4 配置渲染器解耦 | exec | T1 | components↛config、容器共享正常 |
| T5 | P5 全量回归 + 截图 | exec 或只读 | T1-T4 | §6.4 清单全过 |
| T6 | （可选）最终只读核对 | oracle | T5 | 文档-代码一致性 |

> 串行约束：T2→T3 因都触碰 `views/metrics.js`，须按序；T1→T4 与 T1 独立，可与 T2/T3 并行（不碰 metrics/overview），但建议按 P 顺序以简化回归。

## 附录 B — 关闭项
- P1 结束时 `styles.css` **无需改动**（新组件全部复用既有 class）。
- `main.js` 仅在 P4 视所选方案改 5 处 `views.config.*` 委托（或启用薄壳则零改）。
- `theme.js/api.js/util.js/state.js` **零改动**。