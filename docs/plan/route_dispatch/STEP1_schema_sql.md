# STEP1：数据层六表（三方言 SQL + 数据字典）

状态：待实施

## 目标

落地路由分发六张表的建表与 CRUD SQL 脚本（sqlite/postgres/mysql 三方言）及 `docs/DATA_DICT.md` 六表与三枚举章节，为下游领域模型、Rebuild 与 adminapi 端点提供数据地基。表结构与字段口径唯一依据 DESIGN_PLAN「数据结构」章节（六表：dispatch_rule / dispatch_upstream / dispatch_node / dispatch_upstream_node / dispatch_tag / dispatch_rule_tag；三枚举：path_type、algo、priority）。

## 改动文件清单

- 新增 `sql/sqlite/dispatch_*.sql`、`sql/postgres/dispatch_*.sql`、`sql/mysql/dispatch_*.sql`（文件组见下）
- 修改 `docs/DATA_DICT.md`：新增六表字段章节与三枚举映射（与建表脚本注释一一对应）

## 实施步骤（完成一项立即勾选保存；粒度到不可再分的动作）

- [ ] 逐表编写 sqlite 方言脚本（对照 DESIGN_PLAN 数据结构章节字段表，软删「除外」按 MySQL/SQLite 复合唯一键口径）：
  - dispatch_node：create_table / create_index / insert_returning_id / update / soft_delete / restore / query_list / query_all_active / count
  - dispatch_upstream：同 node 动作组
  - dispatch_upstream_node：create_table / create_index / insert_returning_id / soft_delete / query_list / query_all_active（关系行整组替换语义，不设单行 update）
  - dispatch_rule：create_table / create_index / insert_returning_id / update / soft_delete / restore / query_list / query_active / count
  - dispatch_tag：create_table / create_index / insert_returning_id / update / soft_delete / query_all
  - dispatch_rule_tag：create_table / create_index / insert_returning_id / soft_delete / query_list
- [ ] postgres 方言全套（唯一约束经部分唯一索引 `WHERE deleted_at IS NULL`；时间类型 TIMESTAMPTZ；id BIGSERIAL）
- [ ] mysql 方言全套（复合唯一键 `(列, deleted_at)` 借 NULL 不判重；时间 DATETIME(3)；id BIGINT AUTO_INCREMENT）
- [ ] 三方言脚本头注释与 DATA_DICT 说明一一对应（表/字段标题同源）
- [ ] `docs/DATA_DICT.md` 新增六表章节（字段名/标题/说明/三方言类型对照）与三枚举章节（path_type 1/2/3、algo 1/2、priority 0/1）

## 验证

- 接手核实命令（拆步时预写；无副作用、可重复执行；断点后一条命令自证已完成部分完好）：
  `ls sql/sqlite/dispatch_*.sql sql/postgres/dispatch_*.sql sql/mysql/dispatch_*.sql | wc -l && go test ./internal/db/...`
  （预期：三方言 dispatch 脚本齐全（每方言 38 个左右，按实际文件组计数核对）；既有 db 包测试不因脚本引入而破坏）
- 本步完整验证（勾选＝该项已真实执行且通过，凭意图不得勾选；按需含受影响面的全量回归）：
  - [ ] `go test ./internal/db/...` 全绿
  - [ ] sqlite 实建验证：临时库逐表执行 create_table/create_index 无错（经 `go run` 临时程序或 sqlite3 CLI，用后即删临时文件）
  - [ ] WebUI 数据库页表结构检查六表零差异（`go build -tags dev` 后 `cd bin && ./rocksys`，浏览器实看截图留证）

## 完成标准

- 六表 × 三方言脚本齐备，字段/默认值/索引/唯一约束与 DESIGN_PLAN 数据结构章节一致（含「软删行除外」三方言口径差异）
- DATA_DICT 六表 + 三枚举与脚本注释一一对应（数据字典红线三处同步中的两处：脚本 + 字典；Go 枚举归 STEP2/5）
- 既有全量测试不受影响

## 实施回填区

### 产物锚点清单（拆步时预写，实施中随手更正；改名同步更新）
- 新增文件：`sql/{sqlite,postgres,mysql}/dispatch_rule_*.sql`、`dispatch_upstream_*.sql`、`dispatch_node_*.sql`、`dispatch_upstream_node_*.sql`、`dispatch_tag_*.sql`、`dispatch_rule_tag_*.sql`
- 修改文件：`docs/DATA_DICT.md`
- 新增测试：无（本步纯 SQL 与文档；执行闭环由 dbschema 与 STEP6 端点测试消费）

### 偏差与现场记录
- <设计与现实的偏差、试过放弃的方案、阻塞原因——先写这里再继续动手>
