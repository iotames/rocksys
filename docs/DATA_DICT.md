# DATA_DICT — RockSys 数据字典

> 数据层工程化说明：本项目 3 种 SQL 方言（SQLite / PostgreSQL / MySQL）数据类型各不相同，
> 本字典给出每张表的字段名、标题、说明、可能值示例与三方言类型对照。
> **权威数据源为 `sql/<dbtype>/` 建表脚本（含字段注释）**，本文档是其可读性视图，改动须同步。

---

## 0. 总览

- 统一数据访问层：`internal/db`（`DB_DRIVER`/`DB_DSN` 配置，见 `docs/CONFIGURATION.md`）；
- SQL 脚本三方言齐平：`sql/sqlite/`、`sql/postgres/`、`sql/mysql/`（`internal/db/db_test.go` 的 `TestScriptParity` 强制校验文件集一致）；
- 表名/库名等动态标识符用 `{table}` 占位符（运行时由组件替换，**禁止来自外部用户输入**）；
- 全项目共 **10 张业务表**，分属 5 个组件 + 装配层：
  | 表名 | 归属组件 | 条件装配 |
  |---|---|---|
  | `shield_event` | shield（WAF 防护） | 恒建（DB 就绪即建） |
  | `access_log` | obs（访问日志/指标） | 恒建（DB 就绪即建） |
  | `admin_users` | adminapi（管理接口鉴权） | 恒建（DB 就绪即建） |
  | `outbox` | mq（异步消息） | `MQ_ENABLED=true` 才建 |
  | `ip_blacklist` | shield（WAF 防护） | 恒建（DB 就绪即建） |
  | `ip_whitelist` | shield（WAF 防护） | 恒建（DB 就绪即建） |
  | `attack_archive` | shield（WAF 防护） | 恒建（DB 就绪即建） |
  | `sql_exec_log` | adminapi（管理接口） | 恒建（DB 就绪即建，首次执行 SQL 时惰性建） |
  | `geoip_list` | cmd/rocksys（装配层，GEOIP_LIST 方案） | 恒建（DB 就绪即建；obs/shield 建表时一并确保） |
  | `schedule_list` | cmd/rocksys（装配层，GEOIP_LIST 方案） | 恒建（DB 就绪即建；登记器 EnsureTable） |

**通用约定**
- 字段命名统一 `snake_case`；
- 时间列一律存 **UTC**（SQLite 存 DATETIME 文本，PG 为 TIMESTAMPTZ，MySQL 为 DATETIME(3)）；
- 扩展字段 `extra` 为 JSON 文本（向前兼容：新增扩展点只加 JSON 键，不新增列）；
- 枚举类字段数值/取值稳定、只增不改（改动必须同步：Go 枚举源码 + 三方言脚本注释 + 本文档）。

---

## 1. 表清单

| 表名 | 中文名 | 归属组件 | 用途 | 建表脚本（三方言） |
|---|---|---|---|---|
| `shield_event` | WAF 拦截事件明细表 | shield | 仅存**被拦截**的请求（拦截与放行分开记录，与 access_log 互不关联） | `sql/{sqlite,postgres,mysql}/shield_event_create_table.sql` |
| `access_log` | 访问日志表 | obs | 存**放行**请求的访问明细与耗时指标 | `sql/{sqlite,postgres,mysql}/access_log_create_table.sql` |
| `admin_users` | 管理接口超级管理员表 | adminapi | 管理后台登录鉴权用户存储（密码不存明文） | `sql/{sqlite,postgres,mysql}/admin_users_create_table.sql` |
| `outbox` | mq 异步消息 outbox 表 | mq | 本地先落库、后台轮询投递（outbox 模式）；`MQ_ENABLED=true` 时装配 | `sql/{sqlite,postgres,mysql}/mq_create_table.sql` |
| `ip_blacklist` | 动态 IP 黑名单表 | shield | 管理面录入/批量导入的拉黑条目（与外挂 `rules/ip_blacklist.txt` 取并集；热路径只读内存快照） | `sql/{sqlite,postgres,mysql}/ip_blacklist_create_table.sql` |
| `ip_whitelist` | 动态 IP 白名单表 | shield | 管理面录入的白名单条目（白名单唯一来源；白名单优先于黑名单） | `sql/{sqlite,postgres,mysql}/ip_whitelist_create_table.sql` |
| `attack_archive` | 攻击证据归档表 | shield | 攻击证据归档（本期仅建表，归档逻辑见 WAF 方案 §8） | `sql/{sqlite,postgres,mysql}/attack_archive_create_table.sql` |
| `sql_exec_log` | SQL 执行审计表 | adminapi | 管理端「执行SQL」每条语句执行留痕（审计追溯，不清理） | `sql/{sqlite,postgres,mysql}/sql_exec_log_create_table.sql` |
| `geoip_list` | IP 地理信息关联表 | cmd/rocksys（装配层） | 一 IP 一行，与 access_log / shield_event 按 client_ip 关联（地理信息归一，取代逐行冗余列）；GeoIP 同步器维护 | `sql/{sqlite,postgres,mysql}/geoip_list_create_table.sql` |
| `schedule_list` | 定时任务只读登记表 | cmd/rocksys（装配层） | 定时任务登记 + 状态汇总（只读，不驱动任务）；装配期 upsert 登记，geoip_sync 行回写运行状态 | `sql/{sqlite,postgres,mysql}/schedule_list_create_table.sql` |

