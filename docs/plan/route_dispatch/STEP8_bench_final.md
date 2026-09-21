# STEP8：性能基准与项目终验

状态：待实施

## 目标

性能门槛验证与全项目终验：① benchmark——100 与 1000 规则两档下单次匹配 ≤ 10µs（绝对门槛），结果留档回填区；② 全量测试 / `go vet` / 生产构建（无 dev tag）；③ 文档同步清单逐项核对（含 DISPATCH_RULES 删除后的配置文档清理）；④ 实请求终验（上层验收标准 2/3/4 的实操复现）。

## 改动文件清单

- 新增 `plugins/dispatch/bench_test.go`：匹配 benchmark（100/1000 规则两档，含 domain 命中/未命中/纯路径场景）
- 修改：文档同步清单中发现的缺漏项（README.md、docs/CONFIGURATION.md、docs/COMPONENTS.md §3.2、docs/DATA_DICT.md、docs/webui/pages.md、接口文档——按缺漏实际情况补）

## 实施步骤（完成一项立即勾选保存；粒度到不可再分的动作）

- [ ] bench_test.go：构造 100/1000 规则快照（含域名规则、前缀/精确/模式混合），`go test -bench` 测单次匹配耗时
- [ ] benchmark 结果留档（两档数字 + 环境说明写入回填区；不达标 → 触发 IMPL §5 性能回退预案：加载期按 host 有无/首段分桶，回填 STEP 分析后重测）
- [ ] 全量回归：`go test ./... -race -count=1`、`go vet ./...`、生产构建 `go build -o bin/rocksys ./cmd/rocksys`
- [ ] 文档同步清单逐项核对：README（功能与用法）、CONFIGURATION.md（DISPATCH_RULES 已删、DISPATCH_ENABLED 说明）、COMPONENTS.md §3.2（dispatch 组件能力更新）、DATA_DICT.md（六表+三枚举与实现一致）、pages.md（路由分发页章节）、接口文档（19 端点）、配置项注册清单（title/usage 与实现一致）
- [ ] 实请求终验（上层验收标准 2）：登记 3 节点 → 建均衡器 A（round_robin+sticky）与 B（least_conn）引用同批节点 → 建规则（域名+路径、纯路径、域名默认兜底各至少一条）→ `curl -H "Host: a.com"` 实请求验证：分流正确、sticky Cookie 种植与直路由生效、域名兜底链正确、停一节点探活摘除后流量切走（截图/响应留证）
- [ ] 命中测试器一致性核对（验收标准 3）：match-test 结果与实请求的规则命中、均衡器判定一致（节点选点为动态参考值不要求一致）
- [ ] 降级与恢复验证（验收标准 4）：DB 不可用 → 组件状态透出、全部请求走默认 upstream、无 5xx；DB 就绪后后台重试自动完成首次构建、无需人工重载

## 验证

- 接手核实命令（拆步时预写；无副作用、可重复执行；断点后一条命令自证已完成部分完好）：
  `go test ./plugins/dispatch/ -bench . -benchtime=1x -run '^$' && go vet ./...`
  （预期：benchmark 可执行；vet 干净）
- 本步完整验证（勾选＝该项已真实执行且通过，凭意图不得勾选；按需含受影响面的全量回归）：
  - [ ] `go test ./... -race -count=1` 全绿
  - [ ] `go vet ./...` 通过
  - [ ] `go build -o bin/rocksys ./cmd/rocksys` 生产构建成功
  - [ ] benchmark 两档均 ≤ 10µs（留档数字）
  - [ ] 实请求终验四项全过（分流/sticky/域名兜底/探活摘除）+ 截图留证
  - [ ] 文档同步清单逐项打勾核对完毕

## 完成标准

- 上层验收标准 1–5 全项达成（标准 6「明确不做」为边界清单，无越界即满足）
- 全部 STEP「已实施」后：按宪法 §5 终验 → 结果回写 DESIGN「验收结论与已知边界」→ 呈报临时决策（如有）→ 项目状态改「待人类验收」

## 实施回填区

### 产物锚点清单（拆步时预写，实施中随手更正；改名同步更新）
- 新增文件：`plugins/dispatch/bench_test.go`
- 修改文件：文档同步缺漏项（按核对结果）
- 留档：benchmark 两档结果、实请求终验截图清单

### 偏差与现场记录
- <设计与现实的偏差、试过放弃的方案、阻塞原因——先写这里再继续动手>
