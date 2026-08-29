# B2 · 实际结构查询与 diff 引擎

> 实施状态：**已实施**
> 前置：B1 已实施。设计依据：`docs/DB_SCHEMA_SYNC_PLAN.md` §3.3/§3.4、决策 1（保守生成范围）。

## 1. 目标

三方言 catalog 查询脚本（实际结构）+ 期望 vs 实际的 diff 分类引擎（A-F）+ 自动项 SQL 生成。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| sql/{sqlite,mysql,postgres}/schema_query_columns.sql | 新增 ×3 | 列结构查询（{table} 占位符） |
| sql/{sqlite,mysql,postgres}/schema_query_indexes.sql | 新增 ×3 | 索引名查询 |
| sql/{sqlite,mysql,postgres}/schema_query_tables.sql | 新增 ×3 | 库内全部表名（F 级多余表检测） |
| internal/db/schema_catalog.go | 新增 | 统一 CatalogColumn 结构 + 各方言结果归一 |
| internal/db/schema_diff.go | 新增 | diff 分类（A-F）+ GenerateSQL |
| internal/db/schema_diff_test.go | 新增 | 分类矩阵单测 |

## 3. 实施步骤

- [x] columns 脚本（列名跨方言统一别名：name / type_full / is_nullable / col_default / extra）：
  - [x] sqlite：`PRAGMA table_info({table})`（name/type/notnull/dflt_value/pk 映射到统一结构）
  - [x] mysql：`information_schema.columns`（`TABLE_SCHEMA = DATABASE()`；EXTRA 含 auto_increment）
  - [x] postgres：`information_schema.columns`（`table_schema = current_schema()`；SERIAL 列呈 integer + nextval 默认值）
  - [x] 表不存在时返回空结果（不报错），供上层判定「缺表」
- [x] indexes 脚本：sqlite `PRAGMA index_list({table})`；mysql `information_schema.statistics`（DISTINCT INDEX_NAME）；pg `pg_indexes`（current_schema）
- [x] tables 脚本：sqlite `sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`；mysql/pg `information_schema.tables`（BASE TABLE）
- [x] catalog 读取经 `SQLSource`（与运行时同源、支持外挂覆写），结果映射统一结构
- [x] 归一化比较：类型串小写去空白；pg `integer`+`nextval(...)` ↔ 脚本 `SERIAL`；mysql `EXTRA=auto_increment` ↔ 自增标记
- [x] diff 分类（决策 1 保守口径）：
  - [x] A 缺表 → 自动；B 缺普通列 → 自动；C 缺 PK/UNIQUE/自增列 → 需人工（SQLite 不可 ADD，note 说明）；D 缺索引 → 自动
  - [x] E 类型/非空/默认不一致 → 仅提示（期望 vs 实际值展示）；F 多余列/多余表 → 仅提示（不生成 DROP）
- [x] `GenerateSQL(items)`：A = 建表脚本原文（{table} 已替换）；B = `ALTER TABLE {t} ADD COLUMN <Raw>`；D = 建索引脚本原文；段间 `-- {表名} · {差异说明}` 注释分隔
- [x] 单测矩阵：A/B/C/D/E/F 全级别 + 归一化用例（pg serial / mysql extra）+ sqlite_% 内部表过滤 + 生成 SQL 与 SplitStatements 兼容（可逐条执行）
- [x] `go test ./internal/db/` 与 `go vet ./internal/db/` 通过

## 4. 验证

- [x] `go test ./internal/db/ -run 'TestSchemaDiff|TestCatalog' -v` 全绿
- [x] `go vet ./internal/db/` 无告警

## 5. 完成标准

清单全勾 + 验证全过 → 状态改「已实施」→ 更新总纲。

## 6. 实施回填区（中断现场记录）

- 2026-08-29 一次完成，无中断。落地偏差（均已验证合理）：①sqlite catalog 的 `notnull` 是保留字需加引号；②`INTEGER PRIMARY KEY`（sqlite）catalog 报 notnull=0，E 级比对对主键/自增列双侧归一为非空（extra 增加 primary_key 标记）；③自增列不比对默认值（nextval/auto_increment 是实现细节）；④新增 `splitScriptStatements`（深度感知 + 语句起始关键字）兼容「分号分条」与「每行一条」两种脚本约定，`joinStatements` 把生成 SQL 规范化为每条带分号；⑤pg 列查询用 `to_regclass` 规避表不存在报错。验证：go test ./internal/db/ 全绿（含 sqlite 内存库真实脚本零差异闭环），go vet 无告警。