---

## 2. 字段字典

> 类型列按 `sqlite / postgres / mysql` 顺序标注；「默认」为空表示无默认值（NOT NULL）。
> 「可能值示例」为真实场景取值样例。

### 2.1 shield_event — WAF 拦截事件明细表（14 列）

**说明**（GEOIP_LIST 方案）：地理信息不再逐行落库，读侧经 `LEFT JOIN geoip_list ON ip = client_ip` 关联返回 `country_code`/`country_name`/`province`/`city`，JOIN 未命中回退实时解析（「市→省→国名→未知」兜底只在读侧显示）。

**说明**：拦截请求被转发链短路，本表在拦截点就地记录（obs 收不到被拦请求，因此两表各记各的）。
`block_type` 枚举见 §3.1，`rule_hit` 特征名见 §3.3。

| 字段名 | 标题 | 说明 | 可能值示例 | 类型（sqlite/postgres/mysql） | 默认 |
|---|---|---|---|---|---|
| `id` | 主键 | 自增主键 | `1` | INTEGER AUTOINCREMENT / BIGSERIAL / BIGINT AUTO_INCREMENT | — |
| `time` | 拦截时刻 | 拦截时刻（UTC） | `2026-08-20T12:17:11Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `trace_id` | 链路 ID | 链路 ID（仅 shield_event 内部追溯，不与 access_log 关联） | `b725a1878d8fc003` | TEXT / TEXT / VARCHAR(255) | `''` |
| `block_type` | 拦截类别 | 拦截类别枚举（数值稳定，见 §3.1） | `7`（SQL注入） | INTEGER / SMALLINT / SMALLINT | — |
| `client_ip` | 来源 IP | 攻击来源 IP（已按 X-Forwarded-For 取真实客户端地址） | `192.168.1.10` | TEXT / TEXT / VARCHAR(64) | `''` |
| `method` | 请求方法 | 请求方法 | `GET`、`POST` | TEXT / TEXT / VARCHAR(16) | `''` |
| `path` | 请求路径 | URL 路径 | `/login` | TEXT / TEXT / VARCHAR(2048) | `''` |
| `raw_url` | 原始 URL | 含查询串的原始 URL（攻击特征常在此） | `/login?id=1' OR '1'='1` | TEXT / TEXT / VARCHAR(2048) | `''` |
| `user_agent` | 客户端标识 | 客户端 User-Agent（爬虫识别依据） | `Mozilla/5.0 (compatible; Googlebot/2.1)` | TEXT / TEXT / VARCHAR(640) | `''` |
| `host` | 请求主机 | 请求 Host | `127.0.0.1:8080` | TEXT / TEXT / VARCHAR(255) | `''` |
| `status_code` | 拦截响应码 | 拦截响应码（403/413/429，见 §3.2） | `403` | INTEGER / INT / INT | `0` |
| `rule_hit` | 命中规则 | 命中的规则/特征名（见 §3.3） | `sql_pattern` | TEXT / TEXT / VARCHAR(255) | `''` |
| `req_bytes` | 请求体大小 | 请求体字节数（Content-Length） | `0`、`1024` | INTEGER / BIGINT / BIGINT | `0` |
| `extra` | 扩展字段 | 扩展字段（referer / x_forwarded_for 等，JSON） | `{"referer":"http://x","x_forwarded_for":"1.2.3.4"}` | TEXT / TEXT / TEXT | `'{}'` |

> ⚠ 方言差异备注：`path`、`extra` 在 sqlite/postgres 有默认值（`''`/`'{}'`），MySQL 无默认值（NOT NULL，写入必须显式给值）。

### 2.2 access_log — 访问日志表（17 列）

**说明**（GEOIP_LIST 方案）：地理信息经 `geoip_list` 关联（见 §2.1 说明），本表只存事实维度。

**说明**：放行请求的访问明细（拦截请求不经过 obs，见 §2.1 说明）。耗时列单位均为毫秒（ms），
四段拆解：入网 + 转发（业务） + 出网 = 总耗时（±1ms 取整误差）。
**历史行注记**：`egress_ms` 列上线前的旧行恒为 `0`，且 `total_ms` 为旧口径（到转发完成，不含出网段）——
排序解读：降序时旧行沉底、升序时旧行置顶，均属预期。

| 字段名 | 标题 | 说明 | 可能值示例 | 类型（sqlite/postgres/mysql） | 默认 |
|---|---|---|---|---|---|
| `id` | 主键 | 自增主键 | `1` | INTEGER AUTOINCREMENT / BIGSERIAL / BIGINT AUTO_INCREMENT | — |
| `time` | 完成时刻 | 请求完成时刻（UTC）＝出网时刻（响应写回客户端完成，与代码 DoneAt 埋点同点取值） | `2026-08-20T12:17:11Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `trace_id` | 链路 ID | 链路 ID（贯穿整条转发链） | `a1b2c3d4` | TEXT / TEXT / VARCHAR(64) | — |
| `tenant_id` | 租户 ID | 租户 ID（多租户预留，默认空） | `tenant-a` | TEXT / TEXT / VARCHAR(64) | `''` |
| `path` | 请求路径 | 请求 URL 路径 | `/api/order` | TEXT / TEXT / VARCHAR(2048) | — |
| `method` | 请求方法 | 请求方法 | `GET`、`POST` | TEXT / TEXT / VARCHAR(16) | — |
| `client_ip` | 客户端 IP | 客户端 IP（已按 X-Forwarded-For 取真实地址） | `192.168.1.10` | TEXT / TEXT / VARCHAR(64) | `''` |
| `status_code` | 响应码 | 上游返回的响应状态码（与 shield_event 的拦截码语义不同） | `200`、`502` | INTEGER / INT / INT | — |
| `upstream` | 上游地址 | 实际转发的上游地址 | `http://10.0.0.5:9000` | TEXT / TEXT / VARCHAR(255) | `''` |
| `shield_ms` | 入网耗时 | 请求到达→转发前（全部前置中间件）耗时（ms）；仅中间链只挂 shield 时等价防护耗时 | `1`、`35` | INTEGER / BIGINT / BIGINT | `0` |
| `biz_ms` | 转发（业务）耗时 | 转发耗时（ms），含网关↔上游网络往返；内网部署、网络稳定时约等于业务真实处理耗时 | `120` | INTEGER / BIGINT / BIGINT | `0` |
| `total_ms` | 总耗时 | 到达→出网总耗时（ms）＝入网+转发（业务）+出网；历史行为旧口径：到转发完成 | `135` | INTEGER / BIGINT / BIGINT | `0` |
| `egress_ms` | 出网耗时 | 出网耗时（ms）＝响应写回客户端完成−转发完成；含客户端网络传输时间，慢客户端会撑大该值；历史行为 `0` | `2`、`15` | INTEGER / BIGINT / BIGINT | `0` |
| `req_bytes` | 请求字节 | 请求体字节数 | `512` | INTEGER / BIGINT / BIGINT | `0` |
| `resp_bytes` | 响应字节 | 响应体字节数 | `2048` | INTEGER / BIGINT / BIGINT | `0` |
| `user_agent` | 客户端标识 | 客户端 User-Agent（UV 口径=IP+UA） | `Mozilla/5.0 ...` | TEXT / TEXT / VARCHAR(640) | `''` |
| `extra` | 扩展字段 | 扩展字段（JSON，向前兼容） | `{}` | TEXT / TEXT / TEXT | `'{}'` |

