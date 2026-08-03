// Package trace 见 doc.go。本文件实现 Trace 转发链中间件：响应头注入 X-Trace-Id。
package trace

import (
	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/hotswap"
)

// traceIDHdr 响应头中注入的 trace_id 头名。
const traceIDHdr = "X-Trace-Id"

// Trace trace_id 透传中间件。
//
// 生成与透传分离：trace_id 由 dataflow 在请求入口始终生成（即使本挂件关闭）；
// 本挂件仅决定是否把该值写入响应头 X-Trace-Id。挂 Head 槽位。
type Trace struct {
	cfg *conf.Manager // 本挂件无配置项，保留以保持构造签名一致性
}

// New 构造 Trace 实例。
func New(cfg *conf.Manager) *Trace {
	return &Trace{cfg: cfg}
}

// Name 返回挂件名。
func (t *Trace) Name() string { return "trace" }

// Slot 返回挂载槽位：Head（仅需在转发前写入响应头）。
func (t *Trace) Slot() chain.Slot { return chain.Head }

// Handle 在转发前读取 DataFlow 的 TraceID 并写入响应头 X-Trace-Id，
// 随后返回 true 继续转发。WriteHeader 前设置响应头均有效。
func (t *Trace) Handle(ctx *chain.Context) (next bool) {
	ctx.W.Header().Set(traceIDHdr, ctx.DF.TraceID())
	return true
}

// Start 无需运行态，按接口返回 nil。
func (t *Trace) Start(_ any) error { return nil }

// Stop 无资源可清理，按接口返回 nil。
func (t *Trace) Stop() error { return nil }

// 编译期断言：Trace 实现 hotswap.MiddlewareLifecycle。
var _ hotswap.MiddlewareLifecycle = (*Trace)(nil)
