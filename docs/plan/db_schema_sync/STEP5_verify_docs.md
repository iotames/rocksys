# B5 · 全量验证与文档同步收尾（表结构同步项目）

> 实施状态：**已实施**
> 前置：B1-B4 全部已实施。

## 3. 实施步骤

- [x] `go test ./...` 全量通过（主代理完成）
- [x] `go vet ./...` 无告警（主代理完成）
- [x] 生产构建 `go build -o bin/rocksys ./cmd/rocksys` 成功（主代理完成）
- [x] dev 构建 + 浏览器整体回归（B4 §4 清单完整过一遍）（主代理完成）
- [x] `docs/webui-api.md`：补 `GET /admin/db/schema`、`POST /admin/db/exec`（请求/响应结构/错误语义/安全提示）
- [x] `docs/webui.md`：新页面「服务 → 数据库 → 表结构」交互规范（检查/分级展示/编辑/强确认/执行/复核闭环）
- [x] `docs/DATA_DICT.md`：说明期望结构权威来源 = sql/\<dbtype\>/ 建表脚本 + 表清单在装配处注册（含 SHIELD_EVENT_TABLE 可配置口径）
- [x] `docs/PROJECT_STRUCTURE.md`：internal/db 新增 schema_parse / schema_catalog / schema_diff、adminapi dbschema、webui views/database.js
- [x] `docs/DB_SCHEMA_SYNC_PLAN.md`：状态行改「已实施」+ 变更记录收尾行
- [x] `docs/IP_BLACKLIST_PLAN.md` 存量升级小节：去掉「功能未落地则手工 ALTER 兜底」措辞（功能已落地，升级一律走表结构页）——经全文检索无该措辞，无需改动（见 §6）
- [x] `docs/plan/TODO.md`：B5 状态已实施 + 进度日志一行（留给主代理收尾，子代理按指令不动 TODO.md）

## 4. 验证

- [x] 全部命令与浏览器回归通过（主代理完成）
- [x] 文档改动逐份 diff 自查（无遗漏小节、术语与本 STEP 一致）

## 5. 完成标准

清单全勾 → 状态「已实施」→ 总纲更新 → **项目二（ip_blacklist A1）解锁，可继续**。

## 6. 实施回填区（中断现场记录）

文档同步由子代理完成，命令与浏览器验证由主代理完成。文档改动明细：

- `docs/webui-api.md`：§2 端点总览补 #41/42 两行；新增 §3.19「数据库表结构同步端点组」（两端的请求/响应结构、A-F 分级语义表、错误语义、安全提示）；变更记录补 1.6 行。
- `docs/webui.md`：§3 导航树与 §4.1 侧边栏补「数据库 database」子项；新增 §4.11「数据库（服务 → 数据库 · 表结构页签）」交互规范（检查/分级展示/编辑/强确认/执行/复核闭环，提示分级遵循 §4.10）；§6 功能覆盖清单补一行。
- `docs/DATA_DICT.md`：新增 §5「表结构同步」——期望结构权威来源 = 运行期 SQLSource、实际结构 = catalog 脚本、表清单在装配处注册、`SHIELD_EVENT_TABLE` 取实值口径。
- `docs/PROJECT_STRUCTURE.md`：§3.5 数据访问层补 `sql/<dbtype>/schema_query_*.sql` 与表结构同步链路（schema_parse / schema_catalog / schema_diff → adminapi dbschema.go → webui views/database.js，表清单 main.go 装配处注册）。
- `docs/DB_SCHEMA_SYNC_PLAN.md`：状态行改「已实施 · B1-B5 全部完成」，§6 变更记录补收尾行。
- `docs/IP_BLACKLIST_PLAN.md`：全文检索无「手工 ALTER 兜底」措辞（grep "ALTER"/"存量升级"均无命中），无需改动；「存量升级走表结构页」已在 `docs/webui.md` §6 功能覆盖清单注明。
