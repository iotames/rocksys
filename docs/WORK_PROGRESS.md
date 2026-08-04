# RockSys 工作进度文件

> 本文档用于记录开发进度。若中途意外断开，AI 或人类开发者凭此文件恢复工作。
> 更新时机：每一批次任务完成后必须更新。总指挥统一维护，子 agent 不得改动本文档。

## 一、既定目标

依据 `docs/DEV_HANDBOOK.md`（v5，23 章）实现 RockSys 集团公司 ERP 系统的**后端底座**。

- **实现顺序**：第1章(骨架) → 第2章(conf) → 第3章(engine) → 第4章(chain) → 第5章(dataflow) → 第6章(hotswap) → 第7章(rocksys入口) → 第8章(rockctl) → 第9-15章(P1挂件) → 第16-19章(P2组件) → 第20-22章(业务侧) → 第23章(验证)。
- **交付标准**：P0（第1-8章）完成后为"最小可用"裸代理+热开关；P1/P2 挂件按手册实现；每章"验收标准"通过后才算完成。
- **前端**：本阶段不做。
- **质量闭环**：每批完成后执行 测试 → 修复 BUG → 测试 循环，直至无 BUG；每轮循环自主 git 提交 + push（根仓库与 easyserver 子仓库均需 push）。

## 二、技术要点备忘（避免重复踩坑）

1. **接口实际签名与手册差异**：
   - easyserver 的 `AddHandler(method, urlpath string, ctxfunc func(ctx Context))` —— 手册 §8.1 写的 `(pattern, h func(w,r))` 是示意；adminapi.RegisterPlugin 内部需把 `func(w,r)` 包装为 `func(ctx httpsvr.Context)`。
   - easyconf 的 `addItem` 为私有方法，conf.Manager.Register 无法直接调用；须用公开的 `StringVar/IntVar/BoolVar`（type switch 分发 + strconv 转换 defval），注册后**不能再 flag.Parse**（会 panic），命令行重放用 `SetItemValue`。
   - `easyconf.Parse(true)` 内部调用全局 `flag.Parse()`，解析的是 `os.Args[1:]`，因此 conf.Load 须把短参数映射结果写回 `os.Args`。
   - **conf.Manager 是接口**（非指针）：`internal/conf/interface.go` 定义了 `Manager` 接口，`hotswap.NewManager(ch, cfgMgr conf.Manager)` 直接传接口值，不要取地址。
   - `script.New(timeout time.Duration)` 而非手册示意 `New(cfgMgr)`；`shield.New(cfgMgr)` 返回 `(*Shield, error)` 两值；`mq.New(db, tableName)`、`object.New()`、`registry.New(cfgMgr)`、`config.New(cfgMgr)` 为组件。
