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
| `SHIELD_IP_WHITELIST` | 空 | 白名单（逗号分隔，支持精确 IP 与 CIDR）；与 DB 表 `ip_whitelist` 取并集 |
| IP 黑名单 | — | **DB 表 `ip_blacklist`（管理面录入/导入，动态）∪ 外挂 `rules/ip_blacklist.txt`（静态兑底）**，取并集；DB 未启用时仅外挂生效；热路径只读内存快照（TTL 60s 刷新），管理面见「黑白名单」Tab 与 `docs/WAF_BLACKLIST_MIGRATION.md` |
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

### 3.2 dispatch — L2 路由分发（转发链中间件，Middle）

**作用**：按 URL 规则**选择**目标后端并写入转发信息，实际转发由转发引擎执行；未命中路由规则则进入 `ROCKSYS_UPSTREAM` 默认后端，命中但节点不可用返回 503。

**配置项**：`DISPATCH_ENABLED`（父开关，默认 false）、`DISPATCH_RULES`

```
格式：<prefix>=<spec>[;<spec>...]，逗号分隔
  prefix  匹配 pattern（见下）
  spec    节点组：<node>[;<node>...]；节点 <url>[|w=权重][|p=0高优/1备份]
          可选尾缀：[@interval@timeout@path]（健康检查）[|alg=roundrobin|chash][|key=$remote_addr|$http_<h>|$cookie_<c>]
```

**匹配 pattern 支持三种**（Radix Tree 前缀树引擎，最长匹配优先）：

| pattern | 说明 | 示例 |
|---------|------|------|
| 纯前缀 | 匹配以该前缀开头的任意路径 | `/api/order/` |
| 参数 | 匹配单段并捕获 `:param` | `/api/order/:id` |
| 通配 | 匹配剩余所有路径 | `/api/*` |
| 兜底 | 根路径匹配所有 | `/` |

**参数捕获**：命中 `:id` 规则时，参数存入 DataFlow（`rocksys:path_params`）并注入请求头 `X-Route-Param-<name>`（透传上游）。

**负载均衡**：`roundrobin`（默认，平滑加权轮询）/ `chash`（一致性哈希，按 key 稳定选点，会话保持/缓存亲和）。

**示例**：

```bash
# 前缀 + 节点组 + 健康检查 + 权重
DISPATCH_RULES="/api/order/=http://o1:9001;http://o2:9001|w=2@10s@2s@/healthz"

# 参数路由（捕获 id）
DISPATCH_RULES="/api/order/:id=http://order-svc:9001"

# 通配 + 一致性哈希（按用户头稳定选点）
DISPATCH_RULES="/api/*=http://api1:9001;http://api2:9001|alg=chash|key=$http_x-user-id"

# 兜底
DISPATCH_RULES="/=http://default-svc"
```

**内部子组件**：

| 子组件 | 文件 | 作用 |
|--------|------|------|
| Radix Tree 路由引擎 | `router.go` | 前缀树匹配（参数/通配/最长匹配），dispatch 内部实现 |
| 平滑加权轮询 | `balancer.go` | 默认负载均衡算法 |
| 一致性哈希 | `chash.go` | 按 key 稳定选点（会话保持） |
| 主动健康检查 | `healthcheck.go` | 周期探测节点，2xx/3xx 判健康 |

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

**配置项**：`SCRIPT_ENABLED`（父开关，默认 false）、`SCRIPT_TIMEOUT`（执行超时毫秒，装配期生效）。

**使用**：

```bash
rockctl script publish <file.lua>   # 发布策略（沙箱校验，禁 os 等危险库）
rockctl script rollback             # 回滚上一版本
```

### 3.5 obs — RockObs（转发链中间件，Tail + ResponseHook）

**作用**：访问日志（异步落盘，存储后端可切换）+ 指标聚合 + 查询 API。

**配置项**：`OBS_ENABLED`（父开关，默认 false）、`OBS_STORE`（默认 `db`，可选 `file`——已弃用，将不再被支持）、`OBS_LOG_DIR`（默认 logs，仅 file 遗留用）、`OBS_RETENTION_DAYS`（默认 30）、`OBS_LOG_PRUNE_ENABLED`（access_log 自动清理子开关，默认 false）。

**存储后端**：
- `db`（默认）：复用统一数据访问层（`DB_DRIVER`/`DB_DSN`，默认 sqlite `rocksys.db`）写 `access_log` 表；SQL 外置 `sql/<dbtype>/`。dataDB 未就绪时回退 file 并告警。`access_log` 表字段/枚举见 `docs/DATA_DICT.md`。
- `file`（已弃用）：JSONL 文件 `logs/access-YYYY-MM-DD.jsonl`（按天切分、超期清理），仅显式配置 `OBS_STORE=file` 时启用，将不再被支持。
- 切换语义：改 `OBS_STORE` 触发配置热更，新日志写入新后端；查询只读当前启用的后端，旧数据保留（db 表在库、file 文件在磁盘，切回可见）。

**异步落盘**：日志写入有界队列（4096 条，满则丢弃告警），后台 goroutine 批量写入当前后端；`Flush` 保证停机前全部落盘。

**查询**：`GET /admin/metrics` 返回 QPS / P50 / P95 / P99 / 错误率；`GET /admin/logs` 按时间范围（精确到分）+ path 精确/模糊过滤返回 JSONL（详见 webui-api.md §3.11）；`GET /admin/logs/storage` 返回日志存储总占用（file 文件 + db 表，WebUI 日志页顶部展示）。

### 3.6 copy — 请求抄送（转发链中间件，Tail + ResponseHook）

**作用**：复制线上请求异步发送到 shadow 后端（流量审计 / 影子验证），不改写响应、不阻塞主链。

**配置项**：`COPY_ENABLED`（父开关，默认 false）、`COPY_TARGETS`（逗号分隔 shadow URL，空 = 关闭）

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
