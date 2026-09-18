# Admin API · obs 流量统计端点组（summary / series / geo）

### 3.20 obs 流量统计端点组 — summary / series / geo（实现 `plugins/obs/traffic.go`）

概览页「流量统计」区数据源（实现 `plugins/obs/traffic.go`，`cmd/rocksys` 装配注册）。
数据来自 `access_log`（放行侧）与 `shield_event`（拦截侧）两表 SQL 聚合；
时间口径：前端将本地时间换算为 **UTC** 传入，桶标签也以 UTC 返回、前端原样展示并标注 UTC。

**指标口径（闭合定义，验收对账以此为准）**：

- `req_ok` = 放行总数（**含静态资源、含放行后的 4xx/5xx**）；请求次数 = `req_ok + block_total`；
- `req_pv` = `req_ok` 去静态资源后缀（`.js .css .map .ico .png .jpg .jpeg .gif .svg .webp .woff .woff2 .ttf .eot`）；
- `uv` = `COUNT(DISTINCT client_ip, user_agent)`（历史数据 UA 为空串时退化为纯 IP 口径）；
- geo 空串的展示兜底链「市→省→国名→未知」只做在读侧显示（geoip_list 列保持真实解析语义）：country 级空串（未同步/不可解析 IP）计「未知」、province 级空串显示「中国」（该查询已限定 country_code='CN'），均参与排序不悄悄丢量。

**延迟字段（METRICS_WINDOW）**：`lat_avg / lat_p50 / lat_p95 / lat_p99`（毫秒，int）——范围内 access_log.total_ms 的平均与分位数精确统计；范围内无行时为 `null`（前端显示"—"）。

**缓存语义**：服务端 singleflight + TTL 缓存（key = 端点 + from + to + bucket/source），TTL 由 `OBS_TRAFFIC_CACHE_TTL`（秒，缺省 900=15 分钟，0=禁用，支持热更）控制；`POST /admin/obs/traffic/cache_clear` 可清空全部统计缓存条目（在途计算不受影响）；命中时 `computed_at` 保持首次计算时刻，`cache_ttl_sec` 回传当前 TTL、`cache_hit` 标记是否命中。实时 QPS（`/admin/metrics`）不参与缓存。

**范围取整与缓存命中**：三个端点统一把 from/to 按跨度取整后参与查询与缓存 key——跨度 ≤48h 对齐整小时、更大对齐 UTC 整天。「近 24 小时」一小时内重复查询、「近 7 天/30 天」一天内重复查询直接命中同一缓存 key，命中率不再随分钟漂移归零；代价是统计范围末端最多回退一个取整粒度（当前不完整的小时/天不计入）。**跨度不足一个取整粒度时不取整**（如「今日」在 00:00–00:59 内的范围、同小时自定义范围）：两端会被截到同一整点导致区间退化为零宽、查询命中不到任何行，此时保持原区间。

**超时防护**：统计 SQL 受固定 25 秒超时约束（代码常量，非配置项），超时后数据库侧终止执行（`QueryContext` 下推取消），返回 500 与「可缩小时间范围或稍后重试」文案，防慢查询占用连接。查询上下文与调用方解耦（剥离请求取消信号后叠加固定预算）：结果缓存会把同 key 并发请求合并为一次共享计算，若绑定单个调用方，该请求断开将导致所有搭车请求一起失败——故客户端断开只结束它自己的等待，共享计算的产物照常入缓存。

**降级**：

- obs 未注册 / `OBS_ENABLED=false` → `503` 文本引导态（前端按「功能未开启」页内引导卡渲染，豁免 toast 红线②）；
- `SHIELD_EVENT_LOG_ENABLED=false` → 拦截侧字段（`block_total`/`attack_ips`/`block4xx`/`block_rate`/`block4xx_rate`/`blocked_count`）输出 `null`（前端显示「—」）。率字段输出 `null` 而非 `0`——拦截事件未记录时计数为 0 属数据缺失，输出 `0` 会把「无数据」误报成「零拦截」；`err4xx_rate`/`err5xx_rate` 仅取入网侧，不受此开关影响；
- DB 数据访问层未就绪 → `503`（响应文本含「数据访问层」）：与上一条的降级语义不同，引导用户去开启观测是错误出路，前端须按**普通错误**弹 error toast + 行内说明（指出 `DB_DRIVER`/`DB_DSN` 未配置），不得渲染「功能未开启」引导卡；时间参数非法 / `from` 晚于 `to` → `400`。

