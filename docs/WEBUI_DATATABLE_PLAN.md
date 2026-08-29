# WebUI 数据列表组件化实施计划（定稿 · ✅ 已全部完成 2026-08-29）

> **目标**：把 WAF 安全防护（拦截明细、黑白名单）与入网数据（访问日志）三个数据列表页的公共能力——筛选栏、表格、分页、行详情弹层——抽取为业务无关的公共组件，并顺手修复拦截明细被隐性截断的问题（前端未传 limit，一直只看到后端缺省的 500 条）。
> **约束**：维持零依赖、无构建链的静态前端（IIFE + `window.Rock.comp` 命名空间 + 字符串模板 + `data-act` 事件委托）；交互以鼠标为主，只做低成本高收益的优化，严防过度设计。
> **本文档是唯一需求来源**：任何会话（包括无上下文的新会话）按「五、实施计划与进度」逐条执行，实时更新状态列，不需要读历史讨论。

---

## 一、执行规则（给执行者）

1. **状态三值**：`未实施` → `实施中` → `已完成`。开始一项任务时把状态改为"实施中"；验证通过后改为"已完成"，并在备注列记录验证结论（一句话 + 日期）。
2. 按 ID 顺序执行（依赖关系见表）；同一前置步骤内的子任务（如 1.1-1.5）无相互依赖。
3. 动手前先读「二、关键事实」核对代码现状；**实现中发现文档与代码现状冲突时，以代码现状为准，并在对应任务备注列记录偏差**。
4. 文档没写的细节按最简单实现，禁止自由发挥新增能力（负面清单见「六、明确不做」）。
5. 构建/测试一律用原生命令行（`go build` / `go test` / `go vet` / `node --check`），不用 make；提交须经用户确认，不自行 git 写操作（仓库规范见 AGENTS.md）。

## 二、关键事实（现状速查，省去重新勘察）

### 2.1 代码位置与机制

| 事项 | 事实 |
|---|---|
| 组件目录 | `webui/assets/js/components/`（dataTable.js 现仅 67 行：tableHTML/createExpander/pagingHTML 三函数） |
| 视图 | `webui/assets/js/views/`：waf.js（541 行）、logs.js（410 行）、blacklist.js（293 行，由 waf.js 页内 Tab 调度） |
| 弹层 | `webui/assets/js/ui.js`：`Rock.ui.confirmDialog`（Promise）与 `Rock.ui.openModal`（返回 overlay），overlay 均挂 `#modal-root`；**现状无任何键盘处理，ESC 需新增** |
| 事件机制 | `main.js:28` `onAction` 注册表 + `main.js:338` 全局 click 委托 `[data-act]`；**视图导出 `actions` 对象即自动注册**（范例见 logs.js:403 `'log-expand': function(el){…}`） |
| 组件注册 | `index.html` 底部 script 标签（267 行起）+ `main.js:52` 组件名清单，**新增组件两处都要加** |
| 样式 | 单文件 `webui/assets/style.css`（1300+ 行，不按组件拆分）；`.detail-grid/.detail-item/.k/.v` 键值网格样式已存在（logs 展开详情在用） |

### 2.2 后端接口上限（数据量边界的事实依据）

| 接口 | 返回 | 上限 |
|---|---|---|
| `GET /admin/shield/events` | NDJSON | `limit` 缺省 500、最大 10000（`plugins/shield/admin.go:88`）；**前端现状未传 limit** |
| `GET /admin/obs/logs` | NDJSON | 写死 2000（`plugins/obs/admin.go:77` `defaultQueryLimit`，不收 limit 参数） |
| `GET /admin/shield/{black,whitelist}` | JSON `{rows,total}` | 支持 `limit/offset` 服务端分页（blacklist.js 已在用） |

### 2.3 三个页面的现状细节

