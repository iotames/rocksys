# TRAFFIC_ANALYSIS 实施期临时决策（呈报人类审查，宪法 §4.3）

## D-T1：前端时间传参口径为本地时间（2026-09-09 · STEP7 实施中发现）
- 触发问题：PLAN §3.4 写「前端将本地时间换算为 UTC 传参」，但服务端 parseTimeRange 沿 logs 页现状把入参按**本地时区**解析再 `.UTC()` 聚合——若前端再换算 UTC 会双重偏移（本机 +8h 实测确认）。
- 备选方案：① 前端换算 UTC 传参（改服务端解析为 UTC 口径，动 logs 页既有行为）；② 前端传本地时间串，服务端维持现状转 UTC。
- 选择与理由：②。PLAN 同节明确「既有 logs 页时区口径沿袭现状，本批不动」，②与该上级约定一致且改动面最小；桶标签仍以 UTC 返回、前端标注 UTC，用户可见语义不变。
- 影响面：views/overview.js 传参格式（'YYYY-MM-DDTHH:mm' 本地时间）；不动服务端解析。
- 可回退性：若人类判定须前端换算 UTC，改 overview.js 的 fmtLocalMin 一处 + 服务端解析口径，返工面小。

## D-T2：traffic 脚本 {table2} 占位符约定（2026-09-09 · STEP3 实施中发现）
- 触发问题：traffic_series/geo_top 需同语句引用 access_log 与 shield_event 两表，而既有 {table} 占位符机制只支持单表替换。
- 备选方案：① 扩展 internal/db 占位符机制为多表（动数据访问层地基库）；② 脚本层约定 {table}=access_log、{table2}=shield_event，读侧（obs.trafficScript）同时替换两个。
- 选择与理由：②。地基库（easyconf/db）不动，约定局部自洽；shield_event 表名可配置（SHIELD_EVENT_TABLE），读侧经 obs.SetShieldTable 注入实值后替换，口径与既有 {table} 一致。
- 影响面：8 个 traffic 脚本头注释、plugins/obs/traffic.go trafficScript；后续任何人写两表脚本都沿用此约定（已写入脚本注释）。
- 可回退性：改地基机制时只需替换读侧一个函数，脚本不用动。

## D-T3：access_log 老数据 UV 口径自然退化（2026-09-09 · STEP7 验收观察）
- 触发问题：本地库升级后老行 user_agent 为空串，实测 UV=独立 IP（2,447=2,447），与「UV=IP+UA」预期不符。
- 处置：非缺陷——PLAN §3.2 既有约定「老行加列后为空串，UV/geo 自然退化」，文档已标注；新写入数据（含 UA）起口径生效。
- 影响面：无代码改动；已知边界照 PLAN §5.5 呈报。
- 可回退性：不适用。
---

## 人类审查结论（2026-09-09）

- **D-T1（时区传参口径）：维持**。
- **D-T3（老数据 UV 退化）：无异议**。
- **D-T2（{table2} 占位符约定）：问题根因被推翻处置**——人类拍板取消 `SHIELD_EVENT_TABLE` 配置项，表名固定为 `shield_event`（"表名为什么要可配置"：无业务收益，徒增 schema 同步/测试/文档负担）。{table2} 替换由此退化为固定常量注入，前提消失、约定保留为内部机制。**补救已实施**：easyconf 注册、sqlIdentRe 校验、main.go 三处 configValue 注入、obs.SetShieldTable 全部移除；DATA_DICT 口径注记改写；防漏单测同步。{table} 占位符本身保留（对固定表名它只是脚本参数化的内部约定，取消=把字面量写回 70×3 个脚本，纯回退）。
