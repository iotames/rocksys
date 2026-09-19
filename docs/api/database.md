# Admin API · 数据库端点组（表结构同步 / 数据源 / 迁移 / 任务中心）

### 3.19 数据库端点组 — 表结构同步 / 外部数据源 / 数据迁移 / 任务执行中心（实现 `internal/adminapi/dbschema.go`、`dsn.go`、`migrate.go`、`tasks.go`）

WebUI「服务 → 数据库 → 表结构」页数据源。期望结构 = 运行期 SQL 源（`HOT_SCRIPTS_DIR/sql/` 外挂优先、内嵌兜底，与各挂件实际建表同源）经 DDL 解析器产出；实际结构 = 当前数据连接 catalog（`sql/<dbtype>/schema_query_*.sql`，三方言）。仅 `DB_DRIVER`/`DB_DSN` 配置且表清单已装配时可用，否则 503。

| 端点 | 说明 |
|------|------|
| `GET /admin/db/schema` | 逐表比对期望与实际结构，返回差异项与自动项生成 SQL；无差异时 `items:[]`、`sql:""` |
| `POST /admin/db/exec` | body `{sql}`；拆句（分号切分，感知字符串字面量与注释内分号）逐条执行、**遇错即停**（DDL 无跨方言统一事务语义），返回已执行到的位置；进程内互斥（已有执行在途回 409） |
| `POST /admin/db/geoip_sync` | 无 body；**后台任务模式**——提交即返回 `{ok,task_id}`，同步进度/报告文本经 `GET /admin/tasks/{id}` 的 `progress.text` 与 `progress.detail` 取回；**手动同步不受 `GEOIP_ENABLED` 限制**（特殊场景的异步 DB 维护任务，功能关闭时允许单独操作补齐 `geoip_list`），仅 mmdb 缺失/未加载回 503（响应文本给出路）；已有任务在跑回 409 |

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
| B | 缺普通列 | 自动：生成 `ALTER TABLE … ADD COLUMN`（列定义取脚本原文，天然方言正确）；NOT NULL 且无 DEFAULT 的列按类型补安全默认值（数值列 `DEFAULT 0`、字符串列 `DEFAULT ''`——裸 ADD 在有数据的表上 SQLite 报错、PG 回填 NULL 违反非空） |
| C | 缺 PK/UNIQUE/自增列，或 NOT NULL 无默认值的时间列 | 需人工：不生成（SQLite 不支持 ADD 带 PK/UNIQUE 的列；时间列无跨方言安全字面量），`note` 说明原因与建议 |
| D | 缺索引 | 自动：仅生成缺失索引的单条 CREATE INDEX（不整份重放） |
| E | 类型/非空/默认值不一致 | 自动生成 ALTER 对齐（mysql=MODIFY 整列重述 / pg=TYPE+NOT NULL+DEFAULT 三子句；生成仅预览，执行仍需确认）；SQLite 目标不支持改列 → 仅提示需人工 |
| F | 库中多余列/表 | 仅提示：不生成 DROP（危险，可能是历史遗留或有数据） |

**`POST /admin/db/exec` 请求体**：

**后台执行**：请求体加 `"background": true` 时提交任务执行中心后台执行（立即返回 `{ok,task_id,total}`），逐条结果与失败位置经 `GET /admin/tasks/{id}` 的 `progress.detail`（元素 `{seq,sql,ok,rows,cost_ms,error}`）取回；`sql_exec_log` 照常留痕（`source=webui-background`）。同步路径行为与审计口径不变。

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

**审计留痕**：`POST /admin/db/exec` 每条语句执行后同步落 `sql_exec_log` 表（每条语句一行；
`batch_id` 归组一次提交、`seq` 批内序号；失败语句也落行，后续未执行语句不落表——无记录＝未执行；
字段见 `docs/DATA_DICT.md` §2.8）。审计落库失败仅记服务端告警日志，不影响执行结果返回。

