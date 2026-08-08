# 审查报告：配置中心管控与 panic 策略

> 审查范围：全库"无业游民"配置项收敛（红线：所有配置项必须经 `conf.Manager.Register` 注册，服务端禁止绕过配置中心直接读环境变量）+ panic 使用策略与日志分级。
> 方法：两轮无上下文只读子 Agent 全库扫描 → 总工逐点核实（读源码、od 验字节）→ 分级收敛。
> 本文件为审查结论 + 实施蓝图；落地以子 Agent 派发完成，最终验证 `make test` / `make vet`。

---

## 一、审查一：配置中心管控

### 1.1 基线（已注册项，合规）

底座 9 项（internal/conf/impl.go `bindBaseVars`）：`ROCKSYS_LISTEN`、`ROCKSYS_UPSTREAM`、`ROCKSYS_TIMEOUT`、`ROCKSYS_CONFIG`、`ROCKSYS_ADMIN`、`ROCKSYS_LOG_LEVEL`、`ROCKSYS_LOG_TO_FILE`、`ROCKSYS_LOG_FILE`、`ROCKSYS_LOG_MAX_SIZE`。

装配期注册（cmd/rocksys/main.go）：`DB_DRIVER`、`DB_DSN`、`SQL_DIR`、`MQ_ENABLED`。

挂件注册：adminapi 3 项（`ADMIN_INITIALIZED`、`ADMIN_JWT_SECRET`、`ROCKSYS_ADMIN_TOKEN`）；shield 15 项（`SHIELD_*`）；auth 4 项（`AUTH_*`）；obs 3 项（`OBS_*`）；dispatch `DISPATCH_RULES`；rewrite `REWRITE_RULES`；copy `COPY_TARGETS`；result 2 项（`RESULT_*`）。

全库**服务端无一处绕过 conf 直接 `os.Getenv/os.LookupEnv/flag` 读配置**，读取侧红线遵守良好。豁免范围（rockctl、`*_integration_test.go` 门控、conf 自身与地基库）全部合规。

### 1.2 无业游民配置项（应注册而未注册）

| # | 位置 | 配置性质 | 现状态 | 处置 |
|---|------|---------|--------|------|
| W1 | plugins/registry/registry.go:31 `DefaultAddr=":9800"` | 注册服务监听端口 | 硬编码，生产装配无注入 | **修**：注册 `REGISTRY_ADDR` |
| W2 | plugins/registry/registry.go:29 `DefaultTTL=30s` | 心跳 TTL | 硬编码 | **修**：注册 `REGISTRY_TTL`（秒） |
| W3 | plugins/object/object.go:18 `defaultBaseDir="./data/object"` | 对象存储根目录 | 硬编码，`New()` 无参数 | **修**：注册 `OBJECT_BASE_DIR` |
| W4 | plugins/mq/mq.go:41-45 五常量（interval/backoff/maxRetries/fetchLimit/httpTimeout） | 消息投递运行参数 | 常量默认；装配从不注入 Options，消费方地址恒缺 → **组件实际不可用** | **修**：注册 `MQ_POLL_INTERVAL`、`MQ_MAX_RETRIES`、`MQ_BASE_BACKOFF`、`MQ_CONSUMER_BASE_URL` |
| W5 | plugins/script/script.go:26 `defaultTimeout=100ms` + cmd/rocksys/main.go:60 `scriptTimeout` | Lua 执行超时 | main 常量注入 | **修**：注册 `SCRIPT_TIMEOUT`（毫秒） |

### 1.3 硬编码魔法值（评估后**不修**，理由见 §3）

engine 连接池/WS dial 超时（engine.go:23 `upstreamDialTimeout=30s`、pool.go:23-29 五参数）、adminapi 安全策略（auth.go:28 `jwtTTL=12h`、handlers_auth.go:24-27 登录限流四参数）、shutdownTimeout、hotswap drainTimeout、workpool、obs 内部队列/查询上限、shield 限流桶上限、conf watcherPollInterval。

