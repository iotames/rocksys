# 流量大小统计（TRAFFIC_BYTES）实施指导

> 层级定位：本文是**实施指导层（下层）**，受宏观设计 `TRAFFIC_BYTES_DESIGN_PLAN.md`（上层）指导；冲突时上层优先。定稿即冻结，**执行状态不回写本文**（状态归总纲/STEP）。
> 依据：由母文档延伸——口径计算方式、端点契约、决策 D1–D17、验收标准 1–7。
> 状态：随母文档定稿生效；母文档未定稿前本文为草案，不得据此开工（宪法 §2.2 两段式）。
> 本文只答**“怎么拆、红线、风险与回退、影响面”**；实施顺序以总纲为准，操作细节以 STEP 为准。

## 1. 范围

**做**：三段字节口径（报文 = 行 + 头 + 体）的采集修正；`Top IP` / `Top URL PATH` 排行；`/traffic/summary` 三段字节汇总；概览“流量排行”卡；文档同步。

**不做**：`resp_bytes` 历史回填；请求体实测（分块传输体精确化）；method/upstream/status/地区等更多维度；新增独立页 / 新配置项 / 第三方依赖；WAF 页 topIPs 改造。

## 2. 影响面（文件级）

| 类别 | 文件 | 说明 |
|---|---|---|
| 新增 | `internal/netutil/bytes.go` | 报文字节计算共享纯函数（请求/响应） |
| 新增 | `internal/netutil/bytes_test.go` | 算式单测（GET / 小 POST / Host / 多值头 / 长度缺失） |
| 新增 | `sql/{sqlite,postgres,mysql}/traffic_top_ip.sql` | `client_ip` 维度 Top-N 字节聚合 |
| 新增 | `sql/{sqlite,postgres,mysql}/traffic_top_path.sql` | `path` 维度 Top-N 字节聚合 |
| 修改 | `internal/chain/interface.go` | `Context` 增 `RespBytes int64` 字段 |
| 修改 | `internal/chain/adapter.go` | 缓冲 writer 累计实际写出字节并透出 |
| 修改 | `internal/chain`（测试） | >4MB / ≤4MB / 截断路径的计数断言 |
| 修改 | `plugins/obs/obs.go` | `OnDone` 改用报文字节助手 |
| 修改 | `plugins/shield/event_recorder.go` | `newEvent` 改用请求报文字节助手 |
| 修改 | `plugins/obs/traffic.go` | `TrafficTop` 端点（含 D16 降级：`block_available=false` 时 `rows:null` 不入缓存）+ `summary` 字节字段 + 头注释 |
| 修改 | `cmd/rocksys/main.go` | 注册 `/admin/obs/traffic/top` |
| 修改 | `sql/{sqlite,postgres,mysql}/traffic_summary.sql` | 增三段字节 `SUM` |
| 修改 | `webui/assets/js/views/overview.js` | “流量排行”卡 + 三字节指标 |
| 修改 | `docs/api/obs.md` | 新端点与新响应字段 |
| 修改 | `docs/DATA_DICT.md` | 三列口径说明（体 → 报文），无字段增删 |
| 修改 | `sql/{sqlite,postgres,mysql}/access_log_create_table.sql`、`shield_event_create_table.sql` | 列注释口径同步（体 → 报文）；PG 另有 `COMMENT ON COLUMN` 语句 |
| 修改 | `plugins/obs/dim.go`、`plugins/shield/event_recorder.go` | Go 侧维度标题与结构体字段注释同步新口径 |

> 无 DDL、无配置项、无第三方依赖；除 `internal/chain` 外均为“只增不改”或新增文件。

## 3. 实施切片（按依赖排序，每片可独立验证）

### 切片 S1 · 字节口径地基（不接入，可独立验证）
- **范围**：`internal/netutil/bytes.go` 两个纯函数；`internal/chain` 响应体实测累计 + `Context.RespBytes` 透出。
- **验证**：`go test ./internal/netutil ./internal/chain`——算式正确；>4MB 响应计数非 0；≤4MB 准确。
- **依赖**：无。

### 切片 S2 · 采集接入
- **范围**：obs `OnDone`、shield `newEvent` 改用助手；落库口径切换为“报文”。
- **验证**：`go test ./plugins/obs ./plugins/shield`——GET 落库 `req_bytes>0`；>4MB 响应落库 `resp_bytes` 非 0；长度缺失时体部分 0、行/头仍计。
- **依赖**：S1。

