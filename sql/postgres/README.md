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

当前默认仅保证 SQLite（`sql/sqlite/`）脚本完整。PostgreSQL 脚本可参考 SQLite 对应文件改写：

| SQLite 文件 | 改写要点 |
|---|---|
| mq_create_table.sql | `INTEGER PRIMARY KEY AUTOINCREMENT` → `BIGSERIAL PRIMARY KEY`；`?` → `$1` |
| mq_insert.sql | 占位符全部改为 `$1, $2, ...` |
| mq_fetch_pending.sql | `LIMIT ?` → `LIMIT $1` |
| mq_mark_failed.sql | 占位符全部改为 `$1, $2, $3` |
| access_log_create_table.sql | `INTEGER PRIMARY KEY AUTOINCREMENT` → `BIGSERIAL PRIMARY KEY` |
| access_log_create_index.sql | `CREATE INDEX IF NOT EXISTS` 语法一致，可直接复用 |
| access_log_insert.sql | 占位符全部改为 `$1 ... $14` |
| access_log_query.sql | 占位符全部改为 `$1 ... $9`；`'%' || ? || '%'` → `'%' || $n || '%'`（`||` 连接符 PostgreSQL 原生支持） |

补充完成后，将 `DB_DRIVER=postgres` 即可启用。
