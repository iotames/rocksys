# A6 · 前端：拦截明细「IP封禁」垂直切片（含 events in_blacklist 标记）

> 实施状态：**已实施**
> 前置：A3 已实施（ban 端点）。设计依据：决策 9/10、§3.5（含二轮修正：专用 ban 端点、detailModal actions 扩展、快照遍历 in_blacklist）。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| plugins/shield/admin.go | 修改 | events 接口按页附 in_blacklist（后端小改，本切片内聚） |
| webui/assets/js/components/detailModal.js | 修改 | 新增可选 actions 配置（通用插槽，不耦合 shield） |
| webui/assets/js/views/waf.js | 修改 | 操作列 + 详情入口 + 封禁弹窗 |
| 相关测试文件 | 修改 | events in_blacklist 单测 |

## 3. 实施步骤

- [x] 后端：拦截明细 events 接口对当前页 IP **遍历内存快照 InBlacklist** 附 `in_blacklist`（与 stats TOP 同源、零 DB 查询）
- [x] detailModal.js：新增可选 `actions:[{label, className, onClick(values)}]` 渲染为 footer 按钮（组件保持通用；默认不传行为不变）
- [x] waf.js 拦截明细表：
  - [x] 最右新增「操作」列（`col.render` 显式信任边界，client_ip 入 data 属性须 `esc`），「IP封禁」按钮；`in_blacklist` 行按钮置灰 + tooltip（与 TOP「在黑名单不可选」同语义）
  - [x] 行详情弹层经 actions 注入「IP封禁」按钮（共用同一弹窗逻辑，避免双份实现）
- [x] 封禁弹窗（复用 openModal，字段齐全）：
  - [x] 来源 IP 只读 + 当前 warn_times 与是否在黑名单（**打开时单查**：复用列表接口 `GET /admin/shield/blacklist?ip={精确IP}&limit=5` 取 ip 完全相等行——query_list 已返回 deleted_at/expires_at 判软删/过期状态，warn_times 由 A2 补入 SELECT 后可得；不新增端点。在黑名单与否优先用 events 行携带的 `in_blacklist` 标记）
  - [x] 封禁理由 title：预填 `人工封禁：{该行拦截类别名}拦截`（可改）
  - [x] 拉黑原因类别下拉 1-11 默认 11（BLOCK_TYPES 同源）
  - [x] 封禁时长单选：封禁 24 小时 / 永久封禁
  - [x] 效果说明：warn_times +1；限时累计达 5 次自动转永久；软删/过期命中提示「该 IP 有历史封禁记录，将恢复原条目」（打开时已知状态则预展示）
  - [x] 提交走 `POST /admin/shield/blacklist/ban`；成功 toast 自动消失 + 关弹层 + 刷新明细；失败常驻三要素；转永久时成功文案注明
- [x] 单测：events in_blacklist 标记（快照命中/未命中）

## 4. 验证（浏览器回归）

- [x] 操作列按钮：普通行可点、已拉黑行置灰 + tooltip；行详情弹层入口可用
- [x] 弹窗字段齐全：IP 只读、warn_times 展示、预填理由、默认 11、时长单选
- [x] 24h 封禁：提交后黑白名单出现条目（人工收录、封禁次数 1、解封时间 now+24h）；按钮转置灰
- [x] 永久封禁：expires_at 空
- [x] 软删/过期命中：先软删该 IP 再封禁 → 条目恢复 + 次数 +1（决策 10）；达 5 次转永久提示
- [x] 失败态：错误常驻、文案三要素

## 5. 完成标准

清单全勾 + 验证全过 → 状态「已实施」→ 更新总纲。

## 6. 实施回填区（中断现场记录）

- 2026-08-30 代码实施完成（§3 全勾；§4 浏览器验证项留给主代理）：
  - 后端：`plugins/shield/admin.go` Events 逐行附 `row["in_blacklist"] = h.shield.InBlacklist(ip)`（快照遍历、零 DB，与 Stats TOP 同源写法），注释同步。
  - 前端：`detailModal.js` 新增可选 `actions` footer 按钮插槽（`data-detail-act` 索引委托，onClick 收当前行）；`dataTable.js` detail 配置透传 `actions`；`waf.js` 新增操作列（`banBtnHTML`，in_blacklist 置灰 + tooltip）、详情弹层注入「IP封禁」、`openBanModal`（IP 只读/理由预填 `人工封禁：{类别名}拦截`/类别下拉 BLOCK_TYPES 同源默认 11/时长单选/效果说明/打开时单查列表接口回填 warn_times 与历史记录预提示）、提交走 ban 端点，成功 toast 自动消失 + 关弹层 + `queryEvents()` 刷新，失败 `#waf-ban-err` 常驻三要素；转永久成功文案注明。
  - expires_at 判空容忍缺键与空串两种形态（`expStr = hit.expires_at == null ? '' : String(hit.expires_at)`）。
  - 单测：`TestAdmin_EventsInBlacklistFlag`（快照命中 true / 未命中 false + NDJSON 行均携带布尔字段）。
  - 验证：`go test ./...` 全过、`go vet ./...`、`go build ./...` 通过；JS 经 `node --check` 语法校验。
  - 依赖说明：类别下拉直接引用 `Rock.state.BLOCK_TYPES`（过滤 0），未硬编码 11 选项文案——A5 合入后 11「人工收录」即出现在下拉；select 缺省值 '11'，A5 未合入时回退首项。

- 2026-08-30 主代理浏览器回归（§4）全过：操作列按钮渲染、封禁弹窗字段齐全（来源 IP 只读、当前封禁次数 1、历史封禁记录恢复预提示、理由预填「人工封禁：SQL注入拦截」（真实拦截类别）、类别下拉默认 11、24h/永久单选、效果说明）；提交 24h → toast「已封禁 127.0.0.1（24 小时后自动解封）」→ DB 复核 warn_times=2（软删恢复+累计）、expires=now+24h；提交后全部事件行按钮置灰（3/3，title 指引黑白名单管理——注意置灰按钮不带 data-act，选择器统计时需按文本+disabled 判断）。
