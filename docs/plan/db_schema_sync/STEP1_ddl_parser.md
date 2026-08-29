# B1 · DDL 解析器与基础设施（表结构同步 · 后端地基）

> 实施状态：**已实施**（三态：待实施 / 实施中 / 已实施）
> 前置：无（本项目第一步）。设计依据：`docs/DB_SCHEMA_SYNC_PLAN.md` §3.1/§3.2。

## 1. 目标

在 `internal/db` 落地三个与数据库连接无关的纯函数能力 + `TableSpec` 类型：建表 DDL 解析（期望结构来源）、建索引脚本索引名提取、多语句拆分（执行器用），全部表驱动单测、fixtures = 三方言真实脚本。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| internal/db/schema_parse.go | 新增 | TableSpec / ColumnDef / ParseCreateTable / ParseIndexNames / SplitStatements |
| internal/db/schema_parse_test.go | 新增 | fixtures 表驱动测试 |

## 3. 实施步骤（每完成一项立即勾选保存）

- [x] 定义 `TableSpec{Table, CreateScript, IndexScript string}`（实际表名 + 建表/建索引脚本文件名，IndexScript 可空）
- [x] 定义 `ColumnDef{Name, Type string; NotNull bool; Default *string; Raw string; IsPK, IsAutoInc bool}`（Raw = 该列在脚本中的原始定义文本，供 ADD COLUMN 原样复用）
- [x] `ParseCreateTable(ddl string) ([]ColumnDef, error)`：
  - [x] 调用方先完成 `{table}` 占位符替换（沿用现有机制，解析器收到最终 DDL）
  - [x] 剥离 `--` 行注释；提取 `CREATE TABLE ... ( ... )` 列定义块
  - [x] 按逗号切分列定义，**括号深度感知**（`DECIMAL(10,2)` / `VARCHAR(255)` 内逗号不误切）
  - [x] 过滤表级约束行（`PRIMARY KEY` / `UNIQUE` / `KEY` / `INDEX` / `CONSTRAINT` / `CHECK` / `EXCLUDE` 开头）
  - [x] 逐列提取：名称（首 token）、类型（次 token 含括号部分）、`NOT NULL`、`DEFAULT` 值、`Raw` 原文；列内 `PRIMARY KEY` 识别 IsPK；自增识别 `AUTOINCREMENT`(sqlite) / `AUTO_INCREMENT`(mysql) / `SERIAL` / `GENERATED ... AS IDENTITY`(pg)
- [x] `ParseIndexNames(sql string) []string`：正则提取 `CREATE (UNIQUE )?INDEX IF NOT EXISTS <name>`
- [x] `SplitStatements(sql string) []string`：按分号切分，跳过单引号字符串与 `--` / `/* */` 注释内的分号；丢弃空白语句
- [x] 单测（fixtures 读内嵌脚本：根包 `sqlfiles`（`//go:embed all:sql`，变量名 `FS`）经 `fs.Sub` 取 sql/ 子树，或复用 `db.EmbeddedSQLSource(driver)`——`internal/db/db_test.go` 已有先例；覆盖三方言全部 `*_create_table.sql` + `*_create_index.sql`）：
  - [x] 三方言 7 份建表脚本全部解析零错误
  - [x] 列集合断言用**超集包含**（ip_blacklist 含 id/ip/title/block_type/hit_count/expires_at/deleted_at/created_at/updated_at；sqlite 的 id 列 IsPK 且 IsAutoInc）——不断言精确相等，防后续加列（如 warn_times）破坏测试
  - [x] 拆句器：字符串内分号、注释内分号不误切；多语句切分-拼接往返一致
- [x] `go test ./internal/db/` 与 `go vet ./internal/db/` 通过

## 4. 验证

- [x] `go test ./internal/db/ -run 'TestParse|TestSplit' -v` 全绿
- [x] `go vet ./internal/db/` 无告警

## 5. 完成标准

清单全勾 + 验证全过 → 状态改「已实施」→ 更新 `docs/plan/TODO.md` §1/§3。

## 6. 实施回填区（中断现场记录；接手 Agent 先读这里再对照代码核实勾选真实性）

- 2026-08-29 一次完成，无中断。偏差说明：①转义引号（'' 成对）初版状态机有 bug 已修复后全绿；②`tokenState` 扫描器同时服务 stripComments/splitTopLevel/SplitStatements，mysql 行内 COMMENT 串（含 ASCII 括号）不干扰括号深度与分号切分。验证：go test ./internal/db/ 全绿（含既有 db_test），go vet 无告警。
