# 日志系统设计 v3（极简增强式·双通道）

> 状态：**修订版 v3（2026-08-06）**。以项目核心哲学「只有转发是必须的，其余一切皆是可选增强，默认全关，可随时热插拔」为最高指导方针。
> 对象：`github.com/iotames/easyserver/log`（go.mod 已 replace 到 `./easyserver`）。
> v3 相对 v2 的变更：① 默认级别 **info**（v2 写 error，与现状 default.env 冲突且会屏蔽启动等 Info 日志）；② 双通道模型（console 恒开 + 文件可选追加）；③ E3 重定义为「大小上限 + 滑动窗口：超限删头部 1000 条」；④ 修复热更持久化自循环（值比较跳过）；⑤ 修正与代码现状不符的过期描述（conf.Set 持久化已实现、调用方 14 处）。

## 0. 设计哲学

- 转发（代理）是底座唯一必须 → 日志唯一必须的基座是 **ERROR 报错观测**，默认开启、不可关闭（级别最高只能设到 `error`，不存在「关掉 error」档位）。
- **默认运行级别 = info**：与现状 default.env 一致，启动横幅等 Info 日志默认可见；级别是基座能力，**可统一热更**（§3）。
- **双通道**：控制台（console）恒开不可关；文件为可选追加通道。
- **工程化第一原则：热更即持久化**——任何运行时热更必须「立即生效 + 写回 .env」，重启后状态保留（§3.1）。
- 其余一切（文件双通道、大小上限、轮转、JSON 格式、HTTP tail）皆是**可选增强**，默认全关，可随时热插拔。
- 极简体现在：默认运行态只做一件事——日志输出到 stdout（console）。

## 1. 基座（默认开启，不可关闭）

| 项 | 值 | 说明 |
|---|---|---|
| 级别 | `info`（默认，可热更） | error 为保底下限：任何级别下 Error 恒输出 |
| 输出 | `stdout`（console 恒开） | 极简 + 容器/daemon 标准（docker logs / journald 直接可读） |
| 格式 | `text` | 人可读，零依赖 |

基座无需任何配置、任何依赖、任何文件系统，开箱即 ERROR 观测。console 恒开不可关；「关闭日志 / 关闭 ERROR」被禁止（报错观测是排障底线）。

## 2. 可选增强矩阵（默认全关，可随时热插拔）

| # | 增强 | 默认 | 热切开关 | 说明 |
|---|---|---|---|---|
| E1 | 控制台输出 | 开（基座） | 不可关 | stdout 恒开 |
| E2 | 文件双通道 | 关 | `SetFileOutput` / `ROCKSYS_LOG_TO_FILE` | console + file 同时输出 `logs/rocksys.log` |
| E3 | 大小上限（滑动窗口） | 关（E2 开启后生效） | `SetMaxSize` / `ROCKSYS_LOG_MAX_SIZE` | 文件总大小硬上限，默认 500M；超限 → 删除头部 1000 条最旧日志再追加，文件恒 ≤ 上限 |
| E4 | 轮转 | 关 | `SetRotate` / `ROCKSYS_LOG_ROTATE` | 按天 + 大小双维度切分，总量超限删最旧 |
| E5 | JSON 格式 | 关（text） | `SetFormat` / `ROCKSYS_LOG_FORMAT` | 结构化，供 AI 解析 |
| E6 | HTTP tail | 关 | admin 注册 | AI Agent 实时读取（§7） |

每个增强独立开关、互不依赖；关闭 = 回到更简形态，基座不受影响。

## 3. 级别模型

```
debug < info（默认）< warn < error
```

slog 语义：设定级别后，等于或高于该级别均输出。基座约束：级别最高只能设到 `error`，Error 恒输出。

### 3.1 热更即持久化（第一原则）+ 防循环

任何运行时热更必须**同时满足**：① 立即生效（内存）；② 写回 `.env`（重启保留）。