### 2.3 admin_users — 管理接口超级管理员表（5 列）

**说明**：管理后台登录鉴权用户存储。密码不存明文，`password_hash` 为 pbkdf2 哈希
（格式 `pbkdf2$<iter>$<salt>$<hash>`，见 `internal/adminapi/userstore.go`）。

| 字段名 | 标题 | 说明 | 可能值示例 | 类型（sqlite/postgres/mysql） | 默认 |
|---|---|---|---|---|---|
| `id` | 主键 | 自增主键 | `1` | INTEGER AUTOINCREMENT / BIGSERIAL / BIGINT AUTO_INCREMENT | — |
| `username` | 用户名 | 登录用户名（唯一） | `admin` | TEXT UNIQUE / TEXT UNIQUE / VARCHAR(64) UNIQUE | — |
| `password_hash` | 密码哈希 | pbkdf2 哈希（`pbkdf2$<iter>$<salt>$<hash>`，不存明文） | `pbkdf2$10000$...$...` | TEXT / TEXT / VARCHAR(255) | — |
| `created_at` | 创建时间 | 创建时间（UTC） | `2026-08-20T12:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `updated_at` | 更新时间 | 最近更新时间（UTC） | `2026-08-20T12:30:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |

### 2.4 outbox — mq 异步消息 outbox 表（7 列）

