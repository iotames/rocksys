# STEP4：健康检查中心与 Tail 收尾件

状态：已实施

## 目标

落地健康检查中心（供应商-消费者）与 least_conn 在途递减收尾：
① 探活任务管理器——任务集 = Rebuild 时计算「被启用均衡器经有效关系引用的启用节点」去重集合；每节点一个探活 goroutine（参数取节点表 `hc_interval_ms`/`hc_timeout_ms`/`hc_path`；path 为空 = 不探活、视为健康）；启动即探一次防窗口期；2xx/3xx 健康、其余判死；任务集差量增减（新增启探、移除停探并等待退出；探活参数变更视为差量重启该节点任务）。
② 内存 registry（真实现，替换 STEP2 桩）：节点 id 索引、atomic 健康态三态（健康/不健康/未探活）+ 在途计数；记录集 = 探活任务集 ∪ 免探活登记集（`hc_path` 为空的被引用节点登记为健康态、显绿）；任务差量移除即清记录、节点转灰；「跨热更保序」保留不清零仅指任务仍保留的节点；递减饱和处理（≥0，节点失引用清记录后重新登记，迟到递减不得打成负数；记录不存在时递减 no-op）。
③ Tail 收尾件：独立 Name 挂 chain.Tail、Handle 直通放行返回 true、OnResponse 按 DataFlow 中的节点 id 递减（节点 id 跨热更稳定，不经快照 url 反查，杜绝 Rebuild 替换快照后递减失配）。

## 改动文件清单

- 新增 `plugins/dispatch/healthcenter.go`：探活任务管理器（任务集计算、差量增减、goroutine 生命周期、探活 HTTP 客户端复用、超时独立于转发链）
- 修改 `plugins/dispatch/registry.go`：真 registry 实现（原子三态 + 在途计数 + 记录集口径 + 饱和递减 + no-op），保留接口不变、桩移入测试文件或删除
- 新增 `plugins/dispatch/tailfin.go`：Tail 收尾件（独立中间件类型，Name 独立如 `dispatch-tail`；Handle 直通；OnResponse 读 DF 节点 id 递减；Start/Stop 为 no-op——生命周期由主件统一驱动）
- 新增测试 `plugins/dispatch/healthcenter_test.go`、`registry_test.go`、`tailfin_test.go`（`-race`）

## 实施步骤（完成一项立即勾选保存；粒度到不可再分的动作）

- [x] registry.go 真实现：记录集 = 任务集 ∪ 免探活登记集；三态原子读写；在途 +1/-1（饱和 ≥0、记录缺失 no-op）
- [x] healthcenter.go：任务集计算（去重集合，来源对象图）+ 差量算法（新增/移除/参数变更重启）
- [x] healthcenter.go：单节点探活 goroutine（启动即探、周期循环、2xx/3xx 判据、path 空跳过探测恒健康）；停止经 context/chan 排空退出
- [x] healthcenter.go：组件 Stop 全量排空接口（供 STEP5 主件 Stop 调用）
- [x] tailfin.go：收尾件类型（Tail 槽位、Handle 直通、OnResponse 按 DF 键读节点 id 递减；DF 键名沿用 `rocksys:` 前缀惯例，如 `rocksys:dispatch_node_id`）
- [x] STEP2 桩退场：select/sticky 测试改注入真 registry（或保留桩仅测试用），回归 STEP2 单测
- [x] 测试：多均衡器引用同节点只探一份 / Rebuild 差量（增/删/参数变更重启）与跨热更保序（保留节点状态不清零、移除节点转灰）/ goroutine 无泄漏（`-race` + runtime.NumGoroutine 前后对照）/ 收尾件递减跨 Rebuild（旧快照选中、新快照替换后递减仍命中同节点 id）不失配 / 饱和递减不为负

## 验证

- 接手核实命令（拆步时预写；无副作用、可重复执行；断点后一条命令自证已完成部分完好）：
  `go test ./plugins/dispatch/ -run 'TestHealth|TestRegistry|TestTailfin' -race -count=1`
- 本步完整验证（勾选＝该项已真实执行且通过，凭意图不得勾选；按需含受影响面的全量回归）：
  - [x] `go test ./plugins/dispatch/ -race -count=1` 全绿（含 STEP2/3 用例随真 registry 回归）
  - [x] `go vet ./plugins/dispatch/` 通过
  - [x] goroutine 泄漏用例：热更重建 N 次后 goroutine 数不增长（泄漏用例单独连跑 3 次均绿）

## 完成标准

- 探活任务集差量语义完整（含参数变更重启），无 goroutine 泄漏测试兜底
- registry 记录集口径正确（免探活登记显绿、差量移除转灰、保序仅限保留节点）
- 递减跨 Rebuild 不失配（按节点 id，非 url 反查）；饱和处理不为负
- Tail 收尾件可独立实例化（装配归 STEP5）

## 实施回填区

### 产物锚点清单（拆步时预写，实施中随手更正；改名同步更新）
- 新增文件：`plugins/dispatch/healthcenter.go`、`tailfin.go`、`healthcenter_test.go`、`registry_test.go`、`tailfin_test.go`
- 修改文件：`plugins/dispatch/registry.go`（桩 → 真实现）；STEP2 测试文件（注入对象随动）
- 新增类型/函数：任务管理器、差量算法、探活 goroutine 主循环、真 registry、Tail 收尾件类型
- 实际落点：`HealthCenter`（`NewHealthCenter(reg)`，入口 `Rebuild(*GraphInput)` / 全量排空 `Stop()`，成员表 `members` + 任务表 `tasks` 双 map 差量）；`Registry`（`NewRegistry()`，`Ensure(id, init)` / `SetHealth` / `Remove` / `Len`，实现既有 `NodeRegistry` 接口）；`TailFin`（`NewTailFin(reg)`，Name=`dispatch-tail`，Slot=Tail，编译期断言 chain.Middleware/ResponseHook/MiddlewareLifecycle）；DF 键常量 `DFKeyDispatchNodeID = "rocksys:dispatch_node_id"`（定义于 tailfin.go，STEP5 主件选点后写入）；兜底默认 `defaultHCIntervalMS=5000` / `defaultHCTimeoutMS=3000`

### 偏差与现场记录
- `NodeRT` 不携带探活参数（仅 ID/URL/Weight/Priority），任务集计算改从 `GraphInput` 行（NodeRow 的 hc_* 字段）计算，STEP5 主件 Rebuild 时把同一份行传给 `HealthCenter.Rebuild` 即可，语义与设计一致（来源仍是对象图输入）。
- 差量移除除任务表外需「记录集成员表」（`members`）：免探活节点没有任务，仅扫任务表会漏清其 registry 记录（实测踩到：免探活节点失引用后记录残留未转灰），故记录集口径以成员表为准、任务表只管 goroutine。
- hc_interval_ms/hc_timeout_ms 非正值时兜底默认 5s/3s（设计未明说，避免 ticker/超时为 0 失效）。
- `MemRegistry` 桩保留原位（registry.go，仅测试用）；select/sticky 测试注入对象零改动，STEP2/3 用例随真 `Registry` 回归通过。
- 泄漏用例基线差 1 的排查：探活 `http.Client` 空闲连接池的读写 goroutine 在 Stop 后驻留，`Stop()` 补 `CloseIdleConnections()` 后稳定（连跑 3 次基线精确回落）。
- 探活测试全部走 httptest 本地服务器（含 503/404 判死用例），无外网依赖。