- **waf.js 拦截明细**：`eventsHTML()` 手拼表格（未用 tableHTML），客户端 `slice(0,1000)` + 520px 滚动；筛选栏为手写 `.log-toolbar` + render 后逐控件 `addEventListener`（防抖已存在，waf.js:340 起）；行键 `expKey = time|trace_id|client_ip`（waf.js:219）；行内展开详情字段清单可平移进弹层。
- **logs.js 访问日志**：双工具栏——查询条件栏（时间范围 + path + 查询/重置）与本地筛选栏（状态码 select / 仅错误 check / 排序 select）；`filteredLogs()` 本地过滤排序；`renderTable()` 只重渲染 `#log-table-wrap`（**部分重渲染，筛选输入不丢焦点，保持该模式**）；表格 `maxRows:2000` + 640px 滚动；行键 `time|trace_id`；`logDetailHTML()` 的 core+extras 字段清单现成，平移进弹层；导出 `exportLogs()` 基于全量筛选结果，**不得受分页影响**。
- **blacklist.js 黑白名单**：服务端 offset 分页已通（ipListState 的 limit/offset/total）；筛选 ip 模糊/blockType（仅黑名单）/validOnly；**行内 8 列已展示全部字段**；黑/白切换时类别筛选框条件显隐（现状按 isBlack 拼不同 HTML）。

### 2.4 开发验证环境

```bash
go build -tags dev -o bin/rocksys ./cmd/rocksys   # dev 模式：改 webui/ 文件刷新即生效（新增文件需重启一次）
cd bin && ./rocksys                                 # ★红线：必须在 bin/ 运行，严禁项目根目录
# 浏览器打开 http://127.0.0.1:19527/ （管理台回环免登录，可直接测试）
node --check <改动js文件>                            # JS 语法检查
go test ./... && go vet ./...                       # 全量测试与静态检查
```

## 三、产品设计（交互定稿）

### 3.1 分页栏：简单页码方案

```
共 1,234 条 · 每页 [20 ▾]   ‹ 上一页   第 [5] / 62 页   下一页 ›
```

- **不生成页码序列、不做省略号算法**；跳页由"可输入的当前页框"承担：当前页"5"为数字框（宽约 48px），输入页码回车或失焦即跳转，非法/越界值自动钳位回当前页；
- 每页条数下拉 20/50/100，变更即生效并回到第 1 页；
- `‹ ›` 即上一页/下一页，边界禁用；仅 1 页时右侧翻页控件整组不渲染，只留"共 N 条 · 每页 [n ▾]"；
- server 模式下翻页/跳页/改条数全部只回调 `onPaging`，由视图重新拉数。

### 3.2 行详情弹层

- 点行打开键值两列网格弹层：基于 `Rock.ui.modal` 封装，宽 640px（可覆写），长值横向滚动不撑破弹层，长文本 `pre` 等宽块展示，关键字段 `copy` 一键复制（`navigator.clipboard` + toast）；
- `fields` 字段清单**必填**（label + key + 可选 render/pre/copy）——不做"缺省自动弹整行全部字段"的隐式行为，裸键名当标签对用户不可读；
- 行 hover 底色 + `cursor:pointer` 提示可点；操作列按钮 `stopPropagation` 不触发详情；
- 键盘只做 ESC（ui.js 新增，见任务 1.1）；tabindex、Enter/空格触发、焦点移入/归还等完整键盘可达性不做；
- **行内展开交互从三页移除，统一替换为弹层**（"对比相邻行"场景未来真有需求再议，createExpander 随旧 API 一并删除）。

### 3.3 筛选栏

- select/check/text 字段即改即查（内置防抖）；**dateRange 不参与即改即查**——选完开始还没选结束时触发查询会闪现半选区间的错误结果，仅在完整区间确定后触发，或随"查询"按钮；
- 重置回 schema 默认值并触发查询；提交校验留在视图 `onQuery` 前置钩子（业务语义组件不感知）；
- dateRange 字段渲染"开始日期/时间 + 结束日期/时间"四个输入（对齐 waf 页现有形态），组装复用现有 `Rock.util.dateRange` 的 from/to 语义（waf.js:46-47 用法）。

### 3.4 数据量边界

- 拦截明细：前端显式传 `limit=10000` 拉满后客户端分页（单次约 2-3MB NDJSON，局域网管理台可接受，实测偏慢再回调上限）；
- 访问日志：后端写死 2000，前端按 2000 条客户端分页；
- 两者触顶（结果条数 = 上限）时提示"已达单次查询上限，请收窄时间范围或筛选条件"（沿用现有 maxRows 提示样式）；
- 后端放开 limit/offset 服务端分页是后续演进方向，本期不做。

