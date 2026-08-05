# 日志系统设计 v2（极简增强式）

> 状态：**定稿 v2（2026-08-05）**。以项目核心哲学「只有转发是必须的，其余一切皆是可选增强，默认全关，可随时热插拔」为最高指导方针。
> 对象：`github.com/iotames/easyserver/log`（go.mod 已 replace 到 `./easyserver`）。根目录 `log/` 副本已删除，无歧义。

## 0. 设计哲学

**日志领域的「只有转发是必须的」= 只有 ERROR 报错观测是必须的。**

- 转发（代理）是底座唯一必须 → 日志组件唯一必须的基座是 **ERROR 级报错观测**，默认开启、不可关闭。
- **工程化第一原则：热更即持久化**——任何运行时热更必须「立即生效 + 写回 .env」，重启后状态保留（§3.1）。
- 其余一切（详细级别、文件落盘、轮转、格式、AI 读取）皆是**可选增强**，默认全关，可随时热插拔。
- 极简体现在：默认运行态只做一件事——ERROR 输出到 stdout。

## 1. 基座（默认开启，不可关闭）

| 项 | 值 | 说明 |
|---|---|---|
| 级别 | `error` | 生产环境默认的 ERROR 报错观测 |
| 输出 | `stdout` | 极简 + 容器/daemon 标准（docker logs / journald 直接可读） |
| 格式 | `text` | 人可读，零依赖 |

基座无需任何配置、任何依赖、任何文件系统，开箱即 ERROR 观测。关闭 ERROR 是被禁止的（报错观测是排障底线）。

## 2. 可选增强矩阵（默认全关，可随时热插拔）

| # | 增强 | 默认 | 热切开关 | 说明 |
|---|---|---|---|---|
| E1 | 级别观测 | 关（只留 error） | `SetLevel` / `ROCKSYS_LOG_LEVEL` | error → info / debug，更细观测 |
| E2 | 文件落盘 | 关 | `SetOutput` / `ROCKSYS_LOG_TO_FILE` | 落盘 `logs/rocksys.log` |
| E3 | 双写 | 关（随 E2） | 同上 | 控制台 + 文件同时输出 |
| E4 | 大小上限 | 关（E2 开启后生效） | `SetMaxSize` / `ROCKSYS_LOG_MAX_SIZE` | 文件总大小硬上限，默认 500M，防失控 |
| E5 | 轮转 | 关 | `SetRotate` / `ROCKSYS_LOG_ROTATE` | 按天 + 大小双维度切分，超限删最旧 |
| E6 | JSON 格式 | 关（text） | `SetFormat` / `ROCKSYS_LOG_FORMAT` | 结构化，供 AI 解析 |
| E7 | HTTP tail | 关 | admin 注册 | AI Agent 实时读取（§7） |

每个增强独立开关、互不依赖；关闭 = 回到更简形态，基座不受影响。

## 3. 级别模型

```
error（默认，基座） → info（E1） → debug（E1）
```

### 3.1 热更即持久化（第一原则）

任何运行时热更必须**同时满足**：① 立即生效（内存）；② 写回 `.env`（重启保留）。

**现状缺口**：`conf.Manager.Set`（`internal/conf/impl.go:274`）只调 `ec.SetItemValue` 改内存 + 广播，**不写文件** → `PUT /admin/config` 热改后重启丢失。easyconf 已内置 `UpdateFile(fpath)`（把注册项当前值写回文件、保留注释、追加新 key），补上即可。

**分层设计**（easyserver/log 是独立子仓库，不得反向依赖 rocksys/internal/conf）：

- `easyserver/log` 只负责运行时生效：`SetLevel` 生效后触发**变更钩子**（新增 `SetOnLevelChange(fn)`），不感知 .env。
- rocksys 装配时注入持久化回调：钩子 → `conf.Set("ROCKSYS_LOG_LEVEL", v)` → 内部 `ec.UpdateFile(envFile)` 写回 .env。
- 所有热更路径统一收口到「log 生效 + conf 持久化」两处，幂等无副作用。

### 3.2 级别热切路径（4 条，全部「生效 + 持久化」）

