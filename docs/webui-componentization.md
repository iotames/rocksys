# RockSys WebUI 组件化重构 — 设计文档

> 技术负责人：Keel（龙骨）｜文档性质：设计蓝图 + 实施安排（不写实现代码）
> 关联文档：`docs/webui-componentization-dev.md`（分阶段实施文档，供落地区域 Agent 直接开发）
> 目标：对前端现有功能做**高内聚、低耦合**的组件化封装，**不新增任何用户可见功能**。

---

## 1. 现状

### 1.1 技术面貌
- 纯静态单页控制台：HTML + CSS + 原生 JS，**无任何框架**（Vue/React 不允许引入）、无构建链、无第三方库。
- 模块约定：每个模块是 IIFE，挂载到全局命名空间 `window.Rock.*`。
- 加载顺序：`index.html` 中 `<script>` 按依赖顺序排列（util → theme → ui → api → state → views/* → main）。
- 样式：一律引用 `:root` 及各主题覆盖块的**语义化 CSS 变量**（`--bg/--panel/--border/--text/--primary/--ok/--warn/--danger/--text-2` 等），不得硬编码颜色。
- 内嵌方式：`webui/embed.go` 的 `//go:embed index.html assets` 自动包含新文件，**无需改 embed.go**，但需重新 `go build` 生效。
- 全局事件委托：`main.js` 用 `document.addEventListener('click'|'change')` 统一分发 `[data-act]` 动作到 `Rock.views.*`。该契约**是红线，不能破坏**。

### 1.2 现有文件（13 个 JS，约 4800 行）
| 文件 | 行数 | 职责 | 命名空间 |
|---|---|---|---|
| util.js | 107 | 纯函数：$ $$ esc fmt* debounce insertAtCursor | Rock.util |
| theme.js | 61 | 主题切换 | Rock.theme |
| ui.js | 222 | toast/confirmDialog/openModal/skeleton/markUnreachable/tokenDialog | Rock.ui |
| api.js | 110 | fetch 封装/token/401/503 | Rock.api |
| state.js | 156 | store/常量/标准化/fmtRate | Rock.state |
| views/overview.js | 238 | 概览页 | Rock.views.overview |
| views/components.js | 201 | 组件页（组件开关) | Rock.views.components |
| views/config.js | 350 | 配置页 + 共享配置渲染器 | Rock.views.config |
| views/metrics.js | 241 | 指标页 + Canvas 折线图 | Rock.views.metrics |
| views/scripts.js | 369 | 脚本页 + Lua 着色 + 版本 | Rock.views.scripts |
| views/logs.js | 425 | 访问日志页 | Rock.views.logs |
| views/syslogs.js | 408 | 运行日志页（SSE） | Rock.views.syslogs |
| views/auth.js | 165 | 认证视图 | Rock.auth |

---

### 1.3 已核实的重复点 / 耦合点（共 9 项）
对 8 个候选逐一核实，修正并新增 2 项：

| # | 重复/耦合 | 命中位置（文件:行） | 说明 |
|---|---|---|---|
| 1 | **page-head 块**（标题+描述+按钮）复制 | overview:212-215、components:126-129、config:123-127 & 139-142、metrics:113-114 & 121-124、scripts:76-78 & 116-119、logs:284-287、syslogs:262-268 | 7 视图共 10 处；结构同、仅标题/描述/按钮不同 |
| 2 | **空态/重试卡片** `<div class="card"><div class="empty">…<button data-act="X-reload">` | overview:89-93 空 105-106、components:96-98 & 139、config:125-129、metrics:114-115 | 4 视图的「接口不可达 + 重试」模式几乎一致 |
| 3 | **指标卡 metric-tiles / delta / fmtQps** | overview:96-105 与 metrics:45-98 几乎相同；overview 通过 `Rock.views.metrics.delta()/fmtQps()/pushSample()` 跨视图耦合 | **高风险跨视图耦合**，见 §6.1 |
| 4 | **select/options 渲染** | logs:145-151/153-157 + optionsHTML 76-274（改写删函数留调用）、components:20-23 kindOpts、syslogs:29 定义 + levelOptions 24`~~`（主要是`LEVEL_OPTIONS`定义393-406ITEM 4 行选择小二 | 三处 `<option>` 字符串拼装重复 |
| 5 | **组件状态元数据** compStateMeta dot/tag | components:52-56 与 overview:199-205 内联 dotCls | 同为 `enabled→ok/dra或warn/off` 映射 |
| 6 | **Canvas 折线图**（~85 行）+ 硬编码色 | metrics:139-224 | 抽成可复用组件；且当前颜色硬编码 `#2f81f7/#8b949e`，**违反主题 CSS 变量约束** |
| 7 | **skeleton() 样板** | 每个视图各自定义 `skeleton(){host.innerHTML=skeletonHTML(n)}`：overview:67、components:47、config:58、metrics:43、scripts:66、syslogs(隐式) | 6 处重复,仅 `host id + 行数` 不同 |
| 8 | **load() 样板模式** | 各视图 load 首行 first/skeleton + catch + render | 保守处理：**不框架化**，只抽 UI |
| 9 | **（新增）config 渲染器跨视图耦合** | components:178-192 依赖 `Rock.views.config.loadList()/renderConfigItems()` | 组件页配置区复用配置页渲染器 = 跨视图耦合（issues.md 已提醒），抽为独立组件 |

---

## 2. 目标

1. **高内聚**：把重复的 `renderable UI`（page-head、空态、指标卡、图表、select、组件状态、配置编辑器）收敛为命名清晰的独立组件模块。
2. **低耦合**：消除 `views/overview`→`views/metrics`、`views/components`→`views/config` 两类跨视图依赖，改为依赖公共组件层。
3. **保持行为完全一致**：不新增功能、不改任何用户可见行为、不改后端。
4. **合规红线**：不碰后端（`cmd/ internal/ plugins/`）；不引入框架/构建；不破坏 `[data-act]` 事件委托契约、`renderConfigItems` 共享容器注册、syslogs SSE 生命周期。

---

## 3. 拆分思路：通用优先、主次分明、先后有序（设计总纲）

> 本章是本次组件化重构的**设计总纲**，先于 §4 总体方案定基调：封装以「通用前端组件」为第一优先，有主次、有先后，避免把组件做成只服务于本产品（RockSys）的局限业务封装。

### 3.1 封装原则：通用前端组件优先

1. **组件化封装应基于「通用前端组件」，而非基于本产品的局限业务封装。** 判断一个模块是否值得抽成组件，先看它是否产品无关、能否脱离 RockSys 在任意前端复用；只对本产品生效的封装放在业务层，不进通用组件层。
2. **通用组件的判据**：产品无关、入参为通用数据形态（字符串、`[value,label]` 二元组、样本数组 + 取值器等）、不感知接口路径 / 全局 store / 业务命名 / `data-act` 具体动作。
3. **业务语义下沉**：组件名、指标名、配置 key、`/admin/*` 路径等只允许出现在业务组件层与视图层；通用组件保持纯 UI / 纯逻辑抽象。

### 3.2 主次分级：两级组件

| 级 | 组件 | 文件 | 职责（通用/业务判定） |
|---|---|---|---|
| **主（通用，产品无关）** | head | head.js | 页头（标题 + 描述 + 操作区），入参仅 `{title, desc, actions}` |
| | empty | empty.js | 空态/失败提示，入参仅 `{text, action, br, padding}` |
| | select | select.js | 下拉选项渲染，入参仅 `(list, selected)` |
| | chart | chart.js | Canvas 折线图，入参仅 `(canvas, {data, value, …})`，色走 CSS 变量 |
| **次（业务，产品相关）** | componentState | componentState.js | RockSys 组件状态（enabled/draining/disabled）→ dot/tag 映射，绑定 `COMPONENT_META` |
| | metrics | metrics.js | RockSys 指标卡（QPS/延迟分位/错误率）与采样/环比，绑定 `store.metricsHistory` |
| | configEditor | configEditor.js | RockSys 配置项渲染/编辑/掩码/重置（绑定 `/admin/config`、`ENUM_KEYS`、`RESTART_KEYS`） |

- **主**组件应可直接进入任意前端项目复用；**次**组件建立在通用组件之上（如 configEditor 的枚举下拉复用 select、指标卡空态复用 empty），业务组件内不得重复通用逻辑。
- 已落地核验：P1 的三个通用组件（`head/empty/select`）接口均无 RockSys 业务语义，符合「主」级判定，见 §3.7。

### 3.3 先后顺序：先通用、后业务

1. **通用组件是地基，业务组件建立在通用组件之上**——设计与实施都必须先确立通用层，再让业务层引用它，杜绝业务组件与通用组件平级互叠。
2. **当前实施顺序符合此原则**：P1（head/empty/select，纯通用）已先行落地；业务组件 P2（componentState/metrics）、P4（configEditor）与图表 P3（chart，属通用但体量大、唯一消费者为 metrics 页，故独立成 P3）均在通用地基之上展开。主/次划分决定的是**接口性质**，P 顺序还受依赖与体量影响——通用性与排期不冲突（里程碑见 §8）。
3. **后续新增组件一律先做「通用/业务」判定**再决定放通用组件层还是业务层（审视流程见 §3.5）。

### 3.4 通用性约束（接口设计不得携带业务语义）

| 组件 | 允许的入参/输出 | 禁止出现（业务语义） |
|---|---|---|
| head | `title`/`desc`/`actions` 纯字符串 | 不得硬编码「组件/指标/配置」等业务文案；不得硬编码 `data-act` 动作名（由调用方传入 actions） |
| empty | `text`/`action`/`br`/`padding` | 不得出现 `/admin/*`、失败原因文案；action 按钮 HTML 由调用方给出 |
| select | `[value,label]` 列表 + `selected` | 不得出现业务选项标签名 |
| chart | 样本数组 + 取值器 `value` + CSS 变量 | 不得出现 `qps`/`metricsHistory`/指标名 |

业务语义（`COMPONENT_META`、`store.metricsHistory`、`ENUM_KEYS`、`RESTART_KEYS`、`/admin/config` 等）只允许出现在 componentState/metrics/configEditor 层。

### 3.5 对现有划分的审视（通用性自查与调整建议）

1. **componentState —— 半通用半业务，建议按主次拆分**
   - `stateMeta(state) -> {text, dot, tag}`：入参仅是 `state` 字符串，本质是通用「状态徽标」抽象，可提升为通用组件（可更名 `badge`/`status`，映射由调用方注入）。
   - `meta(name)`：查 `COMPONENT_META` + kind 兜底，**纯 RockSys 业务映射**，不应留在通用层——建议下移到业务组件或视图层持有。
   - 调整：把「通用 stateMeta」与「业务 meta」分属两级，避免通用组件携带 `COMPONENT_META`。
2. **metrics —— 建议内部再分「时序采样工具」与「业务指标卡」**
   - `pushSample`/`delta`（环形缓冲、环比）仅依赖通用样本结构（`{t, value}` 数组），可泛化为通用「时序采样」工具，供任意时序场景复用。
   - `metricTiles`/`fmtQps` 绑定 RockSys 指标语义（QPS/延迟分位/错误率），属业务组件层。
   - 调整：metrics 内部分层——通用「时序采样」子能力保持产品无关；业务指标卡只负责组装。若实施成本可控可拆为独立通用组件；否则至少在接口设计上保证采样/环比不出现指标名。
3. **configEditor —— 纯业务组件，无需泛化，但必须建立在通用组件之上**
   - 配置编辑器天然绑定管理后台语义，不做通用化；确认其内部复用 empty/select 等通用组件（如枚举下拉必须用 `select.options`），不重复实现通用逻辑。
4. **head/empty/select/chart —— 已满足通用性**，维护时保持接口纯净即可。

### 3.6 通用性验收（实现检查项）

- 组件文件头注释标注级别：`通用组件（产品无关）` / `业务组件（RockSys 相关）`。
- 回归 grep：`grep -nE "ROCKSYS|/admin/|COMPONENT_META|metricsHistory|data-act" <通用组件文件>` 应**无命中**。
- 通用组件不得引用 `Rock.state.store`、`Rock.views.*`（仅可依赖 `Rock.util` 等基座）。

### 3.7 落地一致性核对（与已实现 P1 对齐）

| 设计文档 §5.1-§5.3 原接口 | 落地实现（webui/assets/js/components/） | 核对 |
|---|---|---|
| `Rock.comp.head.headHTML({title, desc, actions})` | head.js：`Rock.comp.head.headHTML({title, desc, actions})` | ✅ 一致 |
| `Rock.comp.empty.message({text, action, br})` / `emptyCard` | empty.js：`message({text, action, br, padding})` + `emptyCard` | ✅ 一致（落地版额外支持 `padding` 透传，还原既有行内 padding） |
| `Rock.comp.selectOptions.options(list, selected)` | select.js：挂载为 `Rock.comp.select.options(list, selected)` | ⚠️ **命名偏差**：原文档写 `Rock.comp.selectOptions`，落地为 `Rock.comp.select.options`。建议后续视图引用统一为 `Rock.comp.select.options`（本总纲按落地实际书写）。 |

---

## 4. 总体方案

### 4.1 架构分层（从左到右按加载顺序）
```
┌──────────────── Rock 命名空间 ────────────────┐
│  基座层   util · theme · api · state            │  现有，不动
│  ─────────────────────────────────────────────  │
│  组件层   Rock.comp.*  (新建 components/)        │  ← 本次新增
│  ─────────────────────────────────────────────  │
│  视图层   views/*   (overview components … )    │  只改内部实现
│  编排层   main.js (路由 + data-act 委托)         │  只改 1 处 resize 可选
└─────────────────────────────────────────────────┘
```

### 4.2 新组件目录 `webui/assets/js/components/`
七个独立文件，全部挂到 `window.Rock.comp.*`（避免与 `Rock.views.components` 命名冲突）：

| 文件 | 命名空间 | 职责 | 依赖 | 被谁用 |
|---|---|---|---|---|
| head.js | `Rock.comp.head` | page-head 渲染 | utl.esc | overview/components/config/scripts/metrics/logs/syslogs |
| empty.js | `Rock.comp.empty` | 空态/失败卡渲染 | util.esc | overview/components/config/metrics/logs(部分)/scripts |
| select.js | `Rock.comp.selectOptions` | `<option>` 渲染 | util.esc | components/syslogs/logs/config(枚举) |
| componentState.js | `Rock.comp.componentState` | 组件状态→元数据/dot/tag | state | overview/components |
| metrics.js | `Rock.comp.metrics` | 采样累积/环比/format/指标卡 | state | overview/metrics |
| chart.js | `Rock.comp.chart` | Canvas 折线图（主题色） | util | metrics |
| configEditor.js | `Rock.comp.configEditor` | 配置项渲染+行编辑+掩码+重置容器 | state | config/components |

> 组件层**只做渲染 + 纯逻辑 + 本地状态**，不直接发起 API 请求（保持	 view 负责数据装配）。例外 `configEditor` 因复用了 `api.put('/admin/config')` 而内聚了「渲染+保存」原子单元——它本就是由 config 页/组件页共用的业务渲染器（见决策 D4）。

---

## 5. 组件划分 — 接口设计图

### 5.1 `Rock.comp.head`（head.js）
```
headHTML({ title: string, desc: string, actions?: string }) -> string
// actions 为预编译好的按钮 HTML（如 '<button data-act="X-reload">⟳ 刷新</button>'）
// 返回 <div class="page-head"><div><div class="page-title"/><div class="page-desc"/></div>{actions}</div>
```
- 目标 9 处替换：overview/components/config(×2)/metrics(×2)/scripts(×2)/logs/syslogs。

### 5.2 `Rock.comp.empty`（empty.js）
```
message({ text: string, action?: string, br?: boolean }) -> string
// action: 预编译按钮 html；br=true 时在 action 前加 <br>（还原既有布局）
// 返回 <div class="empty">{text}[<br>]{action}</div>
emptyCard({ text, action, br }) -> string
// 返回 <div class="card"> + message(...)；还原「接口不可达 ×2」卡片
```
目标替换：overview、components、config、metrics、logs 的空态/重试块。

### 5.3 `Rock.comp.select`（select.js）
```
options(list: Array<[value,label]>, selected?: string|null) -> string
// 还原 items 保持 selected，等价于 logs.js:optionsHTML、syslogs:levelOptions、components:kindOpts
```
目标替换 3 处 + 枚举下拉 1 处（config 的 ENUM_KEYS 编辑态）。

### 5.4 `Rock.comp.componentState`（componentState.js）→ 消除重复点 #5
```
stateMeta(state: string) -> { text, dot, tag }   // enabled→已启用/dot-ok/tag-green …
meta(name: string) -> { title, desc, slotLabel }  // COMPONENT_META 查 + kind fallback
```
由 overview/components 共用 stateMeta（enabled→dot-ok 等）与 meta 兜底逻辑。

### 5.5 `Rock.comp.metrics`（metrics.js）→ 消除重复点 #3 + 跨视图耦合
```
pushSample(m) -> void                     // 写 store.metricsHistory，上限 240（原 metrics.js）
delta(history?) -> { delta: {txt,cls} } | { delta: null }   // 环比；history 缺省读 store
fmtQps(qps) -> string                     // 大---分位、小数值两位小数
metricTiles({ obsOff }) -> string         // 输出 <div class="metric-grid">…</div>
```
>`metricTiles({obsOff})` 覆盖两个分支：`obsOff`→五个 em-dash 卡（metrics 原样式）；否则无 metrics→「暂无指标」；否则→ 真实 5 卡（QPS 带 delta）。**overview 与 metrics 指标区统一到这个函数**，彻底去掉 overview 对 `Rock.views.metrics` 的依赖。

### 5.6 `Rock.comp.chart`（chart.js）→ 消除重复点 #6 + 主题硬编码
```
line(canvas: HTMLCanvasElement, opts: {
  data: Array,               // store.metricsHistory（或类似 {t,..} 数组）
  value: (sample)=>number,   // 取值器，如 p=>p.qps
  valueLabel?: string,       // Y 轴单位/标题（可选）
  showEmptyText?: boolean,   // data<2 时画「等待采样」
  colorVar?: '--primary',    // 折线/渐变主色取自 CSS 变量
}) -> void
fmtClock(ts) -> 'HH:MM:SS'
```
读取 `getComputedStyle(document.documentElement)` 的语义变量（`--primary/--text-2/--border`），**替换原硬编码**，从而在深/绿主题下自适应。重绘逻辑局宽: 调用方在 resize 时重呼 `line()`。

### 5.7 `Rock.comp.configEditor`（configEditor.js）→ 消除重复点 #9 跨视图耦合
把 `views/config.js` 中的配置**渲染器 + 行编辑 + 掩码 + 容器注册**整体移入独立模块（内部持有原 `configEditing/configMask/configContainers`），config 页与组件页共用同一份状态：
```
render(container, items, opts)   // 原 renderConfigItems（compact 支持）
startEdit(key.clearEdit()
saveEdit(key)                    // PUT /config；ROCKSYS_ 前缀后同步 store.base
resetItem(key)                   // PUT 默认值
toggleMask(key)
refresh()
```
`views/config.js` 保留：分组构建/页签/load/render/`setActiveTab`，向其渲染调用改为 `comp.configEditor.render`；并保留薄壳导出（见 D5）。
`views/components.js` 的展开配置区由 `Rock.views.config.*` 改为 `Rock.comp.configEditor`。

---

## 6. 关键机制

### 6.1 跨视图去耦合（核心价值，两个方向）
- **方向 A（overview→metrics）**：metrics 的历史累积/环比/格式化/指标卡迁入 `comp.metrics`。overview.js 移除 `Rock.views.metrics.delta/fmtQps/pushSample` 三处调用，改调 `comp.metrics`。`views/metrics` 自身也改用 `comp.metrics`。**依赖已是单向公共层，不再有双向耦合。**
- **方向 B（components→config）**：配置渲染与编辑下沉 `comp.configEditor`，`views/components` 不再 import `Rock.views.config`。两页共享同一编辑状态（容器注册表）的行为保留。

### 6.2 事件委托契约（不可破坏）
- `main.js` 的 `[data-act]` 委托到 `Rock.views.*` 的 `load/toggle/saveEdit/…` 保持不变。各视图**保留对 `data-act` 属性名的输出契约**（`cfg-edit/cfg-save/comp-toggle/log-expand/script-*` 等），仅内部实现指向组件层。
- 因此 `main.js` 的委托字典**无改动**（除可选 resize 钩子）。运行 `components/config` 等 `load/toggleConfig` 等原 export 名**保留**（comp.loadConfig 由 config.js 转封），避免大改 main.js。

### 6.3 主题一致性（重难点：Canvas 颜色）
- Chart 用的 `#2f81f7/#8b949e/rgba(47,129,247,…)` 改为读取 CSS 变量（主色取 `--primary`、灰取 `--text-2`、边框取 `--border`）。
- 折线/网格/面积渐变透明度成分通过变量渲染，切换主题重新 `line()` 即可与浅/深/绿三主题一致。

### 6.4 共享配置容器状态（不可破坏）
`renderConfigItems` 的 `configContainers` 注册表（跨 config/components 共用一个集合）下沉到 `comp.configEditor` 模块内部，`saveEdit/resetItem/toggleMask` 后 `refresh()` 会对所有已注册常驻容器统一重渲。**该语义必须 1:1 迁移**，否则组件页展开区与配置页会失步。

### 6.5 syslogs SSE 生命周期（不可破坏）
- main.js 的 `renderPage` 在离开 syslogs 时调用 `views.syslogs.leave()`（关流、`store.syslogPageVisible=false`）。本重构**不触碰** startStream/stopStream/leave 与官网.`component/syslogs` 的任何逻辑；系统日志页仅在样式/状态卡复用 `empty`。此红线承诺：SSE 集成与正文逻辑零改动。

### 6.6 加载顺序（新增脚本位置）
在 `index.html` 的 `<script>` 清单中，于 `state.js`（第 224 行 `views/overview.js` 前）之后、`views/*` 之前插入 `<script src="./assets/js/components/{head,empty,select,componentState,metrics,chart,configEditor}.js">`（可多行）。组件模块只依赖 `Rock.util/Rock.state/Rock.ui`，均已在之前加载。

---

## 7. 风险与缓解

| # | 风险 | 影响 | 缓解 |
|---|---|---|---|
| R1 | 指标卡 `comp.metrics.metricTiles(obsOff)` HTML 与现网 1:1 偏差 | overview/metrics 视觉或结构轻微变化 | P2 验收锁定：比对两页 F12 DOM 结构/截图 |
| R2 | 拆 config 编辑器改变共享容器状态 | 组件页展开配置区失步 | §6.4：configContainers 注册逻辑原样迁移 + 进入 P4 独立回归 |
| R3 | chart 颜色改用 CSS 变量后画风差异 | 既有灰/蓝观感略有变化 | 使用与现完全一致的色值（`--primary:#2f81f7/#409eff` 见浅/深定义），只更改为变量，不断言视觉大改 |
| R4 | 新增 7 文件 `go embed` 缓存 | 新文件不生效 | 每次 P 累积后重新 `go build` 重启测试服务器 |
| R5 | 无 node，无法 `node --check` 语法校验 | 语法错在运行时暴露 | 每阶段用 Chrome CDP 加载全部路由，收 console 无 error 为准 |
| R6 | 改动视图易触碰 `data-act` 或 render 行为 | 功能断链 | 每 P 验收含对应视图交互回归清单 |
| R7 | 跨视图耦合介绍后未清理旧导出 | 死代码/误导 | views/metrics 移除迁移后多余的 delta/fmtQps/metricTiles 导出 |

---

## 8. 里程碑与阶段划分（详见实施文档）
- **P1 组件基座**：新增 `components/head|empty|select` + 替换 7 视图的 page-head + 空态 + 下拉。纯渲染、零行为风险。
- **P2 指标解耦**：新增 `componentState/metrics` + overview 去掉 views.metrics 依赖 + 状态元数据复用。
- **P3 图表组件**：新增 `chart` + metrics 图改用组件。
- **P4 配置渲染器解耦**：新增 `configEditor` + config/components 改走独立组件。
- **P5 收尾回归**：全路由浏览器回归、主题三档、交互回归、`go build` 通过。

> 每个阶段结束后都要求：拦截 `go build` 成功 + Chrome 全路由无 console error + 交互回归通过。允许单阶段独立交付，降低耦合风险。

---

## 9. 待总指挥（Atlas）派发的分工提示
- 设计/图纸已冻结；后续按阶段派发**执行子代理**（每阶段单独派发，验收标准见 dev 文档）。
- 建议派发粒度：P1→1 个子代理；P2→1 个；P3→1 个；P4→1 个；每阶段提供标准回归清单。落地完成后可派一项**只读回归子代理**做最终一致性核对。
- 注意勿派并发子代理同时改同一视图文件（P2/P3 都动 `metrics/overview`），故 P2、P3 串行。

---

## 10. 设计取舍记录（决策）

| 编号 | 决策点 | 结论 | 理由 |
|---|---|---|---|
| D1 | 组件文件组织 | **新建 `assets/js/components/` 子目录（多文件）** | 契合「模块组件化」，避免 ui.js/state.js 膨胀；每组件独立内聚 |
| D2 | 指标卡去耦合 | **抽到 `comp.metrics`，消除 overview→metrics 依赖** | 直接命中目标；overview/metrics 是唯一消费者 |
| D3 | Canvas 折线图 | **抽独立组件 `comp.chart`，并顺带治主题硬编码** | ~85 行内聚度高；解决主题色问题 |
| D4 | load() 样板 | **不框架化**；仅抽空态/卡片/head 等 UI 组件；`skeleton` 合并，`load()` 逐视图保留 | 各视图 load 并行/懒加载差异大，强行抽象过度设计 |
| D5 | 旧 API 兼容 | **迁移后直接改调用点**，不保留 `views.metrics.delta/fmtQps/metricTiles` 旧导出（避免无意义 legacy）；但保留 `views.*` 被 `main.js` 委托所需的入口（如 `toggleConfig`/`startEdit`），后者改薄壳委派组件 | 消除 dead-code；同时不破坏 data-act 契约 |
| D6 | 命名空间 | 新组件统一 `Rock.comp.*` | 规避与 `Rock.views.components`（组件页）同名冲突 |
| D7 | 加载位置 | `state.js` 之后、`views/*` 之插入组件清单 | 逻辑依赖满足、数据在每次渲染前可用 |

> 边界：以上一切均在**纯前端重构**内；后端与行为零改动。