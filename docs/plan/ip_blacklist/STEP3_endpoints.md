# A3 · 端点：sync_file / ban / jail / sort

> 实施状态：**已实施**
> 前置：A2 已实施。设计依据：决策 4/10/13/14、§3.3/§3.5/§3.7/§3.8。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| plugins/shield/admin.go | 修改 | 四组端点 + 头部接口注释同步 |
| cmd/rocksys/main.go | 修改 | 路由常量装配 |
| plugins/shield/admin_blacklist_test.go | 修改/新增 | 端点单测 |

## 3. 实施步骤

- [x] `POST /admin/shield/blacklist/sync_file`（路由常量 PathBlacklistSyncFile）：
  - [x] 经 ScriptHub/ScriptDir 读 `rules/ip_blacklist.txt`（外挂优先、内嵌兜底；sub 目录 `rules`。**优先复用 Shield 现有 ruleLoader 读取路径**——`plugins/shield/rules.go` 的 `ruleSubDir` / `ruleFileIPBlacklist` 常量已封装好该文件的读取，勿另造一套）
  - [x] 复用导入解析（`#` 注释/空行忽略、逐行 validIPEntry）→ `ImportIPList(true, lines, "来自 ip_blacklist.txt 同步", BlockManual)`
  - [x] 响应 `{imported, skipped}`；文件缺失/为空 → 400 三要素文案（发生了什么/为什么/下一步）
- [x] `POST /admin/shield/blacklist/ban`（专用封禁端点，**不过载 addIPList**——保留其「已存在未生效+指引恢复」报错 UX）：
  - [x] body：`ip` / `title` / `block_type`（1-11）/ `duration`（`24h` | `permanent`，服务端换算 expires_at）
  - [x] 三态：无记录 → BanInsert（warn=1）；活跃 → 错误文案含「已在黑名单」+ 去向指引；软删/过期 → RestoreBan（按所选时长 + warn+1，满 5 限时转永久则响应注明）
  - [x] 成功后重建拦截快照（立即生效）
- [x] `GET /admin/shield/jail`：query `limit`（默认 20、上限 100）；响应 `{total, rows}`（rows 含 ip/block_type/hit_count/warn_times/created_at/expires_at）
- [x] 黑名单列表端点：新增 `sort` 参数（白名单见 A2，非法回默认）
- [x] main.go 路由常量与装配；admin.go 头部接口清单注释同步
- [x] 单测：sync_file（正常/缺失/重复幂等 skipped）/ ban 三态 + 满 5 转永久提示 / jail limit 边界 / sort 白名单
- [x] `go test ./plugins/shield/` 与 `go vet ./plugins/shield/` 通过

## 4. 验证

- [ ] `go test ./plugins/shield/ -run 'TestSyncFile|TestBan|TestJail' -v` 全绿；vet 通过
- [ ] dev 运行 curl 冒烟四组端点（回环免登录）

## 5. 完成标准

清单全勾 + 验证全过 → 状态「已实施」→ 更新总纲（正式文档同步集中在 A8）。

## 6. 实施回填区（中断现场记录）

2026-08-30 实施完成（A3 端点层）：

- `plugins/shield/admin.go`：新增 PathBlacklistSyncFile / PathBlacklistBan / PathShieldJail 路由常量与头部接口注释；`syncBlacklistFile`（读文件 + 导入，缺失/为空 400 三要素）、`banIPList`（duration 服务端换算 expires_at，活跃 400「已在黑名单」+ 指引，响应 `to_permanent`）、`Jail`（limit 非法/越界静默回默认 20，不报错）；`listIPList` 接入 `sort` 参数（经 store 白名单映射，非法回默认 id DESC）。
- `plugins/shield/ip_list_mgmt.go`：新增 `Shield.SyncBlacklistFile()`（复用 ruleLoader 读 `rules/ip_blacklist.txt`，逐行 validIPEntry 校验，非法行计入 skipped；无有效行返回错误）与 `Shield.Jail(limit)`（store 层收敛 limit）。
- `cmd/rocksys/main.go`：装配 sync_file / ban / jail 三端点。
- `plugins/shield/admin_blacklist_test.go`：新增 TestAdmin_BlacklistSyncFile / TestAdmin_BlacklistBan / TestAdmin_Jail / TestAdmin_BlacklistSort。
- 验证：`go test ./...` 全绿、`go vet ./...` 通过（curl 冒烟留待 dev 运行阶段）。
- 偏差说明：①「文件缺失」场景实际不可达——ScriptDir 外挂缺失时回落内嵌 `rules/ip_blacklist.txt`（非空），故「为空」与「缺失」统一按无可同步内容 400 处理；② 列表行 expires_at 为 NULL 时 easydb 扫描不落 map 键（JSON 缺键），前端判空需容忍缺键/空串两种形态（既有行为，A5/A6 前端接入时注意）。