### 切片 S3 · 读侧聚合与端点
- **范围**：两份 Top SQL（三方言）+ `traffic_summary.sql` 字节 `SUM`（**字节 `SUM` 对非正值按 0 计，兜底历史 `-1`**）；`plugins/obs/traffic.go` 新增 `TrafficTop` 与 `summary` 字节字段；`cmd/rocksys` 注册。
- **验证**：`go test ./plugins/obs ./internal/db`——`TestScriptParity` 通过；端点排序/`limit`/非法参数 400/`source=blocked` 时 `resp_bytes=0`；小范围造数 Σtop ≤ 全量 SUM；含 `-1` 行时字节和不受污染。
- **依赖**：S2（真实数据）；测试可种子数据。

### 切片 S4 · WebUI
- **范围**：概览“流量排行”卡（`dim`×`source`×Top-N + 表格）与三字节指标。
- **验证**：浏览器实看渲染正确并**截图留证**；失败弹统一 error toast、降级走行内引导（体验红线）。
- **依赖**：S3。

### 切片 S5 · 文档同步与全量核对
- **范围**：`docs/api/obs.md`、`docs/DATA_DICT.md`、`plugins/obs/traffic.go` 头注释；全量 `go test ./...` + `go vet ./...` + `-race`。
- **验证**：文档口径与实现逐项一致；全量测试/静态检查通过；转发 benchmark 无劣化。
- **依赖**：S2、S3（可随前序切片穿插，本片做收口核对）。

## 4. 红线摘录（执行须遵守）

- **数据字典红线**：字段语义变动须三处一致（建表脚本注释 / `docs/DATA_DICT.md` / Go 权威定义）；本批无字段增删，仅改口径说明。
- **配置中心红线**：不新增 `os.Getenv` 读取；本批不新增配置项。
- **文档同步红线**：改行为/文案/接口须同步相关 .md 与配置注释。
- **用户体验红线**：提示统一 `Rock.ui.toast`；服务端报错弹统一 error toast；降级态走行内引导；页面局部不操作全局区域。
- **命令规范**：AI 用原生 `go build -tags dev` / `go test` / `go vet`，不依赖 make。
- **`bin/hotscripts/sql/` 开发期红线**：不放置外挂 SQL，开发期不留残留（改 `sql/` 重编译即生效）。
- **提交规范**：中文单行摘要 + 英文前缀（`feat/fix/docs/chore`），两段式正文；提交/推送前须经用户确认。

## 5. 风险与回退

| 风险 | 影响 | 缓解 | 回退 |
|---|---|---|---|
| 触碰转发内核 `internal/chain` | 唯一共享核心 | 仅附加计数与字段，**不改转发逻辑**；单测 + `-race` 覆盖 | 移除计数与字段，零残留 |
| 口径切换（体 → 报文） | 新旧数据语义不一致 | 文档标注；历史不回填；排行口径统一 | 无数据迁移，二进制级回退 |
| 三方言 SQL 差异 | 占位符/参数数不一致 | 照 `traffic_geo_top.sql` 范式；`TestScriptParity` + 方言渲染测试 | 删新脚本即恢复 |
| 大范围聚合耗时 | 首次查询慢 | 复用 `OBS_TRAFFIC_CACHE_TTL` 缓存与查询超时保护 | 已有机制 |
| 行/头长度近似 | 与真实字节小差 | 数百字节级，对“流量大小”充分；写入已知边界 | 无需处理 |

整批为**加法式改动**：无 DDL、无数据迁移、无新配置/依赖，逐切片可独立回退。

## 6. 验收映射（母文档 → 本层验证）

| 母文档验收 | 覆盖切片 | 验证命令/手段 |
|---|---|---|
| 1 采集合理（GET 非 0 / 大响应非 0 / 无负数） | S1、S2 | `go test ./internal/netutil ./internal/chain ./plugins/obs ./plugins/shield` |
| 2 排行正确 | S3 | `go test ./plugins/obs`（造数精确断言 + Σtop ≤ 全量） |
| 3 汇总正确 | S3 | `go test ./plugins/obs`（summary 对账；拦截字段 `null`） |
| 4 三方言齐平 | S3 | `go test ./internal/db`（`TestScriptParity`） |
| 5 业务无感 | 全批 | `go test -race ./...` + 转发 benchmark |
| 6 UI 可用 | S4 | 浏览器实看 + 截图 |
| 7 文档同步 | S5 | 逐项核对清单 |