### 3.5 各页面交互归属

| 页面 | 筛选 | 分页 | 详情 |
|---|---|---|---|
| WAF 拦截明细 | filterBar（时间范围 + 类别 + 来源 IP） | client | 弹层（payload/UA 长文本 + copy 是刚需） |
| 入网数据（访问日志） | filterBar 双实例（查询条件栏 + 本地筛选排序栏） | client | 弹层 |
| 黑白名单 | filterBar（IP 模糊 + 类别 + 仅有效） | server（offset） | `detail: null`——行内已全字段，弹层零增量 |

## 四、工程架构

### 4.1 分层（依赖只能向下）

```
views/*            业务层：API 调用与 URL 拼参、参数名映射、行/详情字段语义、增删改动作
   │ 只调用
filterBar          筛选栏：字段声明 → HTML + 收集 + 重置 + 即改即查（防抖）
dataTable          表格：列声明 + 行渲染 + 客户端/服务端分页 + 行点击详情钩子
detailModal        行详情弹层：字段清单 → 键值网格弹层（基于 Rock.ui.modal）
   │ 只调用
dateRange/select/util/ui   原子能力（现有，保持不动）
```

组件不感知任何 API、字段名与业务语义；"数据加载 + URL 拼参"留在视图（三页请求语义差异大：NDJSON / JSON / 表单 POST）。

### 4.2 组件规划

| 组件 | 文件 | 动作 | 说明 |
|---|---|---|---|
| dataTable | `assets/js/components/dataTable.js` | 升级 | 新增 `create()` 有状态实例（见 4.3）；旧三函数保留至任务 5.2 删除 |
| detailModal | `assets/js/components/detailModal.js` | 新建 | `show({title, fields, row, width?})` |
| filterBar | `assets/js/components/filterBar.js` | 新建 | `create({ns, live, onQuery, fields})` → `html()/bind(host)/collect()/state()/reset()` |
| Rock.ui.modal | `assets/js/ui.js` | 增强 | modal 层新增 ESC 关闭（任务 1.1） |

- **XSS 单一出入口**：组件渲染的值默认走 `Rock.util.esc`，`render` 回调返回 HTML 属视图显式信任边界——组件头注释写明该约定，review 只盯 render 回调；
- **规模红线**：单组件文件 > 300 行视为内聚不足信号（与 configEditor 311 行同级即预警）。

### 4.3 dataTable.create() 配置与接口

```js
const table = Rock.comp.dataTable.create({
  ns: 'waf-events',                       // id/act 前缀，防多实例冲突
  columns: [                              // 列声明：label 必填；render 可省（默认 esc(row[key])）
    { key: 'ts', label: '时间', render: r => esc(fmtDateTime(r.ts)) },
    { key: 'client_ip', label: '来源 IP', width: '140px' },
  ],
  rowKey: r => r.time + '|' + r.trace_id, // 行键：detail 点击回查行用；不配 detail 可省
  rowActions: r => '<button … data-act="waf-del" data-id="' + r.id + '">删除</button>',  // 可选，操作列
  detail: {                               // 可选：点行打开详情弹层；不配即无点行行为
    title: r => '拦截明细 #' + r.id,       // 缺省用 ns 名
    fields: [                             // 必填，见 3.2
      { key: 'client_ip', label: '来源 IP' },
      { key: 'payload', label: '请求体', pre: true },
    ],
  },                                      // detail: null 显式关闭（黑白名单）
  paging: {                               // 不配即 client 分页 pageSize=20；不提供"关闭分页"配置
    mode: 'client',                       // 'client'（组件内切片）| 'server'（视图回调）
    pageSize: 20, pageSizeOptions: [20, 50, 100],
  },
  emptyText: '暂无拦截记录',
});
```

实例接口（渲染纯函数 + 内部分页状态，事件经 `data-act` 委托回视图）：

