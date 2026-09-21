# STEP8：性能基准与项目终验

状态：已实施

## 目标

性能门槛验证与全项目终验：① benchmark——100 与 1000 规则两档下单次匹配 ≤ 10µs（绝对门槛），结果留档回填区；② 全量测试 / `go vet` / 生产构建（无 dev tag）；③ 文档同步清单逐项核对（含 DISPATCH_RULES 删除后的配置文档清理）；④ 实请求终验（上层验收标准 2/3/4 的实操复现）。

## 改动文件清单

- 新增 `plugins/dispatch/bench_test.go`：匹配 benchmark（100/1000 规则两档，含 domain 命中/未命中/纯路径场景）
- 修改：文档同步清单中发现的缺漏项（README.md、docs/CONFIGURATION.md、docs/COMPONENTS.md §3.2、docs/DATA_DICT.md、docs/webui/pages.md、接口文档——按缺漏实际情况补）

## 实施步骤（完成一项立即勾选保存；粒度到不可再分的动作）

- [x] bench_test.go：构造 100/1000 规则快照（含域名规则、前缀/精确/模式混合），`go test -bench` 测单次匹配耗时
- [x] benchmark 结果留档（两档数字 + 环境说明写入回填区；不达标 → 触发 IMPL §5 性能回退预案：加载期按 host 有无/首段分桶，回填 STEP 分析后重测）
- [x] 全量回归：`go test ./... -race -count=1`、`go vet ./...`、生产构建 `go build -o bin/rocksys ./cmd/rocksys`
- [x] 文档同步清单逐项核对：README（功能与用法）、CONFIGURATION.md（DISPATCH_RULES 已删、DISPATCH_ENABLED 说明）、COMPONENTS.md §3.2（dispatch 组件能力更新）、DATA_DICT.md（六表+三枚举与实现一致）、pages.md（路由分发页章节）、接口文档（19 端点）、配置项注册清单（title/usage 与实现一致）
- [x] 实请求终验（上层验收标准 2）：登记 3 节点 → 建均衡器 A（round_robin+sticky）与 B（least_conn）引用同批节点 → 建规则（域名+路径、纯路径、域名默认兜底各至少一条）→ `curl -H "Host: a.com"` 实请求验证：分流正确、sticky Cookie 种植与直路由生效、域名兜底链正确、停一节点探活摘除后流量切走（截图/响应留证）
- [x] 命中测试器一致性核对（验收标准 3）：match-test 结果与实请求的规则命中、均衡器判定一致（节点选点为动态参考值不要求一致）
- [x] 降级与恢复验证（验收标准 4）：DB 不可用 → 组件状态透出、全部请求走默认 upstream、无 5xx；DB 就绪后后台重试自动完成首次构建、无需人工重载（单元测试已覆盖该语义；真实停库演练待主流程执行）

## 验证

- 接手核实命令（拆步时预写；无副作用、可重复执行；断点后一条命令自证已完成部分完好）：
  `go test ./plugins/dispatch/ -bench . -benchtime=1x -run '^$' && go vet ./...`
  （预期：benchmark 可执行；vet 干净）
- 本步完整验证（勾选＝该项已真实执行且通过，凭意图不得勾选；按需含受影响面的全量回归）：
  - [x] `go test ./... -race -count=1` 全绿（race 因 Windows 环境无 cgo 降级为 `go test ./... -count=1`：27 包全绿、无 FAIL）
  - [x] `go vet ./...` 通过
  - [x] 生产构建成功（命名 `bin/rocksys.prod`，不覆盖运行中二进制）
  - [x] benchmark 两档均 ≤ 10µs（留档数字）——首轮 100 档达标、1000 档超标触发回退预案；经两项加载期优化后复测**两档全达标**（1000 档稳定 5.6-7.2µs），见回填区「性能优化回填」
  - [x] 实请求终验四项全过（分流/sticky/域名兜底/探活摘除）+ curl 响应留证（无浏览器实看，主流程执行）
  - [x] 文档同步清单逐项打勾核对完毕

## 完成标准

- 上层验收标准 1–5 全项达成（标准 6「明确不做」为边界清单，无越界即满足）
- 全部 STEP「已实施」后：按宪法 §5 终验 → 结果回写 DESIGN「验收结论与已知边界」→ 呈报临时决策（如有）→ 项目状态改「待人类验收」

## 实施回填区

