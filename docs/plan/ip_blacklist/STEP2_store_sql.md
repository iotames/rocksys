# A2 · store 层：warn_times 读写 / 续封转永久 / 排序 {order} / 小黑屋 SQL

> 实施状态：**已实施**
> 前置：A1 已实施。设计依据：决策 3/7/8/10/12/13/14、§3.1/§3.4/§3.7/§3.8。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| plugins/shield/ip_list_store.go | 修改 | 全部 SQL 覆盖 warn_times + 辅助方法 + 排序白名单 |
| plugins/shield/ip_list_mgmt.go | 修改 | AddIPList/ImportIPList 缺省 11、ban 三态辅助封装 |
| sql 三方言 ip_blacklist_{insert,import,query_list,update,restore}.sql | 修改 ×15 | 列覆盖 + {order} 占位符 |
| sql 三方言 ip_blacklist_{query_jail,jail_count}.sql | 新增 ×6 | 小黑屋查询 |

## 3. 实施步骤

- [x] insert / import：覆盖 warn_times（显式参数，初始 0/1 由调用方语义定）
- [x] query_list：SELECT 补 warn_times；`ORDER BY id DESC` → `ORDER BY {order}`；Go 侧 sort 白名单映射（默认 `id DESC`；hit_count / warn_times / created_at / expires_at / updated_at / block_type，均固定 DESC；**字符串字段 ip/title 不提供**；非法/缺省回默认）。注意：**`{order}` 替换没有通用框架**——现有 `{table}` 是各组件自行 `strings.ReplaceAll`（internal/db 无占位符机制），在 store 的 `sqlText` 处仿照追加 `{order}` 替换即可
- [x] 辅助方法（命名实施时可调，语义为准）：
  - [x] `BanInsert`：封禁入库（warn_times=1 起算）
  - [x] `RestoreBan`：软删/过期恢复封禁——清 deleted_at、expires_at 按调用方时长（人工=所选 / 自动=TTL×10）、warn_times+1；**+1 后 ≥5 且为限时 → 转永久（expires_at 置 NULL + title 追加「（累计封禁达 5 次转永久）」）**，返回是否转永久供端点提示
- [x] restore.sql 保持纯恢复语义不变（管理面「恢复」按钮行为不受封禁语义影响；RestoreBan 用新语句）
- [x] 小黑屋 SQL 三方言：query_jail（`expires_at IS NOT NULL AND expires_at > now AND deleted_at IS NULL`，`ORDER BY expires_at ASC LIMIT ?`）+ jail_count（同条件 COUNT）
- [x] 校验：黑名单侧 block_type 放宽 0-11、缺省 1 → 11（AddIPList/ImportIPList/管理过滤参数均同步）；`BlockType.Valid()`（1-10）保持不动、继续供拦截事件语境使用；`listIPList` 现校验 `0..blockTypeCount` 且报错文案「block_type 应为 0-10 的整数」，放宽时一并改写；白名单侧不变
- [x] 单测：warn_times 读写 / RestoreBan 三分支（普通恢复·续封·满 5 转永久）/ 排序白名单与非法回退 / jail 条件与升序 / 校验边界 0/11/12
- [x] `go test ./plugins/shield/` 与 `go vet ./plugins/shield/` 通过

## 4. 验证

- [ ] `go test ./plugins/shield/ -run 'TestIPList|TestJail' -v` 全绿；vet 通过

## 5. 完成标准

清单全勾 + 验证全过 → 状态「已实施」→ 更新总纲。

## 6. 实施回填区（中断现场记录）

2026-08-30 实施完成：

- **SQL 脚本（三方言，本步骤工作区内先行就位，本次核验语义一致后接线）**：ip_blacklist_{insert,insert_returning_id,import,query_list,update}.sql 覆盖 warn_times、query_list 改 `ORDER BY {order}`；新增 ip_blacklist_{query_jail,jail_count,restore_ban,get}.sql（get 按精确 ip 取全状态行，供封禁三态判定）；restore.sql 未动。
- **Go 侧改动**：
  - `ip_list_store.go`：`sqlText`/`sqlTextOrder` 统一替换 `{table}`/`{order}`；`blacklistSortWhitelist` 白名单映射（7 键固定 X DESC，非法/缺省回 `id DESC`）；`Insert` 拆出 `insertWarnTimes`（普通录入/导入 warn_times=0）；新增 `BanInsert`（warn_times=1）、`GetByIP`（返回 `BanEntry`，`Deleted` 判定须直接判 NULL，勿经 eventToString——nil 归一为 "<nil>" 会误判已删）、`RestoreBan`（清 deleted_at + warn_times+1，≥5 且限时转永久：expires_at 置 NULL + title 追加标记，防重复追加）、`Jail`（limit 默认 20 上限 100，附 total）；`ListFilter` 增 `Sort` 字段；`normalizeListRow` 补 warn_times。
  - `shield.go`：`ipListStore` 接口补 BanInsert/GetByIP/RestoreBan/Jail 四方法（countingStore 经内嵌 `*IPListStore` 自动满足）。
  - `ip_list_mgmt.go`：新增 `validBlackBlockType`（0-11）、`BanIP` 三态封装（无记录→BanInsert / 活跃→跳过 / 软删过期→RestoreBan，返回 written/perm）、`banEntryActive`；AddIPList/ImportIPList 黑名单侧校验 0-11（白名单不变）。
  - `admin.go`：listIPList 过滤校验放宽 0-11（文案同步）；addIPList 缺省 block_type=0 → 11；importIPList 缺省 1 → 11、显式 0-11。
- **测试**：新增 `plugins/shield/ip_list_ban_test.go`（warn_times 读写与管理面编辑不改、RestoreBan 三分支+永久续封防重复追加、排序白名单映射与注入串回退、jail 条件/升序/limit、BanIP 三态、校验边界 0/11/12/白名单忽略）。
- **偏差**：①"缺省 1 → 11"落在 admin 端点层（addIPList body 缺省 0→11、importIPList 缺省 1→11），mgmt 层不做 0→11 重解释以保留显式 0=其他；②排序端点接线（sort query 参数解析）留 A3，本步仅完成 ListFilter/白名单映射能力。
- **验证**：`go test ./...` + `go vet ./...` 全量通过；带 PG_TEST_DSN/MYSQL_TEST_DSN 的 internal/db 与 plugins/shield 集成测试通过。
