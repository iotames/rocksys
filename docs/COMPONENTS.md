# RockSys 开发者组件手册

面向**开发者用户**：讲解 RockSys 各组件、子组件的作用与使用方法。终端用户请见项目主页 [README](../README.md)。

---

## 1. 三层结构

| 层 | 目录 | 性质 |
|----|------|------|
| 地基库 | 根下 `easyserver/`、`easyconf/`、`easydb/` | 独立子模块，可脱离复用 |
| 框架私有 | `internal/`（engine/chain/dataflow/hotswap/conf/adminapi） | 不可关、外部不可 import |
| 可选挂件 | `plugins/`（13 个平铺目录） | 默认全关，可热插拔 |

红线：**底座不依赖任何挂件；挂件是底座的"挂件"，摘除不影响底座。** 所有挂件经 `internal/hotswap` 统一管理生命周期（启用/禁用/排空/热更）。

---

## 2. 框架私有（internal/）

### 2.1 `internal/engine` — 反向代理引擎

- **作用**：接收全部 HTTP 请求、转发、回传响应；WebSocket 拒绝（501）、转发超时（504）、自动追加 `X-Forwarded-For` / `X-Trace-Id`。
- **使用**：由 `cmd/rocksys` 装配，一般不需要开发者直接操作。`Forward(w, r, target, df)` 是转发核心。

### 2.2 `internal/chain` — 转发链编排

- **作用**：中间件链，三个槽位 `Head`（防护/认证）→ `Middle`（路由/改写）→ 转发 → `Tail`（响应处理）。
- **接口**：

```go
type Middleware interface {
    Name() string
    Handle(ctx *Context) (next bool) // false = 中断链（已写响应）
}

type ResponseHook interface { // 挂 Tail 槽位
    OnResponse(ctx *Context) error
}
```

- **编写中间件**：实现 `chain.Middleware` + `hotswap.MiddlewareLifecycle`（见 §3），即可被热开关管理。

### 2.3 `internal/dataflow` — 请求级数据流

- **作用**：请求穿越转发链的"车厢"：`trace_id` / 三时间戳（BeginAt/BeginBizAt/DoneBizAt）/ 租户 / 转发目标（Target）。
- **使用**：`ctx.DF.SetTarget(url)` 设置转发目标（dispatch 用）；`ctx.DF.TraceID()` 读取链路 ID；耗时分解 `ShieldMs()` / `BizMs()` / `TotalMs()`。

### 2.4 `internal/hotswap` — 生产热运维引擎 ★

- **作用**：统一承载三类热操作：配置热更、组件热开关（原子切换 + 排空）、脚本热载。
- **接口**：

```go
type MiddlewareLifecycle interface {
    chain.Middleware
    Start(cfg any) error // 重建不可变快照并原子替换
    Stop() error
    Slot() chain.Slot
}
```

- **约定**：Start 用不可变快照（`atomic.Value`）承载运行态，保证与在途请求并发安全；Start 失败保留旧快照。
- **ScriptHub 统一内容中枢**（`internal/hotswap/hub.go`）：三类外挂文件（`sql/`、`rules/`、`trusted_proxies/`）的统一内容中枢——缓存 + 监控 + 推送全部内聚，消费端只认识 `GetScriptText(sub, relPath)`（取内容）与 `Subscribe(sub, fn)`（收通知）两个接口，不感知内容如何生产。底层读取仍统一经 `ScriptDir.GetScriptBytes`（外挂优先、内嵌兜底，红线不变）。监控为 `HOT_FILES_WATCH_INTERVAL`（默认 3s）指纹轮询（mtime 纳秒 + size），文件增/删/改均触发；变化 → 重读 → 更新缓存 → 才通知订阅者（读失败保留旧内容仅告警）。三类外挂文件变更均 ≤3s 自动生效：SQL 文本即用吃缓存、WAF 规则订阅后重建快照（复用 `Start(nil)`）、可信代理订阅后解析原子替换。监控循环随 `Manager` 生命周期启停。

### 2.5 `internal/conf` — 底座配置

- **作用**：统一配置源（命令行 > 环境变量 > `.env` 文件），支持热更回调。**第一原则「热更即持久化」**：运行期 `Set` 热改立即生效并同步写回配置文件（`--config` 存在时写 configFile，否则 `.env`），重启后保留。
- **使用**：挂件在 `New()` 里调用 `cfgMgr.Register(&field, "ENV_NAME", defval, title)` 注册配置项，easyconf 自动写入字段；配置变更时 hotswap 对已启用实体调用 `Start(nil)` 重建快照。