**说明**：outbox 模式——业务侧先本地落库，后台轮询 `status in (pending, failed)` 投递到消费方，
成功后标记 `done`，超重试上限转 `dead`。表名由装配指定（`cmd/rocksys/main.go` 传 `"outbox"`）。
`status` 枚举见 §3.4。

| 字段名 | 标题 | 说明 | 可能值示例 | 类型（sqlite/postgres/mysql） | 默认 |
|---|---|---|---|---|---|
| `id` | 主键 | 自增主键 | `1` | INTEGER AUTOINCREMENT / BIGSERIAL / BIGINT AUTO_INCREMENT | — |
| `topic` | 消息主题 | 消息主题（路由到消费方） | `order.created` | TEXT / TEXT / VARCHAR(255) | — |
| `payload` | 消息体 | 消息体（JSON） | `{"order_id":42}` | TEXT / TEXT / TEXT | — |
| `status` | 投递状态 | 投递状态枚举（pending/failed/done/dead，见 §3.4） | `pending` | TEXT / TEXT / VARCHAR(16) | `'pending'` |
| `retry_count` | 重试次数 | 已重试次数（超过上限转 dead） | `0`、`3` | INTEGER / INT / INT | `0` |
| `last_error` | 最近错误 | 最近一次投递失败的错误信息 | `timeout` | TEXT / TEXT / VARCHAR(1024) | `''` |
| `created_at` | 创建时间 | 创建时间（UTC） | `2026-08-20T12:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |

> ⚠ 方言差异备注：`last_error` 在 MySQL 用 `VARCHAR(1024)` 而非 TEXT（8.0.13 以下 TEXT 列不支持 DEFAULT）。

---

### 2.5 ip_blacklist — 动态 IP 黑名单表（10 列）

**说明**：管理面录入/批量导入的动态黑名单（持久化权威），与外挂 `rules/ip_blacklist.txt` 取**并集**；
请求热路径只读内存快照（性能红线：热路径零 DB 查询），本表仅管理操作/启动加载/后台刷新访问。
`block_type` 复用 §3.1 枚举（仅管理面过滤/统计，非运行时匹配依据）；黑名单条目语境可用 0（其他）/11（人工收录），拦截事件语境只写 1-10（见 §3.1 语境说明）；运行时只按 `ip` 精确/CIDR 匹配。
★ 存量库升级：本项目无自动迁移机制，`warn_times` 列经 WebUI「服务 → 数据库 → 表结构」检查并执行自动生成的 ALTER 落库（详见 `docs/done/DB_SCHEMA_SYNC_PLAN.md`）。
软删除/过期语义：`deleted_at` 非 NULL 或 `expires_at` 已过期（UTC now）的条目不参与匹配。

| 字段名 | 标题 | 说明 | 可能值示例 | 类型（sqlite/postgres/mysql） | 默认 |
|---|---|---|---|---|---|
| `id` | 主键 | 自增主键 | `1` | INTEGER AUTOINCREMENT / BIGSERIAL / BIGINT AUTO_INCREMENT | — |
| `ip` | IP/CIDR | 精确 IP 或 CIDR（唯一约束，重复导入幂等拒绝） | `192.168.1.100`、`10.0.0.0/8` | TEXT / TEXT / VARCHAR(45) | — |
| `title` | 标题 | 拉黑原因标题 | `Azure 云段扫描器` | TEXT / TEXT / VARCHAR(64) | `''` |
| `block_type` | 拉黑类别 | 拉黑原因类别（复用 §3.1 枚举，仅管理面过滤统计） | `7`（SQL注入） | INTEGER / SMALLINT / SMALLINT | `1` |
| `hit_count` | 命中计数 | 命中拦截计数（异步累加，观测/排序用；自动拉黑入库/续封时补记本轮触发封禁的拦截次数） | `12` | INTEGER / INT / INT | `0` |
| `warn_times` | 封禁次数 | 该 IP 被人工/风控封禁的累计次数（人工封禁与自动拉黑共用计数；限时封禁累计达 5 次自动转永久） | `3` | INTEGER / INT / INTEGER | `0` |
| `expires_at` | 过期时间 | 过期时间（UTC）；NULL=永久，过期条目不参与匹配 | `2026-09-01T00:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `deleted_at` | 软删除时间 | 软删除时间（UTC）；非 NULL 视为已删除，不参与匹配 | `2026-08-21T10:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `created_at` | 创建时间 | 创建时间（UTC） | `2026-08-20T12:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `updated_at` | 更新时间 | 最后更新时间（UTC） | `2026-08-20T12:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |

### 2.6 ip_whitelist — 动态 IP 白名单表（6 列）

**说明**：管理面录入的动态白名单（持久化权威，白名单唯一来源，不再与 `.env` 配置取并集）；
请求热路径只读内存快照；**白名单优先于黑名单**（命中直接放行短路）。软删除语义同 2.5。

| 字段名 | 标题 | 说明 | 可能值示例 | 类型（sqlite/postgres/mysql） | 默认 |
|---|---|---|---|---|---|
| `id` | 主键 | 自增主键 | `1` | INTEGER AUTOINCREMENT / BIGSERIAL / BIGINT AUTO_INCREMENT | — |
| `ip` | IP/CIDR | 精确 IP 或 CIDR（唯一约束，重复导入幂等拒绝） | `10.0.0.5` | TEXT / TEXT / VARCHAR(45) | — |
| `title` | 标题 | 标题 | `办公出口段` | TEXT / TEXT / VARCHAR(64) | `''` |
| `deleted_at` | 软删除时间 | 软删除时间（UTC）；非 NULL 视为已删除，不参与匹配 | `2026-08-21T10:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `created_at` | 创建时间 | 创建时间（UTC） | `2026-08-20T12:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `updated_at` | 更新时间 | 最后更新时间（UTC） | `2026-08-20T12:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |

