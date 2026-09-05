# DATA_DICT — RockSys 数据字典

> 数据层工程化说明：本项目 3 种 SQL 方言（SQLite / PostgreSQL / MySQL）数据类型各不相同，
> 本字典给出每张表的字段名、标题、说明、可能值示例与三方言类型对照。
> **权威数据源为 `sql/<dbtype>/` 建表脚本（含字段注释）**，本文档是其可读性视图，改动须同步。

---

## 0. 总览

- 统一数据访问层：`internal/db`（`DB_DRIVER`/`DB_DSN` 配置，见 `docs/CONFIGURATION.md`）；
- SQL 脚本三方言齐平：`sql/sqlite/`、`sql/postgres/`、`sql/mysql/`（`internal/db/db_test.go` 的 `TestScriptParity` 强制校验文件集一致）；
- 表名/库名等动态标识符用 `{table}` 占位符（运行时由组件替换，**禁止来自外部用户输入**）；
- 全项目共 **7 张业务表**，分属 4 个组件：
  | 表名 | 归属组件 | 条件装配 |
  |---|---|---|
  | `shield_event` | shield（WAF 防护） | 恒建（DB 就绪即建） |
  | `access_log` | obs（访问日志/指标） | 恒建（DB 就绪即建） |
  | `admin_users` | adminapi（管理接口鉴权） | 恒建（DB 就绪即建） |
  | `outbox` | mq（异步消息） | `MQ_ENABLED=true` 才建 |
  | `ip_blacklist` | shield（WAF 防护） | 恒建（DB 就绪即建） |
  | `ip_whitelist` | shield（WAF 防护） | 恒建（DB 就绪即建） |
  | `attack_archive` | shield（WAF 防护） | 恒建（DB 就绪即建） |

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
| `ip_whitelist` | 动态 IP 白名单表 | shield | 管理面录入的白名单条目（与 `.env` 配置 `SHIELD_IP_WHITELIST` 取并集；白名单优先于黑名单） | `sql/{sqlite,postgres,mysql}/ip_whitelist_create_table.sql` |
| `attack_archive` | 攻击证据归档表 | shield | 攻击证据归档（本期仅建表，归档逻辑见 WAF 方案 §8） | `sql/{sqlite,postgres,mysql}/attack_archive_create_table.sql` |

---

## 2. 字段字典

> 类型列按 `sqlite / postgres / mysql` 顺序标注；「默认」为空表示无默认值（NOT NULL）。
> 「可能值示例」为真实场景取值样例。

### 2.1 shield_event — WAF 拦截事件明细表（14 列）

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
| `user_agent` | 客户端标识 | 客户端 User-Agent（爬虫识别依据） | `Mozilla/5.0 (compatible; Googlebot/2.1)` | TEXT / TEXT / VARCHAR(512) | `''` |
| `host` | 请求主机 | 请求 Host | `127.0.0.1:8080` | TEXT / TEXT / VARCHAR(255) | `''` |
| `status_code` | 拦截响应码 | 拦截响应码（403/413/429，见 §3.2） | `403` | INTEGER / INT / INT | `0` |
| `rule_hit` | 命中规则 | 命中的规则/特征名（见 §3.3） | `sql_pattern` | TEXT / TEXT / VARCHAR(255) | `''` |
| `req_bytes` | 请求体大小 | 请求体字节数（Content-Length） | `0`、`1024` | INTEGER / BIGINT / BIGINT | `0` |
| `extra` | 扩展字段 | 扩展字段（referer / x_forwarded_for 等，JSON） | `{"referer":"http://x","x_forwarded_for":"1.2.3.4"}` | TEXT / TEXT / TEXT | `'{}'` |

> ⚠ 方言差异备注：`path`、`extra` 在 sqlite/postgres 有默认值（`''`/`'{}'`），MySQL 无默认值（NOT NULL，写入必须显式给值）。

### 2.2 access_log — 访问日志表（16 列）

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
| `hit_count` | 命中计数 | 命中拦截计数（异步累加，观测/排序用） | `12` | INTEGER / INT / INT | `0` |
| `warn_times` | 封禁次数 | 该 IP 被人工/风控封禁的累计次数（人工封禁与自动拉黑共用计数；限时封禁累计达 5 次自动转永久） | `3` | INTEGER / INT / INTEGER | `0` |
| `expires_at` | 过期时间 | 过期时间（UTC）；NULL=永久，过期条目不参与匹配 | `2026-09-01T00:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `deleted_at` | 软删除时间 | 软删除时间（UTC）；非 NULL 视为已删除，不参与匹配 | `2026-08-21T10:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `created_at` | 创建时间 | 创建时间（UTC） | `2026-08-20T12:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |
| `updated_at` | 更新时间 | 最后更新时间（UTC） | `2026-08-20T12:00:00Z` | DATETIME / TIMESTAMPTZ / DATETIME(3) | — |

### 2.6 ip_whitelist — 动态 IP 白名单表（6 列）

**说明**：管理面录入的动态白名单（持久化权威），与 `.env` 配置 `SHIELD_IP_WHITELIST` 取**并集**；
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

---

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

存量库的列级演进无需手工 ALTER：管理控制台「服务 → 数据库 → 表结构」页对期望与实际结构做比对，差异按 A-F 分级，自动项（缺表/缺普通列/缺索引）生成同步 SQL 经 danger 强确认后逐条执行（端点契约见 `docs/webui-api.md` §3.19；实现 `internal/db/schema_parse.go` / `schema_catalog.go` / `schema_diff.go` + `internal/adminapi/dbschema.go`）。

- **期望结构权威来源 = 运行期 SQLSource**：即本文档所述 `sql/<dbtype>/` 建表/建索引脚本（外挂 `HOT_SCRIPTS_DIR/sql/` 优先、编译期内嵌兜底），与各挂件实际建表同源；外挂覆写过 sql/ 的部署，检查口径自动跟随，不使用编译期内嵌目录直读。
- **实际结构 = 当前数据连接 catalog**：查询语句为 `sql/<dbtype>/schema_query_{columns,indexes,tables}.sql`（三方言各三份，`{table}` 占位符，支持外挂覆写，与其他 SQL 脚本同生命周期）。
- **表清单在装配处注册**：7 张表的 `表名 ↔ 建表脚本` 对应关系在 `cmd/rocksys/main.go` 装配处（`buildTableSpecs`）注册为唯一事实来源——表名无法从脚本文件名推断（`mq_create_table.sql` 实际表名 `outbox`），一致性由 `TestTableSpecsMatchScripts` 单测防漏防漂移。
- **`SHIELD_EVENT_TABLE` 口径**：`shield_event` 表名是可配置项（重启生效），表清单注册的是**运行期配置实值**——若自定义了表名，检查与同步均按实值进行；catalog 查询经同一 `{table}` 占位符替换，两边口径自动一致。
