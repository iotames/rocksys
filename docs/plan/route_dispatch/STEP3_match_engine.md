# STEP3：匹配引擎与快照（扁平序号命中即停）

状态：已实施

## 目标

新建规则匹配引擎与快照构建：规则数组按 `(match_order, id)` 稳定升序（仅含启用且未软删行）；Host 归一化（剥端口含 IPv6 `[::1]:80` 方括号形态、转小写、空 Host 保持空）；三类型路径匹配（前缀=段对齐且命中自身——`/api` 命中 `/api` 与 `/api/x` 不匹配 `/apix`、`/` 命中一切含 `/` 本身，D13；精确=全等不做尾斜杠归一；模式=`:param`/`*` 段匹配，沿用现有捕获语义与 `X-Route-Param-*` 注入）；domain 空=匹配任意、非空=精确相等（归一小写比对）；路径匹配大小写敏感、域名匹配不敏感（S1）。domain 构建期统一归一（转小写）放行。快照仅含启用且未软删规则行，**引用有效性不做构建期剔除**（停用均衡器/停用节点不吞规则，fail-closed）。

## 改动文件清单

- 新增 `plugins/dispatch/match.go`：Host 归一化 + 三类型路径匹配 + 逐条判定（domain 条件 × 路径条件）+ 命中即停主流程
- 新增 `plugins/dispatch/snapshot.go`：快照类型（规则有序数组持有 STEP2 对象图引用）+ 构建函数（排序、domain 归一、`X-Route-Param-*` 注入所需参数收集）
- 段匹配语义复用：从现有 `router.go` 提取/复用 `:param`/`*` 段匹配逻辑（提取为独立函数供 match.go 调用；**提取不改行为**，router_test 对应用例随提取迁移至新函数并保持断言原样）
- 新增测试 `plugins/dispatch/match_test.go`、`snapshot_test.go`（表驱动，覆盖 M 表匹配语义口径全项）

## 实施步骤（完成一项立即勾选保存；粒度到不可再分的动作）

- [x] match.go：`normalizeHost`（剥端口、IPv6 方括号、转小写、空保持空）
- [x] match.go：三类型路径匹配（前缀段对齐含命中自身 D13 / 精确全等不归一尾斜杠 / 模式段匹配含参数捕获）——模式段匹配经提取的公共段函数实现，与旧 Radix 同语义
- [x] match.go：`Match(req)` 主流程（依序逐条：domain 为空或与归一 Host 精确相等 且 路径按类型匹配 → 命中返回规则+参数；全未命中返回 nil）
- [x] snapshot.go：快照构建（`query_active` 行 → STEP2 对象图 → 按 `(match_order, id)` 稳定排序 → domain 归一转小写）
- [x] match.go/snapshot.go：参数注入辅助（命中模式规则时产出 `X-Route-Param-*` 键值对，供 STEP5 Handle 写入 DataFlow，键名沿用现有惯例）
- [x] 表驱动测试全项：序号命中即停 / domain（空=任意、精确、剥端口、IPv6、大写归一）/ 前缀（段对齐、命中自身、`/` 命中一切）/ 精确（不归一尾斜杠）/ 模式捕获 / 路径大小写敏感·域名不敏感 / 空 Host / 同序号按 id 升序先后

## 验证

- 接手核实命令（拆步时预写；无副作用、可重复执行；断点后一条命令自证已完成部分完好）：
  `go test ./plugins/dispatch/ -run 'TestMatch|TestSnapshot|TestNormalizeHost' -count=1`
- 本步完整验证（勾选＝该项已真实执行且通过，凭意图不得勾选；按需含受影响面的全量回归）：
  - [x] `go test ./plugins/dispatch/ -race -count=1` 全绿（含旧 DSL 测试零改动）
  - [x] `go vet ./plugins/dispatch/` 通过
  - [x] M 表「匹配语义正确性」行全项均有对应用例且绿（逐项对照，见回填区）

## 完成标准

- 匹配语义覆盖 DESIGN M 表口径全项（含 D13 命中自身）
- 快照构建含排序与 domain 归一；fail-closed：不做引用有效性剔除
- 段匹配语义与旧 Radix 一致（提取迁移后旧断言不改语义）

## 实施回填区

