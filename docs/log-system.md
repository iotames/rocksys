# 日志系统设计

> 对象：`github.com/iotames/easyserver/log`（go.mod 已 replace 到 `./easyserver`）。
> 指导方针：只有转发是必须的，其余一切皆是可选增强，默认全关，可随时热插拔。

## 1. 现状（现有能力）

`easyserver/log` 基于 `log/slog` 封装，提供：

- `Debug/Info/Warn/Error` 四个级别函数。
- `SetLevel` 设置级别（`slog.LevelVar`，可热切）。
- `SetLogWriter` / `SetLogWriterByFile` / `SetOptions` / `ResetLogger`（无外部调用）。
- 默认输出到 stdout，默认级别 info。

外部调用方（15 处，见 dev §0.3 清单）仅使用 `Debug/Info/Warn/Error` 与 `SetLevel`。

## 2. 新增功能总览

| 功能 | 说明 |
|---|---|
| 双通道基座 | console + 内存环形缓冲（ring buffer），恒开不可关 |
| 实时监控 | HTTP 轮询 + SSE 推送，读 ring buffer，不依赖文件/stdout |
| 文件存档（可选） | 默认关，**异步写**、超限直接清空，仅供存档；故障不影响主通道 |
| 格式模板 | `log.tpl` 决定输出格式，变量注入替换占位符，外挂优先/内嵌兜底 |
| 级别热更持久化 | 热更即写回 .env，防循环 |

> **红线原则**：console + ring buffer 两个必备通道必须正常运行（ERROR 观测 + 实时监控）。**文件存档是可选增强，其任何故障（磁盘满、权限、慢写、崩溃）不得影响主通道与主业务**——文件 writer 异步写入（入队即返回，后台 goroutine 落盘），主写路径永不等待文件 IO。文件日志的细节取舍（清空策略、丢失、轮转、失败恢复）不作为阻塞评审项，红线只保证「文件故障不影响主通道」。

## 3. 基座（双通道，恒开不可关）

| 通道 | 用途 | 状态 |
|---|---|---|
| console（stdout） | 本地/容器直接可读（docker logs / journald） | 恒开，不可关 |
| ring buffer（内存 8MB） | 实时监控数据源（HTTP tail / SSE） | 恒开，不可关 |

- 级别：`info`（默认，可热更）；error 为保底下限，任何级别下 Error 恒输出（实现层强制：handler 对 `>=error` 的记录放行，不受 `SetLevel` 钳制——见 dev §1.4）。
- 双通道默认开启、不可关闭，保证自包含监控永远可用。

## 4. 可选增强矩阵（默认全关）

| # | 增强 | 默认 | 开关 | 说明 |
|---|---|---|---|---|
| E1 | 文件存档 | 关 | `SetFileWriter` / `ROCKSYS_LOG_TO_FILE` | **异步写**（入队即返回，后台落盘）`ROCKSYS_LOG_FILE`（默认 `logs/rocksys.log`），仅供存档；故障不影响主通道 |
| E2 | 文件大小上限 | 关（E1 开启后生效） | `SetMaxSize` / `ROCKSYS_LOG_MAX_SIZE` | 整数 MB，默认 50；超限直接清空；`0`=不限制 |

## 5. 级别模型

```
debug < info（默认）< warn < error
```

slog 语义：设定级别后，等于或高于该级别均输出。基座约束：级别最高只能设到 `error`，Error 恒输出。

### 5.1 热更即持久化 + 防循环

任何运行时热更必须：① 立即生效（内存）；② 写回配置源文件（`--config` 指定时写配置文件，否则 `.env`，重启保留）。

- `conf.Set` 值比较，相同则跳过（不重写、不重复广播）。
- 级别钩子仅在级别真正变化时触发。
- 热更写回后同步更新内存命令行参数，避免 watcher 重放旧值覆盖。

分层：`easyserver/log` 只负责运行时生效（`SetLevel` 触发钩子），不感知配置源；rocksys 装配时注入持久化回调（钩子 → `conf.Set` → 写配置源）。

> ⚠️ **`--config` 模式下写盘副作用**：`conf.Set` 写的是 `watchFiles()` 末元素（`--config` 指定时为配置文件）。`UpdateFile` 会把所有**未在 configFile 中的已注册项**追加进 configFile（不止热更 key）。`--config` 模式下，这些 key 在 `.env` 中的后续编辑可能被 configFile 高优先级覆盖——属既有 easyconf 行为，使用 `--config` 时注意配置源优先级。