### 2.7 attack_archive — 攻击证据归档表（7 列）

**说明**：攻击证据归档（本期仅建表，归档触发/查询逻辑留待 WAF 方案 §8 后续迭代）；数据不自动清理（审计留存）。
`block_type` 复用 §3.1 枚举。

| 字段名 | 标题 | 说明 | 可能值示例 | 类型（sqlite/postgres/mysql） | 默认 |
|---|---|---|---|---|---|
| `id` | 主键 | 自增主键 | `1` | INTEGER AUTOINCREMENT / BIGSERIAL / BIGINT AUTO_INCREMENT | — |
| `client_ip` | 来源 IP | 来源 IP | `192.168.1.10` | TEXT / TEXT / VARCHAR(45) | `''` |
| `request_uri` | 请求 URI | 请求 URI（含查询串） | `/login?id=1' OR '1'='1` | TEXT / TEXT / VARCHAR(1000) | `''` |
| `request_headers` | 请求头 | 完整请求头 JSON（攻击证据） | `{"Host":"example.com"}` | TEXT / TEXT / TEXT | `'{}'` |
| `block_type` | 拦截类别 | 拦截类别（复用 §3.1 枚举） | `7`（SQL注入） | INTEGER / SMALLINT / SMALLINT | `1` |
| `remark` | 归档备注 | 归档备注 | `2026-08-20 SQL 注入批量探测` | TEXT / TEXT / VARCHAR(64) | `''` |
| `created_at` | 归档时间 | 归档时间（UTC） | `2026-08-20T12:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |

### 2.8 sql_exec_log — SQL 执行审计表（11 列）

**说明**：管理端「数据库同步 / 执行SQL」的审计留痕（`internal/adminapi/execlogstore.go`）。
**每条语句一行**：一次提交拆出的多条语句按 `batch_id` 归组、`seq` 排序；遇错即停——失败语句也落行
（`ok=0` + `error`），后续未执行语句不落表（无记录＝未执行）。数据**不自动清理**（审计永久保留）。
`ok` 取值 `1` 成功 / `0` 失败。

| 字段名 | 标题 | 说明 | 可能值示例 | 类型（sqlite/postgres/mysql） | 默认 |
|---|---|---|---|---|---|
| `id` | 主键 | 自增主键 | `1` | INTEGER AUTOINCREMENT / BIGSERIAL / BIGINT AUTO_INCREMENT | — |
| `time` | 执行时刻 | 语句执行完成时刻（UTC） | `2026-09-05T15:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `batch_id` | 批次标识 | 同一次「执行SQL」提交归一组（16 字节随机 hex，32 字符） | `cc5b703beb69b01ec627...` | TEXT / TEXT / VARCHAR(32) | — |
| `seq` | 批内序号 | 批内第几条（从 1 起，按拆句顺序） | `1`、`2` | INTEGER / INT / INT | — |
| `sql_text` | 语句原文 | 执行的 SQL 语句原文（完整保留，单条） | `ALTER TABLE t ADD COLUMN ...` | TEXT / TEXT / TEXT | — |
| `ok` | 执行结果 | 1 成功 / 0 失败 | `1`、`0` | INTEGER / SMALLINT / TINYINT | `0` |
| `rows_affected` | 受影响行数 | 受影响行数（DDL 通常为 0） | `0` | INTEGER / INT / INT | `0` |
| `error` | 失败原因 | 执行失败的错误信息（成功为空串；MySQL 方言无默认值为 NULL） | `table t1 already exists` | TEXT / TEXT / TEXT | `''` |
| `duration_ms` | 执行耗时 | 单条语句执行耗时（ms） | `2`、`15` | INTEGER / INT / INT | `0` |
| `client_ip` | 来源 IP | 发起执行的客户端 IP（审计归属） | `192.168.1.10` | TEXT / TEXT / VARCHAR(45) | `''` |
| `source` | 触发来源 | 触发来源（预留扩展：webui/api/…，本期恒为 `webui`） | `webui` | TEXT / TEXT / VARCHAR(32) | `'webui'` |