### 2.6 `internal/adminapi` — 管理 API

- **作用**：回环地址管理接口（默认 `127.0.0.1:19527`），供 rockctl / curl / WebUI 在线操作。
- **接口**：
  - `GET /admin/switch/list`、`POST /admin/switch/on|off/<name>`：组件热开关与状态
  - `GET /admin/config`、`PUT /admin/config`、`GET /admin/config/list`：底座/热改/全量配置项清单（供 WebUI 分组展示）
  - `POST /admin/script/publish|rollback`、`GET /admin/script/list`：脚本发布/回滚/版本历史
  - `GET /admin/metrics`、`GET /admin/logs`：观测指标与日志（obs 挂件端点注入）
- **WebUI 托管**：`RegisterWebUI(fsys fs.FS)` 注册静态资源（根路径 `/` 返回 index.html，`/assets/...` 返回各静态文件），**每请求实时 `fs.ReadFile` 读取、不缓存**。控制台为纯静态单页（ElementUI 风格、无框架），静态资源双模式：生产由 `webui/embed.go` 用 `//go:embed index.html assets` 内嵌进二进制；开发（`-tags dev`）由 `webui/embed_dev.go` 用 `os.DirFS("../webui")` 实时读源码目录，改前端文件刷新即见、免重新编译。访问 `http://<admin-addr>/` 打开。

### 2.7 `internal/geoip` — GeoIP 解析器（框架私有）

- **作用**：基于 MaxMind GeoLite2 mmdb 文件的 IP 地理位置解析（无第三方依赖），供 obs / shield **共享一个实例**（GEOIP_LIST 方案）：地理信息不再逐行落库，由 `geoip_list` 关联表承载（同步器增量构建，见 `cmd/rocksys/geoip_sync.go`）；读侧明细/聚合经 JOIN 关联取用，JOIN 未命中回退实时 `Lookup`（Top IP 等少行场景保持查询时逐行解析）。解析结果四字段：ISO 国家码 / 本地化国名（zh-CN 优先）/ 一级行政区 / 城市名（省与市分离，名实相符）。
- **服务提供者模式（功能总开关内聚）**：`GEOIP_ENABLED` 总开关内聚在 `Resolver` 单点裁决（`SetEnabled`/`Enabled`/`Ready`/`Lookup` 少数入口），消费方不各自读配置——`Ready()` = 功能开启 **且** mmdb 已加载，消费侧门控（自动同步定时器、日程登记 `enabled`、`geo_ready` 信号）经它自动遵守开关。禁用时实时 `Lookup` 返回零值，读侧展示层以 `geoip.DisabledText`（「服务未开启」）替代（仅展示、绝不落库——写侧自动同步被 `Ready()` 门控阻断；JOIN 命中的 geoip_list 历史地区照常显示）。**手动同步例外**：定位为特殊场景的异步 DB 维护任务（不影响主程序转发），经 `SyncReady()`（仅要求 mmdb 已加载）与 `LookupSync()`（不受开关限制的解析入口）单独门控——功能关闭时数据库页手动同步照常可用。`GEOIP_ENABLED` 支持配置热更（Watch 回调 `SetEnabled`，秒级生效）；已知边界：启动时禁用、运行期再开启，自动同步定时器需重启拉起（手动同步即时可用），运行期关闭由定时器每轮就绪复查自动停摆。
- **查找链（逐文件独立）**：`GeoLite2-City.mmdb` 与 `GeoLite2-Country.mmdb` 各自按 **`GEOIP_MMDB_DIR`（默认 `geoip`）→ 当前工作目录 → `$HOME/geoip`** 查找，City 优先、缺失回退 Country（仅国家码）。
- **降级与生效**：惰性加载，未放置 mmdb 时启动日志 warning 告警一次（同步器不启动定时器、明细 geo 显示「未知」），**不阻断转发**；文件补放后**重启生效**（不做运行期热载）。`Ready()` 供端点回传 `geo_ready` 就绪信号（前端引导卡判定）与自动同步生效前置判定。
- **配置**：`GEOIP_ENABLED` / `GEOIP_MMDB_DIR`（见 `docs/CONFIGURATION.md`，WebUI 全局配置页「GeoIP」分组）；调用链路见 `docs/PROJECT_STRUCTURE.md`。

