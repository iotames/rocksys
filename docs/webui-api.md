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
| 鉴权 | 三级：① 回环地址（127.0.0.1）且未配置静态 token → 免登录；② 配置了 `ROCKSYS_ADMIN_TOKEN` → 请求头 `Authorization: Bearer <token>`；③ 已初始化 → 登录签发的 JWT（`Authorization: Bearer <token>`） |
| 鉴权失败 | `401`，响应体 JSON `{"ok":false,"error":"<原因>"}`（认证端点）或文本 `unauthorized`（其余端点） |
| 写操作响应 | 统一 `{"ok":true}` 或 `{"ok":false,"error":"<原因>"}` |
| 前端访问凭证 | 登录成功后 JWT 存浏览器本地；每次请求带 `Authorization` 头；收到 401 时跳转登录视图 |

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

**降级链关联**（概览页可视化用）：L1 防护 = `shield`；L2 分发 = `dispatch`；L3 结果 = `result`。三者全开 = 全量能力；逐级关闭 = 逐级降级；最底层裸转发永远可用。

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
  "upstream": "http://127.0.0.1:8080",
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
    "key": "SHIELD_IP_BLACKLIST",
    "title": "黑名单 IP",
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
| `MQ_` | 消息（轮询/重试/退避/消费方地址，无条件注册、装配期生效，热更后需重启） |
| `REGISTRY_` | 注册中心 |
| `OBJECT_` | 对象存储 |
| `SCRIPT_` | 脚本（Lua 策略执行超时） |
| `DB_` / `SQL_DIR` | 数据访问 |

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

**响应 200**：`Content-Type: application/x-ndjson`，每行一个 JSON 对象（平铺维度，扩展负载字段如 `request_body` 直接出现在顶层）：

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

**数据来源**：当前启用的 obs 存储后端（默认 `OBS_STORE=db` 查 `access_log` 表；`OBS_STORE=file` 读 `logs/access-YYYY-MM-DD.jsonl`，已弃用），切换后端后只查当前后端。**返回按完成时间倒序（最新在前），最多 `2000` 条**；耗时排序由 WebUI 端对已加载数据本地排序。

**失败 `400`**：时间格式非法（应为 `YYYY-MM-DD` 或 `YYYY-MM-DDTHH:MM`）/ `from` 晚于 `to`，响应体文本为错误原因。
**失败 `503`**：观测组件未注册。

> 前端按行解析（`split('\n')` + 每行 `JSON.parse`，跳过坏行）。某时段无日志时后端返回空；整段为空时提示"所选时间范围无访问日志"。

### 3.11.1 GET /admin/logs/storage — 日志存储总占用

**响应 200**（`application/json`）：

```json
{"file_bytes":1048576,"db_bytes":40960,"total_bytes":1089536}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| file_bytes | int | 文件日志占用（`OBS_LOG_DIR` 下所有 `access-*.jsonl` 合计，file 后端；file 已弃用，为遗留数据） |
| db_bytes | int | 数据库日志表占用（`access_log` 表 + 索引，db 后端；`dataDB` 未就绪时为 0） |
| total_bytes | int | 合计 = file_bytes + db_bytes |

> 与当前启用后端无关：切换 `OBS_STORE` 后旧数据仍计入（db 表保留在库、file 数据保留在磁盘）。默认后端为 `db`；`OBS_STORE=file` 已弃用，将不再被支持。
> WebUI 日志页顶部展示该统计。

### 3.12 GET /admin/auth/status — 认证状态

返回管理接口认证状态，WebUI 启动时据此决定显示登录/注册/重置面板还是直接进入控制台。

**响应：**

| 字段 | 类型 | 说明 |
|------|------|------|
| `auth_required` | bool | 是否需要登录（绑定非回环地址或有静态 token） |
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

**成功 `200`：** `{"ok":true,"token":"<jwt>","expires_in":43200}`，前端将 token 存本地，后续请求带 `Authorization: Bearer <token>`。
**失败 `401`：** 用户名或密码错误。
**失败 `429`：** 登录尝试过于频繁（5 分钟窗口失败 5 次锁定 5 分钟）。

### 3.15 POST /admin/auth/reset — 重置管理员凭证（忘记密码）

**前置条件：** 运维已将 `.env` 中 `ADMIN_INITIALIZED` 改为 `false`（进入重置模式）。

**请求：** `{"username":"admin","password":"NewPass@67890"}`

**成功 `200`：** `{"ok":true}`，同时恢复 `ADMIN_INITIALIZED=true`。
**失败 `403`：** 未处于重置模式（需先改 `.env`）。
**失败 `400`：** 参数非法。

---

## 4. 数据字典（前端展示映射）

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

> 契约原则：只增不改删；新增字段不影响旧字段语义。