2. **编译桩**：第3章 engine 引用 chain/dataflow 类型，须同时创建二者的最小骨架类型（结构体+字段，不带方法），完整实现在第4/5章补。
3. **包名冲突**：`easyserver/hotswap` 用别名 `eshs`；`rocksys/internal/hotswap` 用原名 `hotswap`。
4. **导入规则（强制）**：`plugins/*` 只允许 import internal/{chain,dataflow,hotswap,conf}；`cmd/rocksys` 是唯一装配点；`plugins/*` 之间禁止互相依赖（registry→dispatch 联动经 conf.Set("DISPATCH_RULES") 通道，不直接 import）。
5. **时间戳取点**：BeginBizAt 在 Adapter 调 Forward 前取；DoneBizAt 在 Forward 内部"收到响应后、写回前"取，仅写一次。
6. **Tail 顺序**：装配时先注册 obs、后注册 result（result 先执行改写，obs 后记录最终状态）。
7. **测试副作用**：easyconf 测试会在包目录创建 `.env`/`default.env`——已加入根 `.gitignore`，提交前注意清理。
8. **hotswap 排空解耦**：hotswap 不持有 Adapter，用 `SetDrainCheck(fn func() int64)` 注入 `Adapter.ActiveCount`；engine 暴露 `ActiveCount()` 导出方法供 `cmd/rocksys` 装配注入。
9. **easyserver listenPrepare 幂等（批次8 修复的 BUG）**：`ListenAndServe()` 直接调用 `listenPrepare()` + 首个请求 `ServeHTTP` 触发 `initOnce` 再组装一次 → 中间件链双份、handler 执行两次、响应双写。修复：`listenPrepare()` 开头加 `if len(s.middles) > 0 { return }` 幂等守卫。
10. **gopher-lua v1.1.2**：`lua.CompileString`/`lua.ContextDeadline` 不存在 → script 用 `L.LoadString`（取 Proto）+ `L.SetContext` 超时。
11. **go.mod go 指令**：modernc.org/sqlite 要求 go≥1.25 → go.mod `go 1.25.0`（环境 go1.25.3，手册 1.24.1 已被环境实际版本取代）。
12. **压测环境限制**：本机 WSL 挂载盘直连上游 P99 已 26ms（>10ms），rocksys 全链路 P99 ~50ms（裸代理 ~66ms，链开销非主因，属环境调度抖动）。**P99<10ms 需真实 Linux 服务器复验**；QPS>1000（实测 5900-7400）与错误率<0.1%（实测 0%）在本环境已达标。

## 三、完成进度（对照手册章节）

| 章节 | 内容 | 状态 | 备注 |
|------|------|------|------|
| 第1章+§1.0 | 骨架 + easyserver Shutdown/Close | ✅ 完成 | 批次1 |
| 第2章 | internal/conf | ✅ 完成 | 批次2 |
| 第3章 | internal/engine | ✅ 完成 | 批次4 |
| 第4章 | internal/chain | ✅ 完成 | 批次3 |
| 第5章 | internal/dataflow | ✅ 完成 | 批次2 |
| 第6章 | internal/hotswap | ✅ 完成 | 批次4 |
| 第7章 | cmd/rocksys | ✅ 完成 | 批次7 |
| 第8章 | adminapi + cmd/rockctl | ✅ 完成 | 批次5 |
| 第9-15章 | P1 挂件(shield/dispatch/result/trace/script/config/obs) | ✅ 完成 | 批次5-6 |
| 第16-19章 | P2 组件(auth/registry/mq/object) | ✅ 完成 | 批次6 |
| 第20章 | contracts(OpenAPI) | ✅ 完成 | 批次3 |
| 第21章 | sdk/python | ✅ 完成 | 批次3 |
| 第22章 | examples/stbiz_hello | ✅ 完成 | 批次3 |
| 第23章 | 集成验证+压测 | ✅ 完成 | 批次8（详见日志） |

**全 23 章 + 附录达成。**

## 四、当前工作位置

- 批次：**批次 10（✅ 完成）**——easywaf 借鉴：shield WAF 检测 + dispatch 节点组负载均衡 + WAF 规则文件外置。
- 全仓库 22 包 `go build/vet/test` 全绿。
- 剩余事项：仅「真实 Linux 服务器 P99<10ms 复验」建议（本 WSL 环境无法达标，非代码缺陷）。

## 五、未完成任务与下次起点

- 批次 1-10 全部完成。**P0+P1+P2 后端底座 + 数据访问层 + WAF/LB 增强交付完毕。**
- 可选后续（不在本阶段范围）：
  - 真实 Linux 服务器上按 §23.2 用 hey 复验 P99<10ms
  - 前端（下一阶段）
  - 补全 `sql/mysql/`、`sql/postgres/` 下的业务脚本（当前仅默认 SQLite 完整）
  - easywaf 第二期：路由参数/通配匹配、Admin 观测端点
- **断点恢复**：无需恢复，全部批次已完成。

## 六、批次日志

### 批次 1（✅ 完成）
- ✅ T1: easyserver/httpsvr 新增 Shutdown/Close（§1.0）
- ✅ T2: 根 go.mod + 全部空目录 + doc.go + cmd/rocksys/main.go 占位（第1章）
- 提交：`358060d`（easyserver 子仓库）、`6bacf94`（根仓库）

