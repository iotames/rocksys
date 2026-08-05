# sql/postgres — PostgreSQL 方言 SQL 脚本目录

> **铁律**：① SQL 必须落盘 `sql/<dbtype>/`，禁止 Go 代码内联；② 换库只改 `.env`（`DB_DRIVER`/`DB_DSN`）；③ 纯 SQL 参数化，不用 ORM/对象模型；④ 本目录与 `sql/sqlite/`、`sql/mysql/` 三方言齐平，缺脚本即报错。

本目录存放 PostgreSQL 方言的数据库查询脚本。加载规则（见 `internal/hotswap/script.go`）：

1. 运行时优先加载外置目录（`SQL_DIR`，默认 `sql/`）中对应的脚本文件，可热修改无需重新编译；
2. 找不到脚本（或内容为空）时，回退到编译期嵌入二进制的本目录文件；
3. 切换数据库驱动为 `postgres` 时，如果某条查询在 `sql/postgres/` 下找不到脚本，系统直接报错。

## 占位符约定

- 参数化查询占位符：`$1, $2, ...`（PostgreSQL 不使用 `?`）
- 表名/库名等动态标识符：`{xxx}`（运行时由组件替换，禁止来自外部用户输入）

## 脚本清单

三方言文件集完全一致（`internal/db/db_test.go` 的 `TestScriptParity` 强制校验，缺脚本即报错）：

| 脚本 | 说明 | 与 SQLite 方言差异 |
|---|---|---|
| mq_create_table.sql | outbox 建表 | `BIGSERIAL PRIMARY KEY`；`?` → `$1` |
| mq_create_index.sql | outbox status 索引 | `CREATE INDEX IF NOT EXISTS` 语法一致 |
| mq_insert.sql | 插入 pending 消息 | 占位符 `$1, $2, $3` |
| mq_insert_returning_id.sql | 自增 id 回读 | `INSERT ... RETURNING id`——lib/pq 不支持 `Result.LastInsertId`，`OutboxStore.Insert` 对本方言专用此脚本 |
| mq_fetch_pending.sql | 取待投递消息 | `LIMIT $1` |
| mq_mark_done/failed/dead.sql | 标记投递结果 | 占位符 `$n`；`CASE WHEN` 语法一致 |
| mq_get_retry_count.sql | 查询重试次数 | 占位符 `$1` |
| access_log_create_table.sql | 访问日志建表 | `BIGSERIAL PRIMARY KEY`；`TEXT` 列 |
| access_log_create_index.sql | 访问日志索引 | `CREATE INDEX IF NOT EXISTS` 语法一致 |
| access_log_insert.sql | 插入访问日志 | 占位符 `$1 ... $14` |
| access_log_query.sql | 查询访问日志 | 占位符 `$1 ... $9`；`'%' || $n || '%'`（`||` 连接符 PostgreSQL 原生支持） |
| access_log_size.sql | 表+索引占用字节 | `pg_total_relation_size`（含 TOAST） |
| admin_users_create_table.sql | 管理接口超管表 | `BIGSERIAL PRIMARY KEY` |
| admin_users_count/get/get_by_username/update/insert.sql | 超管增删改查 | 占位符 `$n` |

> PostgreSQL 方言已用真实实例验证（`internal/db/pg_integration_test.go`，`PG_TEST_DSN` 环境变量触发）。
> 在 `.env` 中设置 `DB_DRIVER=postgres` 与 `DB_DSN` 即可启用（`cmd/rocksys` 已注册 lib/pq）。
