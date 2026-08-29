# A1 · 数据层：warn_times 列 + block_type 枚举 0/11（三处红线同步）

> 实施状态：**待实施**
> 前置：项目一 B5 已实施（存量库加列经「服务 → 数据库 → 表结构」执行）。设计依据：`docs/IP_BLACKLIST_PLAN.md` 决策 1/2/8、§3.1、§5.2。

## 1. 目标

`ip_blacklist` 表新增 `warn_times`（封禁次数）列；`block_type` 枚举新增 0（其他）/ 11（人工收录）并完成三方言脚本 + 数据字典 + Go 权威定义三处同步。本步只动结构与枚举定义，Go 侧读写与端点在 A2/A3。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| plugins/shield/block_type.go | 修改 | BlockOther=0 / BlockManual=11 + 语境注释 |
| sql/{sqlite,mysql,postgres}/ip_blacklist_create_table.sql | 修改 ×3 | 加 warn_times 列 + block_type 注释补 0/11 |
| sql/{sqlite,mysql,postgres}/shield_event_create_table.sql | 修改 ×3 | block_type 注释补语境说明（不改列） |
| docs/DATA_DICT.md | 修改 | warn_times 字段 + 枚举 0/11 + 语境列 + 升级备注 |
| plugins/shield/block_type_test.go | 修改/新增 | 枚举边界单测 |

## 3. 实施步骤

- [ ] block_type.go：`BlockOther BlockType = 0`（其他，兜底）、`BlockManual BlockType = 11`（人工收录）；`String()` 0 → "其他"；注释写明口径：**shield_event 拦截事件永远只写 1-10；0/11 仅出现在 ip_blacklist；查询参数 0=全部 语境分离、拦截明细过滤校验保持 0-10 不变**
- [ ] 三方言 ip_blacklist_create_table.sql：新增列 `warn_times`（sqlite/pg `INTEGER NOT NULL DEFAULT 0`；mysql `INT NOT NULL DEFAULT 0`），行注释 = 封禁次数（人工+自动累计，限时时达 5 次转永久）；表头 block_type 注释补 0=其他（仅黑名单表）/ 11=人工收录（仅黑名单表）
- [ ] 三方言 shield_event_create_table.sql：block_type 注释追加「0 与 11 仅 ip_blacklist 表语境使用」
- [ ] docs/DATA_DICT.md：ip_blacklist 字段表补 warn_times（三方言类型对照）；block_type 枚举表补 0/11 与**语境列**（拦截事件 / 黑名单条目，§5.2-1）；升级备注：存量库经 服务→数据库→表结构 检查并执行生成的 ALTER
- [ ] 单测：String(0)="其他"、String(11)="人工收录"、越界值行为不变；黑名单侧 0-11 合法、shield_event 侧 1-10 校验边界（校验函数在 A2 落地，本步先枚举与注释）
- [ ] `go test ./plugins/shield/` 与 `go vet ./plugins/shield/` 通过
- [ ] **落库动作（兼实战验证 B 功能）**：dev 构建运行 → 服务→数据库→表结构 检查 → 应出现 ip_blacklist 缺 warn_times 的 B 级差异与 ALTER 语句 → 执行 → 复查无差异

## 4. 验证

- [ ] `go test ./plugins/shield/ -run TestBlockType -v` 全绿；vet 通过
- [ ] 浏览器：表结构页检查→执行→复查闭环通过；黑白名单页原有功能不受影响（列未接入前端，A5 做）

## 5. 完成标准

清单全勾 + 验证全过 → 状态「已实施」→ 更新总纲。

## 6. 实施回填区（中断现场记录）

（空）