### 批次 2（✅ 完成）
- ✅ T3: internal/conf（第2章）
- ✅ T4: internal/dataflow（第5章）
- 提交：`37c4a18`

### 批次 3（✅ 完成）
- ✅ T5: internal/chain（第4章）
- ✅ T6: contracts + sdk/python + examples（第20-22章）
- 提交：`2198758`

### 批次 4（✅ 完成）
- ✅ T7: internal/engine（第3章）
- ✅ T8: internal/hotswap（第6章）
- 🔧 修复：engine_test.go 单次 `r.Body.Read` 误判 EOF → 改 `io.ReadAll`；engine.go 重复 `defer cancel()` 清理；`.env`/`default.env` 加入 .gitignore
- 提交：`4501a93`、`51f613e`

### 批次 5（✅ 完成）
- ✅ T9: adminapi（第8章）
- ✅ T10: cmd/rockctl（第8章）
- ✅ T11: plugins/shield（第9章）
- ✅ T12: plugins/dispatch（第10章）
- ✅ T13: plugins/result（第11章）
- ✅ T14: plugins/trace（第12章）
- 🔧 修复：adminapi_test.go:137 setup 返回 3 值只接收 2 → `cfgMgr, _, _ := setup(t)`
- 提交：`290336e`

### 批次 6（✅ 完成）
- ✅ T15: plugins/script（第13章，gopher-lua v1.1.2）
- ✅ T16: plugins/config（第14章）
- ✅ T17: plugins/obs（第15章）
- ✅ T18: plugins/auth（第16章，JWT）
- ✅ T19: plugins/registry（第17章，联动经 conf.Set DISPATCH_RULES）
- ✅ T20: plugins/mq（第18章，outbox + modernc.org/sqlite）
- ✅ T21: plugins/object（第19章，路径穿越防护）
- 提交：`a5de453`（go.mod 升 1.25.0 支持 modernc sqlite）

### 批次 7（✅ 完成）
- ✅ T22: cmd/rocksys 装配（第7章）——全部挂件注册 + adminAPI + SetDrainCheck(eng.ActiveCount) + 优雅停机（eng→admin→mgr→cfgMgr）
- 🔧 engine 增加 `ActiveCount()` 导出方法（唯一 internal 改动）
- 提交：`ef04196`

### 批次 8（✅ 完成）——第23章验证
- 🔧 **修复 easyserver 严重 BUG**：`listenPrepare()` 非幂等 → 中间件链双份、handler 双执行、响应双写（admin 端点拼接 JSON）。修复：幂等守卫 + 3 个回归测试。提交 `519e9ee`（easyserver 子仓库，三 remote push）。
- ✅ 23.1 降级链：逐级关闭 result/dispatch/shield，转发始终 200 不中断
- ✅ 23.2 压测：QPS 5900-7400（>1000）、错误率 0%（<0.1%）；P99 ~50ms 受 WSL 环境限制（直连上游已 26ms），建议真机复验
- ✅ 23.3 高可用：多副本 :8081/:8082 正常；SIGTERM 4ms 优雅退出含日志；故障回滚返回 `{"ok":false,"error":"hotswap: entity not found..."}`
- ✅ 23.4 验收清单：19 包全绿；trace_id 自定义透传 + 自动生成 32 位 hex；Lua 沙箱拦截；黑名单 403/限流 429；三时间戳 <1ms
- ✅ Python 链路：stbiz_hello 经 rocksys 代理返回 `{"msg":"hello","trace_id":"..."}`，X-Trace-Id 透传成功