| 端点 | 说明 |
|------|------|
| `GET /admin/obs/traffic/summary` | query `from`/`to`（`YYYY-MM-DD` 或 `YYYY-MM-DDTHH:MM`，必传）；响应见下 |
| `GET /admin/obs/traffic/series` | query `from`/`to` + `bucket=hour\|day`（缺省自适应：跨度 ≤48h 用 hour，否则 day；非法值 400）；响应 `{bucket,series:[{bucket,ok_count,blocked_count}],cache_hit,cache_ttl_sec}`，`series[].bucket` 为 UTC 时间标签 |
| `GET /admin/obs/traffic/geo` | query `from`/`to` + `source=access\|blocked`（缺省 access）+ `level=country\|province`（缺省 country；非法值 400）；country 级按国家计数倒序取 Top 10；province 级经 `geoip_list` 关联只统计 `country_code='CN'` 按 `province` 聚合（中国地图专用，去 city 前缀字符串切分）；country 级输出行附 `country_name`（中文国名随聚合带回）；响应 `{source,level,geo:[{country,country_name,cnt}\|{region,cnt}],cache_hit,geo_ready}`；`geo_ready`=GeoIP 是否就绪（未装配 mmdb 或加载失败为 false，前端据此显示常驻警告引导卡） |
| `POST /admin/obs/traffic/cache_clear` | 无参数；清空流量统计结果缓存（只清缓存不改数据），响应 `{ok:true,text}`；WebUI 流量统计卡「清空缓存」按钮用，成功后前端强制重聚当前时间范围 |

**`GET /admin/obs/traffic/summary` 响应 200**：

```json
{
  "req_ok": 1000, "req_pv": 800, "uv": 120, "ip_all": 150,
  "block_total": 30, "attack_ips": 5,
  "err4xx": 20, "err5xx": 2, "block4xx": 30,
  "err4xx_rate": 0.02, "err5xx_rate": 0.002, "block_rate": 0.0291, "block4xx_rate": 1.0,
  "computed_at": "2026-09-09T03:00:00Z", "from": "2026-09-08T00:00:00Z", "to": "2026-09-09T00:00:00Z",
  "cache_ttl_sec": 900, "cache_hit": false
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| req_ok / req_pv / uv / ip_all | int | 放行总数 / PV(访问次数，去静态资源) / UV(独立访客，IP+UA 去重) / 独立 IP 数 |
| block_total / attack_ips | int/null | 拦截总数 / 攻击 IP 数（拦截侧去重）；拦截事件记录关闭时 null |
| err4xx / err5xx | int | 放行侧 4xx / 5xx 数（含静态资源口径内） |
| block4xx | int/null | 拦截侧 4xx 数（当前恒等于 block_total，拦截码均为 4xx，字段预留） |
| err4xx_rate / err5xx_rate | float | 错误率，**分母 = req_ok（仅入网数据，不含拦截）**，Go 侧计算，分母 0 输出 0，0~1 |
| block_rate | float/null | 拦截率 = `block_total / 请求次数(req_ok+block_total)`，即拦截在网关出口请求中的占比；过高表示可能遭攻击或规则过严。拦截事件记录关闭时 null |
| block4xx_rate | float/null | 4xx 拦截占拦截总数之比（分母 = block_total）。拦截码恒为 403/413/429 故恒为 1.0，保留作趋势观察位；记录关闭时 null |
| computed_at | string | 首次计算时刻（UTC RFC3339，缓存命中时不变） |
| from / to | string | 实际生效的统计范围（UTC RFC3339） |
| cache_ttl_sec / cache_hit | int / bool | 当前缓存 TTL（0=禁用）/ 本次是否缓存命中 |

