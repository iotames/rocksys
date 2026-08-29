# RockSys 管理接口（Admin API）契约

> **本文件是 WebUI 前端对接的唯一权威依据。前端开发人员只需阅读本文档，无需阅读后端源码。**
> 适用于 RockSys 网关管理地址：默认 `127.0.0.1:19527`（回环地址，不对外网）。

---

## 1. 通用约定

| 项 | 约定 |
|----|------|
| Base URL | `http://127.0.0.1:19527`（实际以网关 `--admin` 参数为准） |
| 协议 | HTTP，仅回环监听 |
| 请求体 | `Content-Type: application/json`（GET 无请求体） |
| 响应体 | 均为 JSON（日志接口除外，见 §3.8） |
| 鉴权 | 回环地址（127.0.0.1）免鉴权；非回环地址需鉴权：① 静态预共享 token（`ROCKSYS_ADMIN_TOKEN`，供 rockctl/脚本）→ `Authorization: Bearer <token>`；② 登录 JWT（`Authorization: Bearer <token>`），两者任一通过即放行 |
| 鉴权失败 | `401`，响应体 JSON `{"ok":false,"error":"<原因>"}`（认证端点）或文本 `unauthorized`（其余端点） |
| 写操作响应 | 统一 `{"ok":true}` 或 `{"ok":false,"error":"<原因>"}` |
| 前端访问凭证 | 账号密码登录后 JWT 存浏览器本地；每次请求自动带 `Authorization` 头；收到 401 时跳转登录视图 |

---

## 2. 端点总览

| # | 方法 | 路径 | 用途 |
|---|------|------|------|
| 1 | GET | `/admin/switch/list` | 组件状态列表 |
| 2 | POST | `/admin/switch/on` | 开启组件 |
| 3 | POST | `/admin/switch/off` | 关闭组件 |
| 4 | GET | `/admin/config` | 查看底座配置 |
| 5 | GET | `/admin/config/list` | 全量配置项清单（含各组件） |
| 6 | PUT | `/admin/config` | 热改配置 |
| 7 | POST | `/admin/script/publish` | 发布策略脚本 |
| 8 | POST | `/admin/script/rollback` | 回滚 / 移除脚本 |
| 9 | GET | `/admin/script/list` | 脚本列表与版本历史 |
| 10 | GET | `/admin/metrics` | 运行指标快照 |
| 11 | GET | `/admin/logs` | 按日期查询访问日志 |
| 12 | GET | `/admin/auth/status` | 认证状态（登录/注册/重置引导） |
| 13 | POST | `/admin/auth/register` | 首次注册超级管理员 |
| 14 | POST | `/admin/auth/login` | 登录，签发 JWT |
| 15 | POST | `/admin/auth/reset` | 重置管理员凭证（忘记密码） |
| 16 | GET | `/admin/warnings` | 数据清理未开启警告（常驻横幅数据源，与登录响应同源） |
| 17 | GET | `/admin/version` | 构建版本信息（左侧版本展示） |
| 18 | GET | `/admin/logs/storage` | 访问日志存储总占用 |
| 19 | POST | `/admin/logs/prune` | 手动清理访问日志（保留期外） |
| 20 | GET | `/admin/shield/metrics` | WAF 近 1 分钟实时计数（内存滑动窗口，无需查库） |
| 21 | GET | `/admin/shield/events` | WAF 拦截明细（JSONL，时间/类别/IP 过滤） |
| 22 | GET | `/admin/shield/stats` | WAF 聚合统计（按日 × 类别 + Top IP） |
| 23 | POST | `/admin/shield/prune` | 手动清理拦截明细（保留期外） |
| 24 | GET / POST | `/admin/shield/blacklist` | 黑名单列表（GET，分页/过滤）/ 新增（POST） |
| 25 | POST | `/admin/shield/blacklist/update` | 更新黑名单条目（title/block_type/expires_at） |
| 26 | POST | `/admin/shield/blacklist/delete` | 软删黑名单条目（可恢复） |
| 27 | POST | `/admin/shield/blacklist/restore` | 恢复软删黑名单条目 |
| 28 | POST | `/admin/shield/blacklist/import` | 批量导入黑名单（body 纯文本，每行一个 IP/CIDR） |
| 29 | GET / POST | `/admin/shield/whitelist` | 白名单列表（GET）/ 新增（POST） |
| 30 | POST | `/admin/shield/whitelist/update` | 更新白名单条目（title） |
| 31 | POST | `/admin/shield/whitelist/delete` | 软删白名单条目（可恢复） |
| 32 | POST | `/admin/shield/whitelist/restore` | 恢复软删白名单条目 |
| 33 | POST | `/admin/shield/whitelist/import` | 批量导入白名单 |
| 34 | GET | `/admin/meta` | 组件/服务元数据（WebUI 全局展示，无状态不缓存） |
| 35 | GET | `/admin/shield/rules` | WAF 规则文件清单（外挂覆写状态/生效行数/修改时间，WebUI·文件编辑 Tab） |
| 36 | GET | `/admin/shield/rules/file` | 读单个规则文件当前生效内容 + 内嵌默认内容（`?name=`，文件名白名单校验） |
| 37 | POST | `/admin/shield/rules/save` | 保存规则文件到 `HOT_SCRIPTS_DIR/rules/`（原子写，ScriptHub ≤3s 自动热更生效；body `{name, content}`，上限 512KB） |
| 38 | GET | `/admin/proxy/trusted` | 可信代理文件清单（外挂覆写状态/生效行数/修改时间，WebUI·全局配置可信代理页签） |
| 39 | GET | `/admin/proxy/trusted/file` | 读可信代理文件当前生效内容 + 内嵌默认内容（`?name=`，仅允许装配的 `TRUSTED_PROXIES_FILE`） |
| 40 | POST | `/admin/proxy/trusted/save` | 保存可信代理文件到 `HOT_SCRIPTS_DIR/trusted_proxies/`（原子写，保存前先解析校验非法 IP/CIDR 直接 400；ScriptHub ≤3s 自动热更生效；body `{name, content}`，上限 512KB） |
| 41 | GET | `/admin/db/schema` | 表结构检查（期望 = 运行期 SQL 源脚本，实际 = 当前数据连接 catalog；返回 A-F 分级差异与自动项生成 SQL） |
| 42 | POST | `/admin/db/exec` | 执行 SQL（拆句逐条执行、遇错即停，返回逐条结果；danger 级危险操作，服务端不做语句白名单） |
| 43 | POST | `/admin/shield/blacklist/sync_file` | 从外挂规则文件 `rules/ip_blacklist.txt` 同步 IP 入库（block_type=11，幂等） |
| 44 | POST | `/admin/shield/blacklist/ban` | 专用封禁端点（三态：入库 / 活跃 400 / 软删过期恢复续封，warn_times 累计） |
| 45 | GET | `/admin/shield/jail` | 小黑屋：当前在押的限时封禁条目（首页页签数据源） |