---

### 2.9 geoip_list — IP 地理信息关联表（7 列）

**说明**：地理信息是 IP 的函数，一 IP 一行（`ip` 主键），与两日志表按 `client_ip` 等值关联（无外键）。
由 GeoIP 同步器（手动 `POST /admin/db/geoip_sync` + 定时 `GEOIP_SYNC_INTERVAL`）增量构建：扫两表
`client_ip` 与本表求差 → mmdb `Lookup` → upsert。私网/回环/解析不出的 IP 不入表（读侧显示「未知」）。
数据不造假：解析不出的字段保持空串，拼接/兜底只在读侧显示。

| 字段名 | 标题 | 说明 | 可能值示例 | 类型（sqlite/postgres/mysql） | 默认 |
|---|---|---|---|---|---|
| `ip` | IP 地址 | 纯 IP 文本（与日志表 client_ip 同格式，无端口；主键） | `8.8.8.8` | TEXT PK / VARCHAR(64) PK / VARCHAR(64) PK | — |
| `country_code` | 国家码 | ISO alpha-2（聚合口径 + 世界地图着色） | `CN` | TEXT / VARCHAR(8) / VARCHAR(8) | `''` |
| `country_name` | 国名 | 本地化国名 zh-CN 优先（显示） | `中国` | TEXT / VARCHAR(64) / VARCHAR(64) | `''` |
| `province` | 省/州 | 一级行政区，zh-CN 以 mmdb 实际返回为准（多为短名如「广东」，部分全称如「北京市」；中国地图两者均正确着色） | `广东` | TEXT / VARCHAR(128) / VARCHAR(128) | `''` |
| `city` | 城市 | 城市名（仅市） | `广州市` | TEXT / VARCHAR(128) / VARCHAR(128) | `''` |
| `created_at` | 首次入库时间 | 首次解析入库时间（UTC）；冲突更新时保持不变 | `2026-09-15T01:30:41Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `updated_at` | 最近解析时间 | 该 IP 最近解析时间（UTC） | `2026-09-15T01:30:41Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |

### 2.10 schedule_list — 定时任务只读登记表（11 列）

**说明**：只做登记 + 状态汇总，**不驱动任何任务**。装配期按 `name` upsert 登记（系统级 `kind=system`
行整行只读，被改则重启重置）；运行状态本期仅 `geoip_sync` 行回写（任务结束单条原子 UPDATE），
其余行 `last_run_at` 空表示「未登记」（不代表从未执行）。无 `enabled` 列——启用状态由
`GET /admin/schedule/list` 行内附带（服务端读 `config_key` 对应配置现值，配置中心唯一真源）；
`geoip_sync` 行额外附带 `geoip_enabled`（功能开关现值）与 `geoip_sync_ready`（手动同步就绪 =
mmdb 已加载，不受开关限制），均每请求现算。

| 字段名 | 标题 | 说明 | 可能值示例 | 类型（sqlite/postgres/mysql） | 默认 |
|---|---|---|---|---|---|
| `id` | 主键 | 自增主键 | `1` | INTEGER AUTOINCREMENT / BIGSERIAL / BIGINT AUTO_INCREMENT | — |
| `name` | 任务标识 | 任务唯一标识（与代码常量绑定；唯一） | `geoip_sync` | TEXT UNIQUE / VARCHAR(64) UNIQUE / VARCHAR(64) UNIQUE | — |
| `title` | 中文名 | 展示名 | `GeoIP 关联表同步` | TEXT / VARCHAR(128) / VARCHAR(128) | `''` |
| `kind` | 任务类型 | 任务类型枚举（见 §3.5） | `configurable` | TEXT / VARCHAR(16) / VARCHAR(16) | `''` |
| `config_key` | 关联开关 | 关联 easyconf 配置名（系统级空） | `GEOIP_SYNC_INTERVAL` | TEXT / VARCHAR(64) / VARCHAR(64) | `''` |
| `plan` | 计划描述 | 仅展示（every@1h / every@24h / window/3 等） | `every@N 分钟（可配，0=关闭）` | TEXT / VARCHAR(64) / VARCHAR(64) | `''` |
| `last_run_at` | 上次执行完成 | 上次执行完成时刻（UTC；语义=执行结束而非开始）；仅回写者有意义 | `2026-09-15T01:30:41Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | NULL |
| `last_status` | 上次结果 | 结果枚举（见 §3.5） | `success` | TEXT / VARCHAR(16) / VARCHAR(16) | `''` |
| `last_message` | 结果摘要 | 人读报告文案（超 255 截断） | `access_log：新同步 758 个 IP…` | TEXT / VARCHAR(255) / VARCHAR(255) | `''` |
| `remark` | 说明 | 含「不纳入原因/只读」等说明 | `系统级只读` | TEXT / VARCHAR(255) / VARCHAR(255) | `''` |
| `updated_at` | 行更新时间 | 行最近更新时间（UTC） | `2026-09-15T01:30:41Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |

## 3. 枚举与取值附录

### 3.1 block_type — 拦截类别（shield_event.block_type / ip_blacklist.block_type / attack_archive.block_type）

数值稳定、只增不改（`plugins/shield/block_type.go` 为权威定义）。

| 值 | 中文名 | 说明 | 拦截响应码 |
|---|---|---|---|
| 1 | IP黑名单 | IP 黑名单命中（外挂文件 `rules/ip_blacklist.txt` ∪ DB 表 `ip_blacklist`） | 403 |
| 2 | 限流 | 令牌桶限流 | 429 |
| 3 | 方法不允许 | 方法白名单 | 403 |
| 4 | 请求体超限 | 请求体超限 | 413 |
| 5 | 风险路径 | 内置 + `SHIELD_WAF_RISK_PATHS` 追加 | 403 |
| 6 | 路径遍历 | 路径遍历特征 | 403 |
| 7 | SQL注入 | SQL 注入特征 | 403 |
| 8 | XSS | XSS 特征 | 403 |
| 9 | 爬虫/扫描器UA | 爬虫 UA 特征 | 403 |
| 10 | 路径/UA规则deny | 外挂规则 deny | 403 |
| 0 | 其他 | ★ 仅 ip_blacklist 表语境（黑名单条目来源兜底，非拦截识别类别）；shield_event 拦截事件永远只写 1-10 | — |
| 11 | 人工收录 | ★ 仅 ip_blacklist 表语境（管理员人工录入/批量导入/从文件同步的拉黑条目）；shield_event 拦截事件永远只写 1-10 | — |

★ 语境分离：`block_type=0` 在拦截明细**查询参数**语境 =「全部」（现有行为不变）；在黑名单条目**存储**语境 =「其他」。两语境分离，查询过滤不改。

### 3.2 status_code — 拦截响应码（shield_event.status_code）

| 值 | 含义 | 对应 block_type |
|---|---|---|
| 403 | 拦截（黑名单/风险路径/遍历/SQL注入/XSS/爬虫UA/规则deny/方法不允许） | 1,3,5,6,7,8,9,10 |
| 413 | 请求体超限 | 4 |
| 429 | 限流 | 2 |

> 注意与 access_log.status_code 语义区分：access_log 存的是**上游**响应码（如 200/502），shield_event 存的是**拦截响应码**。

### 3.3 rule_hit — 命中特征名（shield_event.rule_hit）

| 特征名 | 含义 |
|---|---|
| `sql_pattern` | 命中 SQL 注入特征 |
| `xss_pattern` | 命中 XSS 特征 |
| `path_traversal` | 命中路径遍历特征 |
| `risk_path` | 命中风险路径 |
| `crawler_ua` | 命中爬虫/扫描器 UA |
| `ip_blacklist` | 命中 IP 黑名单 |
| `rate_limit` | 触发限流 |
| `method_whitelist` | 触发方法白名单 |
| `max_body_size` | 触发请求体超限 |
| `path_rule` | 命中外挂规则 deny |

### 3.4 status — 投递状态（outbox.status）

`plugins/mq/mq.go` 为权威定义（`statusPending/statusFailed/statusDone/statusDead`）。

| 值 | 含义 |
|---|---|
| `pending` | 待投递（初始状态） |
| `failed` | 投递失败（可重试，轮询仍会取到） |
| `done` | 已投递成功（不再处理） |
| `dead` | 超过重试上限转死信（不再自动投递） |

### 3.5 schedule_list.kind / last_status — 任务类型与结果状态（schedule_list）

`cmd/rocksys/schedule.go` 为权威定义（登记清单 `scheduleRows`；与建表脚本注释一一对应）。

**kind（任务类型）**

