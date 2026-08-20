# sql/sqlite — SQLite 方言 SQL 脚本目录

> **铁律**：① SQL 必须落盘 `sql/<dbtype>/`，禁止 Go 代码内联；② 换库只改 `.env`（`DB_DRIVER`/`DB_DSN`）；③ 纯 SQL 参数化，不用 ORM/对象模型；④ 本目录与 `sql/mysql/`、`sql/postgres/` 三方言齐平，缺脚本即报错。

SQLite 为默认零配置方言（`DB_DRIVER` 默认 `sqlite`，`DB_DSN` 默认 `rocksys.db`）。
本目录是脚本对齐的基准方言：新增查询时先在 `sql/sqlite/` 落脚本，再按 `sql/mysql/README.md`、`sql/postgres/README.md` 中的差异表改写另外两份。

## 脚本清单

- `mq_*.sql`：RockMQ outbox 表（建表/索引/插入/轮询/标记结果/重试次数），8 个；
- `access_log_*.sql`：RockObs 访问日志（建表/索引/插入/查询/表大小），5 个；
- `admin_users_*.sql`：管理接口超级管理员表（建表/计数/查询/登录查询/更新/插入），6 个。

## 占位符约定

- 参数化查询占位符：`?`（SQLite 亦兼容 `$1`，但本项目统一使用 `?`）
- 表名/库名等动态标识符：`{xxx}`（运行时由组件替换，禁止来自外部用户输入）
- 自增主键：`INTEGER PRIMARY KEY AUTOINCREMENT`
- 索引幂等：`CREATE INDEX IF NOT EXISTS`
- 表大小：查询 `dbstat` 虚拟表（modernc.org/sqlite 默认启用）

## 字段注释约定

建表脚本（`*_create_table.sql`）为数据表唯一权威说明，字段必须注释（DBA/客户端只看库，不看 Go 源码）：

- **SQLite**：字段行内 `--` 注释 + 表头块注释（枚举、状态码等映射写表头，字段写行内）；
- **MySQL**：字段定义内 `COMMENT '...'` + 表尾 `COMMENT='...'`（`SHOW CREATE TABLE`/DBeaver 可见）；
- **PostgreSQL**：`CREATE TABLE` 后追加 `COMMENT ON TABLE/COLUMN` 多语句（lib/pq simple query 可一次 Exec，幂等）；
- 枚举类字段（如 `shield_event.block_type`、`mq.status`）必须把**完整取值映射**写进注释，数值稳定、只增不改；
- 新增/改动 Go 侧枚举常量时，须同步更新三方言建表脚本注释（如 `plugins/shield/block_type.go` 头部有同步提醒）。

三方言文件集完全一致，由 `internal/db/db_test.go` 的 `TestScriptParity` 强制校验。

> 字段/枚举的完整可读视图见 `docs/DATA_DICT.md`（数据字典：字段标题、说明、三方言类型对照、枚举映射）。
