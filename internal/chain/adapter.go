package chain

import (
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"rocksys/internal/dataflow"

	"github.com/iotames/easyserver/httpsvr"
	"github.com/iotames/easyserver/log"
)

// respBufferLimit 缓冲上限：超出后停止缓冲，直写底层 writer。
const respBufferLimit = 4 << 20 // 4MB

// Adapter 实现 httpsvr.MiddleHandle 接口。
// 负责将 easyserver 的 DataFlow 包装为 dataflow.DataFlow，然后执行 Chain，
// 是 easyserver 进入 rocksys 转发链的唯一入口。
type Adapter struct {
	chain           *Chain
	defaultUpstream atomic.Value // 持有 string：默认 upstream，支持配置热更（SetDefaultUpstream）
	forward         func(w http.ResponseWriter, r *http.Request, target string, df *dataflow.DataFlow) error
	activeCount     atomic.Int64 // 活跃请求计数：Handler 入口 +1、出口 -1，hotswap 排空依赖
}

// NewAdapter 创建适配器。
func NewAdapter(ch *Chain, defaultUpstream string, forward func(http.ResponseWriter, *http.Request, string, *dataflow.DataFlow) error) *Adapter {
	a := &Adapter{chain: ch, forward: forward}
	a.defaultUpstream.Store(defaultUpstream)
	return a
}

// SetDefaultUpstream 热更新默认上游（配置热更时由 engine 调用，§2.4/§8.2）。
func (a *Adapter) SetDefaultUpstream(upstream string) {
	a.defaultUpstream.Store(upstream)
}

// defaultUpstreamValue 返回当前默认上游。
func (a *Adapter) defaultUpstreamValue() string {
	if v := a.defaultUpstream.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// ActiveCount 返回当前活跃请求数（供 hotswap 排空轮询）。
func (a *Adapter) ActiveCount() int64 {
	return a.activeCount.Load()
}

// Handler 实现 httpsvr.MiddleHandle 接口，rocksys 处理请求的唯一入口。
func (a *Adapter) Handler(w http.ResponseWriter, r *http.Request, innerDF *httpsvr.DataFlow) (next bool) {
	// 0. 活跃请求计数 +1
	a.activeCount.Add(1)
	defer a.activeCount.Add(-1)

	// 1. 包装 easyserver DataFlow → rocksys DataFlow
	df := dataflow.New(innerDF, r)

	// 2. 创建链上下文（Tail 响应阶段字段由步骤 7 填充）
	ctx := &Context{W: w, R: r, DF: df, RespW: w}

	// 3. 执行转发前链（Head → Middle；Tail 不在本阶段执行）
	shouldForward := a.chain.Execute(ctx)

	// 4. 链中断 → 中间件已自行写入响应，取 DoneAt（出网时刻）后返回
	if !shouldForward {
		df.SetDoneAt(time.Now())
		return false
	}

	// 5. 确定转发目标（dispatch 中间件负责写入；未命中回退默认 upstream——支持热更）
	target := df.Target()
	if target == "" {
		target = a.defaultUpstreamValue()
	}
	if target == "" {
		// 未命中路由且无默认上游：不写响应直接放行，交给 easyserver 链尾处理
		// （SetWWWRoot 兜底目录返回文件 → 自定义/默认 404）。
		// 注意：此路径下 Tail 响应钩子不执行（与"链被中间件中断"语义一致）。
		return true
	}

	// 6. 转发前一刻取点（必须在 Forward 前，禁止 defer）
	df.SetBeginBizAt(time.Now())

	// 7. 执行转发；DoneBizAt 由 Forward 内部取点，Adapter 不负责
	var bufW *respBufferWriter
	if IsWebSocketUpgrade(r) {
		// 7a. WebSocket 隧道：respBufferWriter 不支持 Hijack，必须直写底层连接。
		//     forward 成功（隧道建立或后端拒绝升级后按普通响应透传）时 Tail 按 101 记录；
		//     转发失败（502/500，forward 已写错误响应）时不伪装 101，RespCode 保持零值供日志反映异常。
		if err := a.forward(w, r, target, df); err == nil {
			ctx.RespCode = http.StatusSwitchingProtocols
		}
	} else if a.chain.HasResponseHook(Tail) {
		// 7b. 存在响应处理中间件 → 缓冲上游响应，供 Tail 阶段读取/改写
		bufW = newRespBufferWriter(w)
		_ = a.forward(bufW, r, target, df) // err 忽略：forward 内部已写入 502/504 错误响应
		ctx.RespCode, ctx.RespHeader, ctx.RespBody = bufW.Status(), bufW.Header(), bufW.Body()
	} else {
		// 7c. 无响应处理中间件 → 直接流式写回客户端
		_ = a.forward(w, r, target, df)
	}

	// 8. 响应处理阶段：ResponseHooks 返回逆序切片，正向遍历即为逆序执行。
	//    单个 hook panic → log.Error（hook 名 + 堆栈），继续后续 hook（与"err 不中断后续 hook"语义一致）。
	//    ★ 响应阶段不写 500：可能已写回客户端，旁路 hook 写 500 会污染已发出的响应。
	for _, h := range a.chain.ResponseHooks(Tail) {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("chain: response hook panic recovered", "name", hookName(h), "panic", r, "stack", string(debug.Stack()))
				}
			}()
			if err := h.OnResponse(ctx); err != nil {
				log.Warn("response hook error", "name", hookName(h), "err", err)
			}
		}()
	}

	// 9. 缓冲未被任何 Tail 中间件消费（ctx.done == false）时写回客户端
	if bufW != nil && !ctx.done {
		copyHeader(w.Header(), ctx.RespHeader)
		w.WriteHeader(ctx.RespCode)
		w.Write(ctx.RespBody)
	}

	// 9b. 写回客户端完成 → 取 DoneAt（出网时刻），再调用实现 DoneHook 的 Tail 钩子。
	//     语义注记：7a/7c 路径写回可横跨长时段（流式转发 DoneAt＝整条流写毕时刻），
	//     被拦截路径不产生 access_log，链中断路径已在步骤 4 取点。
	df.SetDoneAt(time.Now())
	for _, h := range a.chain.ResponseHooks(Tail) {
		dh, ok := h.(DoneHook)
		if !ok {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("chain: done hook panic recovered", "name", hookName(h), "panic", r, "stack", string(debug.Stack()))
				}
			}()
			dh.OnDone(ctx)
		}()
	}

	// 10. 始终返回 false — 转发已完成，easyserver 后续链不再执行
	return false
}

