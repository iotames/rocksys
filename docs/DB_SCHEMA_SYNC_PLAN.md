# 数据库表结构同步功能方案：表结构比对 · 差异展示 · SQL 生成与执行（计划文档）

> 状态：**已实施** · B1-B5 全部完成（含文档同步），细节以 STEP 文档为准。
> 最后更新：2026-08-29

## 1. 背景与现状排查结论

1. **无 schema 迁移机制**：项目建表由各挂件启动时自跑幂等 `CREATE TABLE IF NOT EXISTS`（shield `ip_list_store.go`/`event_recorder.go`、obs `db_store.go`、mq `mq.go`、adminapi `userstore.go`），全新部署自动建表；但**列级演进无任何机制**——加列（如 IP 黑名单方案的 `warn_times`）后存量库不会自动变更，写库直接报错（IP_BLACKLIST_PLAN.md 评审发现的存量升级缺口，本功能即其产品化解法）。
2. **表清单（7 张，文件名 ≠ 表名）**：

   | 建表脚本（sql/\<dbtype\>/） | 实际表名 | 表名来源 |
   |---|---|---|
   | admin_users_create_table.sql | admin_users | 常量（userstore） |
   | ip_blacklist_create_table.sql | ip_blacklist | 常量（IPListStore） |
   | ip_whitelist_create_table.sql | ip_whitelist | 常量（IPListStore） |
   | attack_archive_create_table.sql | attack_archive | 常量 |
   | shield_event_create_table.sql | shield_event | ★配置项 `SHIELD_EVENT_TABLE`（可改，重启生效） |
   | access_log_create_table.sql | access_log | 常量（obs） |
   | mq_create_table.sql | **outbox** | main.go 装配字面量（文件名与表名不一致） |

   → 表名无法从脚本文件名推断，表清单必须在装配处（表名已知点）注册。
3. **期望结构的权威来源**：运行期 `SQLSource`（ScriptHub 外挂优先、内嵌兜底）——与各挂件实际建表同源；外挂覆写过 sql/ 的部署，检查口径自动跟随（不能用编译期内嵌目录直读，否则与实际建表源不一致）。
4. **前端现状**：`服务` 菜单组下只有动态服务详情页（配置/注册/存储/消息），无静态「数据库」页；`codeEditor` 组件现支持 lua/lines 两种高亮，无 sql。
5. **管理面已有数据连接**：adminapi 持 `edb`（`*easydb.EasyDb`，main.go 经 `dataDB.EasyDB()` 注入）与 `SQLSource`（统一数据访问层 `db.DB`，`DB_DRIVER`/`DB_DSN`），表结构检查与 SQL 执行都走该连接；方言经 `(*db.DB).Driver()` 已知（adminapi 侧无同名方法，需接口探测获取，先例见 `plugins/shield/ip_list_store.go`）。

## 2. 已确认决策（用户拍板）

| # | 决策点 | 结论 |
|---|--------|------|
| 1 | 差异自动生成 SQL 的范围 | **保守：安全项才生成**——缺表（建表原文）+ 缺普通列（ADD COLUMN）+ 缺索引（建索引原文）自动生成；缺 PK/UNIQUE/自增列标注"需人工"（SQLite 不可 ADD，不生成）；类型/非空/默认值不一致与库中多余对象**仅提示不生成**（SQLite 改列需重建表、DROP 危险，跨方言不可靠） |
| 2 | 「执行SQL」的安全边界 | **任意 SQL + danger 级强确认**——编辑器内容原样执行（可自由编辑、可手工写救急语句），执行前强确认弹窗（说明作用对象、DDL 不可回滚、建议先备份）；不做语句类型白名单 |

## 3. 设计方案

### 3.1 后端：表清单注册（装配处单一事实来源）