### 产物锚点清单（拆步时预写，实施中随手更正；改名同步更新）
- 新增文件：`plugins/dispatch/bench_test.go`、`docs/api/dispatch.md`
- 修改文件：`docs/CONFIGURATION.md`（DISPATCH_RULES → DISPATCH_ENABLED）、`docs/COMPONENTS.md`（§3.2 全文重写为三层体系）、`README.md`（功能条目更正 + 新增「路由分发」章节）、`docs/HTTP_DATAFLOW.md`（④ dispatch 环节描述更正）、`docs/api/README.md`（总览表补 58-76 行 + 按端点定位表）、`docs/plan/route_dispatch/STEP8_bench_final.md`
- 留档：benchmark 两档结果（见下）、实请求终验 curl 响应（见下）

### 偏差与现场记录

**benchmark 结果**（`go test -bench . ./plugins/dispatch/`；Windows amd64，i5-10400 @2.90GHz，12 线程；快照含每 5 条一域名 + 前缀/精确/模式轮转混合）：

| 场景 | 100 规则 | 1000 规则 |
|---|---|---|
| 域名命中 | 5.2µs/op（48 allocs） | 51.0µs/op（528 allocs） |
| 纯路径命中 | 4.6µs/op（50 allocs） | 44.5µs/op（530 allocs） |
| 全未命中 | 5.7µs/op（52 allocs） | 54.8µs/op（532 allocs） |

- **100 档达标（≤10µs）；1000 档超标约 4.5-5.5 倍**——已触发 IMPL §5 性能回退预案评审条件，按红线仅报告、未擅改架构。量级成因：线性全表扫描 + 模式规则每次匹配重复分段分配（splitSegments ~2 allocs/次），超标主要来自分配而非比较。候选优化方向（供回退预案分析）：① 加载期预分段模式串（RuleRT 持缓存，消每请求分配）；② 按 host 有无/首段分桶，缩小扫描集。均属加载期/结构优化，不改匹配语义。
- race 测试：Windows 环境无 cgo，`-race` 不可用，降级为无 race 全量测试（27 包全绿）。
- 生产构建产物命名 `bin/rocksys.prod`（dev 服务运行中，不覆盖 `bin/rocksys`）。
- 代码注释 STEP 引用：`plugins/dispatch` 核心文件与 `cmd/rocksys/main.go` 存在 30+ 处 STEP/计划文档引用（过程留痕），超出本步「新增面」边界未改动，留待主流程决定是否统一清理。

**实请求终验**（网关 :8080，后端 9901/9902/9903 临时 Go 程序，节点探活 2s 周期/1s 超时）：

1. 分流：`curl -H "Host: a.com" :8080/api/x` → 200 `backend-9901 uri=/api/x`（核验池A round_robin，单节点全落 9901）；
2. sticky：响应头 `Set-Cookie: rocksys_node=3; Path=/; HttpOnly; SameSite=Lax`；带 `Cookie: rocksys_node=3` 请求 → 稳定落 9901；
3. 域名兜底：`b.com/whatever/deep` → 200 `backend-9901`（序号 999 兜底规则命中核验池A）；未命中域名 `c.com` → **502**（走默认 upstream `127.0.0.1:9000` 连接拒绝，属预期观测——默认后端无服务，非 dispatch 缺陷）；
4. 纯路径：`/app/*` → 均衡器B（least_conn）三轮请求呈 9901/9902/9903 均匀轮转（串行请求在途恒 0，平局回落轮询游标，符合设计）；
5. 探活摘除：杀 9902 → 约 4s（一个探活周期 + 余量）后 `/admin/dispatch/health` 该节点 `bad`，后续 /app 请求只落 9901/9903；重启 9902 → 4s 后恢复 `ok`，轮转回归三节点；
6. 命中测试器一致性：match-test 与实请求逐场景一致（a.com/api/x → 规则#2→核验池A→node；/app/x → 规则#3→均衡器B；b.com/z → 规则#4 序号999；c.com → default_upstream）；节点选点为动态参考值，与文档口径一致。

**终验后清理**：终验规则（#3/#4）、终验均衡器B、终验节点（9902/9903）均经 adminapi 软删；保留核验节点A（含探活配置）、核验池A、a.com 规则；三个临时后端进程已杀、`/tmp/rocksys_bench` 已删除。

### 性能优化回填（回退预案落地：1000 档超标整改）

