# 执行总纲

> 执行依据：docs/plan/README.md（计划目录工作宪法）——记号语义、状态机、关口与裁决以宪法为准。
> 设计依据：docs/plan/TRAFFIC_ANALYSIS_PLAN.md（2026-09-09 已定稿确认，D1–D17 决策表为唯一权威）。

项目状态：进行中

## §0 断点续传（接手者从这里开始）
1. 读项目状态行与 §1 总表：全部「已实施」→ 先做终验呈报（宪法 §5）改「待人类验收」；「待人类验收/等待人类」→ 不实施动作，向用户呈报等待点；
2. 定位第一个非「已实施」步骤；含受阻标注先读该 STEP 回填区，未解除则转向不依赖它的步骤；
3. 「实施中」→ 先跑该 STEP 验证节预写的接手核实命令；通过则从首个未勾选项继续（禁止重做）；不通过按锚点清单只补缺口；裁决不了以代码为准修文档；
4. 「待实施」但锚点检索已有产物命中 → 先核实产物再续做，禁止从零重写覆盖现场；
5. 每次接手核实后、每次状态迁移收口时，跑宪法 §4.1 附状态一致性自检；
6. 严禁凭会话记忆做任何实施判断。

## §1 状态总表
| # | STEP | 内容 | 依赖 | 状态 |
|---|------|------|------|------|
| 1 | TRAFFIC_ANALYSIS/STEP1_数据层三同步.md | access_log 加 user_agent/country/city、shield_event 加 country/city：三方言建表+INSERT、DATA_DICT、Dims/AccessRecord/ShieldEvent/写入链路、TableSpecs；含集成测试基建前置修复（§5.1） | — | 已实施 |
| 2 | TRAFFIC_ANALYSIS/STEP2_geoip包.md | internal/geoip 包（v2 依赖）+ GEOIP_MMDB_DIR 配置 + obs/shield 写时解析接线 | 1 | 已实施 |
| 3 | TRAFFIC_ANALYSIS/STEP3_统计SQL脚本.md | traffic_summary/series_hour/series_day/geo_top 三方言 + sqlite 单测 + 真库门控集成 | 1 | 实施中(子Agent) |
| 4 | TRAFFIC_ANALYSIS/STEP4_obs读侧端点.md | /admin/obs/traffic/{summary,series,geo} + singleflight TTL 缓存 + OBS_TRAFFIC_CACHE_TTL | 1,2,3 | 待实施 |
| 5 | TRAFFIC_ANALYSIS/STEP5_shield窗口与落库总数.md | 实时窗口 1h=60 桶 + window 参数（1m/5m/15m/1h）、落库总数端点、Top IP geo 查询时解析 | 2 | 待实施 |
| 6 | TRAFFIC_ANALYSIS/STEP6_启动缺列检测.md | 启动检测两表缺列打 warning（D16） | 1 | 已实施 |
| 7 | TRAFFIC_ANALYSIS/STEP7_概览流量统计区.md | overview.js 流量统计区：时间范围/指标卡/趋势/geo 卡 + 降级引导（D17） | 2,4 | 待实施 |
| 8 | TRAFFIC_ANALYSIS/STEP8_waf与topIPs前端.md | waf.js 实时卡桶宽切换+落库总数瓦片+标签更正、topIPs.js 地区列、geo 警告 toast | 5,7 | 待实施 |
| 9 | TRAFFIC_ANALYSIS/STEP9_文档同步与终验.md | §3.6 文档清单、bin/hotscripts/sql 同步、全量终验 | 1-8 | 待实施 |

## §2 执行期红线（自仓库法摘录，冲突时以仓库法原文为准）
- 原生命令行（go build/go test/go vet，禁 make）；开发构建 `go build -tags dev -o bin/rocksys ./cmd/rocksys`；程序必须在 bin/ 目录运行。
- 数据字典三同步：三方言脚本 + docs/DATA_DICT.md + Go 权威定义，缺一即违规。
- 三方言脚本改后同步 `cp -r sql/* bin/hotscripts/sql/`。
- WebUI 提示统一 `Rock.ui.toast`；服务端报错必须 error toast（silent 刷新与降级引导态豁免）；文案三要素。
- 全局/局部解耦：概览页改动只动页面局部区块，不动顶栏/菜单。
- 新增配置项必须经 conf.Manager.Register；禁止绕过配置中心 os.Getenv。
- gofmt 格式；提交前 go vet 通过。
- git：本次任务【适用】破例授权（宪法 §3.9，用户 2026-09-09 明示）：每 STEP 验证通过即提交并推送全部远程，失败每分钟重试；push 前 go test ./... 与 go vet ./... 必须通过；禁止 force push。

## §3 进度日志（一行一事）
| 日期 | 执行者 | STEP | 结果 |
|---|---|---|---|
| 2026-09-09 | 智能体 | — | 19:19 定时开工：读 PLAN（含 §5.1 真库环境）重建总纲与 STEP1–9，开始 STEP1 |
| 2026-09-09 | 智能体 | 2 | 子 Agent 并行完成 internal/geoip 包（v2 依赖、查找链、惰性加载、7 单测全过）；接线归 STEP2 |
| 2026-09-09 | 智能体 | 1 | 已实施：两表加列三同步 + integration 基建修复（Flock 拆分/updated_at 旧版 DDL/PG 排序 CAST 存量缺陷）；go test ./... 与 vet 全绿，双真库 integration 全过 |
| 2026-09-09 | 智能体 | 2 | 已实施：GEOIP_MMDB_DIR 注册 + 共享 Resolver 注入 obs/shield 写时解析；真实 mmdb 落值验证留 STEP7/9 |
| 2026-09-09 | 智能体 | 6 | 已实施：missingLogColumns 启动缺列检测 + 三子用例单测；运行时冒烟并入 STEP7 |
