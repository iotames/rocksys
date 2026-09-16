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
| 响应体 | 均为 JSON（日志接口除外，见 observability.md §3.11） |
| 鉴权 | 回环地址（127.0.0.1）免鉴权；非回环地址需鉴权：① 静态预共享 token（`ROCKSYS_ADMIN_TOKEN`，供 rockctl/脚本）→ `Authorization: Bearer <token>`；② 登录 JWT（`Authorization: Bearer <token>`），两者任一通过即放行 |
| 鉴权失败 | `401`，响应体 JSON `{"ok":false,"error":"<原因>"}`（认证端点）或文本 `unauthorized`（其余端点） |
| 写操作响应 | 统一 `{"ok":true}` 或 `{"ok":false,"error":"<原因>"}` |
| 前端访问凭证 | 账号密码登录后 JWT 存浏览器本地；每次请求自动带 `Authorization` 头；收到 401 时跳转登录视图 |

---

## 2. 端点总览

| # | 方法 | 路径 | 用途 | 详情文件 |
|---|------|------|------|------|
| 1 | GET | `/admin/switch/list` | 组件状态列表 | `switch.md` |
| 2 | POST | `/admin/switch/on` | 开启组件 | `switch.md` |
| 3 | POST | `/admin/switch/off` | 关闭组件 | `switch.md` |
| 4 | GET | `/admin/config` | 查看底座配置 | `config.md §3.4-3.6` |
| 5 | GET | `/admin/config/list` | 全量配置项清单（含各组件） | `config.md §3.4-3.6` |
| 6 | PUT | `/admin/config` | 热改配置 | `config.md §3.4-3.6` |
| 7 | POST | `/admin/script/publish` | 发布策略脚本 | `script.md` |
| 8 | POST | `/admin/script/rollback` | 回滚 / 移除脚本 | `script.md` |
| 9 | GET | `/admin/script/list` | 脚本列表与版本历史 | `script.md` |
| 10 | GET | `/admin/metrics` | 运行指标快照 | `observability.md` |
| 11 | GET | `/admin/logs` | 按日期查询访问日志 | `observability.md` |
| 12 | GET | `/admin/auth/status` | 认证状态（登录/注册/重置引导） | `auth.md` |
| 13 | POST | `/admin/auth/register` | 首次注册超级管理员 | `auth.md` |
| 14 | POST | `/admin/auth/login` | 登录，签发 JWT | `auth.md` |
| 15 | POST | `/admin/auth/reset` | 重置管理员凭证（忘记密码） | `auth.md` |
| 16 | GET | `/admin/warnings` | 数据清理未开启警告（常驻横幅数据源，与登录响应同源） | `observability.md` |
| 17 | GET | `/admin/version` | 构建版本信息（左侧版本展示） | `observability.md` |
| 18 | GET | `/admin/logs/storage` | 访问日志存储总占用 | `observability.md` |
| 19 | POST | `/admin/logs/prune` | 手动清理访问日志（保留期外） | `observability.md` |
| 20 | GET | `/admin/shield/metrics` | WAF 实时计数（内存窗口，`window=1m/5m/15m/1h` 可选，缺省 1m；无需查库） | `shield.md` |
| 21 | GET | `/admin/shield/events` | WAF 拦截明细（JSONL，时间/类别/IP 过滤） | `shield.md` |
| 22 | GET | `/admin/shield/stats` | WAF 聚合统计（按日 × 类别 + Top IP） | `shield.md` |
| 23 | POST | `/admin/shield/prune` | 手动清理拦截明细（保留期外） | `shield.md` |
| 24 | GET / POST | `/admin/shield/blacklist` | 黑名单列表（GET，分页/过滤）/ 新增（POST） | `shield.md` |
| 25 | POST | `/admin/shield/blacklist/update` | 更新黑名单条目（title/block_type/expires_at） | `shield.md` |
| 26 | POST | `/admin/shield/blacklist/delete` | 软删黑名单条目（可恢复） | `shield.md` |
| 27 | POST | `/admin/shield/blacklist/restore` | 恢复软删黑名单条目 | `shield.md` |
| 28 | POST | `/admin/shield/blacklist/import` | 批量导入黑名单（body 纯文本，每行一个 IP/CIDR） | `shield.md` |
| 29 | GET / POST | `/admin/shield/whitelist` | 白名单列表（GET）/ 新增（POST） | `shield.md` |
| 30 | POST | `/admin/shield/whitelist/update` | 更新白名单条目（title） | `shield.md` |
| 31 | POST | `/admin/shield/whitelist/delete` | 软删白名单条目（可恢复） | `shield.md` |
| 32 | POST | `/admin/shield/whitelist/restore` | 恢复软删白名单条目 | `shield.md` |
| 33 | POST | `/admin/shield/whitelist/import` | 批量导入白名单 | `shield.md` |
| 34 | GET | `/admin/meta` | 组件/服务元数据（WebUI 全局展示，无状态不缓存） | `本文件 §4.0` |
| 35 | GET | `/admin/shield/rules` | WAF 规则文件清单（外挂覆写状态/生效行数/修改时间，WebUI·文件编辑 Tab） | `shield.md` |
| 36 | GET | `/admin/shield/rules/file` | 读单个规则文件当前生效内容 + 内嵌默认内容（`?name=`，文件名白名单校验） | `shield.md` |
| 37 | POST | `/admin/shield/rules/save` | 保存规则文件到 `HOT_SCRIPTS_DIR/rules/`（原子写，ScriptHub ≤3s 自动热更生效；body `{name, content}`，上限 512KB） | `shield.md` |
| 38 | GET | `/admin/proxy/trusted` | 可信代理文件清单（外挂覆写状态/生效行数/修改时间，WebUI·全局配置可信代理页签） | `config.md §3.11.2` |
| 39 | GET | `/admin/proxy/trusted/file` | 读可信代理文件当前生效内容 + 内嵌默认内容（`?name=`，仅允许装配的 `TRUSTED_PROXIES_FILE`） | `config.md §3.11.2` |
| 40 | POST | `/admin/proxy/trusted/save` | 保存可信代理文件到 `HOT_SCRIPTS_DIR/trusted_proxies/`（原子写，保存前先解析校验非法 IP/CIDR 直接 400；ScriptHub ≤3s 自动热更生效；body `{name, content}`，上限 512KB） | `config.md §3.11.2` |
| 41 | GET | `/admin/db/schema` | 表结构检查（期望 = 运行期 SQL 源脚本，实际 = 当前数据连接 catalog；返回 A-F 分级差异与自动项生成 SQL） | `database.md` |
| 42 | POST | `/admin/db/exec` | 执行 SQL（拆句逐条执行、遇错即停，返回逐条结果；每条语句落 `sql_exec_log` 审计留痕；danger 级危险操作，服务端不做语句白名单）。**混合模式**：body 带 `background:true` 时改提交任务执行中心后台执行、立即返回 `{ok,task_id,total}`，逐条结果经 `GET /admin/tasks/{id}` 的 `progress.detail` 取回（长语句摆脱 HTTP 超时；`source` 记为 `webui-background`） | `database.md` |
| 43 | GET | `/admin/db/execlog` | SQL 执行历史查询（`sql_exec_log` 表，时间倒序 + offset 服务端分页） | `database.md` |
| 44 | GET | `/admin/db/size` | 数据库空间占用统计（表名/备注/精确条数 + 库级总空间；逐表占用含数据/索引拆分，只取缓存，未计算返回 `bytes_known=false`）；`GET /admin/db/table_size?table=` 单表精确占用按需计算（白名单校验 + 10 分钟缓存） | `database.md` |
| 45 | POST | `/admin/db/geoip_sync` | GeoIP 关联表增量同步：扫 access_log / shield_event 中「`client_ip` 未入 `geoip_list`」的 IP，按已加载 mmdb 逐 IP 解析后 upsert `geoip_list`（一 IP 一行；发现阶段按 id 主键游标分块增量扫描 + 对 geoip_list IN 点查内存求差；无时间预算——后台任务默认无超时，人工取消在块边界收工、重复执行自动续接；geo 未就绪 503；私网/回环/解析不出的 IP 跳过并计数；同步成功后自动清流量统计缓存；结束回写 `schedule_list` 的 `geoip_sync` 行（任务定义登记面，只记轮次执行结果）：完成记 `success`，人工取消致本轮未跑完记 `partial`，真错误记 `failed`）。**后台任务模式**：手动与定时（GEOIP_SYNC_INTERVAL 到点）皆经任务中心提交实例——提交即返回 `{ok,task_id}`，进度经 `GET /admin/tasks/{id}` 查询（流式更新）；互斥命中返回 409。定时轮同集被占则该轮跳过并登记 `skipped`（不动最近执行时间），下轮到点续接 | `database.md` |
| 45a | GET | `/admin/schedule/list` | 定时任务只读清单（GEOIP_LIST D15/D18）：`schedule_list` 登记行 + 行内 `enabled`（bool，服务端读 `config_key` 对应 easyconf 现值；空 `config_key` 系统级恒 true；`GEOIP_SYNC_INTERVAL` 按 0=关闭语义判定且 mmdb 未就绪视为停用）。响应 `{ok,tasks:[{name,title,kind,config_key,plan,last_run_at,last_status,last_message,remark,enabled}]}`；`last_run_at=null` 表示「未登记」（非「从未执行」）。只读，无写端点 | `database.md` |
| 46 | GET | `/admin/db/dsn` | 外部数据源列表（DSN 脱敏：mysql/postgres 凭据段打码，sqlite 路径原样；`{code,name,driver,dsn}`）。数据源为迁移目标与备选源，配置持久化在 `<CONF_DIR>/dsn.json` | `database.md` |
| 47 | POST | `/admin/db/dsn` | 添加数据源：body `{name,driver,dsn[,test]}`。校验驱动白名单（sqlite/mysql/postgres）与已注册、`name` 唯一、DSN 重复（同 Code）拦截、MySQL 密码裸 `@` 预检；`test:true` 时先连通测试再落盘。成功返回 `{ok,code}` | `database.md` |
| 48 | POST | `/admin/db/dsn/delete` | 删除数据源：body `{code}`；有数据迁移/结构对齐任务进行中时 409 拒绝（防删除在用目标） | `database.md` |
| 49 | POST | `/admin/db/dsn/test` | 连通测试：body `{driver,dsn}`，`sql.Open`+Ping+版本回显（sqlite 报内置版本），不落盘，可测未保存的连接串 | `database.md` |
| 50 | GET | `/admin/db/migrate/schema` | 目标库结构差异预览：`?code=<数据源 Code>`，期望 = 本系统内嵌 SQL 脚本（与表同步同源），返回 `{driver,items,sql}`（只读） | `database.md` |
| 51 | POST | `/admin/db/migrate/schema_apply` | 对目标库执行对齐 DDL：`?code=`；**一律后台任务化**（长 DDL 必超 HTTP 超时），零差异直接返回 `{ok,noop:true}`，否则返回 `{ok,task_id,total}`；拆句逐条执行、遇错即停（索引「已存在」类错误幂等跳过），逐条结果经任务 `progress.detail` 取回。**不写运行库 `sql_exec_log`**（目标库动作不属于运行库审计域） | `database.md` |
| 52 | POST | `/admin/db/migrate/start` | 启动数据迁移：body `{source,target,tables[],batch,mode}`（`source` 缺省/`self`=本机运行库；`target` 必须是外部数据源，指向运行库被拒；`mode` ∈ replace/skip；`batch` clamp 100–10000，缺省 1000，仅存任务内存态不落盘）。返回 `{ok,task_id,tables,batch,mode}`；已有任务在跑 409 | `database.md` |
| 53 | GET | `/admin/db/migrate/status` | 迁移任务状态便捷视图：`{state,task_id?,result?,tables:[{table,status,rows_done,rows_total,err?}]}`，无任务时 `state=idle` | `database.md` |
| 54 | POST | `/admin/db/migrate/cancel` | 取消迁移：body `{table}` 取消该待迁移表（仅 pending 可取消）；空 body 取消整任务（当前批事务完成后停止，已写入行保留、重跑幂等续接） | `database.md` |
| 55 | GET | `/admin/tasks` | 任务执行中心任务列表：`{items:[Task], allow_creators:[来源白名单], mutex_task_field:"CreatedBy", mutex_list:[互斥公共集], mutex_map:{标签:[互斥标签]}}`；items 含 running 与保留期内终态（终态仅留最近 500 条），按新→旧排序；轻量口径：终态任务的 progress 仅含 text 不含 detail，且默认只返回最近 20 条终态（?limit=N 自定义，0=全量；运行中任务始终全带不受限），has_more=true 表示终态被截断；完整明细经 GET /admin/tasks/{id} 单查获取。Task = `{id,created_by,title,status,progress{text,detail},result,created_at,finished_at}` | `database.md` |
| 56 | GET | `/admin/tasks/{id}` | 单任务详情（`progress`/`status`/`result`/时间字段）；不存在（含已淘汰/重启后旧 ID）返回 404 | `database.md` |
| 57 | POST | `/admin/tasks/{id}/cancel` | 统一取消（context 送达，落点由业务定）：返回 `{ok,message,task}`；任务已终态时返回该终态与提示、不报错；不存在返回 404 | `database.md` |
| 43 | POST | `/admin/shield/blacklist/sync_file` | 从外挂规则文件 `rules/ip_blacklist.txt` 同步 IP 入库（block_type=11，幂等） | `shield.md` |
| 44 | POST | `/admin/shield/blacklist/ban` | 专用封禁端点（三态：入库 / 活跃 400 / 软删过期恢复续封，warn_times 累计） | `shield.md` |
| 45 | POST | `/admin/db/geoip_sync` | GeoIP 关联表增量同步：扫 access_log / shield_event 中「`client_ip` 未入 `geoip_list`」的 IP，按已加载 mmdb 逐 IP 解析后 upsert `geoip_list`（一 IP 一行；发现阶段按 id 主键游标分块增量扫描 + 对 geoip_list IN 点查内存求差；无时间预算——后台任务默认无超时，人工取消在块边界收工、重复执行自动续接；geo 未就绪 503；私网/回环/解析不出的 IP 跳过并计数；同步成功后自动清流量统计缓存；结束回写 `schedule_list` 的 `geoip_sync` 行（任务定义登记面，只记轮次执行结果）：完成记 `success`，人工取消致本轮未跑完记 `partial`，真错误记 `failed`）。**后台任务模式**：手动与定时（GEOIP_SYNC_INTERVAL 到点）皆经任务中心提交实例——提交即返回 `{ok,task_id}`，进度经 `GET /admin/tasks/{id}` 查询（流式更新）；互斥命中返回 409。定时轮同集被占则该轮跳过并登记 `skipped`（不动最近执行时间），下轮到点续接 | `database.md` |
| 46 | GET | `/admin/system` | 运行时长 + 机器资源概况（概览页运行时间瓦片与资源监控卡数据源） | `observability.md` |
| 47 | GET | `/admin/shield/total` | WAF 拦截事件落库总数（查库 COUNT 全范围，受保留期影响） | `shield.md` |
| 48 | GET | `/admin/obs/traffic/summary` | 流量统计指标标量 + 率（概览页流量统计区数据源） | `obs.md` |
| 49 | GET | `/admin/obs/traffic/series` | 流量统计访问/拦截时间桶趋势（hour/day，缺省自适应） | `obs.md` |
| 50 | GET | `/admin/obs/traffic/geo` | 流量统计地区分布（access/blocked × country/province 切换，含 geo_ready） | `obs.md` |

