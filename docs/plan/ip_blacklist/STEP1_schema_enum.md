# A1 · 数据层：warn_times 列 + block_type 枚举 0/11（三处红线同步）

> 实施状态：**已实施**
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

- [x] block_type.go：`BlockOther BlockType = 0`（其他，兜底）、`BlockManual BlockType = 11`（人工收录）；`String()` 0 → "其他"；注释写明口径：**shield_event 拦截事件永远只写 1-10；0/11 仅出现在 ip_blacklist；查询参数 0=全部 语境分离、拦截明细过滤校验保持 0-10 不变**
- [x] 三方言 ip_blacklist_create_table.sql：新增列 `warn_times`（sqlite/pg `INTEGER NOT NULL DEFAULT 0`；mysql `INT NOT NULL DEFAULT 0`），行注释 = 封禁次数（人工+自动累计，限时时达 5 次转永久）；表头 block_type 注释补 0=其他（仅黑名单表）/ 11=人工收录（仅黑名单表）
- [x] 三方言 shield_event_create_table.sql：block_type 注释追加「0 与 11 仅 ip_blacklist 表语境使用」
- [x] docs/DATA_DICT.md：ip_blacklist 字段表补 warn_times（三方言类型对照）；block_type 枚举表补 0/11 与**语境列**（拦截事件 / 黑名单条目，§5.2-1）；升级备注：存量库经 服务→数据库→表结构 检查并执行生成的 ALTER
- [x] 单测：String(0)="其他"、String(11)="人工收录"、越界值行为不变；黑名单侧 0-11 合法、shield_event 侧 1-10 校验边界（校验函数在 A2 落地，本步先枚举与注释）
- [x] `go test ./plugins/shield/` 与 `go vet ./plugins/shield/` 通过
- [x] **落库动作（兼实战验证 B 功能）**：dev 构建运行 → 服务→数据库→表结构 检查 → 应出现 ip_blacklist 缺 warn_times 的 B 级差异与 ALTER 语句 → 执行 → 复查无差异

## 4. 验证

- [x] `go test ./plugins/shield/ -run TestBlockType -v` 全绿；vet 通过
- [x] 浏览器：表结构页检查→执行→复查闭环通过；黑白名单页原有功能不受影响（列未接入前端，A5 做）

## 5. 完成标准

清单全勾 + 验证全过 → 状态「已实施」→ 更新总纲。

## 6. 实施回填区（中断现场记录）

- 2026-08-30：编码由子代理完成（block_type.go 0/11+String 特判、三方言两脚本、blockTypeCount 保持 10 供统计窗口），主代理收尾（DATA_DICT 同步、block_type_test.go 新增、旧断言修正）。
- 连带修正两个既有测试缺陷：①event_recorder_test.go 按日聚合比对用本地日期而 SQL 按 UTC 分桶——本地 0-8 点必失败（午夜跑测试时暴露），改 `base.UTC()` 口径；②越界样本 `blockTypeCount+1`（=11）现为合法枚举，改 `+2`。
- 落库实录（兼实战验证表结构页）：**首次检查未报 warn_times 缺列——bin/hotscripts/sql/ 外挂覆写目录仍是旧脚本（外挂优先于内嵌）**，`cp -r sql/* bin/hotscripts/sql/` 同步后 B 级差异正确报出；执行生成 SQL（2 条：outbox 建表 + ALTER ADD COLUMN）→ 补 D 级缺索引 → 复核 0 差异；存量 413 条记录 warn_times 全部默认 0。★ 接手 Agent 注意：改了 sql/ 脚本必须同步 bin/hotscripts/sql/（若存在）才能在本地验证到新结构。
- 真库集成：internal/db/schema_sync_integration_test.go（PG_TEST_DSN/MYSQL_TEST_DSN 门控）旧版表→diff→生成→执行→复核零差异，mysql/pg 双双通过；连带修正 normType 归一化按字母 token 整词替换（子串替换会把 bigint 误伤成 biginteger）+ pg INT↔integer 别名。