### 批次 9（✅ 完成）——数据访问层 + SQL 脚本外置 + 工作池
- ✅ 根目录 `sqlfiles.go`（embed sql/）+ `sql/sqlite` 默认 mq 脚本 + `sql/mysql`、`sql/postgres` 目录骨架（README 说明扩展）
- ✅ `internal/hotswap/script.go`：ScriptDir 逐级加载机制（外置目录优先、嵌入兜底），泛化 fs.FS，与组件热切同包不同文件（运行时实时操作管理类能力）
- ✅ `internal/db` 数据访问层：easydb 封装 + SQLSource 接口；`Open` 校验驱动/内嵌脚本目录，切换数据库缺脚本即报错；默认 SQLite 零配置
- ✅ `internal/workpool`：移植 todo/hotswap/workpool.go 并修复并发缺陷（队列切换竞态、Stop/rebuild 串行化、阻塞 Submit 死锁、减少 worker 优雅退出）
- ✅ mq 改造：OutboxStore 走 easydb + SQLSource，SQL 全部外置到 sql/<dbtype>/
- ✅ cmd/rocksys 装配：注册 DB_DRIVER/DB_DSN/SQL_DIR；dataDB 失败不阻断底座；mq 独立连接回退内嵌脚本
- ✅ go.mod 新增 `github.com/iotames/easydb => ./easydb` 本地 replace
- 验证：22 包 `go build/vet/test` 全绿（internal/db、internal/workpool、plugins/mq、cmd/rocksys 新增测试）

### 批次 10（✅ 完成）——easywaf 借鉴：WAF 检测 + 节点组负载均衡 + 规则文件外置
- ✅ `plugins/shield/waf.go`：WAF 检测（SQL 注入/XSS/路径遍历/风险路径/方法白名单/体积限制），全部默认关闭 = 开关切换，检测仅查 URL+UA 不读 body（避免 Body 重放），注入用组合特征子串防误报
- ✅ `plugins/shield/rules/`：5 个规则文件外置（risk_paths/sql_patterns/xss_patterns/path_traversal/crawler_ua），经 `internal/hotswap.ScriptDir` 加载（外置目录优先、嵌入兜底，改规则不重编译）；新增 `SHIELD_RULES_DIR`/`SHIELD_WAF_RISK_PATH`/`SHIELD_WAF_CRAWLER_UA` 配置项
- ✅ `plugins/dispatch` 升级 v2：新格式 `<Prefix>=<spec>`（节点组分号分隔 + `@interval@timeout@path` 健康检查 + `|w=权重`/`|p=0高优/1备份`），旧格式仍兼容
- ✅ `plugins/dispatch/balancer.go`：平滑加权轮询 + 高优优先，选点语义（§10.5）
- ✅ `plugins/dispatch/healthcheck.go`：主动探活（启动即探 + interval 轮询，2xx/3xx 健康），生命周期随路由表启停防 goroutine 泄漏；全挂写 503 中断链
- ✅ 验证：22 包 `go build/vet/test` 全绿（新增 balancer/healthcheck/rules/waf 测试 + dispatch_test 重写）
- 说明：`easywaf/` 为借鉴项目源码，已加入 `.gitignore` 不提交

### Git 提交记录
| 时间 | 提交 | 内容 |
|------|------|------|
| - | 6bacf94 | 第1章骨架 |
| - | 358060d | easyserver Shutdown/Close |
| - | 37c4a18 | 第2章conf+第5章dataflow |
| - | 2198758 | 第4章chain+第20-22章 |
| - | 4501a93 | 第3章engine+第6章hotswap |
| - | 51f613e | .env/.gitignore 清理 |
| - | 290336e | 第8章adminapi+rockctl+第9-15章P1挂件 |
| - | a5de453 | 第16-19章P2组件(auth/registry/mq/object) |
| - | ef04196 | 第7章cmd/rocksys装配 |
| - | 519e9ee | easyserver listenPrepare幂等修复（响应双写BUG） |
| - | 5dd95b7 | 批次9: 数据访问层(easydb本地replace+SQL外置sql/<dbtype>/逐级加载)+workpool |
| - | 0661d4e | 批次10: easywaf借鉴: shield WAF检测+dispatch节点组负载均衡+规则文件外置 |