- `table.html(rows, opts)` → 表格 + 分页栏完整 HTML；`opts.total` 仅 server 模式喂总数（client 模式自动 = rows.length）；
- `table.state()` → `{ page, pageSize, offset }`（server 模式视图拼 URL 用）；
- `table.go(n)` / `table.setPageSize(n)` → 内部钳位改状态，**不触发渲染**（渲染由视图驱动）；
- `table.onDetail = row => modal`：视图注入，缺省 `r => Rock.comp.detailModal.show({...detail, row: r})`。

### 4.4 事件接线规范（组件生成的 `data-act`，视图在自己 `actions` 对象注册）

| data-act | 载荷 | 视图处理 |
|---|---|---|
| `<ns>-page` | `data-page` 目标页码 | `table.go(+el.dataset.page)` → client 模式重渲染表格容器；server 模式重新拉数（用 `table.state()` 拼 limit/offset） |
| `<ns>-size` | `data-size` 每页条数 | `table.setPageSize(+el.dataset.size)` → 同上 |
| `<ns>-detail` | `data-key` 行键 | 从视图自身 rows 按 rowKey 查行 → `table.onDetail(row)` |

filterBar：`bar.html()` 渲染后视图调 `bar.bind(host)` 绑定即改即查（内部防抖；dateRange 仅两端齐后触发）；查询/重置按钮留在视图（data-act 调 `onQuery(bar.collect())` / `bar.reset()`）。

### 4.5 视图改造职责

| 视图 | 保留（业务） | 移交组件 |
|---|---|---|
| `waf.js` | API 调用与 `limit=10000`、`block_type`/`client_ip` 参数映射、明细字段清单、趋势图/实时计数 | 筛选栏 HTML/收集/重置；表格 + 分页；展开行改弹层 |
| `logs.js` | API 调用、`filteredLogs()` 本地排序过滤、导出（全量，不受分页影响） | 双筛选栏；分页；详情弹层 |
| `blacklist.js` | 增/删/恢复/导入、黑/白切换、IP/CIDR 校验 | 筛选栏；表格壳（删手拼表体）；server 分页接线 |

## 五、实施计划与进度

> 状态列由执行者实时维护：`未实施` / `实施中` / `已完成`（完成后备注记验证结论 + 日期）。