---

## 3. 端点详解

### 3.1 GET /admin/switch/list — 组件状态列表

返回全部可热开关实体（默认 12 个；消息组件 `mq` 仅在配置满足时装配，可能缺席）。

**响应 200**：

```json
[
  {
    "name": "shield",
    "kind": "middleware",
    "state": "enabled",
    "started_at": "2026-08-04T10:12:03+08:00",
    "last_switch_at": "2026-08-04T10:12:03+08:00",
    "message": "enabled"
  }
]
```

**字段表**：

| 字段 | 类型 | 说明 |
|------|------|------|
| name | string | 组件名（枚举见 §4.1） |
| kind | string | `component`（独立服务）\| `middleware`（链中间件） |
| state | string | `enabled`（已启用）\| `disabled`（已关闭）\| `draining`（切换中/排空，瞬态） |
| started_at | string | 最近一次启动时间（RFC3339），从未启用则为零值时间 |
| last_switch_at | string | 最近一次状态切换时间（RFC3339） |
| message | string | 最近操作结果 / 故障信息（成功为 `enabled`/`disabled`/`hot reload ok`，失败为错误原文） |

**组件名称映射（前端展示用）**：

| name | 中文名 | kind | 环节 |
|------|--------|------|------|
| shield | 防护 | middleware | Head = 入口环节 |
| trace | 透传 | middleware | Head = 入口环节 |
| auth | 认证 | middleware | Head = 入口环节 |
| dispatch | 分发 | middleware | Middle = 分发环节 |
| rewrite | 改写 | middleware | Middle = 分发环节 |
| script | 脚本 | middleware | Middle = 分发环节 |
| obs | 观测 | middleware | Tail = 响应环节 |
| copy | 抄送 | middleware | Tail = 响应环节 |
| result | 结果 | middleware | Tail = 响应环节 |
| config | 配置服务 | component | 独立服务 |
| registry | 注册 | component | 独立服务 |
| object | 存储 | component | 独立服务 |
| mq | 消息 | component | 独立服务（条件装配） |

