# Admin API · 配置与可信代理文件（config / proxy）

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
    "key": "SHIELD_RATE_LIMIT_RPS",
    "title": "限流速率（每秒请求数）",
    "type": "int",
    "defval": "0",
    "current": "100",
    "example": "100"
  }
]
```

`type` 为注册时的真实类型（`bool` / `int` / `string`，即 `Register` 支持的三类，由后端从注册值指针推导），
前端据此选择编辑控件（如 `bool` → 开关），不靠键名猜测——避免新增非 `_ENABLED` 结尾的布尔项被误渲染为文本框。

| 字段 | 类型 | 说明 |
|------|------|------|
| key | string | 配置项注册名（即环境变量名，热改 `PUT /admin/config` 时用此名） |
| title | string | 中文说明 |
| type | string | 注册类型（`bool` / `int` / `string`），前端据真实类型选编辑控件（如 `bool` → 开关），不靠键名猜测 |
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

---

### 3.11.2 GET/POST /admin/proxy/trusted* — 可信代理文件在线编辑

WebUI「可信代理」页数据源（实现 `internal/netutil/proxies_admin.go`）。文件固定为装配配置的 `TRUSTED_PROXIES_FILE`（相对 `HOT_SCRIPTS_DIR/trusted_proxies/`，默认 `trusted_proxies.txt`），保存落点为外挂目录，ScriptHub ≤3s 自动重读快照热更生效。

| 端点 | 说明 |
|------|------|
| `GET /admin/proxy/trusted` | 文件清单：`{"files":[{name,title,desc,override,lines,modified?,bytes?}]}` |
| `GET /admin/proxy/trusted/file?name=` | 当前生效内容 + 内嵌默认内容：`{name,content,embedded,override,modified,hot_path,max_tokens}` |
| `POST /admin/proxy/trusted/save` | body `{name, content}` → 原子写外挂文件（上限 512KB）；保存前先解析校验，非法 IP/CIDR 返回 400 且不落盘（空内容合法 = 回退内嵌默认） |

> 安全约束：文件名仅允许 `TRUSTED_PROXIES_FILE`；保存端点仅 POST（防本机恶意页面无凭证触发）。