### 1.4 运行时文件残留（违反红线 2 精神）

`internal/engine/.env`、`internal/engine/default.env`、`internal/adminapi/.env`、`internal/adminapi/default.env`（测试 `conf.Load` 未清理）、`cmd/rocksys/rocksys.db*`（main_test 建库，cleanup 漏删 `*.db`）。**修**：删除残留 + 补测试清理逻辑。

---

## 二、审查二：panic 策略与日志分级

### 2.1 panic 使用点审计

全库 rocksys 代码 panic 均位于**启动装配期或防御/工具代码**，无运行期 panic。具体：

| 位置 | 阶段 | 结论 |
|------|------|------|
| internal/adminapi/adminapi.go:85/90/95（Register 失败） | 启动装配 | ✅ 合理（编程错误 fail-fast） |
| internal/hotswap/script.go:36（GetScriptDir 单例守卫） | 防御 | ✅ 保留（rocksys 无调用者） |
| internal/hotswap/script.go:143/150（LsDirByEmbedFS） | 工具函数 | ⚠️ 无调用者，保留防御性 panic，**不修**（改签名收益低） |
| internal/chain/chain_test.go（panic 中间件测试） | 测试 | 不算 |

### 2.2 启动期缺失决断检查（必修）

| 位置 | 缺失 | 修复 |
|------|------|------|
| cmd/rocksys/main.go:108-119 | **主引擎/admin 端口绑定失败仅 `log.Error`，进程继续存活**（`ListenAndServe` 返回 error 被吞） | **修**：err channel + select，监听失败 `os.Exit(1)` fail-fast；过滤 `http.ErrServerClosed`（正常停机） |
| cmd/rocksys/main.go:103-105 | StartWatcher 失败仅 log.Error | 弱依赖（仅热更失效），保留 |
| cmd/rocksys/main.go:232-233 | db.Open 失败降级 | 有意设计（不阻断底座），保留 |
| plugins/obs|result|copy|rewrite|dispatch 的 `_ = cfgMgr.Register(...)` | 挂件配置项注册失败被静默吞掉 | **修**：改 `if err := ...; err != nil { log.Warn(...) }`（对齐 auth.go:91） |

### 2.3 运行期吞错（必修）

| 位置 | 现状 | 修复 |
|------|------|------|
| plugins/registry/registry.go:299 `_ = srv.Serve(ln)` | HTTP 服务意外退出无感知 | **修**：补 `log.Error` |
| internal/conf/impl.go:285 `_ = m.reloadFilesLocked()` | 热更重载失败静默（3s 轮询无限静默） | **修**：补 `log.Warn` |

### 2.4 日志分级

全库零 `log.Debug`（符合"debug 之前不宜过多"）；Warn/Error 分级整体合理，无级别错乱。
**澄清**：子 Agent 报告 plugins/obs/store.go、docs/ 等"GBK 乱码"为**误报**——od 字节级验证全部合法 UTF-8（grep 工具在 Windows 下显示层误判），无需修复。

---

## 三、不修项与理由（收敛纪律）

| 项 | 理由 |
|----|------|
| engine 连接池/WS dial 超时 | 属内部转发调优参数，非"离散不受管控配置"；现有默认合理；改动转发核心路径风险>收益，无运维配置诉求证据 |
| adminapi 安全策略（jwtTTL、loginLimiter 四参数） | 内部安全基线常量：登录限流参数暴露配置可能被误配削弱安全（负收益）；JWT TTL 12h 为合理默认，注册需改 newAdminAuth 签名与全部测试调用点，成本>收益，留作独立增强 |
| hotswap LsDirByEmbedFS panic | rocksys 内无调用者，纯防御工具函数；改签名无收益 |
| obs 内部常量（查询上限/队列容量/重试阈值）、workpool、hotswap drainTimeout、conf watcherPollInterval | 纯内部实现细节，非部署配置诉求 |
| shield bucketsMax | 内部限流桶容量上限，防内存膨胀的硬保护 |