| 值 | 含义 |
|---|---|
| `configurable` | 可配型（关联 `config_key` 对应 easyconf 开关；启用状态读配置现值） |
| `system` | 系统级只读（整行只读，被改则重启按 `name` 重置；`config_key` 恒空、enabled 恒 true） |

**last_status（上次执行结果）**

> 域边界：schedule_list 是任务定义（工厂）的登记面，只记「上一轮调度执行的结果」；任务实例的运行时状态（进行中/取消/进度）属任务执行中心，本表不承载，取值全部为轮次执行结果域词汇。

| 值 | 含义 |
|---|---|
| `success` | 执行成功（本期仅 `geoip_sync` 行回写）。分趟同步「单趟到点收工、下次续接」亦记 `success`（分趟是设计内的正常节奏，非异常） |
| `failed` | 执行失败（真错误：库不可用、SQL 出错等） |
| `skipped` | 跳过（预留：该轮未执行，如前置条件不满足） |
| `partial` | 上一轮未跑完（人工经任务中心取消执行实例，或进程收尾中止）；已完成部分已写入，重复执行从断点续接。区别于 `skipped`「整轮未执行」与 `failed`「执行出错」 |
| `''`（空） | 未登记（该行无回写者，不代表从未执行） |

---

## 4. 维护约定

1. **改表结构（新增/修改字段）**：三方言建表脚本（`sql/{sqlite,postgres,mysql}/`）同步修改 →
   同步更新脚本内字段注释 → 同步更新本文档对应表；运行期 `{table}` 占位符不变。
2. **改枚举（block_type / status / rule_hit）**：先改 Go 权威定义（`block_type.go` / `mq.go`）→
   同步三方言建表脚本注释 → 同步本文档 §3。
3. **权威性与一致性**：建表脚本是运行时真实执行的 DDL（含 COMMENT），本文档是其人工维护的
   可读视图，两者须保持一致；`internal/db/db_test.go` 的 `TestScriptParity` 保证三方言文件集一致
   （不校验字段内容，字段差异靠人工与本文档核对）。
4. **类型差异要点**（避免踩坑）：
   - 自增主键：sqlite `INTEGER PRIMARY KEY AUTOINCREMENT` / pg `BIGSERIAL` / mysql `BIGINT AUTO_INCREMENT`；
   - 时间：sqlite `DATETIME`（文本）/ pg `TIMESTAMPTZ` / mysql `DATETIME(3)`（毫秒）；
   - 字符串长度：sqlite/pg 用无长度 `TEXT`，mysql 需显式 `VARCHAR(n)`（长度见各表）；
   - 默认值差异：mysql 部分字段无默认值（见 §2 各表方言差异备注），跨库迁移时注意。

---

## 5. 表结构同步（服务 → 数据库 · 表结构页）

存量库的列级演进无需手工 ALTER：管理控制台「服务 → 数据库 → 表结构」页对期望与实际结构做比对，差异按 A-F 分级，自动项（缺表/缺普通列/缺索引/E 级类型·非空·默认值不一致——E 级仅 mysql/pg 目标自动生成 ALTER，SQLite 需重建表保持人工）生成同步 SQL 经 danger 强确认后逐条执行（端点契约见 `docs/api/database.md`；实现 `internal/db/schema_parse.go` / `schema_catalog.go` / `schema_diff.go` + `internal/adminapi/dbschema.go`）。

- **期望结构权威来源 = 运行期 SQLSource**：即本文档所述 `sql/<dbtype>/` 建表/建索引脚本（外挂 `HOT_SCRIPTS_DIR/sql/` 优先、编译期内嵌兜底），与各挂件实际建表同源；外挂覆写过 sql/ 的部署，检查口径自动跟随，不使用编译期内嵌目录直读。
- **实际结构 = 当前数据连接 catalog**：查询语句为 `sql/<dbtype>/schema_query_{columns,indexes,tables}.sql`（三方言各三份，`{table}` 占位符，支持外挂覆写，与其他 SQL 脚本同生命周期）。
- **表清单在装配处注册**：7 张表的 `表名 ↔ 建表脚本` 对应关系在 `cmd/rocksys/main.go` 装配处（`buildTableSpecs`）注册为唯一事实来源——表名无法从脚本文件名推断（`mq_create_table.sql` 实际表名 `outbox`），一致性由 `TestTableSpecsMatchScripts` 单测防漏防漂移。
- **表名口径**：`shield_event`/`access_log` 均为**固定表名**（不开放配置——配置面徒增 schema 同步与测试负担，无业务收益）；脚本经 `{table}`/`{table2}` 占位符替换注入固定表名，表清单注册与 catalog 查询口径自动一致。