| # | 路径 | 立即生效 | 持久化 |
|---|---|---|---|
| 1 | 代码 `log.SetLevel(...)` | ✅ | ✅ 变更钩子 → `conf.Set` → 写 .env |
| 2 | 改 `.env` 文件 | ≤3s（watcher 广播） | ✅ 天然（.env 即持久层） |
| 3 | `PUT /admin/config`（带 `ROCKSYS_LOG_LEVEL`） | 秒级 | ✅ `conf.Set` 补 `UpdateFile` 后 |
| 4 | `POST /admin/log/level` | 秒级 | ✅ 内部走同一收口 |

最低即 `error`，不存在"关闭日志"。

## 4. 技术实现

### 4.1 转发 writer（热切核心，E1-E6 的载体）

```go
// 包级状态（伪码）
var (
    lgLevel *slog.LevelVar         // 级别：热切
    out     output                 // 输出组合，RWMutex 保护
)
type output struct {
    console bool                   // 是否写 os.Stdout（基座恒 true 不可关）
    file    *rotatingFile          // nil = 未开文件（E2）
}
```

- 写路径持 `RLock` 读快照后分发；`SetOutput` 持 `Lock` 替换并**确定性关闭旧文件句柄**（无在途写入风险）。
- 级别沿用 `lgLevel`（`slog.HandlerOptions.Level` 已绑定该变量），与 writer 切换互不影响。
- 基座 console 恒 true：可选增强只能"加"，不能关掉基座。

### 4.2 文件 writer（E2/E4/E5 的载体）

- 打开文件即受 `maxSize`（默认 500M）约束：写前检查，超限即裁剪，**无论轮转开关**。
- E5 关（默认）＝单文件滑动窗口：超限 → 关句柄 → 删旧 → 开新，文件恒 ≤ 上限，`tail -F` 始终可见最新。
- E5 开：按天 + 大小双维度切分（`rocksys-2006-01-02.log`、`.1.log`…），总量超 `maxSize` 删最旧。
- 大小非精确：写前检查，单条超长日志允许略超上限。

## 5. API 变更（向后兼容，现有 13 处调用方行为不变）

| API | 动作 | 说明 |
|---|---|---|
| `Debug/Info/Warn/Error` | 保留 | 经转发 writer 分发 |
| `SetLevel(level)` | 保留 | 已是热切的 |
| `SetLogWriter(w)` / `SetLogWriterByFile(path)` | 保留 | 实现改为替换转发层目标（等效增强 E2） |
| `SetOutput(console, file bool)` | **新增** | 热切文件/双写开关（console 只允许 true→false 的降级提示，不允许关闭基座） |
| `SetRotate(on bool)` | **新增** | 热切轮转开关 |
| `SetMaxSize(n int64)` | **新增** | 热切硬上限 |
| `SetFormat(fmt)` | **新增** | 热切 text/json |
| `SetOnLevelChange(fn func(slog.Level))` | **新增** | 级别变更钩子：`SetLevel` 生效后触发；rocksys 注入持久化回调（§3.1） |
| `GetInfo() Info` | **新增** | 级别/格式/输出/文件状态/文件清单 |
| `Tail(n, since) (lines, next, eof, err)` | **新增** | 读文件尾部 N 行 + 字节游标（供 E7） |
| `ResetLogger()` | deprecated | 不再需要，保留不删 |

## 6. 配置项

| 配置项 | 默认 | 说明 |
|---|---|---|
| `ROCKSYS_LOG_LEVEL` | `error` | 级别（error/info/debug）；error 是基座 |
| `ROCKSYS_LOG_TO_CONSOLE` | `true` | 控制台输出（基座，恒 true，不提供关闭） |
| `ROCKSYS_LOG_TO_FILE` | `false` | 文件落盘（E2） |
| `ROCKSYS_LOG_FILE` | `logs/rocksys.log` | 日志文件路径 |
| `ROCKSYS_LOG_FORMAT` | `text` | `text` / `json`（E6） |
| `ROCKSYS_LOG_MAX_SIZE` | `500M` | 文件总硬上限（E4；E2 开启后生效） |
| `ROCKSYS_LOG_ROTATE` | `off` | 轮转开关（E5） |
| `ROCKSYS_LOG_FILE_SIZE` | `100M` | 轮转开时单份切分大小 |

