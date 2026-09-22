// Tail 收尾件（least_conn 在途递减收尾）：转发完成（响应返回）后按
// DataFlow 中的节点 id 对 registry 在途计数 -1。
//
// 设计要点：
//   - 独立中间件类型，Name 独立（dispatch-tail），挂 chain.Tail 槽位，
//     可独立实例化（装配归转发主件）；
//   - Handle 直通放行返回 true（不写 ResponseWriter，遵守链红线）；
//   - 递减按节点 id：选点阶段由主件把选中节点 id 写入 DataFlow
//     （DFKeyDispatchNodeID），节点 id 跨热更稳定，不经快照 url 反查——
//     杜绝 Rebuild 替换快照后递减失配；
//   - Start/Stop 为 no-op：生命周期由主件统一驱动（registry 排空与探活中心
//     停止均归主件），本件无自有资源。
package dispatch

import (
	"rocksys/internal/chain"
	"rocksys/internal/hotswap"
)

// DFKeyDispatchNodeID DataFlow 中「本次请求选中的转发节点 id」的 KV key
// （rocksys: 前缀惯例）。主件选点成功后写入，收尾件读取后递减。
const DFKeyDispatchNodeID = "rocksys:dispatch_node_id"

// TailFin Tail 收尾件：least_conn 在途递减的唯一收口。
type TailFin struct {
	reg NodeRegistry // 递减目标（真 registry；接口注入便于测试）
}

// NewTailFin 创建收尾件（独立实例化，装配归主件）。
func NewTailFin(reg NodeRegistry) *TailFin {
	return &TailFin{reg: reg}
}

// Name 返回中间件名称（独立于主件 dispatch，避免链上重名冲突）。
func (t *TailFin) Name() string { return "dispatch-tail" }

// Slot 挂载位置：Tail（转发完成后响应处理阶段执行）。
func (t *TailFin) Slot() chain.Slot { return chain.Tail }

// Handle 直通放行：请求阶段不做任何事，返回 true 继续/转发（不写响应——链红线）。
func (t *TailFin) Handle(_ *chain.Context) bool { return true }

// OnResponse 转发完成后按 DF 中的节点 id 递减在途计数。
// DF 未写节点 id（如未命中路由）或值类型异常时静默跳过（无选点即无 +1，无需 -1）。
// registry 侧饱和递减（≥0、记录缺失 no-op）兜底迟到递减。
func (t *TailFin) OnResponse(ctx *chain.Context) error {
	if ctx == nil || ctx.DF == nil {
		return nil
	}
	v, ok := ctx.DF.Get(DFKeyDispatchNodeID)
	if !ok {
		return nil
	}
	nodeID, ok := v.(int64)
	if !ok {
		return nil
	}
	t.reg.DecInflight(nodeID)
	return nil
}

// Start no-op（生命周期由主件统一驱动）。
func (t *TailFin) Start(_ any) error { return nil }

// Stop no-op（生命周期由主件统一驱动）。
func (t *TailFin) Stop() error { return nil }

// 编译期断言：实现链中间件（含 Tail 槽位可选接口）与生命周期接口。
var (
	_ chain.Middleware            = (*TailFin)(nil)
	_ chain.ResponseHook          = (*TailFin)(nil)
	_ hotswap.MiddlewareLifecycle = (*TailFin)(nil)
)
