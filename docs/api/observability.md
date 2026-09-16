# Admin API · 观测与日志（metrics / logs / version / warnings / system）

### 3.10 GET /admin/metrics — 运行指标快照（窗口可切）

**查询参数**（可选）：`window=1m|5m|15m|1h`——实时窗口桶宽（内存窗口固定 1 小时 = 60×1 分钟桶，按所选窗口整分钟桶聚合零误差），缺省 `1m` 兼容现状；非法值 400。

**响应 200**：

```json
{ "qps": 1204.5, "error_rate": 0.0002, "window": "15m", "window_seconds": 900 }
```

| 字段 | 类型 | 说明 |
|------|------|------|
| qps | float | 每秒请求数（所选窗口总量 / 窗口秒数） |
| error_rate | float | 错误率（4xx/5xx 占比，0~1） |
| window / window_seconds | string / int | 回显窗口名与窗口秒数 |

> **延迟分位数口径（METRICS_WINDOW）**：本端点不输出 p50/p95/p99（内存窗口跨桶分位数只能近似），
> 由 `GET /admin/obs/traffic/summary` 的 `lat_p50/lat_p95/lat_p99/lat_avg` 提供——基于 access_log.total_ms
> 在所选时间范围内**精确**统计（PG percentile_cont / MySQL·sqlite PERCENT_RANK 最近秩）。

**失败 `503`**：观测组件（`obs`）未注册/未启用，响应体文本 `obs 未注册`。前端应显示"观测未开启"并引导到组件页开启。

> 该接口为内存聚合快照，无历史数据。前端趋势图需按刷新周期自行累积采样。

### 3.11 GET /admin/logs — 按条件查询访问日志

**查询参数**（均可选）：

| 参数 | 说明 |
|------|------|
| from | 开始时间，`YYYY-MM-DD`（当日 00:00）或 `YYYY-MM-DDTHH:MM`（精确到分），缺省当天 00:00 |
| to | 结束时间，`YYYY-MM-DD`（当日 23:59）或 `YYYY-MM-DDTHH:MM`（精确到分），缺省当天 23:59 |
| path | 请求路径精确匹配（如 `/api/order/1`） |
| path_like | 请求路径模糊匹配（子串包含，如 `/api/order`） |
| trace_id | 链路标识模糊匹配（API 层保留，WebUI 已移除该输入框） |
| status_group | 状态分组，状态码首字符 `'2'`-`'5'`（如 `'4'` = 4xx），缺省不过滤 |
| only_error | `'1'` = 仅异常（`status_code >= 400`），缺省不过滤 |
| sort | 排序：`time_desc`（缺省，最新在前）/ `total_desc`（耗时降序）/ `total_asc`（耗时升序） |
| limit | 单页条数，1-50000，缺省 2000 |
| offset | 分页偏移，非负整数，缺省 0 |

**响应 200**：`Content-Type: application/x-ndjson`，每行一个 JSON 对象；`X-Total-Count` 响应头回传满足条件的总条数（与 `limit`/`offset` 配合实现服务端分页）（平铺维度，扩展负载字段如 `request_body` 直接出现在顶层）：

```json
{"time":"2026-08-04T10:12:03+08:00","trace_id":"ab34...","path":"/api/order/1","method":"GET","client_ip":"203.0.113.7","status_code":200,"upstream":"http://o1:9001","shield_ms":1,"biz_ms":11,"total_ms":12,"req_bytes":512,"resp_bytes":1024,"user_agent":"Mozilla/5.0 ...","country_code":"CN","country_name":"中国","province":"广东省","city":"深圳市"}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| time | string | 请求完成时间（RFC3339） |
| trace_id | string | 链路标识 |
| tenant_id | string | 租户标识（可为空，字段可能缺省） |
| path | string | 请求路径 |
| method | string | HTTP 方法 |
| client_ip | string | 客户端地址 |
| status_code | int | 响应状态码 |
| upstream | string | 最终转发目标（未命中路由时为默认后端） |
| shield_ms | int | 防护耗时（毫秒） |
| biz_ms | int | 业务/转发耗时（毫秒） |
| total_ms | int | 总耗时（毫秒） |
| req_bytes | int | 请求流量（字节） |
| resp_bytes | int | 响应流量（字节） |
| user_agent | string | 客户端 User-Agent（UV 口径=IP+UA） |
| country_code | string | 客户端 GeoIP 国家码（ISO，如 `CN`；geoip_list 未命中且实时解析失败为空串，前端显示「未知」） |
| country_name | string | 本地化国名（zh-CN 优先，如 `中国`；同上可为空串） |
| province | string | 一级行政区（省/州，如 `广东省`；可为空串） |
| city | string | 城市名（仅市，如 `深圳市`；可为空串） |
| city | string | 客户端 GeoIP 省市（City 库解析；mmdb 未加载为空串） |
| （扩展维度） | 不定 | 负载维度（如 `request_body`），由 obs 维度注册表定义，平铺输出 |

**数据来源**：`access_log` 表（复用统一数据访问层，`DB_DRIVER`/`DB_DSN`）；数据访问层未就绪时返回空。`access_log` 表字段定义见 `docs/DATA_DICT.md`。**按 `sort` 排序（缺省完成时间倒序），`limit`/`offset` 服务端分页（WebUI 每页 20/50/100）**；状态分组/仅异常/耗时排序均由后端执行。

**失败 `400`**：时间格式非法（应为 `YYYY-MM-DD` 或 `YYYY-MM-DDTHH:MM`）/ `from` 晚于 `to`，响应体文本为错误原因。
**失败 `503`**：观测组件未注册。

> 前端按行解析（`split('\n')` + 每行 `JSON.parse`，跳过坏行）。某时段无日志时后端返回空；整段为空时提示"所选时间范围无访问日志"。

### 3.11.1 GET /admin/logs/storage — 日志存储总占用

**响应 200**（`application/json`）：

```json
{"total_bytes":1089536}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| total_bytes | int | 日志库占用（`access_log` 表 + 索引；数据访问层未就绪时为 0） |

