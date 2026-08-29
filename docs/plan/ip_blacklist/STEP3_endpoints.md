# A3 · 端点：sync_file / ban / jail / sort

> 实施状态：**待实施**
> 前置：A2 已实施。设计依据：决策 4/10/13/14、§3.3/§3.5/§3.7/§3.8。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| plugins/shield/admin.go | 修改 | 四组端点 + 头部接口注释同步 |
| cmd/rocksys/main.go | 修改 | 路由常量装配 |
| plugins/shield/admin_blacklist_test.go | 修改/新增 | 端点单测 |

## 3. 实施步骤

- [ ] `POST /admin/shield/blacklist/sync_file`（路由常量 PathBlacklistSyncFile）：
  - [ ] 经 ScriptHub/ScriptDir 读 `rules/ip_blacklist.txt`（外挂优先、内嵌兜底；sub 目录 `rules`。**优先复用 Shield 现有 ruleLoader 读取路径**——`plugins/shield/rules.go` 的 `ruleSubDir` / `ruleFileIPBlacklist` 常量已封装好该文件的读取，勿另造一套）
  - [ ] 复用导入解析（`#` 注释/空行忽略、逐行 validIPEntry）→ `ImportIPList(true, lines, "来自 ip_blacklist.txt 同步", BlockManual)`
  - [ ] 响应 `{imported, skipped}`；文件缺失/为空 → 400 三要素文案（发生了什么/为什么/下一步）
- [ ] `POST /admin/shield/blacklist/ban`（专用封禁端点，**不过载 addIPList**——保留其「已存在未生效+指引恢复」报错 UX）：
  - [ ] body：`ip` / `title` / `block_type`（1-11）/ `duration`（`24h` | `permanent`，服务端换算 expires_at）
  - [ ] 三态：无记录 → BanInsert（warn=1）；活跃 → 错误文案含「已在黑名单」+ 去向指引；软删/过期 → RestoreBan（按所选时长 + warn+1，满 5 限时转永久则响应注明）
  - [ ] 成功后重建拦截快照（立即生效）
- [ ] `GET /admin/shield/jail`：query `limit`（默认 20、上限 100）；响应 `{total, rows}`（rows 含 ip/block_type/hit_count/warn_times/created_at/expires_at）
- [ ] 黑名单列表端点：新增 `sort` 参数（白名单见 A2，非法回默认）
- [ ] main.go 路由常量与装配；admin.go 头部接口清单注释同步
- [ ] 单测：sync_file（正常/缺失/重复幂等 skipped）/ ban 三态 + 满 5 转永久提示 / jail limit 边界 / sort 白名单
- [ ] `go test ./plugins/shield/` 与 `go vet ./plugins/shield/` 通过

## 4. 验证

- [ ] `go test ./plugins/shield/ -run 'TestSyncFile|TestBan|TestJail' -v` 全绿；vet 通过
- [ ] dev 运行 curl 冒烟四组端点（回环免登录）

## 5. 完成标准

清单全勾 + 验证全过 → 状态「已实施」→ 更新总纲（正式文档同步集中在 A8）。

## 6. 实施回填区（中断现场记录）

（空）