---

## 3. 可选挂件（plugins/）

**设计原则：每个挂件只有一个开关，不设双重概念。** 影响 HTTP 流动/观测的中间件挂件的唯一开关是 `XXX_ENABLED`（`插件目录名转大写_ENABLED`，默认 `false`），它同时决定"是否挂载"与"是否生效"——**挂载即生效，不存在"挂载但放行"状态**。默认全关，开启途径（同一开关，永不分裂）：

- `.env` 写 `XXX_ENABLED=true` → 重启自动挂载；运行期热改该值即时联动挂载/摘除（配置中心是挂载状态的唯一真源）。
- `rockctl switch on/off` 或 `POST /admin/switch/on|off/<name>` → 即时切换，并自动持久化回 `.env`（重启后按配置恢复）。

独立组件（config/registry/object）不参与单请求处理、无此开关（无条件注册）；mq 已由 `MQ_ENABLED` 条件装配控制。

> 子开关（如 `SHIELD_WAF_*`、`OBS_LOG_PRUNE_ENABLED`）是挂件**内部行为**开关：`XXX_ENABLED` 关闭时不挂载、子开关一律不生效；开启后子开关按各自值决定子功能是否启动。

### 3.1 shield — L1 防护（转发链中间件，Head）

**作用**：IP 黑白名单、路径/UA 规则、令牌桶限流、WAF 检测。

**配置项**：

| 配置 | 默认 | 说明 |
|------|------|------|
| `SHIELD_ENABLED` | false | 父开关：false=不挂载（默认）；true=挂载并拦截 |
| IP 黑名单 | — | **DB 表 `ip_blacklist`（管理面录入/导入，动态）∪ 外挂 `rules/ip_blacklist.txt`（静态兑底）**，取并集；DB 未启用时仅外挂生效；热路径只读内存快照（TTL 60s 刷新），管理面见「黑白名单」Tab（录入/批量导入） |
| IP 白名单 | — | **仅 DB 表 `ip_whitelist`（管理面录入/导入，动态）**；白名单优先于黑名单；热路径只读内存快照（TTL 60s 刷新），管理面见「黑白名单」Tab（录入/批量导入）；不再走 `.env` 配置 |
| `SHIELD_RATE_LIMIT_RPS` / `BURST` | 0 / 0 | 限流速率与突发容量（0=不限流） |
| `SHIELD_RATE_LIMIT_BY` | ip | 限流维度（当前仅支持 ip） |
| `SHIELD_ALLOW_METHODS` | 空 | HTTP 方法白名单（空=不限） |
| `SHIELD_MAX_BODY_SIZE` | 0 | 请求体上限字节（0=不限） |
| `SHIELD_WAF_SQL_INJECTION` / `XSS` / `PATH_TRAVERSAL` / `RISK_PATH` / `CRAWLER_UA` | false | WAF 检测开关 |
| `SHIELD_WAF_RISK_PATHS` | 空 | 追加风险路径（逗号分隔，需先开启 `SHIELD_WAF_RISK_PATH`） |
| `SHIELD_EVENT_*` | 见 default.env | 拦截事件落库配置（`LOG_ENABLED`/`RETENTION_DAYS`/`PRUNE_ENABLED`/`TABLE`/`BUFFER`/`FLUSH_ROWS`/`FLUSH_INTERVAL`，共 7 项） |
| `HOT_SCRIPTS_DIR` | hotscripts | 脚本外挂统一根目录；WAF 规则外挂子目录固定 `rules/`（优先加载，嵌入兜底） |

**示例**：

```bash
rockctl switch on shield
SHIELD_RATE_LIMIT_RPS=100 \
SHIELD_WAF_SQL_INJECTION=true SHIELD_WAF_XSS=true rocksys --upstream http://127.0.0.1:9000
```

