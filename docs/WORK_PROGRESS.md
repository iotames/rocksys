# RockSys 工作进度文件

> 本文档用于记录开发进度。若中途意外断开，AI 或人类开发者凭此文件恢复工作。
> 更新时机：每一批次任务完成后必须更新。

## 一、既定目标

依据 `docs/DEV_HANDBOOK.md`（v5，23 章）实现 RockSys 集团公司 ERP 系统的**后端底座**。

- **实现顺序**：第1章(骨架) → 第2章(conf) → 第3章(engine) → 第4章(chain) → 第5章(dataflow) → 第6章(hotswap) → 第7章(rocksys入口) → 第8章(rockctl) → 第9-15章(P1挂件) → 第16-19章(P2组件) → 第20-22章(业务侧) → 第23章(验证)。
- **交付标准**：P0（第1-8章）完成后为"最小可用"裸代理+热开关；P1/P2 挂件按手册实现；每章"验收标准"通过后才算完成。
- **前端**：本阶段不做。
- **质量闭环**：每批完成后执行 测试 → 修复 BUG → 测试 循环，直至无 BUG；每轮循环自主 git 提交。

## 二、技术要点备忘（避免重复踩坑）

1. **接口实际签名与手册差异**：
   - easyserver 的 `AddHandler(method, urlpath string, ctxfunc func(ctx Context))` —— 手册 §8.1 写的 `(pattern, h func(w,r))` 是示意；adminapi.RegisterPlugin 内部需把 `func(w,r)` 包装为 `func(ctx httpsvr.Context)`。
   - easyconf 的 `addItem` 为私有方法，conf.Manager.Register 无法直接调用；须用公开的 `StringVar/IntVar/BoolVar`（type switch 分发 + strconv 转换 defval），注册后**不能再 flag.Parse**（会 panic），命令行重放用 `SetItemValue`。
   - `easyconf.Parse(true)` 内部调用全局 `flag.Parse()`，解析的是 `os.Args[1:]`，因此 conf.Load 须把短参数映射结果写回 `os.Args`。
2. **编译桩**：第3章 engine 引用 chain/dataflow 类型，须同时创建二者的最小骨架类型（结构体+字段，不带方法），完整实现在第4/5章补。
3. **包名冲突**：`easyserver/hotswap` 用别名 `eshs`；`rocksys/internal/hotswap` 用原名 `hotswap`。
4. **导入规则（强制）**：`plugins/*` 只允许 import internal/{chain,dataflow,hotswap,conf}；`cmd/rocksys` 是唯一装配点；`plugins/*` 之间禁止互相依赖。
5. **时间戳取点**：BeginBizAt 在 Adapter 调 Forward 前取；DoneBizAt 在 Forward 内部"收到响应后、写回前"取，仅写一次。
6. **Tail 顺序**：装配时先注册 obs、后注册 result（result 先执行改写，obs 后记录最终状态）。

## 三、完成进度（对照手册章节）

| 章节 | 内容 | 状态 | 备注 |
|------|------|------|------|
| 第1章+§1.0 | 骨架 + easyserver Shutdown/Close | ✅ 已完成 | 批次1 |
| 第2章 | internal/conf | ✅ 已完成 | 批次2 |
| 第3章 | internal/engine | ⬜ 未开始 | 批次3 |
| 第4章 | internal/chain | ✅ 已完成 | 批次3 |
| 第5章 | internal/dataflow | ✅ 已完成 | 批次2 |
| 第6章 | internal/hotswap | ⬜ 未开始 | 批次4 |
| 第7章 | cmd/rocksys | ⬜ 未开始 | 批次4 |
| 第8章 | adminapi + cmd/rockctl | ⬜ 未开始 | 批次4 |
| 第9-15章 | P1 挂件(shield/dispatch/result/trace/script/config/obs) | ⬜ 未开始 | 批次5 |
| 第16-19章 | P2 组件(auth/registry/mq/object) | ⬜ 未开始 | 批次6 |
| 第20章 | contracts(OpenAPI) | ⬜ 未开始 | 批次7 |
| 第21章 | sdk/python | ⬜ 未开始 | 批次7 |
| 第22章 | examples/stbiz_hello | ⬜ 未开始 | 批次7 |
| 第23章 | 集成验证+压测 | ⬜ 未开始 | 批次8 |

## 四、当前工作位置

- 批次：**批次 3（进行中）**
- 已完成：第4章 internal/chain（转发链编排）
- 下次：第3章 internal/engine（依赖 conf+chain+dataflow）

## 五、未完成任务与下次起点

- 批次 3：第3章 engine（依赖 conf+chain+dataflow；chain 已完成）
- 批次 4：hotswap(依赖 chain+conf)、adminapi(依赖 hotswap+conf)、cmd/rocksys(依赖 engine+hotswap)、rockctl(依赖 admin API 协议)
- 批次 5：P1 挂件 7 个（依赖 chain+dataflow+hotswap+conf）
- 批次 6：P2 组件 4 个
- 批次 7：contracts/sdk/examples
- 批次 8：第23章集成验证 + 全量 BUG 修复闭环

**下次从哪里开始**：若批次 1 已完成并验证通过，直接进入批次 2（并行派发 conf + dataflow）。

## 六、批次日志

### 批次 3（进行中）
- [x] T1: internal/chain 转发链编排（第4章）：interface.go / impl.go / adapter.go / chain_test.go
- 验证：`go build ./... && go vet ./... && go test -count=1 ./internal/chain/...` 全部通过
- 未提交：等待总指挥统一提交

### Git 提交记录
| 时间 | 提交 | 内容 |
|------|------|------|
| - | - | （尚未有本批次提交） |
