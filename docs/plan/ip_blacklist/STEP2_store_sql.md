# A2 · store 层：warn_times 读写 / 续封转永久 / 排序 {order} / 小黑屋 SQL

> 实施状态：**待实施**
> 前置：A1 已实施。设计依据：决策 3/7/8/10/12/13/14、§3.1/§3.4/§3.7/§3.8。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| plugins/shield/ip_list_store.go | 修改 | 全部 SQL 覆盖 warn_times + 辅助方法 + 排序白名单 |
| plugins/shield/ip_list_mgmt.go | 修改 | AddIPList/ImportIPList 缺省 11、ban 三态辅助封装 |
| sql 三方言 ip_blacklist_{insert,import,query_list,update,restore}.sql | 修改 ×15 | 列覆盖 + {order} 占位符 |
| sql 三方言 ip_blacklist_{query_jail,jail_count}.sql | 新增 ×6 | 小黑屋查询 |

## 3. 实施步骤

- [ ] insert / import：覆盖 warn_times（显式参数，初始 0/1 由调用方语义定）
- [ ] query_list：SELECT 补 warn_times；`ORDER BY id DESC` → `ORDER BY {order}`；Go 侧 sort 白名单映射（默认 `id DESC`；hit_count / warn_times / created_at / expires_at / updated_at / block_type，均固定 DESC；**字符串字段 ip/title 不提供**；非法/缺省回默认）。注意：**`{order}` 替换没有通用框架**——现有 `{table}` 是各组件自行 `strings.ReplaceAll`（internal/db 无占位符机制），在 store 的 `sqlText` 处仿照追加 `{order}` 替换即可
- [ ] 辅助方法（命名实施时可调，语义为准）：
  - [ ] `BanInsert`：封禁入库（warn_times=1 起算）
  - [ ] `RestoreBan`：软删/过期恢复封禁——清 deleted_at、expires_at 按调用方时长（人工=所选 / 自动=TTL×10）、warn_times+1；**+1 后 ≥5 且为限时 → 转永久（expires_at 置 NULL + title 追加「（累计封禁达 5 次转永久）」）**，返回是否转永久供端点提示
- [ ] restore.sql 保持纯恢复语义不变（管理面「恢复」按钮行为不受封禁语义影响；RestoreBan 用新语句）
- [ ] 小黑屋 SQL 三方言：query_jail（`expires_at IS NOT NULL AND expires_at > now AND deleted_at IS NULL`，`ORDER BY expires_at ASC LIMIT ?`）+ jail_count（同条件 COUNT）
- [ ] 校验：黑名单侧 block_type 放宽 0-11、缺省 1 → 11（AddIPList/ImportIPList/管理过滤参数均同步）；`BlockType.Valid()`（1-10）保持不动、继续供拦截事件语境使用；`listIPList` 现校验 `0..blockTypeCount` 且报错文案「block_type 应为 0-10 的整数」，放宽时一并改写；白名单侧不变
- [ ] 单测：warn_times 读写 / RestoreBan 三分支（普通恢复·续封·满 5 转永久）/ 排序白名单与非法回退 / jail 条件与升序 / 校验边界 0/11/12
- [ ] `go test ./plugins/shield/` 与 `go vet ./plugins/shield/` 通过

## 4. 验证

- [ ] `go test ./plugins/shield/ -run 'TestIPList|TestJail' -v` 全绿；vet 通过

## 5. 完成标准

清单全勾 + 验证全过 → 状态「已实施」→ 更新总纲。

## 6. 实施回填区（中断现场记录）

（空）
