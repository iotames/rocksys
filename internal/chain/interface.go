// Package chain 转发链编排：Middleware/Context/Slot/ResponseHook 接口定义。
package chain

import (
	"net/http"

	"rocksys/internal/dataflow"
)

// Middleware 转发链中间件接口（我们的接口，非 easyserver 的 MiddleHandle）。
type Middleware interface {
	Name() string
	// Handle 处理请求；返回 false 表示中断链，中间件已自行写入响应。
	// ★ 返回 true 的中间件禁止写入 ResponseWriter（Write/WriteHeader），
	// 否则后续链或 Adapter.Forward 写入时会 panic（http: superfluous response.WriteHeader call）。
	Handle(ctx *Context) (next bool)
}

// Context 请求级上下文（在链上流转）。
type Context struct {
	W  http.ResponseWriter
	R  *http.Request
	DF *dataflow.DataFlow // 封装了 httpsvr.DataFlow；Target 由其持有

	// ★ 以下字段仅响应阶段（Tail 槽位）有效，由 Adapter 填充：
	RespCode   int                 // 上游响应状态码（无上游响应时为 0）
	RespHeader http.Header         // 上游响应头
	RespBody   []byte              // 上游响应体（仅存在 ResponseHook 时由 Adapter 缓冲）
	RespW      http.ResponseWriter // 中间件写入目标：默认 = W；响应头须在 WriteHeader 前设置

	done bool // 内部标记：是否已有 Tail 中间件通过 WriteFinal 写入最终响应
}

// Slot 枚举。执行时序：转发前依次执行 Head → Middle；转发完成后执行 Tail（响应处理）。
type Slot int

const (
	Head   Slot = iota // 转发前最先执行（防护/认证）
	Middle             // 转发前执行（路由分发等）
	Tail               // 转发完成后执行（响应处理）
)

// ResponseHook 可选接口：实现此接口的中间件必须挂 Tail 槽位，
// 在 Forward 完成（收到上游响应并写入缓冲）后、写回客户端之前被 Adapter 调用。
type ResponseHook interface {
	// OnResponse 转发完成后执行；ctx.RespCode/RespHeader/RespBody 为上游响应。
	// 需要改写响应时调用 ctx.WriteFinal；仅读取则无需写响应。
	// 返回 err 仅记录告警，不中断后续 hook。
	OnResponse(ctx *Context) error
}
