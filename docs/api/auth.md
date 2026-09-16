# Admin API · 认证（auth）

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

**成功 `200`：** `{"ok":true,"token":"<jwt>","expires_in":43200,"warnings":["拦截记录清理未开启，shield_event 表可能持续膨胀"]}`，前端将 token 存本地，后续请求带 `Authorization: Bearer <token>`；`warnings` 为数据清理未开启提醒（`SHIELD_EVENT_PRUNE_ENABLED` / `OBS_LOG_PRUNE_ENABLED` 为 false 时出现，组件未装配则无对应项，恒为数组），WebUI 用于渲染常驻置顶横幅（详见 observability.md §3.17）。
**失败 `401`：** 用户名或密码错误。
**失败 `429`：** 登录尝试过于频繁（5 分钟窗口失败 5 次锁定 5 分钟）。

### 3.15 POST /admin/auth/reset — 重置管理员凭证（忘记密码）

**前置条件：** 运维已将 `.env` 中 `ADMIN_INITIALIZED` 改为 `false`（进入重置模式）。

**请求：** `{"username":"admin","password":"NewPass@67890"}`

**成功 `200`：** `{"ok":true}`，同时恢复 `ADMIN_INITIALIZED=true`。
**失败 `403`：** 未处于重置模式（需先改 `.env`）。
**失败 `400`：** 参数非法。