**拦截事件与 GeoIP / 实时窗口**：拦截事件落 `shield_event` 表（装配注入共享 `internal/geoip` 解析器，见 §2.7；geo 经 `geoip_list` 关联 + 未命中回退解析（GEOIP_LIST 方案）；Top IP 统计行为查询时逐行解析）。实时计数内存窗口固定 **1 小时 = 60×1 分钟桶**，`GET /admin/shield/metrics?window=1m|5m|15m|1h` 按所选窗口整桶聚合（缺省 1m）；`GET /admin/shield/total` 返回落库总数（查库口径，受保留期影响）。契约见 `docs/api/shield.md`。

### 3.2 dispatch — L2 路由分发（转发链中间件，Middle + Tail 双件）

**作用**：基于数据库路由四表的三层路由体系——**路由规则 → 负载均衡器 → 上游节点**。请求经规则匹配（域名 × 路径，`match_order` 升序命中即停）选定均衡器，再由均衡策略选出健康节点写入转发目标（`ctx.DF.SetTarget`），实际转发由转发引擎执行；未命中规则走 `ROCKSYS_UPSTREAM` 默认后端。

**配置项**：仅 `DISPATCH_ENABLED`（父开关，默认 false；false=不挂载）。路由数据不进配置项，统一存数据库四表（`dispatch_rule` / `dispatch_upstream` / `dispatch_node` / `dispatch_upstream_node`，标签经 `dispatch_tag` / `dispatch_rule_tag` 关联），经管理接口 `/admin/dispatch/*` 或 WebUI「路由分发」页维护，保存后自动重建内存快照热更（免重启）。

**规则匹配**（域名条件 × 路径条件均满足即命中）：

- 域名：留空 = 匹配任意域名；非空 = 精确匹配（保存时归一转小写，匹配时剥端口），不支持通配；
- 路径三类型：`前缀`（段对齐且命中自身，`/api` 命中 `/api` 与 `/api/x` 不匹配 `/apix`；`/` = 全路径兜底）、`精确`（全等）、`模式`（`:param` 捕获单段注入 `X-Route-Param-*` 请求头 / `*` 通配剩余路径）。

**负载均衡**：`round_robin`（平滑加权轮询，权重取关系表，默认 1）/ `least_conn`（在途最少优先，平局回落轮询游标）。**会话保持（sticky）**：均衡器可开启，网关自种 Cookie（默认名 `rocksys_node`，值为节点 id）实现粘性直路由。**节点优先级**：高优（默认）/ 备份（NGINX backup 同款语义，高优健康集全不健康时才启用）。

**健康检查中心**：节点登记探活参数（周期/超时/路径，`hc_path` 空 = 不探活视为健康）后由探活任务中心周期探测（2xx/3xx 判健康），健康状态只在内存 registry（单一事实源，不落库），不健康节点自动摘出选点集并经 `/admin/dispatch/health` 透出。

**降级语义**：DB 不可用时保持空表快照（全部请求走默认 upstream，无 5xx），后台按固定间隔重试直至首次构建成功即停，无需人工重载；快照整体不可变、热更经原子替换（请求热路径零 DB 查询、零锁）。

**双中间件**：主件 `dispatch`（Middle 槽，匹配与选点、在途计数 +1）与收尾件 `dispatch-tail`（Tail 槽，请求收口在途计数 -1，两者经 `DISPATCH_ENABLED` 同键联动启停）；least_conn 的在途统计依赖收尾件。已知边界：后继中间件中断链的请求不经过收尾回调，存在低频在途计数偏差（不影响转发）。

**管理接口**：19 端点（`/admin/dispatch/*`：规则 CRUD + 命中测试 + 枚举元数据、均衡器 CRUD（引用保护 409）、节点 CRUD（引用保护 409）、标签管理、手动重载快照、健康快照），契约见 `docs/api/dispatch.md`。

### 3.3 rewrite — 转发前改写（转发链中间件，Middle）

**作用**：转发前改写 URI 前缀与请求头（路径归一化 / 版本剥离 / 注入标记头）。

**配置项**：`REWRITE_ENABLED`（父开关，默认 false）、`REWRITE_RULES`

```
格式：<prefix>=<spec>[;<spec>...]，逗号分隔
  spec：uri|<new_prefix>        改写 URI 前缀
        header=<name>:<value>   设置请求头
```

**示例**：

```bash
# /api/v1/orders/123 → /api/orders/123，并注入标记头
REWRITE_RULES="/api/v1/=uri|/api/;header=X-Proxy-Tag:rewrite"
```

