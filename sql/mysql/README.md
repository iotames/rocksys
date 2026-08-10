# sql/mysql — MySQL 方言 SQL 脚本目录

> **铁律**：① SQL 必须落盘 `sql/<dbtype>/`，禁止 Go 代码内联；② 换库只改 `.env`（`DB_DRIVER`/`DB_DSN`）；③ 纯 SQL 参数化，不用 ORM/对象模型；④ 本目录与 `sql/sqlite/`、`sql/postgres/` 三方言齐平，缺脚本即报错。

本目录存放 MySQL 方言的数据库查询脚本。加载规则（见 `internal/hotswap/script.go`）：

1. 运行时优先加载外置目录（`HOT_SCRIPTS_DIR/sql/`，默认 `hotscripts/sql/`）中对应的脚本文件，可热修改无需重新编译；
2. 找不到脚本（或内容为空）时，回退到编译期嵌入二进制的本目录文件；
3. 切换数据库驱动为 `mysql` 时，如果某条查询在 `sql/mysql/` 下找不到脚本，系统直接报错。

## 占位符约定

- 参数化查询占位符：`?`（MySQL 不使用 `$1`）
- 表名/库名等动态标识符：`{xxx}`（运行时由组件替换，禁止来自外部用户输入）

## 脚本清单

三方言文件集完全一致（`internal/db/db_test.go` 的 `TestScriptParity` 强制校验，缺脚本即报错）：

| 脚本 | 说明 | 与 SQLite 方言差异 |
|---|---|---|
| mq_create_table.sql | outbox 建表 | `BIGINT AUTO_INCREMENT PRIMARY KEY`；`last_error` 用 `VARCHAR`（MySQL 8.0.13 以下 TEXT 列无 DEFAULT） |
| mq_create_index.sql | outbox status 索引 | MySQL `CREATE INDEX` 不支持 `IF NOT EXISTS`，重复执行报 "Duplicate key name"——mq/obs 组件对索引创建做幂等容错（该错误忽略） |
| mq_insert.sql | 插入 pending 消息 | 占位符 `?` 语法一致 |
| mq_insert_returning_id.sql | 自增 id 回读脚本 | 仅驱动不支持 `LastInsertId` 时使用；go-sql-driver/mysql 支持 `LastInsertId`，本文件仅为三方言文件集齐平保留 |
| mq_fetch_pending.sql | 取待投递消息 | `LIMIT ?` 语法一致 |
| mq_mark_done/failed/dead.sql | 标记投递结果 | `CASE WHEN` 各数据库均支持 |
| mq_get_retry_count.sql | 查询重试次数 | 语法一致 |
| access_log_create_table.sql | 访问日志建表 | `BIGINT AUTO_INCREMENT`；`extra TEXT NOT NULL` |
| access_log_create_index.sql | 访问日志索引 | MySQL `CREATE INDEX` 不支持 `IF NOT EXISTS`（组件幂等容错） |
| access_log_insert.sql | 插入访问日志 | 占位符 `?` 语法一致 |
| access_log_query.sql | 查询访问日志 | 模糊匹配用 `CONCAT('%', ?, '%')`（MySQL 的 `||` 默认是 OR 语义） |
| access_log_size.sql | 表+索引占用字节 | 查 `information_schema.tables` |
| admin_users_create_table.sql | 管理接口超管表 | `BIGINT AUTO_INCREMENT`；`VARCHAR` 时间列 |
| admin_users_count/get/get_by_username/update/insert.sql | 超管增删改查 | 占位符 `?` 语法一致 |

在 `.env` 中设置 `DB_DRIVER=mysql` 与 `DB_DSN` 即可启用（`cmd/rocksys` 已注册 go-sql-driver/mysql）。

> MySQL 方言已用真实实例验证（MariaDB 10.6，`internal/db/mysql_integration_test.go`，`MYSQL_TEST_DSN` 环境变量触发）：
> 三组脚本全流程 + 死信语义（`mq_mark_failed.sql` 的 status 先于 retry_count 赋值，避免 MySQL SET 左→右求值导致死信提前一拍）+ 索引 `Duplicate key name` 幂等容错 + `information_schema` 表大小查询均实测通过。
