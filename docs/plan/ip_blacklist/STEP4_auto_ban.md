# A4 · 自动拉黑引擎

> 实施状态：**已实施**
> 前置：A3 已实施。设计依据：决策 3/7/11/12、§3.4（含二轮复查修正：排除自我拦截、按 IP 合计、Go 侧聚合）。

## 2. 改动文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| plugins/shield/auto_ban.go | 新增 | 引擎主体 |
| sql 三方言 shield_event_auto_ban_candidates.sql | 新增 ×3 | 候选查询 |
| plugins/shield/event_recorder.go（或配置注册所在文件） | 修改 | 四配置项 Register |
| cmd/rocksys/main.go | 修改 | 装配（按配置开启） |
| plugins/shield/auto_ban_test.go | 新增 | 单测 |

## 3. 实施步骤

- [x] 配置注册（Register，中文 title/usage；default.env 自动同步）：
  `SHIELD_AUTO_BAN_ENABLED=false` / `SHIELD_AUTO_BAN_THRESHOLD=50` / `SHIELD_AUTO_BAN_WINDOW=10m` / `SHIELD_AUTO_BAN_TTL=24h`（0=永久；续封 10 倍基数取此值）
- [x] 候选 SQL（三方言同构）：近窗口、**`block_type >= 2`（决策 11：排除黑名单自我拦截事件，防循环封禁）**、`GROUP BY client_ip, block_type` → `(client_ip, block_type, cnt)`；窗口过滤走 `time` 列（已有 idx_time）
- [x] Go 侧聚合（决策 12）：按 IP **跨类别合计判阈值**；拉黑类别取该 IP 次数最多者（**并列取枚举值小者**，规则定死可测）；不用 SQL 窗口函数（老 MySQL 无 ROW_NUMBER）
- [x] 引擎主体（参照 EventRecorder 的 ticker/Stop 生命周期模式）：
  - [x] 运行周期 = window/3、下限 1 分钟；**每轮开始读配置最新值（热更）**，开关关闭则空转
  - [x] 候选仅精确 IP 入库（拦截事件来源为精确 IP，无 CIDR）
  - [x] 白名单过滤：复用拦截快照 **CIDR 匹配语义**（白名单可含网段，不能只精确比对）——快照无导出方法则补只读判定方法
  - [x] 四态处理（处理完重建快照）：无记录 → 新增（block_type=真实类别 1-10、title=`自动拉黑：{window}内拦截≥{threshold}次`、expires_at=now+TTL、warn=1）；活跃 → 跳过；软删/过期 → RestoreBan（expires_at=now+TTL×10、warn+1）；+1 后 ≥5 限时 → 转永久（title 追加）
- [x] main.go 装配：配置开启时随 shield 生命周期启动/停止
- [x] 单测：聚合口径（合计/最多类别/并列）/ 排除 block_type=1 / 四态 / 转永久 / 白名单 CIDR 过滤 / 周期计算（window/3 下限）
- [x] `go test ./plugins/shield/` 与 `go vet ./plugins/shield/` 通过（另全量 `go test ./...` + `go vet ./...` 通过）

## 4. 验证

- [x] `go test ./plugins/shield/ -run TestAutoBan -v` 全绿；vet 通过
- [ ] dev 实战：bin/.env 临时 `SHIELD_AUTO_BAN_ENABLED=true` + 调低阈值/窗口（如 5 次/1 分钟）→ 制造拦截 → 黑白名单页出现自动拉黑条目（类别=真实拦截类别、封禁次数=1、解封时间=now+TTL）→ 改回默认值

## 5. 完成标准

清单全勾 + 验证全过 → 状态「已实施」→ 更新总纲。

## 6. 实施回填区（中断现场记录）

- 实施日期：2026-08-30（STEP A4 自动拉黑引擎）。
- 改动文件：`plugins/shield/auto_ban.go`（引擎主体：配置注册/循环/聚合/四态处理）、
  `plugins/shield/auto_ban_test.go`（单测 7 项）、`plugins/shield/shield.go`（补 `InWhitelist`
  只读判定方法，复用快照 CIDR 匹配语义）、`sql/{sqlite,mysql,postgres}/shield_event_auto_ban_candidates.sql`
  （候选查询三方言同构，`block_type >= 2` 排除自我拦截）、`cmd/rocksys/main.go`（装配 + 停机 Stop）。
- 实现要点：
  - 四配置项在 `NewAutoBanEngine(cfgMgr, shield, recorder)` 内注册（conf.Manager.Register，
    中文 title）；窗口/时长以字符串注册、每轮解析（支持热更）；TTL=0 表示永久。
  - 数据访问复用 EventRecorder（edb/sqlText/表名随 `SHIELD_EVENT_TABLE`）；封禁复用 store 层
    `GetByIP`/`BanInsert`/`RestoreBan` 三态；有变更统一重建一次拦截快照（`rebuildAfter("auto_ban")`）。
  - 白名单过滤经新增 `Shield.InWhitelist(ip)`（与 Handle 放行判定同源，支持 CIDR 网段）。
  - 停机顺序：main.go 中 `autoBan.Stop()` 先于 `recorder.Stop()`（引擎同步写库无缓冲，
    先停写入口再 flush 事件），均先于 dataDB 关闭。
- 偏差说明：main.go 装配为「dataDB 就绪即构造引擎 + Enabled() 时 Start」；循环每轮读配置最新值，
  运行期开关关闭则空转（阈值/窗口/TTL 均热更即生效）。开关 false→true 的热更需重启进程生效
  （引擎 goroutine 未启动）；其余热更（含 true→false）无需重启。
- 验证：`go test ./plugins/shield/ -run TestAutoBan -v` 全绿；全量 `go test ./...` 与
  `go vet ./...` 通过。dev 实战验证（§4 第二项）未执行（沙箱环境禁止启动服务器），待人工按步骤验证。
