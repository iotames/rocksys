# 执行总纲

> 执行依据：docs/plan/README.md（计划目录工作宪法）——记号语义、状态机、关口与裁决以宪法为准。

项目状态：进行中（GEOIP_LIST）

> 关口记录：用户 2026-09-15 指令「执行 docs/plan/GEOIP_LIST_PLAN.md 开发方案，期间自主验收，提交Git，并推送远程」视为母文档定稿确认（宪法 §2.2 认可认定）；git 破例授权同步授予，范围 = 本项目 STEP 的 master 分支提交与推送，禁止 force push（宪法 §3.9）。

## §0 断点续传（接手者从这里开始）
1. 读项目状态行与 §1 总表；
2. 定位第一个非「已实施」的步骤；受阻按铁律 4 处理；
3. 「实施中」先跑该 STEP 验证节预写的接手核实命令；
4. 「待实施」但锚点已有产物命中 → 先核实产物再续做；
5. 每次接手核实后、每次状态迁移收口时跑 §4.1 附状态一致性自检；
6. 严禁凭会话记忆做任何实施判断。

## §1 状态总表
| # | STEP | 内容 | 依赖 | 状态 |
|---|------|------|------|------|
| 1 | geoip_list/STEP1_数据层.md | geoip_list/schedule_list 三方言脚本 + 两表删 country/city 4 列与 idx_*_time_country + 聚合 SQL 去字符串切分 | — | 已实施 |
| 2 | geoip_list/STEP2_同步器.md | geoip 包拆 Province；geoSyncAll 改增量构建 geoip_list；GEOIP_SYNC_INTERVAL 定时器；同步后清统计缓存 | 1 | 已实施 |
| 3 | geoip_list/STEP3_登记与端点.md | schedule_list 装配期 upsert 登记（系统级重启重置）+ GET /admin/schedule/list + geoip_sync 状态回写 | 1,2 | 已实施 |
| 4 | geoip_list/STEP4_读侧.md | /admin/logs、/admin/shield/events 明细 JOIN + 读侧回退 Lookup；traffic geo 聚合端点适配新 SQL | 1 | 待实施 |
| 5 | geoip_list/STEP5_前端.md | #/schedule 只读页；数据库页同步卡改文案+上次同步时间；概览地理位置卡同步按钮+能力边界注记；logs/waf/topIPs 字段适配 | 3,4 | 待实施 |
| 6 | geoip_list/STEP6_文档.md | DATA_DICT/webui-api/COMPONENTS/webui/CONFIGURATION/PROJECT_STRUCTURE/sql README 同步 | 1-5 | 待实施 |

## §2 执行期红线（自仓库法摘录，冲突时以仓库法原文为准）
- 构建/测试一律原生命令行：`go build -tags dev -o bin/rocksys ./cmd/rocksys`、`go test ./...`、`go vet ./...`；不调 make。
- 运行必须 `cd bin` 工作目录，严禁项目根目录执行。
- 数据字典红线：数据层/枚举变动同步 三方言脚本 + docs/DATA_DICT.md + Go 权威定义三处。
- UX 红线：报错统一 `Rock.ui.toast(msg,'error')` 不自动消失；页面 load 透传 opts（含 silent）。
- geoip_list.province 存 zh-CN 全称（不得存短名，否则中国地图着色静默错位）。
- 增量发现禁无界 DISTINCT（D23）。
- git 提交：本次任务【适用】破例授权（见头部关口记录），一个 STEP 验证通过即提交；推送远程在全部步骤完成后执行一次（多远程全部成功）。

## §3 进度日志（一行一事）
| 日期 | 执行者 | STEP | 结果 |
|---|---|---|---|
| 2026-09-15 | Claude | — | 现状排查完成；总纲与 STEP1-6 建纲；用户指令视为母文档确认+git 破例授权 |
| 2026-09-15 | Claude | STEP1-2 | 数据层新表/删列 + 同步器改造完成并提交（写路径适配前置，两步合并提交） |
| 2026-09-15 | Claude | STEP3 | schedule_list 登记 + 端点 + 状态回写完成（reset 脚本改全列覆写 upsert）；提交 |