**收敛定义**：无业游民高优先级（W1–W5）全部收敛；剩余"硬编码魔法值"均属内部实现或负收益项，报告中列明即止，不无限挑刺。

---

## 四、实施蓝图

### P1 配置注册收敛

1. **plugins/registry/registry.go**：`New(cfgMgr conf.Manager)` 内（cfgMgr 非 nil 时）注册 `REGISTRY_ADDR`（默认 `:9800`）、`REGISTRY_TTL`（默认 `30`，秒）、`REGISTRY_STATIC_FILE`（默认空，可选）；注册后读配置值赋 `addr/ttl/staticPath`；保留 SetAddr/SetTTL/SetStaticPath（测试与自定义装配用）。`serve` 补 `log.Error`。
2. **plugins/object/object.go**：`New(cfgMgr conf.Manager) *Object` 内（非 nil）注册 `OBJECT_BASE_DIR`（默认 `./data/object`），绑定 `&o.baseDir`。
3. **plugins/mq/mq.go**：`MQ` 增加 `options *Options` 字段与 `SetOptions(Options)`；`Start` 内 `opts := defaultOptions()`，`m.options != nil` 时用 `*m.options`（各 setter 0 值兜底）。
4. **cmd/rocksys/main.go**：
   - `object.New(cfgMgr)`（替换 `object.New()`）；
   - mq 分支注册 `MQ_POLL_INTERVAL`（默认 `1000`，毫秒）、`MQ_MAX_RETRIES`（默认 `3`）、`MQ_BASE_BACKOFF`（默认 `100`，毫秒）、`MQ_CONSUMER_BASE_URL`（默认空），构造 `mq.Options` 注入 `mqComp.SetOptions`；
   - 删 `scriptTimeout` 常量，注册 `SCRIPT_TIMEOUT`（默认 `100`，毫秒），`script.New(time.Duration(v)*time.Millisecond)`。
5. 测试适配：registry_test/object_test 的 `New()` → `New(nil)` 或 `New(fake)`；新增注册项默认值断言（如 main_test 检查 default.env 含新键）。

### P2 panic/日志修复

1. **cmd/rocksys/main.go**：启动段改 err channel（cap 2）+ `select { <-quit | err := <-errCh }`；err 非 `http.ErrServerClosed` 时 `log.Error + os.Exit(1)`。
2. **plugins/obs|result|copy|rewrite|dispatch**：`_ = cfgMgr.Register(...)` → `if err := ...; err != nil { log.Warn(...) }`。
3. **internal/conf/impl.go**：watchLoop 内 `_ = m.reloadFilesLocked()` → 失败 `log.Warn`。

### P3 清理与文档

1. 删除残留文件：`internal/engine/.env`、`internal/engine/default.env`、`internal/adminapi/.env`、`internal/adminapi/default.env`、`cmd/rocksys/rocksys.db*`。
2. 测试清理：cmd/rocksys/main_test.go `cleanupEnvFiles` 增删 `rocksys.db*`；internal/engine/engine_test.go、internal/adminapi/adminapi_test.go 的 `conf.Load` 后补 `.env`/`default.env` 清理。
3. 文档同步：README.md、docs/DEV_HANDBOOK.md 配置表补 `ROCKSYS_LOG_TO_FILE`、`ROCKSYS_LOG_FILE`、`ROCKSYS_LOG_MAX_SIZE` 与新增 `REGISTRY_*`、`OBJECT_BASE_DIR`、`MQ_*`、`SCRIPT_TIMEOUT`；docs/webui-api.md MQ_ 分组与实际注册一致；ARCHITECTURE.md 若涉及。

### 验收

- `make test`（go test ./...）与 `make vet`（go vet ./...）全绿。
- 新增注册项进入 default.env 快照（main_test 断言）。
- 无业游民清单（§1.2）清零；剩余魔法值见 §3 清单，报告列明。