**优化前后对比**（同环境同口径：Windows amd64，i5-10400 @2.90GHz，12 线程；`-benchtime=10x`；括号 allocs/op）：

| 场景 | 优化前 100 | 优化前 1000 | 优化后 100 | 优化后 1000 |
|---|---|---|---|---|
| 域名命中 | 28.8µs（48） | 50.5µs（528） | 0.9-1.5µs（1） | 6.4-8.8µs（1） |
| 纯路径命中 | 4.0µs（50） | 32.1µs（530） | 0.9-2.5µs（1） | 6.4-7.2µs（1） |
| 全未命中 | 4.0µs（52） | 39.2µs（532） | 0.9µs（1） | 5.7-6.8µs（1） |

- 大样本复核（`-benchtime=2000x`）：100 档全部 ≈0.65-0.7µs；1000 档 5.6-6.3µs——**两档均 ≤10µs 达标**，单次匹配分配 532 → 1（仅模式规则命中时的参数 map 或路径切分）。
- 优化项（纯加载期/热路径结构优化，不改匹配语义）：
  1. **加载期预分段**：模式规则 pattern 段数组在 BuildGraph 时切好存入 `RuleRT.patSegs`，匹配零分配直接消费；
  2. **按 Host 预计算候选序**：BuildGraph 派生 `RouteSnapshot.anyDomain`（无域名规则子序列）与 `byDomain[域名]` = 「无域名规则 ∪ 该域名规则」按 (match_order,id) 两路归并的候选数组；Match 按归一 Host 取候选数组依序判定、命中即停；
  3. **路径单次切分**：单次 Match 内请求路径只在首个模式规则处切分一次、全候选复用（切分结果只依赖 path 本身，跨候选复用不改判定结果）。

**语义保持论证**：快照 `Rules` 已按 (match_order,id) 全局升序，任一子序列（无域名规则集、某域名规则集）继承该全序；两个各自有序的子序列按 (match_order,id) 归并，结果即二者并集在全局序中的唯一排列，故候选数组内「依序首个命中」与原全表线性扫描「命中即停」选出的规则完全一致（match/snapshot 既有测试零改动全绿佐证）。

**简单分桶为何错误（反例）**：设 R1(order=5, domain="a.com", 精确 /x)、R2(order=6, domain="", 精确 /x)、R3(order=7, domain="a.com", 精确 /x)，请求 Host=a.com、Path=/x：全局序正确冠军是 R2（order 最小）；若「先查 a.com 域名桶、未中再扫通用表」，会先扫到桶内 R3 而误判 R3 命中——路径重复时赢家取决于桶扫描次序而非全局序，破坏命中优先语义。归并序把 R1/R2/R3 还原为全局次序 R1→R2→R3，天然正确。空 Host 只扫无域名规则亦等价：非空域名规则与空 Host 精确比对必不等，跳过无语义差。

**注释清理**：`plugins/dispatch` 核心文件与 `cmd/rocksys/main.go` 中全部计划文档引用（STEPn/Sn/Dn/DESIGN_PLAN/IMPL_PLAN/TRAFFIC_ANALYSIS 等）改为自包含表述，共 64 处（match 5、model 10、snapshot 3、select 3、sticky 2、tailfin 4、registry 9、healthcenter 6、dispatch.go 6、admin.go 11、main.go 5），测试文件按红线零改动；清理后 `grep` 复核零残留，行为不变。

**验证留档**：`CGO_ENABLED=1 go test ./plugins/dispatch/ -race -count=1` 全绿（match/snapshot/select/sticky/model 测试零改动）；`go test ./... -count=1` 全绿；`go vet ./...` 通过；基准留档数字如上表（含 `-benchtime=1x` 单次样本：两档 2.5-12.1µs，含预热噪声，正式口径以 10x/2000x 为准）。
- 【主流程补验】2026-09-22 DB 降级演练（真实环境）：bin/.env 置坏 DSN 重启 → /admin/dispatch/health 明确报「数据库未配置」引导、网关请求全部走默认 upstream（502=上游连接拒绝）、零 panic；恢复 DSN 后自动重建 ready=true。终验环境保留：核验节点A/核验池A/a.com 规则；9901 后端已停（属终验清理，health 显示 bad 为正确表现）。
- 【性能优化回退执行】1000 档首测超标（≈50µs），按 IMPL §5 预案实施加载期预分段 + 按域名归并候选序（语义保持，反例论证见上），复测两档 ≤10µs 达标。