### 5.2 级别热切路径（4 条，全部「生效 + 持久化」）

| # | 路径 | 立即生效 | 持久化 |
|---|---|---|---|
| 1 | 代码 `log.SetLevel(...)` | ✅ | ✅ 钩子 → conf.Set → 写配置源 |
| 2 | 改 `.env` 文件 | ≤3s（watcher 广播） | ✅ 天然（注：启动带命令行级别参数时，命令行优先级更高，编辑可能被重放覆盖） |
| 3 | `PUT /admin/config`（带 `ROCKSYS_LOG_LEVEL`） | ✅ 秒级（经 watcher 回调异步生效，依赖 dev §2.5 日志订阅存在，否则静默不生效） | ✅ conf.Set |
| 4 | `POST /admin/log/level` | 秒级 | ✅ 同一收口 |

## 6. 格式模板（log.tpl）

**输出格式完全由模板文件 `log.tpl` 决定**，无内置 text/json 二选一配置。

- 模板是**纯文本字符串**，可写成 text 风格、json 风格或任意格式。
- 程序把日志变量**注入模板、替换占位符**。
- 占位符语法：`{{.字段名}}`（text/template 语法，字段前带 `.`），字段名遵循 slog 标准（`time`、`level`、`msg`，以及任意 attr key）。
- **建议模板自带换行 `\n` 结尾**：实现层兜底强制——渲染后若末尾无 `\n` 则补一个（保证按行切分与实时监控可用）。

示例（text 风格）：

```
{{.time}} [{{.level}}] {{.msg}}
```

示例（json 风格，注意：值不做转义，字段值需自行保证不含 `"`，或模板作者自备转义方案）：

```
{"time":"{{.time}}","level":"{{.level}}","msg":"{{.msg}}"}
```

- 模板经 hotswap `ScriptDir` 兜底加载：外置目录优先（可独立于编译二进制修改），缺失/为空回退内嵌默认。
- **启动时加载，运行期不支持热切换**（格式定死，换取实现简化）。
- 无 `ROCKSYS_LOG_FORMAT` 配置——格式由模板文件本身决定。
- **默认内嵌模板**为静态形态 `time={{.time}} level={{.level}} msg={{.msg}}`（`time` 用 `time.DateTime` 格式），贴近 slog 默认 text 输出的关键字段与行结构；**模板未引用的 attr 不输出**（默认模板即丢弃全部 attr，属模板化输出的既定取舍），attr 值也不做引号转义。

## 7. 实时监控（双通道，读 ring buffer）

不依赖文件、不依赖 stdout（daemon 化后 stdout 可能被 systemd/journald 接管，文件可能被清空，均不可靠）。两者均读 ring buffer，复用 admin 端口。

### 7.1 HTTP 轮询（AI 主路径）

```bash
GET /admin/log/tail?n=100                     # 首次：尾部 100 行（since 缺省=尾部首拉，取窗口尾部最后 n 行）
GET /admin/log/tail?n=100&since=<offset>      # 增量
```

返回 `{"lines":[...],"next_offset":12345,"eof":true,"reset":false}`。

- `since` 为 ring buffer 单调递增字节游标（按行对齐）；已被覆盖 → `reset=true`。
- **首次拉取**：`since` 缺省时从**窗口尾部向前取最后 n 个完整行**（等价 tail 尾部）；增量续读从 `next_offset` 起。
- **reset 后协议**：客户端收到 `reset=true` 后，应丢弃本次 lines，以缺省 since 重新尾部首拉，而非用返回的 `next_offset` 续读（那会跳过当前窗口内容）。
- 无状态、可重试、可断点续读。

### 7.2 SSE 实时推送（WebUI 实时监控板块）

```bash
GET /admin/log/stream
```

- 标准库实现（`text/event-stream`），零新增依赖。
- 服务端订阅 ring buffer，新日志即推。
- **断线重连从最新开始**（不续读历史）——SSE 供人工观测，断线即网络异常，历史由 HTTP 轮询补读。
- 鉴权走现有 `requireAuth`。

## 8. admin API（走现有 `requireAuth`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/log/info` | 级别/输出/文件状态/ring 状态 |
| POST | `/admin/log/level` | `{"level":"debug"}`（级别热切） |
| POST | `/admin/log/output` | `{"file":true}`（E1） |
| GET | `/admin/log/tail` | `?n=100&since=<offset>`（HTTP 轮询） |
| GET | `/admin/log/stream` | SSE 实时推送 |