### 产物锚点清单（拆步时预写，实施中随手更正；改名同步更新）
- 新增文件：`plugins/dispatch/match.go`、`snapshot.go`、`match_test.go`、`snapshot_test.go`
- 修改文件：`plugins/dispatch/router.go`（仅限段匹配函数提取，行为不变）；`router_test.go`（对应用例迁移）
- 新增函数：`normalizeHost`、`matchPrefix`、`matchExact`、`matchRoutePath`、`Match`、`BuildSnapshot`、`RouteParamHeaders`（snapshot.go）、`matchSegments`（router.go，公共段匹配）、`RouteParamHeaderPrefix` 常量

### 偏差与现场记录

- 段匹配提取方式为「复用」路线：新增公共单模式段匹配函数 `matchSegments(pattern, path)` 于 router.go（复用既有 `splitSegments`），Radix 树内部逻辑零改动、行为天然不变；语义与 Radix 单链一致（含 `/api/*` 不命中 `/api`——通配至少消费一段、`/` 分段为空命中一切）。
- router_test.go 迁移：`TestRouter_ParamCapture` / `TestRouter_ParamCapture_Multi` / `TestRouter_Wildcard` 三个段匹配用例迁至 match_test.go `TestMatchSegments_Pattern`（断言原样改经 `matchSegments` 直测）；树级整体行为用例（最长匹配、兜底、Handle 注入）留在 router_test.go。
- `Match` 主流程签名为 `Match(snap *RouteSnapshot, host, path string)`（STEP5 Handle 尚未定型，先以快照 + Host/Path 入参；STEP5 装配时由 Handle 取请求值传入）。
- domain 匹配口径：库存裸域名（不含端口，构建期统一转小写），请求 Host 经 `normalizeHost` 剥端口+转小写后与规则 domain 精确相等比对——「剥端口/IPv6/大写归一」语义全部落在请求 Host 侧，用例照此覆盖。
- normalizeHost 细节：裸 IPv6（多冒号无方括号，如 `::1`）不含端口整体保留（`LastIndex(":")` 会误剥）；方括号形态 `[::1]:80` 剥端口后保留方括号 `[::1]`。

### M 表「匹配语义正确性」逐项对照（用例 → 位置）

| M 表口径项 | 对应用例（match_test.go 除注明外） |
|---|---|
| 序号命中即停 | `TestMatch_Semantics`（"序号命中即停-前缀/精确"） |
| domain 空=任意 | `TestMatch_Semantics`（"序号命中即停-前缀"，规则 id=2 无 domain） |
| domain 非空=精确（归一小写） | `TestMatch_Semantics`（"domain-精确归一小写"、"domain-不匹配则继续下条"） |
| Host 剥端口 | `TestNormalizeHost`（"api.example.com:8443"）；`TestMatch_Semantics`（"domain-剥端口归一"） |
| IPv6 方括号形态 | `TestNormalizeHost`（"[::1]:80"、"[::1]"、裸 "::1"）；`TestMatch_Semantics`（"domain-IPv6方括号"） |
| 大写归一 | `TestNormalizeHost`（"Example.COM"）；`TestMatch_Semantics`（"domain-精确归一小写"） |
| 前缀=段对齐且命中自身 | `TestMatch_Semantics`（"前缀-命中自身"、"前缀-段对齐子路径"、"前缀-不匹配非段边界" /apix） |
| `/` 命中一切含 / 本身（D13） | `TestMatch_Semantics`（"根兜底-任意路径"、"根兜底-根本身"） |
| 精确=全等不归一尾斜杠 | `TestMatch_Semantics`（"精确-全等命中"、"精确-尾斜杠不归一"） |
| 模式=:param/* 段匹配+捕获 | `TestMatchSegments_Pattern`；`TestMatch_Semantics`（"模式-捕获参数/段数不足/域名不符跳过"） |
| X-Route-Param-* 注入 | `TestMatch_HeaderParams`；snapshot_test.go `TestSnapshot_RouteParamHeaders` |
| 路径大小写敏感 | `TestMatch_Semantics`（"路径大小写敏感" /API/x） |
| 空 Host | `TestNormalizeHost`（"" 保持空）；`TestMatch_Semantics`（"空Host-任意domain空规则"） |
| 同序号按 id 升序先后 | `TestMatch_SameOrderByIDAsc`；snapshot_test.go `TestSnapshot_BuildSortAndDomain` |
| 快照仅启用且未软删、fail-closed 不做引用有效性剔除 | snapshot_test.go `TestSnapshot_FailClosed`、`TestSnapshot_ReferIntegrity` |
