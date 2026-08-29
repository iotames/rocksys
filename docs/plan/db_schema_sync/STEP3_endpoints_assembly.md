# B3 · 端点与装配（schema 检查 / SQL 执行）

> 实施状态：**已实施**
> 前置：B2 已实施。设计依据：`docs/DB_SCHEMA_SYNC_PLAN.md` §3.1/§3.5、决策 2（任意 SQL + danger 强确认）。

## 1. 目标

`GET /admin/db/schema`（比对并返回差异 + 生成 SQL）与 `POST /admin/db/exec`（拆句逐条执行、遇错即停）；main.go 装配处注册 7 表清单并挂路由。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| internal/adminapi/dbschema.go | 新增 | 两端点 handler |
| internal/adminapi/adminapi.go | 修改 | 路由注册 + 头部接口清单注释 |
| cmd/rocksys/main.go | 修改 | 表清单注册（7 TableSpec）+ 装配 |
| internal/adminapi/dbschema_test.go | 新增 | sqlite 内存库端点单测 + 表清单一致性单测 |

## 3. 实施步骤

- [x] `GET /admin/db/schema`：遍历注册表清单 → 期望（SQLSource 脚本 + B1 解析）vs 实际（B2 catalog）→ 响应 `{driver, items:[{level, auto, table, object, expected, actual, note}], sql}`；无差异 `items:[]`、`sql:""`。方言获取：adminapi 持 `edb`/`sqls`（无 `dataDB` 字段、无 `Driver()` 方法），经接口探测 `s.sqls.(interface{ Driver() string })` 取方言（先例 `plugins/shield/ip_list_store.go`）
- [x] `POST /admin/db/exec`：body `{sql}`；`SplitStatements` 逐条 `Exec`，**遇错即停**；响应 `{results:[{sql, ok, rows, error}], executed, failed}`；失败文案三要素（第 N 条失败 + 原因 + 前 N-1 条已生效、可仅重发剩余语句）
- [x] main.go 注册 7 表清单（表名来源，**已在设计期核实**）：`ip_blacklist` / `ip_whitelist`（IPListStore 构造内字面量，非导出常量）、`shield_event`（**读 SHIELD_EVENT_TABLE 配置实值**）、`access_log`、`admin_users`、`attack_archive`、`outbox`（mq_create_table.sql → outbox，文件名≠表名）。注入方式：`AdminServer` 现无表清单字段，仿 `SetSQLSource` 先例新增 setter（如 `SetTableSpecs`）由 main.go 装配注入
- [x] 一致性单测：三方言 `*_create_table.sql` 文件集合 == 注册清单（防未来新表漏注册，文件有而清单无即失败）
- [x] 端点单测（sqlite 内存库 + EmbeddedSQLSource）：无差异 / 删表后缺表 / 手工建缺列表 / 类型不一致仅提示 / 多余列仅提示 / 执行端点成功与失败中断两态
- [x] adminapi.go 头部接口注释补两端点；main.go 装配路由
- [x] `go test ./internal/...` 与 `go vet ./internal/...` 通过

## 4. 验证

- [x] `go test ./internal/adminapi/ -run 'TestDBSchema' -v` 全绿
- [x] `go vet ./internal/adminapi/ ./cmd/...` 无告警
- [x] 手工冒烟：dev 构建运行后 `curl http://127.0.0.1:19527/admin/db/schema` 返回 items（全新库应为空差异）

## 5. 完成标准

清单全勾 + 验证全过 → 状态改「已实施」→ 更新总纲。

## 6. 实施回填区（中断现场记录）

- 2026-08-29 一次完成，无中断。落地偏差：①`splitScriptStatements` 导出为 `db.SplitScriptStatements`（索引脚本「每行一条无分号」约定需要它，测试与装配共用）；②GenerateSQL 对 D 级按表去重（索引脚本一份含全部索引，逐条生成会重复建索引语句）并经 `joinStatements` 规范化每条带分号；③一致性单测落在 `cmd/rocksys/main_test.go`（清单定义在 main.go，测试须同包）；④exec 端点失败响应经 writeJSON 200 返回（结果结构化，非 http.Error 纯文本）。
- 冒烟实录：dev 构建运行，GET /admin/db/schema 正确报 outbox 缺表（A 级+建表 SQL，MQ 未启用表不存在属预期）；POST /admin/db/exec 执行 SELECT 1 返回 executed=1。注意事项：**曾有一个 19:59 启动的旧 rocksys 进程占用 19527 端口导致新实例 fail-fast、curl 打到旧进程 404**——接手 Agent 验证前先 `ps aux | grep rocksys` 清理旧实例。