- 注册进 `conf.bindBaseVars`；大小类配置存可读字符串（`"500M"`），`ParseSize` 解析，失败回退默认。
- 热更联动：日志初始化处订阅 `cfgMgr.Watch`，回调内按新配置调 `SetLevel`/`SetOutput`/`SetRotate`/`SetMaxSize`/`SetFormat`（幂等）。`PUT /admin/config` 与改 `.env` 同路径。
- **持久化**：`conf.Manager.Set` 补写回——内部 `ec.SetItemValue` 后调 `ec.UpdateFile(envFile)`，使所有 `PUT /admin/config`、日志钩子落盘（§3.1）。

## 7. AI 实时读取（增强 E7）

**主走 HTTP tail 端点，`tail -F` 仅作 Linux 本地调试辅助；不监控 stdout**（daemon 化后 stdout 被重定向，绕过大小限制且格式不可控；小写 `-f` 在轮转场景跟丢，必须大写 `-F`）。

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
| POST | `/admin/log/level` | `{"level":"debug"}`（E1） |
| POST | `/admin/log/output` | `{"console":true,"file":true}`（E2/E3） |
| POST | `/admin/log/rotate` | `{"enabled":true}`（E5） |
| GET | `/admin/log/tail` | `?n=100&since=<offset>[&follow=true]`（E7） |

边界语义：文件输出未开启时 tail 返回 409；文件不存在返回空列表 `eof=true`；轮转重建后 `since` 失效返回 `reset=true`；`n` 上限 1000 默认 100。

**持久化**：所有写端点（`/level`、`/output`、`/rotate`）一律经「log 生效 + `conf.Set` 写 .env」收口，热改即落盘、重启保留（§3.1）。

## 9. 测试计划

1. **基座不可关**：`SetOutput(false, ...)` 后 ERROR 仍写 stdout；`go test -race` 无竞争。
2. **级别热切**：error↔info↔debug 立即生效。
3. **硬上限**：`maxSize=1KB` 连写 10KB，文件 ≤ 1KB（允许单条超长行溢出）。
4. **轮转**：E5 关 = 单文件滑动窗口；E5 开 = 按天/大小切分 + 总量超限删最旧。
5. **兼容性回归**：现有 13 处调用方默认行为不变。
6. **admin 端点**：httptest 覆盖 §8（含鉴权 401、409、reset）。
7. **conf 热更**：改 `.env` → 3s 内级别/输出/轮转/格式生效。
8. **持久化闭环**：`POST /admin/log/level` 与代码 `SetLevel` 后，`.env` 中 `ROCKSYS_LOG_LEVEL` 同步更新；重启进程后级别保留。

## 10. 实施步骤

| 阶段 | 内容 | 验收标准 |
|---|---|---|
| P1 | `easyserver/log` 增强：转发 writer + 热切 API + `rotatingFile` + `ParseSize` | 单测全绿（含 race）；13 处调用方回归零变化 |
| P2 | conf 注册 §6 配置项 + `conf.Set` 补 `UpdateFile` 持久化 + watcher 联动 | 改 `.env` 3s 内生效；`PUT /admin/config` 同路径且重启保留 |
| P3 | admin API §8 端点 + 集成测试 | httptest 全绿 |
| P4 | AI 集成验证：HTTP tail 与 `tail -F` 双路径 | 增量游标正确、轮转后 reset 恢复 |

## 11. 风险与注意事项

- **Windows 文件锁**：轮转须"先关句柄 → 删目标 → 新建"（`os.Rename` 目标存在会失败）。
- **超限裁剪瞬间** `tail -F` 短暂断流（可接受）。
- **影响面**：`easyserver/log` 被 tcpsvr 等 13 处使用，P1 保证默认路径零行为变化。
- **双写 IO**：文件 IO 失败不影响控制台输出（逐目标独立处理）。
