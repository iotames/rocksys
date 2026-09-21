# STEP3：匹配引擎与快照（扁平序号命中即停）

状态：待实施

## 目标

新建规则匹配引擎与快照构建：规则数组按 `(match_order, id)` 稳定升序（仅含启用且未软删行）；Host 归一化（剥端口含 IPv6 `[::1]:80` 方括号形态、转小写、空 Host 保持空）；三类型路径匹配（前缀=段对齐且命中自身——`/api` 命中 `/api` 与 `/api/x` 不匹配 `/apix`、`/` 命中一切含 `/` 本身，D13；精确=全等不做尾斜杠归一；模式=`:param`/`*` 段匹配，沿用现有捕获语义与 `X-Route-Param-*` 注入）；domain 空=匹配任意、非空=精确相等（归一小写比对）；路径匹配大小写敏感、域名匹配不敏感（S1）。domain 构建期统一归一（转小写）放行。快照仅含启用且未软删规则行，**引用有效性不做构建期剔除**（停用均衡器/停用节点不吞规则，fail-closed）。

## 改动文件清单

- 新增 `plugins/dispatch/match.go`：Host 归一化 + 三类型路径匹配 + 逐条判定（domain 条件 × 路径条件）+ 命中即停主流程
- 新增 `plugins/dispatch/snapshot.go`：快照类型（规则有序数组持有 STEP2 对象图引用）+ 构建函数（排序、domain 归一、`X-Route-Param-*` 注入所需参数收集）
- 段匹配语义复用：从现有 `router.go` 提取/复用 `:param`/`*` 段匹配逻辑（提取为独立函数供 match.go 调用；**提取不改行为**，router_test 对应用例随提取迁移至新函数并保持断言原样）
- 新增测试 `plugins/dispatch/match_test.go`、`snapshot_test.go`（表驱动，覆盖 M 表匹配语义口径全项）

## 实施步骤（完成一项立即勾选保存；粒度到不可再分的动作）

- [ ] match.go：`normalizeHost`（剥端口、IPv6 方括号、转小写、空保持空）
- [ ] match.go：三类型路径匹配（前缀段对齐含命中自身 D13 / 精确全等不归一尾斜杠 / 模式段匹配含参数捕获）——模式段匹配经提取的公共段函数实现，与旧 Radix 同语义
- [ ] match.go：`Match(req)` 主流程（依序逐条：domain 为空或与归一 Host 精确相等 且 路径按类型匹配 → 命中返回规则+参数；全未命中返回 nil）
- [ ] snapshot.go：快照构建（`query_active` 行 → STEP2 对象图 → 按 `(match_order, id)` 稳定排序 → domain 归一转小写）
- [ ] match.go/snapshot.go：参数注入辅助（命中模式规则时产出 `X-Route-Param-*` 键值对，供 STEP5 Handle 写入 DataFlow，键名沿用现有惯例）
- [ ] 表驱动测试全项：序号命中即停 / domain（空=任意、精确、剥端口、IPv6、大写归一）/ 前缀（段对齐、命中自身、`/` 命中一切）/ 精确（不归一尾斜杠）/ 模式捕获 / 路径大小写敏感·域名不敏感 / 空 Host / 同序号按 id 升序先后

## 验证

- 接手核实命令（拆步时预写；无副作用、可重复执行；断点后一条命令自证已完成部分完好）：
  `go test ./plugins/dispatch/ -run 'TestMatch|TestSnapshot|TestNormalizeHost' -count=1`
- 本步完整验证（勾选＝该项已真实执行且通过，凭意图不得勾选；按需含受影响面的全量回归）：
  - [ ] `go test ./plugins/dispatch/ -race -count=1` 全绿（含旧 DSL 测试零改动）
  - [ ] `go vet ./plugins/dispatch/` 通过
  - [ ] M 表「匹配语义正确性」行全项均有对应用例且绿（逐项对照）

## 完成标准

- 匹配语义覆盖 DESIGN M 表口径全项（含 D13 命中自身）
- 快照构建含排序与 domain 归一；fail-closed：不做引用有效性剔除
- 段匹配语义与旧 Radix 一致（提取迁移后旧断言不改语义）

## 实施回填区

### 产物锚点清单（拆步时预写，实施中随手更正；改名同步更新）
- 新增文件：`plugins/dispatch/match.go`、`snapshot.go`、`match_test.go`、`snapshot_test.go`
- 修改文件：`plugins/dispatch/router.go`（仅限段匹配函数提取，行为不变）；`router_test.go`（对应用例迁移）
- 新增函数：`normalizeHost`、`Match`、快照构建函数、段匹配公共函数

### 偏差与现场记录
- <设计与现实的偏差、试过放弃的方案、阻塞原因——先写这里再继续动手>