**已实现**：`conf.Manager.Set`（[internal/conf/impl.go:277](file:///persistent/home/hankin/projects/rocksys/internal/conf/impl.go#L277-L287)）已内置 `ec.UpdateFile` 持久化（保留注释、追加新 key）。v3 补充 **防循环补丁**：

- `conf.Set` 内先比较新旧值，**相同则跳过**（不重写 .env、不重复广播）。
- 级别钩子仅在级别**真正变化**时触发。

**分层设计**（easyserver/log 是独立子仓库，不得反向依赖 rocksys/internal/conf）：

- `easyserver/log` 只负责运行时生效：`SetLevel` 生效后触发**变更钩子**（`SetOnLevelChange`），不感知 .env。
- rocksys 装配时注入持久化回调：钩子 → `conf.Set("ROCKSYS_LOG_LEVEL", v)` → `ec.UpdateFile(envFile)` 写回 .env。
- 所有热更路径统一收口到「log 生效 + conf 持久化」两处。

**收敛证明**：任何热更路径最多写盘一次——watcher 重放相同值 → `conf.Set` 值比较跳过 → 钩子不触发 → 循环终止（不再每 3s 写一次）。

### 3.2 级别热切路径（4 条，全部「生效 + 持久化」）

| # | 路径 | 立即生效 | 持久化 |
|---|---|---|---|
| 1 | 代码 `log.SetLevel(...)` | ✅ | ✅ 钩子 → conf.Set → 写 .env |
| 2 | 改 `.env` 文件 | ≤3s（watcher 广播） | ✅ 天然（.env 即持久层） |
| 3 | `PUT /admin/config`（带 `ROCKSYS_LOG_LEVEL`） | 秒级 | ✅ conf.Set |
| 4 | `POST /admin/log/level` | 秒级 | ✅ 同一收口 |

最低即 `error`，不存在"关闭日志"。

## 4. 技术实现

### 4.1 转发 writer（E2-E5 的载体）

```go
// 包级状态（伪码）
var (
    lgLevel *slog.LevelVar              // 级别：热切（slog 原生，绑定 HandlerOptions.Level）
    cur     atomic.Pointer[slog.Logger] // 当前 logger：格式热切时原子替换（§4.3）
    mu      sync.RWMutex                // 保护 out
    out     output
)
type output struct {
    console bool                  // 恒 true（基座，不可关）
    file    *rotatingFile         // nil = 未开文件（E2）
}
```

- 写路径：取当前 logger（原子读）→ handler 底层转发 writer 持 `RLock` 读快照分发；`SetFileOutput` 持 `Lock` 替换并**确定性关闭旧文件句柄**（无在途写入风险）。
- 级别沿用 `lgLevel`（`slog.HandlerOptions.Level` 已绑定该变量），slog 内部即时生效，与 writer 切换互不影响。
- console 恒 true：可选增强只能"加"，不能关掉基座。

### 4.2 文件 writer（E2/E3/E4 的载体）

- 打开文件即受 `maxSize`（默认 500M）约束：**写前检查**，超限即按当前模式处理（**无论轮转开关**）：
  - **E4 关（默认）＝单文件滑动窗口**：新日志到达且文件超限 → 先删除头部 1000 条最旧日志 → 末尾追加本条。实现：定位第 1001 个换行偏移，剩余内容写临时文件后 `rename`（O(n)，500M 级百毫秒内，可接受）。`tail -F` 始终可见最新。
  - **E4 开＝按天 + 大小双维度切分**（`rocksys-2006-01-02.log`、`.1.log`…）：超限即切入新分片，总量超 `maxSize` 删最旧分片；此模式**不做头部删除**。
- 大小非精确：写前检查，单条超长日志允许略超上限。

### 4.3 格式热切（E5）

- text↔json 切换 = 重建 `slog.Handler` + `slog.Logger`，经 `cur atomic.Pointer` **原子替换**；写路径每次原子读当前 logger 调用（slog.Logger 本身并发安全）。
- 与 writer/级别互不影响：格式只影响编码，不改变输出通道与级别过滤。

## 5. API 变更

**兼容性事实**（已核查）：现有调用方仅使用 `Debug/Info/Warn/Error` 与 `SetLevel`；`SetLogWriter*`/`SetOptions`/`ResetLogger` **无任何外部调用**。故 v3 可安全重定义 writer 系 API。

| API | 动作 | 说明 |
|---|---|---|
| `Debug/Info/Warn/Error` | 保留 | 经转发 writer 分发 |
| `SetLevel(level)` | 保留 | 热切；级别变化触发钩子 |
| `SetLogWriter(w)` | 重定义 | 设置**文件通道**目标 writer（等价 E2 开启并指向 w） |
| `SetLogWriterByFile(path)` | 保留 | 创建文件并开启文件通道 |
| `SetFileOutput(on bool)` | **新增** | 文件双通道开关（E2/E3 载体；console 恒开不受影响） |
| `SetRotate(on bool)` | **新增** | 热切轮转开关（E4） |
| `SetMaxSize(n int64)` | **新增** | 热切硬上限（E3） |
| `SetFormat(fmt)` | **新增** | 热切 text/json（E5） |
| `SetOnLevelChange(fn func(slog.Level))` | **新增** | 级别变更钩子：`SetLevel` 生效后触发；rocksys 注入持久化回调（§3.1） |
| `GetInfo() Info` | **新增** | 级别/格式/输出/文件状态/文件清单 |
| `Tail(n, since) (lines, next, eof, err)` | **新增** | 读文件尾部 N 行 + 字节游标（供 E6） |
| `ResetLogger()` | deprecated | 不再需要，保留不删 |

## 6. 配置项

| 配置项 | 默认 | 说明 |
|---|---|---|
| `ROCKSYS_LOG_LEVEL` | `info` | 级别（debug/info/warn/error）；info 是默认基座，error 是保底下限 |
| `ROCKSYS_LOG_TO_FILE` | `false` | 文件双通道（E2） |
| `ROCKSYS_LOG_FILE` | `logs/rocksys.log` | 日志文件路径 |
| `ROCKSYS_LOG_FORMAT` | `text` | `text` / `json`（E5） |
| `ROCKSYS_LOG_MAX_SIZE` | `500M` | 文件总硬上限（E3；E2 开启后生效） |
| `ROCKSYS_LOG_ROTATE` | `off` | 轮转开关（E4） |
| `ROCKSYS_LOG_FILE_SIZE` | `100M` | 轮转开时单份切分大小 |

> 说明：v2 草案的 `ROCKSYS_LOG_TO_CONSOLE` **已移除**——console 恒开是基座行为而非配置，暴露恒 true 项只会造成「能关掉」的误导。

- 注册进 `conf.bindBaseVars`；大小类配置存可读字符串（`"500M"`），`ParseSize` 解析，失败回退默认。
- 热更联动：日志初始化处订阅 `cfgMgr.Watch`，回调内按新配置调 `SetLevel`/`SetFileOutput`/`SetRotate`/`SetMaxSize`/`SetFormat`（幂等，值比较跳过）。`PUT /admin/config` 与改 `.env` 同路径。
- **持久化**：`conf.Manager.Set` 已实现——内部 `ec.SetItemValue` 后调 `ec.UpdateFile(envFile)`；v3 补值比较跳过（§3.1）。

## 7. AI 实时读取（增强 E6）

**主走 HTTP tail 端点，`tail -F` 仅作 Linux 本地调试辅助；不监控 stdout**（daemon 化后 stdout 被重定向，绕过大小限制且格式不可控；小写 `-f` 在轮转/滑动窗口场景跟丢，必须大写 `-F`）。

```bash
# Linux 本地调试（注意大写 F）
tail -F -n 100 logs/rocksys.log

# AI 主路径：HTTP 轮询（每 2~3s），返回 {"lines":[...], "next_offset":12345, "eof":true, "reset":false}
GET /admin/log/tail?n=100                     # 首次：尾部 100 行
GET /admin/log/tail?n=100&since=<offset>      # 增量
```

## 8. admin API（增强，走现有 `requireAuth`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/log/info` | 当前级别/格式/输出/文件状态/文件清单 |
| POST | `/admin/log/level` | `{"level":"debug"}`（级别热切，§3） |
| POST | `/admin/log/output` | `{"file":true}`（E2/E3；console 恒 true，不再接受 console 参数） |
| POST | `/admin/log/rotate` | `{"enabled":true}`（E4） |
| GET | `/admin/log/tail` | `?n=100&since=<offset>`（E6） |

边界语义：文件输出未开启时 tail 返回 409；文件不存在返回空列表 `eof=true`；滑动窗口/轮转重建后 `since` 失效返回 `reset=true`；`n` 上限 1000 默认 100。

**命名区分**：obs 插件已有 `/admin/logs`（复数，业务访问日志查询）；本设计为 `/admin/log/*`（单数，进程文件日志）。easyserver 路由精确匹配，两者互不冲突。

**持久化**：所有写端点（`/level`、`/output`、`/rotate`）一律经「log 生效 + `conf.Set` 写 .env」收口，热改即落盘、重启保留（§3.1）。

## 9. 测试计划

1. **基座不可关**：`SetFileOutput(false)` 后 Info/Error 仍写 stdout；`go test -race` 无竞争。
2. **级别热切**：debug↔info↔warn↔error 立即生效。
3. **硬上限 + 滑动窗口**：`maxSize=1KB` 连写 10KB，文件 ≤ 1KB（允许单条超长行溢出），且头部最旧日志被删、最新日志可见。
4. **轮转**：E4 关 = 单文件滑动窗口（删头 1000 条）；E4 开 = 按天/大小切分 + 总量超限删最旧。
5. **兼容性回归**：现有 14 处调用方默认行为不变。
6. **admin 端点**：httptest 覆盖 §8（含鉴权 401、409、reset）。
7. **conf 热更**：改 `.env` → 3s 内级别/文件/轮转/格式生效。
8. **持久化闭环**：`POST /admin/log/level` 与代码 `SetLevel` 后，`.env` 中 `ROCKSYS_LOG_LEVEL` 同步更新；重启进程后级别保留。
9. **防循环收敛**：级别热更后 6s 内 `.env` mtime 不再变化（无持续写盘）。
10. **双通道**：文件开启后控制台与文件同时输出；文件写失败不影响控制台。

## 10. 实施步骤

| 阶段 | 内容 | 验收标准 |
|---|---|---|
| P1 | `easyserver/log` 增强：转发 writer + 热切 API + `rotatingFile` + 滑动窗口（删头 1000 条）+ logger 原子替换 + `ParseSize` | 单测全绿（含 race）；14 处调用方回归零变化 |
| P2 | conf 注册 §6 配置项 + `conf.Set` 补值比较防循环 + watcher 联动 | 改 `.env` 3s 内生效；`PUT /admin/config` 同路径且重启保留；无自循环写盘 |
| P3 | admin API §8 端点 + 集成测试 | httptest 全绿 |
| P4 | AI 集成验证：HTTP tail 与 `tail -F` 双路径 | 增量游标正确、滑动窗口/轮转后 reset 恢复 |

## 11. 风险与注意事项

- **滑动窗口删头重写**：O(n) 读改写（500M 级百毫秒内），期间 `tail -F` 短暂断流（可接受）。
- **Windows 文件锁**：轮转/重写须"先关句柄 → 删目标 → 新建"（`os.Rename` 目标存在会失败）。
- **影响面**：`easyserver/log` 被 tcpsvr 等 14 处使用，P1 保证默认路径零行为变化。
- **双写 IO**：文件 IO 失败不影响控制台输出（逐目标独立处理）。
