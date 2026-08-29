# A5 · 前端：黑白名单页 + TOP 批量 + 枚举数组

> 实施状态：**待实施**
> 前置：A3 已实施（页面依赖端点）。设计依据：决策 1/4/5/14、§3.2/§3.6/§5.3。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| webui/assets/js/state.js | 修改 | BLOCK_TYPES 补 [0,'其他'],[11,'人工收录'] |
| webui/assets/js/views/blacklist.js | 修改 | 列/标题/排序/label/默认值/同步按钮 |
| webui/assets/js/views/topIPs.js | 修改 | 批量加黑改 import |

均为**已有文件**：dev 模式改完刷新即见，无需重启。

## 3. 实施步骤

- [ ] state.js：`BLOCK_TYPES` 数组补 `[0, '其他'], [11, '人工收录']`（类别列、新增/导入下拉同源生效——已核实该数组是唯一数据源，经 `Rock.state.blockTypeName` 查名、未命中返回「未知」）；**筛选下拉选项生成时排除 0**（查询语境 0=全部，决策 2——否则「其他」选项实际查出的是全部）
- [ ] blacklist.js：
  - [ ] 列表新增「封禁次数」列（warn_times，fmtInt）
  - [ ] 表格标题改「黑名单条目（DB表）」/「白名单条目（DB表）」+ data-tip 消歧（数据来自数据库；rules/ip_blacklist.txt 仅参与拦截判定，可经「从文件同步」入库）
  - [ ] 筛选栏新增「排序」下拉（七选项：默认/命中次数/封禁次数/封禁时间/解封时间/最后更新/封禁原因类别；即改即查、回第 1 页；白名单不加）
  - [ ] 导入/新增 select 前置 label「拉黑原因类别」+ data-tip（§5.3 文案）+ 默认选项 11；提交缺省兜底 `Number(value) || 1`（blacklist.js 新增/导入两处）同步改 `|| 11`
  - [ ] 操作区「从文件同步」按钮 + tooltip（同步意义说明）→ 调 sync_file → toast 成功展示 imported/skipped（自动消失）、异常常驻三要素
- [ ] topIPs.js：批量加黑改一次 `POST /admin/shield/blacklist/import?title=攻击源TOP批量加黑&block_type=11`（body=选中 IP 每行一个）；toast「已导入 X 条，跳过 Y 条」；删除逐条循环。调用方式照抄 blacklist.js 现有 import 调用（api.post 对字符串 body 走 JSON 字符串编码，后端 importIPList 已双向兼容 curl 纯文本与 JSON 字符串两种形态，勿改协议）
- [ ] 黑/白切换时实例重建逻辑复核（buildInstances 已有，新增列/筛选随重建生效）

## 4. 验证（浏览器回归）

- [ ] 类别筛选下拉出现「人工收录/其他」；类别列正确显示新枚举名
- [ ] 封禁次数列展示；排序下拉逐选项验证排序正确、翻页正常
- [ ] 新增/导入默认「人工收录」、label 与 tip 齐全；手工选 1-10 仍可入库
- [ ] 从文件同步：成功/文件缺失两态 toast 分级正确
- [ ] TOP 批量：一次请求完成、重复 IP 计入 skipped 不报错
- [ ] 标题与消歧 tooltip；白名单页不受影响（无类别/排序/封禁次数）

## 5. 完成标准

清单全勾 + 验证全过 → 状态「已实施」→ 更新总纲。

## 6. 实施回填区（中断现场记录）

（空）