| ID | 任务 | 依赖 | 验证要点 | 状态 | 备注 |
|---|---|---|---|---|---|
| 1.1 | ui.js modal 层 ESC：document keydown Escape 关闭 `#modal-root` 最上层 overlay；confirmDialog 需走 close(false) 等价取消（实现提示：confirmDialog 内已有 close 回调，可挂 `overlay._rockClose`，ESC 优先调它，openModal 直接 remove） | — | node --check；浏览器任一删除确认框按 ESC = 取消且 Promise resolve(false) | 已完成 | node --check 通过（2026-08-29）；confirmDialog 挂 overlay._rockClose、openModal 直接 remove，document keydown Escape 关最上层；ESC 行为并入 6.2 浏览器统一回归 |
| 1.2 | 新建 detailModal.js：`show({title,fields,row,width?})`，键值网格复用现有 `.detail-grid` 样式，新增 `pre` 等宽块与 `copy` 按钮（clipboard + toast） | — | node --check；浏览器控制台临时 `Rock.comp.detailModal.show({...})` 冒烟（含 ESC 关闭） | 已完成 | node --check 通过（2026-08-29）；show({title,fields,row,width?}) 基于 openModal，render 显式信任边界、pre 等宽块、copy 按钮弹层内委托；浏览器冒烟并入 6.2 |
| 1.3 | 新建 filterBar.js：`create/html/bind/collect/state/reset`，四类字段（dateRange 四输入/select/text/check），live 防抖即改即查，dateRange 仅完整区间触发 | — | node --check；控制台临时创建实例冒烟 collect/reset | 已完成 | node --check 通过（2026-08-29）；create({ns,live,onQuery,fields})，dateRange 前缀键 fromDate/…/toTime，四输入齐才触发；冒烟并入 6.2 |
| 1.4 | style.css：分页栏样式（左右布局、48px 页码输入框）、行 hover 可点态、`.detail-pre`、copy 按钮 | — | 浏览器目测 | 已完成 | 追加第 29 节（.dt-paging/.dt-page-input/tr.dt-row-clickable/.detail-grid-modal/.detail-pre/.detail-copy），目测并入 6.2 |
| 1.5 | index.html script 标签 + main.js:52 清单各加 `filterBar`、`detailModal`；main.js 启动对 `Rock.comp` 逐项断言，缺失 console.error | 1.2 1.3 | 浏览器 console 无错误 | 已完成 | 两处均已添加（script 置 dataTable 之后，依赖 select/util）；启动断言复用既有 assertDeps 清单；console 复查并入 6.2 |
| 2.1 | dataTable 增加 `create()`（按 4.3/4.4 实现：分页状态、分页栏 HTML、detail 行渲染、rowActions 列、emptyText），旧三函数不动 | 1.4 | node --check；以任务 3.1/3.2 的 WAF 接入作实际验证 | 已完成 | node --check 通过（2026-08-29）。偏差记录：全局委托仅覆盖 click，select/input 的 change 无通道，分页控件（按钮/跳页输入/每页条数）改由实例 `bind(host)` 内部委托 + `cfg.onPaging(state)` 回调驱动视图重渲染，视图不再逐个注册 `<ns>-page`/`<ns>-size` action；`<ns>-detail` 仍走全局 click 委托（4.4 不变）。另增 `rowClass(row)` 可选配置（logs 行错误底色等价能力）。实际验证以 3.2 接入为准 |
| 3.1 | waf.js 拦截明细筛选栏换 filterBar（时间范围+类别+来源 IP），删手写 toolbar 与 render 后逐控件 addEventListener 段 | 2.1 前4.4 | 浏览器：即改即查/查询/重置行为与改造前一致 | 已完成 | 代码完成（2026-08-29）：eventsBar = filterBar.create(live)，手写 toolbar 与逐控件 addEventListener 已删，查询/重置按钮留视图（waf-query/waf-reset）；行为回归并入 6.2 |
| 3.2 | waf.js 明细表换 dataTable.create(client) + detailModal：列=时间/类别/IP/方法/URL/状态，rowKey 用现 expKey，detail.fields 平移现展开行字段；`limit=10000` 显式传参；删 eventsHTML 手拼与 slice(1000) | 3.1 | 浏览器：翻页/跳页/每页条数/点行弹层/触顶提示（结果=10000 时） | 已完成 | 代码完成（2026-08-29）：eventsTable = dataTable.create(client, cap=10000)，limit=10000 显式传参，detail.fields 平移含 extra.referer/xff 解析，行内展开已删改弹层（waf-events-detail）；浏览器回归并入 6.2 |
| 4.1 | blacklist.js：filterBar（ip 模糊/类别/仅有效）+ dataTable.create(server, detail:null) + rowActions 删/恢复；查询拼 limit/offset 取自 table.state()，loadIPList 后 `html(rows,{total})`；删 ipListRowsHTML 手拼；黑/白切换时**重建 filterBar 实例**以处理类别字段显隐（不发明 visible 配置） | 2.1 | 浏览器：增删恢复导入回路 + 翻页/跳页/筛选 + 黑白切换 | 已完成 | 代码完成（2026-08-29）：buildInstances() 按黑/白构建字段与列，server 分页经 onPaging(state) 回调（limit/offset 取自 table.state()），删 page() 与 waf-iplist-page action；新增/导入后 ipTable.go(1) 回首页；浏览器回归并入 6.2 |
| 5.1 | logs.js：filterBar 双实例（查询条件栏 + 本地筛选排序栏）+ dataTable.create(client) + 弹层（fields 平移 logDetailHTML）；保持 renderTable 只重渲染 `#log-table-wrap` 的部分重渲染模式；导出仍基于全量筛选结果 | 2.1 | 浏览器：本地筛选/排序/翻页/跳页/弹层/导出条数不受分页影响 | 已完成 | 代码完成（2026-08-29）：queryBar/filterBar 双实例，logsTable client 分页（cap=2000）+ onDetail 动态字段（核心+extras 平移），rowClass 错误底色保留，renderTable 部分重渲染保留，导出走 filteredLogs() 全量；偏差：动态扩展字段经 onDetail 注入（detail.fields 声明式不支持动态清单）；浏览器回归并入 6.2 |
| 5.2 | 删除旧 API：`tableHTML`/`createExpander`/`pagingHTML` 实现+导出，先 `grep -rn "tableHTML\|createExpander\|pagingHTML" webui/` 确认无引用 | 3.2 4.1 5.1 | grep 仅剩 dataTable.js 自身→删除；node --check；三页回归无回归 | 已完成 | 2026-08-29：grep 确认仅剩 dataTable.js 自身私有 pagingHTML 助手（非旧导出）后删除三函数与导出；node --check 全部通过；三页回归并入 6.2 |
| 6.1 | 文档同步：docs/webui.md 三页交互描述（分页/弹层/筛选/触顶提示）+ 三页回归清单固化为小节；两个新组件 + dataTable 头注释（接口、XSS 约定） | 5.2 | 人工核对覆盖三个页面小节 | 已完成 | 2026-08-29：webui.md 4.6 更新 + 新增 4.7「数据列表公共交互与三页回归清单」（原 4.7 顺延 4.8），功能覆盖清单"行展开"改"详情弹层"；dataTable/detailModal/filterBar 头注释均含接口与 XSS 信任边界约定 |
| 6.2 | 全量验证：`go test ./...` + `go vet ./...` + node --check 全部改动 JS + 浏览器三页对照回归清单全过 | 6.1 | 全绿后在本文档头部标记"已全部完成"，提交信息注明验证结果 | 已完成 | 2026-08-29 全绿：go test/vet 通过；8 个改动 JS node --check 通过；浏览器实测（-tags dev 构建，bin/ 运行）：WAF 页筛选即改即查/重置/翻页/跳页/每页条数/弹层/ESC 全过，黑白名单服务端分页(403 条 21 页)/IP 模糊/黑白切换重建实例(白名单无类别字段)全过，入网数据客户端分页(1,235 条 62 页)/只看异常本地筛选(225 条全 4xx)/弹层含复制按钮/ESC 全过，confirmDialog ESC resolve(false) 验证通过，全程 console 无错误 |

