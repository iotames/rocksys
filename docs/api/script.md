# Admin API · 策略脚本（script）

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
