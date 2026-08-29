# A6 · 前端：拦截明细「IP封禁」垂直切片（含 events in_blacklist 标记）

> 实施状态：**待实施**
> 前置：A3 已实施（ban 端点）。设计依据：决策 9/10、§3.5（含二轮修正：专用 ban 端点、detailModal actions 扩展、快照遍历 in_blacklist）。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| plugins/shield/admin.go | 修改 | events 接口按页附 in_blacklist（后端小改，本切片内聚） |
| webui/assets/js/components/detailModal.js | 修改 | 新增可选 actions 配置（通用插槽，不耦合 shield） |
| webui/assets/js/views/waf.js | 修改 | 操作列 + 详情入口 + 封禁弹窗 |
| 相关测试文件 | 修改 | events in_blacklist 单测 |

## 3. 实施步骤

- [ ] 后端：拦截明细 events 接口对当前页 IP **遍历内存快照 InBlacklist** 附 `in_blacklist`（与 stats TOP 同源、零 DB 查询）
- [ ] detailModal.js：新增可选 `actions:[{label, className, onClick(values)}]` 渲染为 footer 按钮（组件保持通用；默认不传行为不变）
- [ ] waf.js 拦截明细表：
  - [ ] 最右新增「操作」列（`col.render` 显式信任边界，client_ip 入 data 属性须 `esc`），「IP封禁」按钮；`in_blacklist` 行按钮置灰 + tooltip（与 TOP「在黑名单不可选」同语义）
  - [ ] 行详情弹层经 actions 注入「IP封禁」按钮（共用同一弹窗逻辑，避免双份实现）
- [ ] 封禁弹窗（复用 openModal，字段齐全）：
  - [ ] 来源 IP 只读 + 当前 warn_times 与是否在黑名单（**打开时单查**：复用列表接口 `GET /admin/shield/blacklist?ip={精确IP}&limit=5` 取 ip 完全相等行——query_list 已返回 deleted_at/expires_at 判软删/过期状态，warn_times 由 A2 补入 SELECT 后可得；不新增端点。在黑名单与否用该行携带的 `in_blacklist` 标记）
  - [ ] 封禁理由 title：预填 `人工封禁：{该行拦截类别名}拦截`（可改）
  - [ ] 拉黑原因类别下拉 1-11 默认 11（BLOCK_TYPES 同源）
  - [ ] 封禁时长单选：封禁 24 小时 / 永久封禁
  - [ ] 效果说明：warn_times +1；限时累计达 5 次自动转永久；软删/过期命中提示「该 IP 有历史封禁记录，将恢复原条目」（打开时已知状态则预展示）
  - [ ] 提交走 `POST /admin/shield/blacklist/ban`；成功 toast 自动消失 + 关弹层 + 刷新明细；失败常驻三要素；转永久时成功文案注明
- [ ] 单测：events in_blacklist 标记（快照命中/未命中）

## 4. 验证（浏览器回归）

- [ ] 操作列按钮：普通行可点、已拉黑行置灰 + tooltip；行详情弹层入口可用
- [ ] 弹窗字段齐全：IP 只读、warn_times 展示、预填理由、默认 11、时长单选
- [ ] 24h 封禁：提交后黑白名单出现条目（人工收录、封禁次数 1、解封时间 now+24h）；按钮转置灰
- [ ] 永久封禁：expires_at 空
- [ ] 软删/过期命中：先软删该 IP 再封禁 → 条目恢复 + 次数 +1（决策 10）；达 5 次转永久提示
- [ ] 失败态：错误常驻、文案三要素

## 5. 完成标准

清单全勾 + 验证全过 → 状态「已实施」→ 更新总纲。

## 6. 实施回填区（中断现场记录）

（空）