### 3.2 POST /admin/switch/on — 开启组件

请求体：

```json
{ "name": "shield" }
```

成功 `200`：

```json
{ "ok": true }
```

失败 `500`（实体不存在 / Start 失败，`error` 透出原因）：

```json
{ "ok": false, "error": "hotswap: entity not found: xxx" }
```

参数非法 `400`：`{ "ok": false, "error": "invalid body, require {\"name\":\"...\"}" }`

### 3.3 POST /admin/switch/off — 关闭组件

请求体 / 响应同 §3.2（路径 `/admin/switch/off`）。

### 3.4 GET /admin/config — 查看底座配置

**响应 200**：

```json
{
  "listen": ":8080",
  "upstream": "http://127.0.0.1:9000",
  "timeout": 18,
  "admin": "127.0.0.1:19527",
  "config_file": "",
  "log_level": "info"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| listen | string | 代理监听地址 |
| upstream | string | 默认后端 |
| timeout | int | 转发超时（秒） |
| admin | string | 管理接口地址 |
| config_file | string | 配置文件路径（可为空） |
| log_level | string | 日志级别（debug/info/warn/error） |

### 3.5 GET /admin/config/list — 全量配置项清单

返回**全部已注册配置项**（底座 + 各组件），含元数据。前端据此做分组展示、帮助与恢复默认，无需硬编码配置名。

**响应 200**：

```json
[
  {
    "key": "SHIELD_IP_WHITELIST",
    "title": "白名单 IP",
    "defval": "",
    "current": "10.0.0.5,192.168.1.0/24",
    "example": "10.0.0.5,192.168.1.0/24"
  }
]
```

| 字段 | 类型 | 说明 |
|------|------|------|
| key | string | 配置项注册名（即环境变量名，热改 `PUT /admin/config` 时用此名） |
| title | string | 中文说明 |
| defval | string | 默认值（字符串形态） |
| current | string | 当前值（字符串形态） |
| example | string | 示例（可能为空） |

**前端分组规则**（按 key 前缀映射，无规则命中归入"其他"）：

| key 前缀 | 分组 |
|----------|------|
| `ROCKSYS_` | 网关 |
| `SHIELD_` | 防护 |
| `DISPATCH_` | 分发 |
| `REWRITE_` | 改写 |
| `OBS_` | 观测 |
| `COPY_` | 抄送 |
| `RESULT_` | 结果 |
| `AUTH_` | 认证 |
| `MQ_` | 消息（轮询/重试/退避/消费方地址，无条件注册、修改后需重启服务生效） |
| `REGISTRY_` | 注册中心 |
| `OBJECT_` | 对象存储 |
| `SCRIPT_` | 脚本（Lua 策略执行超时） |
| `DB_` | 数据访问 |

> `HOT_SCRIPTS_DIR`、`TRUSTED_PROXIES_FILE` 等非组件前缀配置在 WebUI 归入『其他』组（前端按前缀匹配分组，未匹配即其他）。

**展示规则**：
- 敏感项：`key` 含 `SECRET` / `TOKEN` / `PASSWORD` 时默认掩码，可切换明文。
- 需重启项：`ROCKSYS_LISTEN` / `ROCKSYS_ADMIN` / `ROCKSYS_CONFIG` 置灰并标注"需重启后生效"。
- 恢复默认：用 `defval` 回填后走 `PUT /admin/config`。

### 3.6 PUT /admin/config — 热改配置

请求体（支持多键，`key` 必须为注册名全名）：

```json
{ "ROCKSYS_UPSTREAM": "http://127.0.0.1:9001", "SHIELD_RATE_LIMIT_RPS": "100" }
```

成功 `200`：`{ "ok": true }`

失败 `400`（JSON 非法 / 空 body）：`{ "ok": false, "error": "..." }`
失败 `500`（某键设置失败）：`{ "ok": false, "error": "set <KEY>: <原因>" }`

> 注意：未注册的 key 会被后端静默忽略。前端应使用 §3.5 的清单约束输入，避免无效 key。
>
> **第一原则「热更即持久化」**：本端点热改立即生效，并同步写回配置文件（`--config` 存在时写 configFile，否则 `.env`），重启后状态保留。

### 3.7 POST /admin/script/publish — 发布策略脚本

请求体：

```json
{ "name": "rule1", "source": "if req.path() == \"/block\" then return resp.deny(403) end" }
```

成功 `200`：

```json
{ "ok": true, "version": 3 }
```

| 字段 | 类型 | 说明 |
|------|------|------|
| version | int | 本次发布生成的单调递增版本号 |

失败 `400`（沙箱拒绝 / 编译失败，`error` 透出原因）：`{ "ok": false, "error": "..." }`
参数非法 `400`：`{ "ok": false, "error": "invalid body, require {\"name\":\"...\",\"source\":\"...\"}" }`

> 沙箱禁止引用 `os` / `io` / `file` / `net` / `ffi` 模块，违反即发布失败。

### 3.8 POST /admin/script/rollback — 回滚 / 移除脚本

请求体：

```json
{ "name": "rule1", "version": 2 }
```

- `version > 0`：回滚到该历史版本。
- `version <= 0`：移除该脚本（下线）。

成功 `200`：`{ "ok": true }`
失败 `400`：`{ "ok": false, "error": "<原因>" }`（脚本不存在 / 历史版本不存在等）

> 前端交互：回滚前用 §3.9 获取历史版本，二次确认后调用。

### 3.9 GET /admin/script/list — 脚本列表与版本历史

**响应 200**：

```json
{
  "scripts": [
    {
      "name": "rule1",
      "current_version": 3,
      "versions": [
        { "version": 1, "published_at": "2026-08-04T09:40:00+08:00" },
        { "version": 2, "published_at": "2026-08-04T09:55:00+08:00" },
        { "version": 3, "published_at": "2026-08-04T10:12:00+08:00" }
      ]
    }
  ]
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| scripts | array | 全部已发布脚本（未发布任何脚本时为空数组） |
| scripts[].name | string | 脚本名 |
| scripts[].current_version | int | 当前生效版本（0 = 尚未发布） |
| scripts[].versions | array | 版本历史（按版本号升序） |
| versions[].version | int | 版本号 |
| versions[].published_at | string | 发布时间（RFC3339） |

> 脚本为**内存态**：网关重启后全部脚本与历史清空，需重新发布。前端应明示此提示。

### 3.10 GET /admin/metrics — 运行指标快照

**响应 200**：

```json
{ "qps": 1204.5, "p50_ms": 12, "p95_ms": 48, "p99_ms": 92, "error_rate": 0.0002 }
```

| 字段 | 类型 | 说明 |
|------|------|------|
| qps | float | 每秒请求数（最近 1 分钟窗口） |
| p50_ms / p95_ms / p99_ms | int | 延迟分位（毫秒），窗口内无样本时为 0 |
| error_rate | float | 错误率（4xx/5xx 占比，0~1） |

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
{"time":"2026-08-04T10:12:03+08:00","trace_id":"ab34...","path":"/api/order/1","method":"GET","client_ip":"127.0.0.1:1234","status_code":200,"upstream":"http://o1:9001","shield_ms":1,"biz_ms":11,"total_ms":12,"req_bytes":512,"resp_bytes":1024}
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

### 3.11.2 GET/POST /admin/proxy/trusted* — 可信代理文件在线编辑

WebUI「可信代理」页数据源（实现 `internal/netutil/proxies_admin.go`）。文件固定为装配配置的 `TRUSTED_PROXIES_FILE`（相对 `HOT_SCRIPTS_DIR/trusted_proxies/`，默认 `trusted_proxies.txt`），保存落点为外挂目录，ScriptHub ≤3s 自动重读快照热更生效。

| 端点 | 说明 |
|------|------|
| `GET /admin/proxy/trusted` | 文件清单：`{"files":[{name,title,desc,override,lines,modified?,bytes?}]}` |
| `GET /admin/proxy/trusted/file?name=` | 当前生效内容 + 内嵌默认内容：`{name,content,embedded,override,modified,hot_path,max_tokens}` |
| `POST /admin/proxy/trusted/save` | body `{name, content}` → 原子写外挂文件（上限 512KB）；保存前先解析校验，非法 IP/CIDR 返回 400 且不落盘（空内容合法 = 回退内嵌默认） |

> 安全约束：文件名仅允许 `TRUSTED_PROXIES_FILE`；保存端点仅 POST（防本机恶意页面无凭证触发）。

### 3.12 GET /admin/auth/status — 认证状态

返回管理接口认证状态，WebUI 启动时据此决定显示登录/注册/重置面板还是直接进入控制台。

**响应：**

| 字段 | 类型 | 说明 |
|------|------|------|
| `auth_required` | bool | 是否需要登录（仅绑定非回环地址时为 true；回环地址始终免鉴权） |
| `has_user` | bool | 是否已注册超级管理员 |
| `username` | string | 已有管理员用户名（未注册时为空） |
| `setup_mode` | bool | 是否处于重置模式（`ADMIN_INITIALIZED=false` 且已有用户） |

**前端引导逻辑：** `auth_required=false` → 直接进入控制台；`has_user=false` → 注册页；`setup_mode=true` → 重置页；否则 → 有 token 进控制台，无 token 显示登录页。

### 3.13 POST /admin/auth/register — 首次注册超级管理员

**请求：** `{"username":"admin","password":"Admin@12345"}`（密码至少 8 位）

**成功 `200`：** `{"ok":true}`，同时置 `ADMIN_INITIALIZED=true`。
**失败 `403`：** 系统已初始化，禁止重复注册（超管仅一个）。
**失败 `400`：** 参数非法（用户名空或密码不足 8 位）。

### 3.14 POST /admin/auth/login — 登录

**请求：** `{"username":"admin","password":"Admin@12345"}`

**成功 `200`：** `{"ok":true,"token":"<jwt>","expires_in":43200,"warnings":["拦截记录清理未开启，shield_event 表可能持续膨胀"]}`，前端将 token 存本地，后续请求带 `Authorization: Bearer <token>`；`warnings` 为数据清理未开启提醒（`SHIELD_EVENT_PRUNE_ENABLED` / `OBS_LOG_PRUNE_ENABLED` 为 false 时出现，组件未装配则无对应项，恒为数组），WebUI 用于渲染常驻置顶横幅（详见 §3.17）。
**失败 `401`：** 用户名或密码错误。
**失败 `429`：** 登录尝试过于频繁（5 分钟窗口失败 5 次锁定 5 分钟）。

### 3.15 POST /admin/auth/reset — 重置管理员凭证（忘记密码）

**前置条件：** 运维已将 `.env` 中 `ADMIN_INITIALIZED` 改为 `false`（进入重置模式）。

**请求：** `{"username":"admin","password":"NewPass@67890"}`

**成功 `200`：** `{"ok":true}`，同时恢复 `ADMIN_INITIALIZED=true`。
**失败 `403`：** 未处于重置模式（需先改 `.env`）。
**失败 `400`：** 参数非法。

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

### 3.18 shield 管理端点组 — WAF 监控统计 + 动态黑白名单

**WAF 监控统计**（metrics/events/stats/prune，实现见 `plugins/shield/admin.go`）：

| 端点 | 说明 |
|------|------|
| `GET /admin/shield/metrics` | 近 1 分钟实时计数（内存滑动窗口，DB 未配置也可用）；响应 `{window_seconds,total,by_type,written,dropped}` |
| `GET /admin/shield/events` | 拦截明细（JSONL）；query：`from`/`to`（日期或分钟精度）、`block_type`（1-10）、`client_ip`、`limit`（1-10000，缺省 500）、`offset`；总数经 `X-Total-Count` 头回传；每行 JSON 额外携带 `in_blacklist` 字段（bool，该行 IP 是否命中当前生效黑名单，内存快照判定与 stats TOP 同源，供前端「IP封禁」按钮置灰） |
| `GET /admin/shield/stats` | 聚合统计；响应 `{days,total,daily:[{day,block_type,cnt}],top_ips:[{client_ip,cnt,in_blacklist}],blacklist_addable}`（`in_blacklist`=该 IP 是否命中当前生效黑名单，与拦截判定同源；`blacklist_addable`=DB 黑名单是否可用，WebUI 据此显示勾选列与批量加黑按钮） |
| `POST /admin/shield/prune` | 手动清理拦截明细；body `{"days":N}`（0-3650，缺省用配置默认值）；响应 `{"ok":true,"deleted":N}` |

**动态黑白名单**（WAF 方案 DB 化，DB 未配置时端点统一 503；副作用一律 POST）：

| 端点 | 方法 / 语义 |
|------|------|
| `/admin/shield/blacklist` | GET 列表（query：`ip` 模糊、`block_type`、`valid_only`、`limit`、`offset`、`sort`；响应 `{total,rows}`）；POST 新增（body `{"ip","title","block_type","expires_at"}`，expires_at 为 RFC3339 可空）。`sort` 服务端排序（黑名单专属）：白名单映射 `hit_count`/`warn_times`/`created_at`/`expires_at`/`updated_at`/`block_type` → 对应列 **DESC**（固定倒序），非法/缺省回默认 `id DESC`（最近添加在前）；字符串字段（ip/title）不参与排序 |
| `/admin/shield/blacklist/update` | POST 更新（body `{"id","title","block_type","expires_at"}`） |
| `/admin/shield/blacklist/delete` | POST 软删（body `{"id"}`；可恢复） |
| `/admin/shield/blacklist/restore` | POST 恢复软删（body `{"id"}`） |
| `/admin/shield/blacklist/import` | POST 批量导入：**body 为纯文本**（每行一个精确 IP/CIDR，兼容外挂文件格式；兼容 JSON 字符串编码），query 可选 `title`/`block_type`；响应 `{"ok":true,"imported":N,"skipped":N}` |
| `/admin/shield/blacklist/sync_file` | POST 从外挂规则文件 `HOT_SCRIPTS_DIR/rules/ip_blacklist.txt` 同步 IP 入库（外挂优先、内嵌兜底；`#` 注释/空行忽略），固定 `title="来自 ip_blacklist.txt 同步"`、`block_type=11` 人工收录；幂等（重复同步 skipped 递增）；响应 `{"ok":true,"imported":N,"skipped":N}`；文件缺失/为空/无有效行 → 400 文本（三要素文案：发生了什么 + 原因 + 检查 `hotscripts/rules/ip_blacklist.txt` 后重试） |
| `/admin/shield/blacklist/ban` | POST 专用封禁端点：body `{"ip","title","block_type","duration"}`（`block_type` 1-11 缺省 11 人工收录；`duration`=`"24h"`（缺省，服务端换算 now+24h）\|`"permanent"`→expires_at=NULL；title 空 → `"人工封禁"`）。三态：① 无记录 → 新增入库 `warn_times=1`；② 活跃条目 → 400「已在黑名单」+ 前往黑名单列表管理指引；③ 软删/过期条目 → 恢复（清 deleted_at）+ 按所选时长落 expires_at + `warn_times`+1；**累计满 5 次的限时封禁自动转永久**（本就永久的条目恢复后仍永久）。成功 `{"ok":true,"to_permanent":bool}`（`to_permanent`=本次已由限时转永久）；写库成功即重建拦截快照 |
| `/admin/shield/jail` | GET 小黑屋：当前在押的限时封禁条目（`expires_at` 非 NULL 且未过期、未软删），按解封时间升序（临近解封在前）；query `limit` 默认 20、上限 100（非法/越界回默认）；响应 `{"total":N,"rows":[{ip,block_type,hit_count,warn_times,created_at,expires_at}]}`；DB 未配置 503 |
| `/admin/shield/whitelist` 及 update/delete/restore/import | 白名单同构（无 block_type/expires_at 字段） |

> 全部变更写库后**即时重建拦截快照**（不等 TTL 兜底）；数据表见 `docs/DATA_DICT.md` §2.5-2.7；外挂 `rules/ip_blacklist.txt` 存量条目可直接粘贴本接口批量导入。

---

### 3.19 数据库表结构同步端点组 — 表结构检查 + SQL 执行（实现 `internal/adminapi/dbschema.go`）

WebUI「服务 → 数据库 → 表结构」页数据源。期望结构 = 运行期 SQL 源（`HOT_SCRIPTS_DIR/sql/` 外挂优先、内嵌兜底，与各挂件实际建表同源）经 DDL 解析器产出；实际结构 = 当前数据连接 catalog（`sql/<dbtype>/schema_query_*.sql`，三方言）。仅 `DB_DRIVER`/`DB_DSN` 配置且表清单已装配时可用，否则 503。

| 端点 | 说明 |
|------|------|
| `GET /admin/db/schema` | 逐表比对期望与实际结构，返回差异项与自动项生成 SQL；无差异时 `items:[]`、`sql:""` |
| `POST /admin/db/exec` | body `{sql}`；拆句（分号切分，感知字符串字面量与注释内分号）逐条执行、**遇错即停**（DDL 无跨方言统一事务语义），返回已执行到的位置；进程内互斥（已有执行在途回 409） |

**`GET /admin/db/schema` 响应 200**：

```json
{
  "driver": "sqlite",
  "items": [
    {"level": "B", "auto": true, "table": "ip_blacklist", "object": "warn_times",
     "expected": "INTEGER NOT NULL DEFAULT 0", "actual": "", "note": "缺普通列，可自动补齐"}
  ],
  "sql": "-- ip_blacklist · 缺普通列 warn_times\nALTER TABLE ip_blacklist ADD COLUMN warn_times INTEGER NOT NULL DEFAULT 0;"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| driver | string | 数据方言（`sqlite` / `postgres` / `mysql`） |
| items | array | 差异项列表（无差异为空数组） |
| items[].level | string | 差异分级 `A`-`F`（语义见下表） |
| items[].auto | bool | 是否可自动处理（A/B/D 为 true；C/E/F 为 false） |
| items[].table | string | 表名 |
| items[].object | string | 差异对象（列名 / 索引名 / 表名自身） |
| items[].expected | string | 期望结构（来自脚本解析） |
| items[].actual | string | 实际结构（来自 catalog；对象缺失时为空串） |
| items[].note | string | 差异说明与建议文案 |
| sql | string | 全部自动项（A/B/D）生成的 SQL 文本（各段带 `-- 表名 · 差异说明` 注释分隔），直接喂前端编辑器；无自动项时为空串 |

**A-F 分级语义**：

| 级别 | 差异类型 | 处理 |
|------|----------|------|
| A | 缺表 | 自动：生成建表脚本原文 + 配套索引脚本（`{table}` 替换后） |
| B | 缺普通列 | 自动：生成 `ALTER TABLE … ADD COLUMN`（列定义取脚本原文，天然方言正确） |
| C | 缺 PK/UNIQUE/自增列 | 需人工：不生成（SQLite 不支持 ADD 带 PK/UNIQUE 的列），`note` 说明原因与建议 |
| D | 缺索引 | 自动：仅生成缺失索引的单条 CREATE INDEX（不整份重放） |
| E | 类型/非空/默认值不一致 | 仅提示：不生成（SQLite 改列需重建表，跨方言不可靠），展示期望 vs 实际值 |
| F | 库中多余列/表 | 仅提示：不生成 DROP（危险，可能是历史遗留或有数据） |

**`POST /admin/db/exec` 请求体**：

```json
{ "sql": "ALTER TABLE ip_blacklist ADD COLUMN warn_times INTEGER NOT NULL DEFAULT 0;" }
```

**响应 200**（全部成功或中途失败，均回逐条结果）：

```json
{
  "results": [{"sql": "ALTER TABLE ip_blacklist ADD COLUMN warn_times …", "ok": true, "rows": 0}],
  "executed": 1,
  "failed": 0
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| results | array | 逐条执行结果（按语句顺序） |
| results[].sql | string | 该条语句文本 |
| results[].ok | bool | 是否执行成功 |
| results[].rows | int | 受影响行数（仅成功且驱动可取时出现） |
| results[].error | string | 失败原因（仅失败条目出现） |
| executed | int | 成功执行条数 |
| failed | int | 失败条数 |
| message | string | **仅失败时出现**：三要素文案（第 N 条执行失败 + 原因；前面 N-1 条已生效且不可回滚；修正后可仅重发剩余语句，再执行表结构检查复核） |

**错误语义**：

- `503`：数据连接或表清单未装配（响应文本，含排查指引）。
- `400`：请求体非法 / `sql` 为空 / 未解析出可执行语句（仅含注释或空白），响应文本含原因与下一步。
- `500`：`GET /admin/db/schema` 检查或生成 SQL 失败，响应文本为错误原文 + 重试指引。
- 执行中途失败仍返回 200（结果在 `results`/`failed`/`message` 中）：前面已执行的语句**不可回滚**，前端据此引导仅重发剩余语句。

> **安全提示**：执行端点为 danger 级危险操作，前端须做强确认（说明作用对象、DDL 不可回滚、建议先备份）；服务端**不做语句类型白名单**——编辑器内容可自由编辑（含手工救急语句），原样逐条执行。调用方（如脚本）务必自行确认 SQL 内容来源可信。

---

## 4. 数据字典（前端展示映射）

### 4.0 组件/服务元数据（/admin/meta 返回结构）

`GET /admin/meta` 返回 `components`（9 个链中间件）与 `services`（4 个独立服务）两组元数据，
供 WebUI 概览/详情/配置等页面全局展示；无状态、不做缓存（前端页面会话内持有）。

**components 字段**（链中间件）：

| 字段 | 说明 |
|------|------|
| name | 组件英文名（路由/开关键） |
| title | 中文名 |
| desc | 用户视角说明（简明无歧义） |
| slot | 链槽位：Head / Middle / Tail |
| slot_label | 环节展示名：入口环节 / 分发环节 / 响应环节 |
| enabled_key | 自动开关配置键（XXX_ENABLED） |
| kind | 恒为 `middleware` |

**services 字段**（独立服务）：

| 字段 | 说明 |
|------|------|
| name | 服务英文名 |
| title | 中文名 |
| desc | 用户视角说明 |
| kind | 恒为 `component` |

> 元数据权威源为 `internal/catalog`，描述文案变更时同步 `docs/COMPONENTS.md` 语义。

### 4.1 组件中文名与环节（见 §3.1 表）

### 4.2 状态枚举 → 展示

| state | 中文 | 状态色 |
|-------|------|--------|
| enabled | 已启用 | 绿 |
| disabled | 已关闭 | 灰/红 |
| draining | 切换中（排空） | 橙（瞬态） |

### 4.3 指标字段 → 展示

| 字段 | 展示 |
|------|------|
| qps | 每秒请求（千分位） |
| p50_ms / p95_ms / p99_ms | 延迟 50% / 95% / 99%（毫秒） |
| error_rate | 错误率（百分比，保留 2 位） |

---

## 5. 变更记录

| 版本 | 时间 | 变更 |
|------|------|------|
| 1.0 | 2026-08-04 | 初稿：覆盖 11 个端点，含新增 `GET /admin/config/list`、`GET /admin/script/list`（用于 WebUI 配置分组与脚本版本历史） |
| 1.1 | 2026-08-08 | 鉴权行为更新：回环地址一律免鉴权；非回环地址下静态预共享 token（`ROCKSYS_ADMIN_TOKEN`）与登录 JWT 双轨并行、任一通过即放行；WebUI 移除手动输入「访问凭证」，统一账号密码登录 |
| 1.2 | 2026-08-19 | 新增 `GET /admin/version`（WebUI 左上角品牌区展示版本号，与 `rocksys --version` 同源） |
| 1.3 | 2026-08-20 | 登录响应新增 `warnings` 字段 + 新增 `GET /admin/warnings`（数据清理未开启警告，WebUI 常驻置顶横幅数据源） |
| 1.4 | 2026-08-21 | 端点总览补齐 WAF 监控统计与动态黑白名单端点（`/admin/shield/*` 14 个 + `/admin/logs/prune` + `/admin/logs/storage`），新增 §3.18 shield 管理端点组详解 |
| 1.5 | 2026-08-27 | 新增 `GET /admin/meta`（组件/服务元数据统一出口，前端不再硬编码说明文案；无状态不缓存） |
| 1.6 | 2026-08-29 | 新增 §3.19 数据库表结构同步端点组：`GET /admin/db/schema`（A-F 分级差异检查 + 自动项生成 SQL）、`POST /admin/db/exec`（拆句逐条执行、遇错即停；danger 级，无语句白名单） |
| 1.7 | 2026-08-29 | IP 黑名单增强：新增 `POST /admin/shield/blacklist/sync_file`（从文件同步）、`POST /admin/shield/blacklist/ban`（专用封禁，warn_times 累计满 5 转永久）、`GET /admin/shield/jail`（小黑屋在押预览）；黑名单列表 GET 新增 `sort` 排序参数（白名单映射固定倒序）；拦截明细 events 每行新增 `in_blacklist` 字段 |

> 契约原则：只增不改删；新增字段不影响旧字段语义。