**限制**：不支持改写 Host（engine 转发时强制使用目标节点 host，改 Host 属路由职责，由 dispatch 的 Target 决定）。

### 3.4 script — RockScript（转发链中间件，Middle）

**作用**：Lua 策略引擎，只做网关策略（安全规则、路由改写、A/B 分流），不落业务数据。

**配置项**：`SCRIPT_ENABLED`（父开关，默认 false）、`SCRIPT_TIMEOUT`（执行超时毫秒，修改后需重启服务生效）。

**使用**：

```bash
rockctl script publish <file.lua>   # 发布策略（沙箱校验，禁 os 等危险库）
rockctl script rollback             # 回滚上一版本
```

### 3.5 obs — RockObs（转发链中间件，Tail + ResponseHook）

**作用**：访问日志（异步落库）+ 指标聚合 + 查询 API。

**配置项**：`OBS_ENABLED`（父开关，默认 false）、`OBS_LOG_PRUNE_ENABLED`（access_log 自动清理子开关，默认 false）、`OBS_LOG_RETENTION_DAYS`（保留天数，默认 7）、`OBS_TRAFFIC_CACHE_TTL`（流量统计结果缓存 TTL，秒，缺省 600=10 分钟，0=禁用，支持热更）。

**存储**：访问日志统一写数据库——复用统一数据访问层（`DB_DRIVER`/`DB_DSN`，默认 sqlite `rocksys.db`）写 `access_log` 表；SQL 外置 `sql/<dbtype>/`。数据访问层未就绪/建表失败时降级丢弃日志并告警（不阻断转发）。`access_log` 表字段/枚举见 `docs/DATA_DICT.md`。

**异步落盘**：日志写入有界队列（4096 条，满则丢弃告警），后台 goroutine 批量写入当前后端；`Flush` 保证停机前全部落盘。

**查询**：`GET /admin/metrics` 返回 QPS / P50 / P95 / P99 / 错误率；`GET /admin/logs` 按时间范围（精确到分）+ path 精确/模糊过滤返回 JSONL（详见 docs/api/observability.md §3.11）；`GET /admin/logs/storage` 返回日志库占用（access_log 表 + 索引，WebUI 日志页顶部展示）。

**流量统计报表**（读侧聚合端点，实现 `plugins/obs/traffic.go` + `traffic_cache.go`）：`GET /admin/obs/traffic/summary`（指标标量+率）、`GET /admin/obs/traffic/series`（访问/拦截时间桶趋势，hour/day 缺省自适应）、`GET /admin/obs/traffic/geo`（地区分布，source=access/blocked × level=country/province 双维切换，含 `geo_ready` 就绪信号）——数据为 `access_log` ∪ `shield_event` 两表 SQL 聚合（geo 经 `geoip_list` 关联聚合，GEOIP_LIST 方案；geo 同步成功自动清本缓存），服务端 singleflight+TTL 缓存（`OBS_TRAFFIC_CACHE_TTL`）；obs 未启用 503 引导降级、拦截事件记录关闭时拦截侧字段 null。指标闭合口径与响应契约见 `docs/api/obs.md`。

**GeoIP 关联表（GEOIP_LIST 方案）**：日志表只写 `client_ip` 事实，地理信息由 `geoip_list`（一 IP 一行）承载；同步器增量构建（扫两表 `client_ip` 求差 → 逐 IP `Lookup` → upsert，**流式**：每万行块边扫边写、内存恒定），手动 `POST /admin/db/geoip_sync`、WebUI 概览「立即同步」与定时 `GEOIP_SYNC_INTERVAL`（int 分钟，0=关闭、最小 10、默认 60；mmdb 未加载不启动）收敛 `geoSyncAll` 唯一入口，结束回写 `schedule_list` 状态（success=完成或单趟到点收工 / cancelled=人工经任务取消端点终止 / failed=真错误；取值定义见 `docs/DATA_DICT.md` §3.5）。单趟配额 `GEOIP_SYNC_IPS_PER_RUN`（缺失 IP 上限，默认 50000）/ `GEOIP_SYNC_SCAN_CAP`（扫描行数上限，默认 1000000）先到为准，0=不限，配额截断下轮断点自动续扫。手动同步为**后台任务模式**：端点提交任务执行中心后立即返回任务 ID，进度与报告经 `GET /admin/tasks/{id}` 查询，摆脱 HTTP 15 秒超时压制（详见「服务 → 数据库 → 表数据」页签）。读侧明细 `LEFT JOIN geoip_list` 未命中回退实时解析；「市→省→国名→未知」兜底只在读侧显示。同步成功清 traffic 缓存；同步间隔内新 IP 在聚合视图暂缺（概览卡已注记）。名称解析取值优先 zh-CN、缺失回落 en；省份值以 mmdb 实际返回为准（多为短名）。已入表 IP 不随 mmdb 更换自动重解析，需要时删 `geoip_list` 全量重建。

