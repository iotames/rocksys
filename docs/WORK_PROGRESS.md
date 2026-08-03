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
2. **编译桩**：第3章 engine 引用 chain/dataflow 类型，须同时创建二者的最小骨架类型（结构体+字段，不带方法），完整实现在第4/5章补。
3. **包名冲突**：`easyserver/hotswap` 用别名 `eshs`；`rocksys/internal/hotswap` 用原名 `hotswap`。
4. **导入规则（强制）**：`plugins/*` 只允许 import internal/{chain,dataflow,hotswap,conf}；`cmd/rocksys` 是唯一装配点；`plugins/*` 之间禁止互相依赖。
5. **时间戳取点**：BeginBizAt 在 Adapter 调 Forward 前取；DoneBizAt 在 Forward 内部"收到响应后、写回前"取，仅写一次。
6. **Tail 顺序**：装配时先注册 obs、后注册 result（result 先执行改写，obs 后记录最终状态）。
7. **测试副作用**：easyconf 测试会在包目录创建 `.env`/`default.env`——已加入根 `.gitignore`，提交前注意清理。
8. **hotswap 排空解耦**：hotswap 不持有 Adapter，用 `SetDrainCheck(fn func() int64)` 注入 `Adapter.ActiveCount`；`cmd/rocksys` 装配时必须注入。

## 三、完成进度（对照手册章节）

| 章节 | 内容 | 状态 | 备注 |
|------|------|------|------|
| 第1章+§1.0 | 骨架 + easyserver Shutdown/Close | ✅ 完成 | 批次1 |
| 第2章 | internal/conf | ✅ 完成 | 批次2 |
| 第3章 | internal/engine | ✅ 完成 | 批次4（含测试断言修复） |
| 第4章 | internal/chain | ✅ 完成 | 批次3 |
| 第5章 | internal/dataflow | ✅ 完成 | 批次2 |
| 第6章 | internal/hotswap | ✅ 完成 | 批次4 |
| 第7章 | cmd/rocksys | ⬜ 未开始 | 批次7（依赖全部挂件+adminapi） |
| 第8章 | adminapi + cmd/rockctl | ⬜ 进行中 | 批次5 |
| 第9-15章 | P1 挂件(shield/dispatch/result/trace/script/config/obs) | ⬜ 进行中 | 批次5-6 |
| 第16-19章 | P2 组件(auth/registry/mq/object) | ⬜ 未开始 | 批次6 |
| 第20章 | contracts(OpenAPI) | ✅ 完成 | 批次3 |
| 第21章 | sdk/python | ✅ 完成 | 批次3 |
| 第22章 | examples/stbiz_hello | ✅ 完成 | 批次3 |
| 第23章 | 集成验证+压测 | ⬜ 未开始 | 批次8 |

## 四、当前工作位置

- 批次：**批次 5（进行中）**——并行扇出 adminapi/rockctl + P1 挂件前 4 个
- 已派发任务：adminapi(第8章)、rockctl(第8章)、shield(第9章)、dispatch(第10章)、result(第11章)、trace(第12章)
- 执行详情见"批次日志"。

## 五、未完成任务与下次起点

- 批次 5：adminapi、rockctl、shield/dispatch/result/trace —— **并行中**
- 批次 6：script/config/obs(13-15章) + auth/registry/mq/object(16-19章) —— 并行扇出 7 个
- 批次 7：cmd/rocksys(第7章装配，依赖全部挂件+adminapi，注入 drainCheck)
- 批次 8：第23章集成验证（降级链/压测/高可用）+ 全量 BUG 修复闭环

**下次从哪里开始**：若批次 5 完成并验证通过，进入批次 6（script/config/obs + P2 组件并行）。

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
- 🔧 修复：engine_test.go 单次 `r.Body.Read` 恰好读满 buffer 返回 `(n, io.EOF)` 误判失败 → 改 `io.ReadAll`；engine.go 重复 `defer cancel()` 清理；`.env`/`default.env` 加入 .gitignore
- 提交：`4501a93`、`51f613e`

### 批次 5（进行中）
- [ ] adminapi（第8章）
- [ ] cmd/rockctl（第8章）
- [ ] plugins/shield（第9章）
- [ ] plugins/dispatch（第10章）
- [ ] plugins/result（第11章）
- [x] plugins/trace（第12章）

### Git 提交记录
| 时间 | 提交 | 内容 |
|------|------|------|
| - | 6bacf94 | 第1章骨架 |
| - | 358060d | easyserver Shutdown/Close |
| - | 37c4a18 | 第2章conf+第5章dataflow |
| - | 2198758 | 第4章chain+第20-22章 |
| - | 4501a93 | 第3章engine+第6章hotswap |
| - | 51f613e | .env/.gitignore 清理 |