---

> **✅ 已全部完成（2026-08-29）**：全部任务 1.1–6.2 已完成并经浏览器三页回归验证。提交 4b85622。
> **二期（七、服务端分页改造）✅ 已全部完成（2026-08-29）**：拦截明细/访问日志已改服务端分页，7.1–7.8 全部完成并经浏览器两页回归验证。

---

## 七、二期：后端服务端分页改造（2026-08-29 启动）

> 原「六、明确不做」中"后端服务端分页改造本期不动"一项经用户确认解除。
> 设计要点：两接口保持 NDJSON 响应不变，新增 `offset` 参数，总数经 `X-Total-Count` 响应头回传；
> 访问日志原前端本地筛选（状态分组/仅异常）与本地排序（耗时）必须下沉后端（`status_group`/`only_error`/`sort` 参数），否则翻页后失真；
> 前端两页 dataTable 切 `server` 模式，limit/offset 取自 `table.state()`；导出改为按条件全量拉取（大 limit 单次请求）。

| ID | 任务 | 依赖 | 验证要点 | 状态 | 备注 |
|---|---|---|---|---|---|
| 7.1 | SQL 三方言：`shield_event_query.sql`/`access_log_query.sql` 加 OFFSET（访问日志另加状态分组/仅异常/排序参数）；新增 `shield_event_count.sql`/`access_log_count.sql` | — | go test（db 门控测试过，其余单测覆盖 file store） | 已完成 | SQL 三方言 query 加 OFFSET（访问日志含状态分组/仅异常/排序 15 参数）+ 新增 count 脚本（COUNT(*) AS cnt 跨方言列名稳定）；bin/hotscripts/sql 已同步（外挂优先防旧文件覆盖）（2026-08-29） |
| 7.2 | shield 后端：EventQuery 加 Offset、QueryEvents 传 offset、新增 CountEvents、admin.go Events 解析 offset 并回 X-Total-Count | 7.1 | go test：单测覆盖 offset 分页与 count；events 参数校验测试 | 已完成 | EventQuery 加 Offset、QueryEvents 传 limit+offset、CountEvents 复用查询条件；admin.go 解析 offset 并回 X-Total-Count；单测覆盖 offset 分页与 count（2026-08-29） |
| 7.3 | obs 后端：Query 加 Offset/StatusGroup/OnlyError/Sort，DBStore/FileStore 实现分页与排序，新增 Count，admin.go Logs 解析新参数并回 X-Total-Count | 7.1 | go test：file store 分页/排序/筛选单测；logs 参数校验测试 | 已完成 | Query 加 Offset/StatusGroup/OnlyError/Sort；DBStore SQL 分页/排序，FileStore 收集后排序切片；Store 接口加 Count（AsyncStore/Obs 转发）；admin.go 解析 4 个新参数并回 X-Total-Count；file store 单测覆盖分页/计数/分组/排序（2026-08-29） |
| 7.4 | 前端：api.js 增加 textMeta（读 X-Total-Count）；dataTable 删除 cap 触顶提示（服务端分页后无截断语义） | — | node --check | 已完成 | api.textMeta 读 X-Total-Count；dataTable 删除 cap/capText（服务端分页后无截断语义，无使用方）（2026-08-29） |
| 7.5 | 前端 waf.js：eventsTable 切 server 分页，limit/offset 取自 table.state()，筛选变更回第 1 页；删 cap 提示与 limit=10000 | 7.2 7.4 | 浏览器：翻页/跳页/每页条数走服务端、总数正确 | 已完成 | eventsTable 切 server 分页，limit/offset 取自 table.state()，条件变更 go(1) 再查，翻页回调直接 queryEvents()；删除 cap 提示与 limit=10000（2026-08-29） |
| 7.6 | 前端 logs.js：logsTable 切 server 分页；状态分组/仅异常/耗时排序改传后端参数；导出改按条件全量拉取（不受分页影响） | 7.3 7.4 | 浏览器：翻页/筛选/排序/导出条数=服务端总数 | 已完成 | logsTable 切 server 分页；状态分组/仅异常/耗时排序传后端参数（buildLogParams 统一组参）；导出改单次 limit=50000 全量拉取；删除 filteredLogs/statusGroup 本地过滤排序（2026-08-29） |
| 7.7 | 文档同步：webui-api.md 两端点参数、webui.md 4.6/4.7 服务端分页描述、本文档状态 | 7.5 7.6 | 人工核对 | 已完成 | webui-api.md /admin/logs 参数表+响应说明与 shield events 行已更新；webui.md 4.6/4.7 改为服务端分页描述（2026-08-29） |
| 7.8 | 全量验证：go test/vet + node --check + 浏览器两页回归 | 7.7 | 全绿后标记完成，提交注明验证结果 | 已完成 | 2026-08-29 全绿：go test/vet 通过；浏览器实测：拦截明细 1002 条 51 页翻页/跳页/类别过滤回第 1 页(27 条)/每页条数/弹层/ESC 全过；入网数据 1235 条 62 页/仅异常后端筛选 225 条全 4xx/耗时降序后端排序/弹层/ESC 全过；导出全量 225 条=服务端总数；全程 console 无错误。验证中发现并修复两问题：hotscripts 旧 SQL 副本覆盖（已在 7.1 备注）、api.js 导出清单漏 textMeta |

---

## 六、明确不做（防过度设计）

- 不引 MVVM/虚拟 DOM/构建链；
- 不做完整键盘可达性（tabindex / Enter·空格触发 / 焦点移入归还）——键盘只做 modal 层 ESC；
- 不做页码序列与 `…` 省略号算法——跳页由可输入的当前页框承担；
- 不做"缺省自动弹整行全部字段"的隐式详情——`detail.fields` 必填；
- 不做 dataTable"关闭分页"配置——三视图都用分页，无使用方；
- 不做 filterBar 字段动态显隐配置——blacklist 黑/白切换用重建实例解决；
- 不把"数据加载 + URL 拼参"抽成通用层——留在视图；
- 不做表格列排序/多选/列宽拖拽等本期无使用方的能力；
- 后端服务端分页改造（拦截明细/访问日志放开 limit·offset）本期不动，触顶以提示收窄条件兜底；
- `tabs`/`head`/`codeEditor` 等现有组件不动。