- `internal/db` 新增 `TableSpec{Table, CreateScript, IndexScript string}` 类型与导出辅助；**注册点在 `cmd/rocksys/main.go` 装配处**（各表名在那里已知：字面量/常量/`SHIELD_EVENT_TABLE` 配置实值），经 adminapi 构造注入，避免再造一份会漂移的表名映射。
- 防漏防漂移单测：比对三方言 `sql/*/` 下 `*_create_table.sql` 文件集合与注册清单一一对应（文件有而清单无 → 测试失败，提醒装配同步）。

### 3.2 后端：期望结构解析（DDL 解析器，`internal/db` 新文件）

- 输入：经 `SQLSource.SQL(name)` 读取的建表/建索引脚本文本（`{table}` 占位符替换为实际表名）。
- 解析步骤：剥离 `--` 行注释 → 提取 `CREATE TABLE (...)` 列定义块 → **括号深度感知**按逗号切分（`DECIMAL(10,2)`/`VARCHAR(255)` 内逗号不误切）→ 过滤表级约束行（`PRIMARY KEY`/`UNIQUE`/`KEY`/`INDEX`/`CONSTRAINT`/`CHECK`）→ 产出 `ColumnDef{Name, Type, NotNull, Default, Raw(原始列定义文本), IsPK, IsAutoInc}`。
- 建索引脚本解析：正则提取 `CREATE (UNIQUE )?INDEX IF NOT EXISTS <name>` 索引名集合。
- 方言标记：`IsAutoInc` 识别 `AUTOINCREMENT`(sqlite)/`AUTO_INCREMENT`(mysql)/`SERIAL`/`GENERATED ... AS IDENTITY`(pg)。

### 3.3 后端：实际结构查询（三方言新脚本，走同一 SQLSource）

| 方言 | 列结构 | 索引 |
|---|---|---|
| sqlite | `PRAGMA table_info({table})` | `PRAGMA index_list({table})` |
| mysql | `information_schema.columns`（`TABLE_SCHEMA=DATABASE()`） | `information_schema.statistics` |
| postgres | `information_schema.columns`（`current_schema()`） | `pg_indexes` |

- 脚本命名 `schema_query_columns.sql` / `schema_query_indexes.sql`，三方言各一份，`{table}` 占位符复用现有替换约定；支持外挂覆写（与全项目 SQL 同生命周期）。

### 3.4 后端：diff 分类与 SQL 生成

| 级别 | 差异类型 | 处理 | 生成内容 |
|---|---|---|---|
| A | 缺表 | 自动 | 建表脚本原文（`{table}` 替换后，多语句由执行器拆条） |
| B | 缺普通列 | 自动 | `ALTER TABLE {t} ADD COLUMN <原始列定义 Raw>`（Raw 取自方言脚本，天然方言正确） |
| C | 缺 PK/UNIQUE/自增列 | **需人工** | 不生成（SQLite 不支持 ADD 带 PK/UNIQUE 的列），差异表标注原因与建议 |
| D | 缺索引 | 自动 | 建索引脚本原文（幂等 `IF NOT EXISTS`） |
| E | 类型/非空/默认值不一致 | **仅提示** | 不生成（SQLite 改列需重建表，跨方言不可靠）；展示期望 vs 实际值 |
| F | 库中多余列/表 | **仅提示** | 不生成 DROP（危险；可能是历史遗留或有数据） |

- 归一化比较：类型串忽略大小写与空白；pg 的 `SERIAL` 列在 catalog 呈现 `integer`+`nextval` 默认值、mysql 自增在 `EXTRA`，比较时归一化对齐（仅影响 E 级判定准确性）。

### 3.5 后端：端点（`/admin/db/*`，main.go 装配路由）