边界：`n` 上限 1000 默认 100；ring 空返回 `eof=true`；`since` 被覆盖返回 `reset=true`。

命名区分：obs 插件已有 `/admin/logs`（复数，业务访问日志）；本设计为 `/admin/log/*`（单数，进程日志）。路由精确匹配，互不冲突。

## 9. 测试计划

1. 基座不可关：`SetFileWriter(false)` 后 Info/Error 仍写 console + ring；`go test -race` 无竞争。
2. 级别热切：debug↔info↔warn↔error 立即生效。
3. ring 游标：写满覆盖最旧；`since` 增量正确；被覆盖 → `reset`；返回行无半行（NextOffset 可为半行起点，续读不产生截断脏行）。
4. 文件存档：`maxSize=1` 超限清空并留标记；`maxSize=0` 不限制；**文件异步写（入队即返回，console/ring 不受文件 IO 阻塞）**。
5. 格式模板：外挂 `log.tpl` 生效；缺失回退内嵌；json 风格模板可用。
6. 兼容性回归：现有 15 处调用方**级别过滤/时间格式/通道**不变；默认模板仅输出 `time/level/msg` 三字段，**attr 不输出**（属模板化输出的既定行为变化——attr 由自定义 `log.tpl` 决定是否展示）；行级格式贴近 slog text（`time=... level=... msg=...`），含空格值不做引号（与现状引号差异为既定变化，明确豁免）。
7. admin 端点：httptest 覆盖 §8（含鉴权 401、reset、SSE）。
8. conf 热更：改 `.env` → 3s 内级别/文件生效。
9. 持久化闭环：热更后 `.env` 同步更新；重启保留。
10. 防循环收敛：热更后 6s 内 `.env` mtime 不再变化。
11. 多路分发：文件写失败/队列满不影响 console/ring（红线，异步隔离）。

## 10. 实施步骤

| 阶段 | 内容 | 验收 |
|---|---|---|
| P1 | `easyserver/log` 增强：分发 writer + ring buffer + 文件 writer + 模板渲染 + 级别钩子 | 单测全绿（含 race）；15 处调用方级别/时间/通道不变，行结构贴近现状（attr 丢弃与引号差异豁免，见 §9.6） |
| P2 | conf 注册配置项 + `conf.Set` 值比较防循环 + 命令行同步 + watcher 联动 | 改 `.env` 3s 内生效；重启保留；无自循环 |
| P3 | admin API §8 端点（含 SSE）+ 集成测试 | httptest 全绿 |
| P4 | AI 集成验证：HTTP tail 增量/reset、SSE 推送 | 增量正确、reset 恢复、SSE 实时 |

## 11. 风险与注意事项

- **ring 覆盖丢数据**：8MB 满即丢最旧，高频日志下轮询可能追不上 → 频繁 `reset`。高频日志本就不该靠实时监控看。
- **格式不可热切换**：改格式需重启（设计取舍）。
- **文件 truncate 竞态**：单实例内 `Write` 已持 `fileWriter.mu`（stat/truncate/append 实际串行）；TODO 备注的是「多 fileWriter 实例共享同一文件」或未来重构的跨实例风险，一期不处理。
- **命令行覆盖**：热更写回后同步更新内存参数值。
- **文件清空丢存档**：设计行为，留标记便于排查。
- **多实例局限**：ring 是进程内，多进程各看各的；单进程底座可接受。
- **影响面**：`easyserver/log` 被 15 处使用，P1 保证默认路径零变化。
- **默认模板丢弃 attr（L3）**：默认模板仅输出 `time/level/msg`，**排障字段默认全丢**——如 obs 的 `trace_id/drop_count`、mq 的 `err/id`、main 的 `upstream/listen/admin`。需要这些字段时须外挂 `log.tpl` 显式引用（或直接使用实时监控/文件存档排查）。属模板化输出的既定行为，非缺陷。
- **双写 IO**：文件失败不影响 console/ring（**文件 writer 异步写 + 逐目标独立处理**，红线保证）。
- **文件异步丢弃**：队列满/落盘失败时文件日志丢弃（`dropped` 计数可查）——存档完整性不保证，红线只保证主通道不受影响。