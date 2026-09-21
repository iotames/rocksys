# STEP2：领域模型与选点引擎（旁路新建）

状态：待实施

## 目标

新建运行时对象图（Rule/Upstream/Node/关系展开）与选点引擎：round_robin 平滑加权复用、least_conn 在途计数（选中 +1 及 sticky 直路由计入；递减归 STEP4 Tail 收尾件）、sticky cookie（直路由/种值 Add/失效回落）、加载期校验（引用存在、节点 URL 合法、值与类型匹配、序号范围、domain 归一放行）。**旁路新建文件/类型**：旧 DSL 链（`dispatch.go` Handle→`parseRules`→Radix/chash）保持原样可编译，整体移除归 STEP5。健康态判定经注入的 registry 桩接口完成（选点分布与 sticky 失效回落测试均用桩；STEP4 真 registry 就绪后替换桩并回归本片单测）。

## 改动文件清单

- 新增 `plugins/dispatch/model.go`：运行时对象图类型（RuleSnapshot/UpstreamRT/NodeRT 命名自定，语义即名）+ 关系展开 + 加载期校验 `ValidateGraph`
- 新增 `plugins/dispatch/select.go`：策略选点（round_robin 平滑加权、least_conn 在途最小区分、平局回落轮询游标）+ 高优/备份回落 + 选中计数接口调用
- 新增 `plugins/dispatch/sticky.go`：sticky cookie 直路由判定（节点在当前关系内且健康）/ 种值（`W.Header().Add("Set-Cookie", ...)`，禁止 Set）/ 失效回落 / Secure 属性跟随 X-Forwarded-Proto
- 新增 `plugins/dispatch/registry.go`：registry 接口定义（节点 id → 健康态 + 在途计数读写）+ 内存桩实现（测试注入用）
- 新增测试 `plugins/dispatch/model_test.go` / `select_test.go` / `sticky_test.go`（表驱动，`-race`）

## 实施步骤（完成一项立即勾选保存；粒度到不可再分的动作）

- [ ] model.go：对象图类型与从 DB 行到运行时对象的展开（均衡器含关系展开为节点列表，含权重/优先级）；类型上带算法所需游标/计数字段（并发安全：原子或不可变+替换）
- [ ] model.go：`ValidateGraph` 加载期校验（引用存在、节点 URL 合法 `http(s)://`、weight 正整数、priority ∈ {0,1}、match_order 1–999、path_value 以 `/` 开头、domain 构建期归一转小写放行不拒绝）
- [ ] registry.go：接口（按节点 id 查健康态、在途 +1/-1）+ 内存桩（map + 原子，测试可预设健康/失效态）
- [ ] select.go：round_robin 平滑加权（复用现有 balancer.go r rState 思路，作用于新对象图）；least_conn（读 registry 在途计数取最小，平局回落轮询游标）；高优（priority=0）健康集优先、全不健康回落备份（priority=1）健康集、再无 → 不可用
- [ ] select.go：选中即调 registry 在途 +1（sticky 直路由同样真实计入）
- [ ] sticky.go：直路由判定（Cookie 值=节点 id 在当前均衡器关系内且 registry 判健康；不校验 priority——粘性优先于高优回落）；无 Cookie/失效 → 返回需按策略选点标记；种值方法（Add 追加、HttpOnly、SameSite=Lax、Path=/、无 Max-Age 会话级、https 经 X-Forwarded-Proto 附加 Secure）
- [ ] 表驱动测试：策略分布（round_robin 平滑加权序列断言 / least_conn 并发下偏向低在途）、sticky（直路由/失效回落重种/不跨均衡器误粘/Secure 跟随协议）、优先级回落（高优健康→备份→不可用）、校验矩阵（合法/非法值逐项）

## 验证

- 接手核实命令（拆步时预写；无副作用、可重复执行；断点后一条命令自证已完成部分完好）：
  `go test ./plugins/dispatch/ -run 'TestSelect|TestSticky|TestModel|TestValidate' -race -count=1`
- 本步完整验证（勾选＝该项已真实执行且通过，凭意图不得勾选；按需含受影响面的全量回归）：
  - [ ] `go test ./plugins/dispatch/ -race -count=1` 全绿（含既有 DSL 测试——本步不动旧行为，旧用例零改动全绿）
  - [ ] `go vet ./plugins/dispatch/` 通过
  - [ ] Go 侧枚举常量（path_type/algo/priority）与 STEP1 落库枚举值一致（人工对照 DATA_DICT，一次核对）

## 完成标准

- 新对象图/选点/sticky 全部纯内存可测（不依赖 DB 与真实 HTTP），桩注入健康态
- 旧行为零改动：现有 dispatch_test/router_test/chash_test 不改一行全绿
- 选点与 sticky 语义覆盖 IMPL 切片 2 验证列全项（`-race`）

## 实施回填区

### 产物锚点清单（拆步时预写，实施中随手更正；改名同步更新）
- 新增文件：`plugins/dispatch/model.go`、`select.go`、`sticky.go`、`registry.go`、`model_test.go`、`select_test.go`、`sticky_test.go`
- 新增类型/函数：对象图类型组、`ValidateGraph`、选点入口（Select 类）、sticky 判定/种值方法、registry 接口与桩
- 新增测试：表驱动用例组（分布/粘性/回落/校验矩阵）

### 偏差与现场记录
- <设计与现实的偏差、试过放弃的方案、阻塞原因——先写这里再继续动手>