> 端点详解按域拆分到本目录各文件。**按端点定位**：上表行号 → 下表文件 → 文件内按原章节号检索：

| 文件 | 覆盖端点（§ 章节号） | 何时读 |
|------|----------------------|--------|
| [switch.md](switch.md) | 组件状态/开启/关闭（总览 1-3 行） | 组件启停、状态字段、组件名→中文映射 |
| [config.md](config.md) | 配置查看/清单/热改（§3.4-3.6，4-6 行）、可信代理文件编辑（§3.11.2，38-40 行） | 配置项元数据与分组、热改语义、可信代理三端点 |
| [script.md](script.md) | 策略脚本发布/回滚/列表（总览 7-9 行） | 脚本版本管理、沙箱限制、内存态提示 |
| [auth.md](auth.md) | 认证状态/注册/登录/重置（总览 12-15 行） | 登录引导逻辑、JWT、warnings 字段、限流锁定 |
| [observability.md](observability.md) | 运行指标/访问日志/日志存储（§3.10-3.11.1，10-11、18-19 行）、版本/警告/系统资源（§3.16-3.17.1，16-17、46-47 行） | 查询参数与字段表、JSONL 解析约定、CPU/内存字段降级语义 |
| [shield.md](shield.md) | WAF 统计 + 动态黑白名单 + 小黑屋（总览 20-35、43-44、47 行） | 实时窗口口径、黑白名单 CRUD/导入/封禁/从文件同步、Top IP geo |
| [database.md](database.md) | 表结构同步/SQL 执行/执行历史/空间占用/外部数据源/数据迁移/任务中心（总览 41-45a、55-57 行） | A-F 分级语义、danger 执行语义与审计、DSN 校验、迁移与后台任务 |
| [obs.md](obs.md) | 流量统计 summary/series/geo/清缓存（总览 48-50 行） | 指标闭合口径、UTC 时间与取整、缓存语义、降级分支 |

### 数据字典定位（§4）

- 组件/服务元数据结构 → 本文件 §4.0；组件中文名与环节 → [switch.md](switch.md) §3.1 表（本文件 §4.1）；状态枚举与指标展示映射 → 本文件 §4.2/§4.3。

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

### 4.1 组件中文名与环节（见 switch.md §3.1 表）

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

## 5. 契约原则

只增不改删；新增字段不影响旧字段语义。