- `GET /admin/db/schema`：执行检查，响应 `{driver, items:[{level:"A"-"F", auto, table, object, expected, actual, note}], sql}`——`sql` 为全部自动项（A/B/D）拼接的语句文本（各段带 `-- 表名·差异说明` 注释分隔），直接喂给前端编辑器；无差异时 `items:[]`、`sql:""`。
- `POST /admin/db/exec`：body `{sql}`；服务端**拆句器**（分号切分，感知字符串字面量与注释内的分号）逐条执行，**遇错即停**（DDL 无跨方言统一事务语义，返回已执行到的位置），响应 `{results:[{sql, ok, rows, error}], executed, failed}`。
- 文案遵循三要素：执行失败报"第 N 条执行失败：<原因>；前 N-1 条已生效；修正后可仅重发剩余语句"。

### 3.6 前端：服务 → 数据库 → 表结构

- 新静态页 `webui/assets/js/views/database.js` + 固定路由 `database`（ROUTES/pageLoaders/index.html 页容器与 script 标签），菜单入 `服务` 分组子项「数据库」（data-route="database"，风格随现有 menu-sub-item：`<span>数据库</span><i>database</i>`，现有子项无 SVG 图标）。
- 页内 `tabs` 页签：**「表结构」**（首期唯一，页面结构预留后续扩展如备份/清理，本期不做）。
- 表结构页签布局（自上而下）：
  1. 说明区：期望结构 = 当前运行 SQL 源（外挂 `HOT_SCRIPTS_DIR/sql/` 优先、内嵌兜底）；实际结构 = 当前数据连接 catalog；
  2. 操作区两按钮：**「表结构检查」**（主按钮）· **「执行SQL」**（危险色，编辑器有内容才可点）；
  3. 差异结果表（`dataTable` client 模式）：表 / 对象 / 差异类型 / 期望 / 实际 / 建议——自动项绿、需人工橙、仅提示灰；
  4. SQL 编辑器（`codeEditor` **新增 'sql' 轻量高亮**：关键字/注释/字符串，~30 行，复用现有组件接口）：检查后预填生成的 SQL，用户可自由编辑。
- 交互流：检查 → 无差异：成功 toast（自动消失）+ 空态；有差异：结果表 + SQL 预填 → 「执行SQL」→ `confirmDialog` danger 强确认（将执行 N 条语句直接作用于当前数据库，DDL 不可回滚，建议先备份）→ 逐条结果展示（表内明细，失败项红色）→ 失败常驻 toast / 成功汇总自动消失，文案引导"再次执行表结构检查复核"。
- 提示统一走 `Rock.ui.toast`（UX 红线），确认走唯一 `confirmDialog`。

## 4. 实施步骤

实施已逐一拆分为 `docs/plan/db_schema_sync/STEP1..STEP5`（B1-B5，每份含勾选清单/验证标准/实施状态/中断回填区），执行顺序与进度总纲见 `docs/plan/TODO.md`——**从总纲开始，按序全自动**；本项目先行，完成后解锁 IP 黑名单项目。本节不再重复步骤明细，与 STEP 文档冲突时：顺序以总纲为准、细节以 STEP 文档为准。

## 5. 测试与验收

- 解析器：三方言真实脚本全量 fixtures 单测（内嵌文件直接读，改脚本即改测试输入，防脱节）。
- diff 分类：期望/实际结构组合矩阵覆盖 A-F。
- 端点：sqlite 内存库（缺表：建库后 DROP；缺列：建旧版表结构；类型不一致：手工建偏差表）。
- 集成（可选，环境变量门控 `_integration_test.go`）：mysql/pg 真库冒烟，沿用现有集成测试基建。
- 浏览器回归清单见实施步骤 5。

## 6. 变更记录

- 2026-08-29 定稿：现状排查与设计（装配处表清单注册、DDL 解析器、三方言 catalog 脚本、A-F 差异分类、两端点、前端新页）已全部在正文；两项决策（保守生成范围、任意 SQL + danger 强确认）经确认拍板，开放问题清零，过程记录不再保留。
- 2026-08-29 已实施：B1-B5 全部完成（解析/比对/端点/前端页/文档同步，全量测试+vet 通过），方案与实现一致，接口契约见 `docs/webui-api.md` §3.19。