**`GET /admin/db/execlog` 查询参数**：`limit`（默认/上限 50）、`offset`（默认 0）。

**响应 200**：

```json
{
  "items": [{
    "id": 1, "time": "2026-09-05T15:00:00Z", "batch_id": "cc5b703beb69b01ec627c84c5d59635e",
    "seq": 1, "sql_text": "ALTER TABLE ip_blacklist ADD COLUMN …", "ok": true,
    "rows_affected": 0, "error": "", "duration_ms": 2, "client_ip": "127.0.0.1", "source": "webui"
  }],
  "total": 1
}
```

- `503`：数据连接未装配；`500`：查询或计数失败（响应文本含原因）。

**`GET /admin/db/size` 响应 200**（只读统计，不落库；逐表占用只取缓存，毫秒级返回）：

```json
{
  "driver": "sqlite", "total_bytes": 27541504,
  "tables": [{"name": "access_log", "comment": "", "rows": 49187, "bytes": 6807552,
              "data_bytes": 2641920, "index_bytes": 4165632, "bytes_known": true}]
}
```

口径：`rows` 为精确值（逐表动态 `COUNT(*)`，走覆盖索引；MySQL/PG 系统表行数为估算故不采用）；
`total_bytes` 为库级总占用（SQLite `page_count×page_size` 毫秒级，含空闲页；MySQL/PG 系统表合计）；
`bytes`/`data_bytes`/`index_bytes`/`bytes_known` 为**逐表**占用（合计及其数据/索引拆分）与是否已计算——
SQLite 无逐表空间系统表，逐表占用必须走 `dbstat` 按页聚合（遍历整库页树，大库首次秒级~数十秒），
故**默认不算**（`bytes=0, bytes_known=false`，前端显示「计算」按钮），仅返回进程内缓存（10 分钟）中已有的值；
MySQL/PG 由系统表直接给出，`bytes_known` 恒为 `true`。`503` 数据连接未装配；`500` 查询失败。

**占用空间拆分口径**（`data_bytes` = 数据，`index_bytes` = 索引，`bytes` = 合计）：

| 方言 | 数据 | 索引 | 合计 | 未纳入项 |
|---|---|---|---|---|
| SQLite | 表 B-tree 页（leaf/internal，含大字段溢出页） | 该表全部索引 B-tree（含主键/唯一约束的 `sqlite_autoindex_*`）之和 | 数据 + 索引 | 库级 `freelist`（空闲页，计入 `total_bytes`） |
| MySQL/InnoDB | `DATA_LENGTH`（聚簇索引即数据本体，含主键） | `INDEX_LENGTH`（二级索引合计） | 数据 + 索引 | `DATA_FREE`（碎片/空闲页） |
| PostgreSQL | `pg_relation_size`（表堆主体） | `pg_indexes_size`（该表全部索引） | `pg_total_relation_size`（含 TOAST） | —（TOAST 计入合计，故合计可能略大于两列之和） |

**`GET /admin/db/table_size?table=<表名>` 响应 200**（单表精确占用，按需计算 + 进程内缓存 10 分钟）：

```json
{"table": "access_log", "bytes": 443244544, "data_bytes": 192868352, "index_bytes": 250376192, "cached": false}
```

`table` 须为库内实际表名（服务端按表清单白名单校验）：缺参数 `400`、表不存在/非法表名 `404`
（拒绝任意标识符拼接入 SQL）；`cached=true` 表示命中服务端缓存（表空间变化缓慢，10 分钟内复用）。
SQLite 下为**两次** `dbstat` 过滤遍历（表数据一次、该表索引合并为一条 `IN` 查询一次；实测带 `name=?`
过滤比全量 `GROUP BY name` 快约 6 倍，故不做单次全量聚合）；MySQL/PG 各为一条系统表查询。
方言口径：SQLite `dbstat` 按页聚合、MySQL `DATA_LENGTH+INDEX_LENGTH`、PG `pg_total_relation_size`。