// IsWebSocketUpgrade 判断请求是否为 WebSocket Upgrade 请求。
// 唯一权威实现：engine 的 Forward 据此走隧道分支；Adapter 据此绕过响应缓冲（支持 Hijack）。
func IsWebSocketUpgrade(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !strings.EqualFold(r.Header.Get("Connection"), "Upgrade") {
		return false
	}
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// hookName 获取 hook 的中间件名称，断言失败回退 "unknown"。
func hookName(h ResponseHook) string {
	if m, ok := h.(Middleware); ok {
		return m.Name()
	}
	return "unknown"
}

// respBufferWriter 实现 http.ResponseWriter：缓冲 ≤4MB，超出直写底层并置截断标记。
type respBufferWriter struct {
	underlying http.ResponseWriter
	header     http.Header
	status     int
	body       []byte
	truncated  bool
	written    bool // 是否已记录 status
	buffering  bool // 是否仍处于缓冲状态
}

// newRespBufferWriter 创建缓冲 writer。
func newRespBufferWriter(w http.ResponseWriter) *respBufferWriter {
	return &respBufferWriter{underlying: w, header: make(http.Header), buffering: true}
}

// Header 返回可修改的响应头（WriteHeader 后仍可读取）。
func (b *respBufferWriter) Header() http.Header {
	return b.header
}

// WriteHeader 记录状态码；重复调用忽略（不 panic、不覆盖）。
func (b *respBufferWriter) WriteHeader(code int) {
	if b.written {
		return
	}
	b.written = true
	b.status = code
	if !b.buffering {
		b.underlying.WriteHeader(code)
	}
}

// Write 缓冲 ≤4MB；满则停止缓冲直写底层并置截断标记。
func (b *respBufferWriter) Write(data []byte) (int, error) {
	if b.buffering {
		if len(b.body)+len(data) <= respBufferLimit {
			b.body = append(b.body, data...)
			return len(data), nil
		}
		// 缓冲已满：写回已缓冲内容 + 当前数据，进入直写模式
		b.flushToUnderlying(data)
		return len(data), nil
	}
	return b.underlying.Write(data)
}

// flushToUnderlying 停止缓冲，将已缓冲内容与当前数据直写底层 writer。
func (b *respBufferWriter) flushToUnderlying(data []byte) {
	b.truncated = true
	b.buffering = false
	if b.written {
		b.underlying.WriteHeader(b.status)
	}
	if len(b.body) > 0 {
		b.underlying.Write(b.body)
	}
	b.body = nil
	b.underlying.Write(data)
}

// Status 返回缓冲的状态码（未 WriteHeader 时为 0）。
func (b *respBufferWriter) Status() int {
	return b.status
}

// Body 返回缓冲的响应体（截断时为已缓冲部分，后续数据已直写客户端）。
func (b *respBufferWriter) Body() []byte {
	return b.body
}
