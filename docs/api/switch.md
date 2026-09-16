# Admin API · 组件开关（switch）

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
| name | string | 组件名（枚举见 README.md §4.1） |
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