> WebUI 日志页顶部展示该统计。

---

### 3.16 GET /admin/version — 构建版本信息

返回构建期注入的版本信息（与 `rocksys --version` 命令同源，保证两处一致），WebUI 左上角品牌区展示。

**响应 `200`：**

| 字段 | 类型 | 说明 |
|------|------|------|
| `version` | string | 版本号（当前 git 最新 tag，无 tag 时 `dev`；tag 后有提交时 `tag-dev`，如 `v0.0.1-dev`） |
| `build_time` | string | 构建时间（如 `2026-08-19T16:11:46+08:00`） |
| `go_version` | string | 编译用 Go 版本（如 `go1.25.3`） |

### 3.17 GET /admin/warnings — 数据清理未开启警告

返回数据清理未开启提醒（与 `POST /admin/auth/login` 响应 `warnings` 字段同源，均为 `pruneWarnings()` 扫描结果）。WebUI 在应用启动与登录后调用，用于渲染**常驻置顶横幅**（登录态为 localStorage token、无会话内缓存，刷新页面后经本端点重拉，配置变更实时反映）。鉴权与其余内建端点一致（回环免鉴权 / token 或登录 JWT）。

**响应 `200`：**

| 字段 | 类型 | 说明 |
|------|------|------|
| `warnings` | string[] | 数据清理未开启提醒：`SHIELD_EVENT_PRUNE_ENABLED=false` → 拦截明细膨胀提醒；`OBS_LOG_PRUNE_ENABLED=false` → 访问日志膨胀提醒；组件未装配则无对应项；无警告时为空数组 `[]` |

**示例：** `{"warnings":["拦截记录清理未开启，shield_event 表可能持续膨胀（可在配置页开启 SHIELD_EVENT_PRUNE_ENABLED）","访问日志清理未开启，access_log 表可能持续膨胀（可在配置页开启 OBS_LOG_PRUNE_ENABLED）"]}`

---

### 3.17.1 GET /admin/system — 运行时长 + 机器资源概况（实现 `internal/adminapi/handlers_system.go`）

概览页数据源：「运行指标」卡的**运行时间瓦片**与「资源监控」卡。标准库实现（不引第三方依赖）：
无常驻采集协程，CPU% 走惰性采样（距上次真实采样 ≥3s 才重读一次 `/proc`，窗口内直接复用缓存
结果、既不读 `/proc` 也不推进快照；无人访问零开销）；系统级 CPU 按 busy/total 口径计算
（全机时间片总和扣除 idle+iowait 的占比，与 top 一致，天然归一化到 0-100）；系统级 CPU / 内存
依赖 Linux `/proc`，非 Linux 平台或 `/proc` 读取失败（如加固容器）时对应字段为 `null`
（前端按平台与成因降级提示）；首次采样前（尚无差值
可比）`cpu_percent` / `proc_cpu_percent` 亦为 `null`（两者就绪相互独立：`/proc/stat` 不可得时
进程值仍可单独得出）。

**响应 `200`：**

| 字段 | 类型 | 说明 |
|------|------|------|
| `uptime_seconds` | int | 进程启动至今秒数（运行时间瓦片展示，前端格式化为 `N 天 hh:mm:ss`） |
| `started_at` | string | 进程启动时刻（RFC3339） |
| `cpu_percent` | float/null | 全机 CPU 占用（busy/total 口径，0-100，一位小数；非 Linux、`/proc/stat` 不可得或首次采样前为 null） |
| `proc_cpu_percent` | float/null | 本进程 CPU 占用（与 top 同口径，多线程可超 100；非 Linux 或首次采样前为 null，就绪与 `cpu_percent` 相互独立） |
| `mem_total` | int/null | 系统内存总量字节（非 Linux 或 `/proc/meminfo` 读取失败为 null） |
| `mem_used` | int/null | 系统内存已用量字节（total - available，available 优先 MemAvailable、老内核回退 MemFree+Buffers+Cached；非 Linux 或读取失败为 null） |
| `proc_mem_bytes` | int | 进程从 OS 获取的内存总量（`runtime.MemStats.Sys`） |
| `heap_bytes` | int | 进程在用堆内存（`HeapAlloc`） |
| `goroutines` | int | 当前 Goroutine 数 |
| `num_cpu` | int | 逻辑核心数 |
| `os` / `arch` | string | 运行平台（如 `linux` / `amd64`） |