### 3.6 copy — 请求抄送（转发链中间件，Tail + ResponseHook）

**作用**：复制线上请求异步发送到 shadow 后端（流量审计 / 影子验证），不改写响应、不阻塞主链。

**配置项**：`COPY_ENABLED`（父开关，默认 false）、`COPY_TARGETS`（分号分隔影子后端 URL，空 = 关闭）

```bash
COPY_TARGETS="http://shadow-a:9100;http://shadow-b:9100"
```

**限制**：不复制请求体（engine 转发时请求体已被上游消费）；发送失败仅告警不阻塞。

### 3.7 result — L3 结果处理（转发链中间件，Tail + ResponseHook）

**作用**：统一出口格式、字段脱敏。

**配置项**：`RESULT_ENABLED`（父开关，默认 false）、`RESULT_WRAP`（响应封装）、`RESULT_MASK_FIELDS`（脱敏字段）。

### 3.8 trace — trace_id 透传（转发链中间件，Head）

**作用**：将 trace_id 注入转发请求头与响应头，确保上游与客户端拿到同一 ID（框架默认生成/透传 trace_id，此挂件负责显式注入响应头）。

**配置项**：`TRACE_ENABLED`（父开关，默认 false）。

### 3.9 auth — RockAuth（转发链中间件，Head）

**作用**：JWT 认证。

**配置项**：`AUTH_ENABLED`（父开关，默认 false；true=挂载并认证）、`AUTH_JWT_SECRET`、`AUTH_JWT_ISSUER`、`AUTH_JWT_TTL`。

### 3.10 config — RockConfig（独立组件）

**作用**：KV 配置服务，集中下发配置并热更新广播。

### 3.11 registry — RockRegistry（独立组件）

**作用**：服务注册与发现（`POST /register` 注册实例，实例变更发布）。

### 3.12 object — RockObject（独立组件）

**作用**：本地对象存储（默认 `./data/object` 目录，含路径穿越防护）。

### 3.13 mq — RockMQ（独立组件）

**作用**：异步消息解耦（Outbox 模式）。`MQ_ENABLED=true` 且数据访问层就绪时装配；
outbox 表建于统一数据访问层业务库（`DB_DRIVER`/`DB_DSN`），与业务数据同库，支持本地事务同提交。
数据访问层未就绪时跳过注册（组件降级，不阻断底座）。`outbox` 表字段/枚举见 `docs/DATA_DICT.md`。

---

## 4. 编写新挂件（三步）

```go
// 1. 实现 chain.Middleware + hotswap.MiddlewareLifecycle
type MyPlugin struct {
    cfg conf.Manager
    rules string                 // 配置字段（*string 注册）
    snap  atomic.Value           // 不可变快照
}

func (p *MyPlugin) Name() string { return "my-plugin" }
func (p *MyPlugin) Slot() chain.Slot { return chain.Middle }
func (p *MyPlugin) Handle(ctx *chain.Context) bool { /* 返回 false 中断链 */ }
func (p *MyPlugin) Start(cfg any) error { /* 重建快照 */ }
func (p *MyPlugin) Stop() error { return nil }

// 2. 注册配置项（New 中调用）
cfgMgr.Register(&p.rules, "MY_RULES", "", "说明", "示例")

// 3. 装配（cmd/rocksys/main.go）
mgr.RegisterMiddleware(New(cfgMgr))
```

要点：
- **快照不可变**：Start 整体重建快照，`atomic.Value` 原子替换，与在途 Handle 并发安全。
- **Start 失败保留旧快照**：实例继续以旧配置服务，不中断。
- **默认关闭**：注册即 Disabled，启用才挂载；`XXX_ENABLED` 配置项（父开关）默认 false，装配层启动时按配置自动挂载，热改即时联动（见 §3 总述）。
