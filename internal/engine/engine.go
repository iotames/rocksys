package engine

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/dataflow"
	"rocksys/internal/netutil"

	"github.com/iotames/easyserver"
)

// upstreamDialTimeout WebSocket 分支直连后端的建连/握手超时（握手后为长连接，不再设超时）。
// var 而非 const：便于测试注入较短值验证握手超时路径。
var upstreamDialTimeout = 30 * time.Second

// Engine 反向代理引擎，封装 *easyserver.Server（即 *httpsvr.EasyServer）。
// 无任何业务逻辑，负责 HTTP 转发与生命周期管理。
type Engine struct {
	server  *easyserver.Server // 底层 HTTP 服务器
	chain   *chain.Chain       // 转发链
	conf    conf.Manager       // 配置管理器
	pool    *UpstreamPool      // 上游连接池
	adapter *chain.Adapter     // 转发链适配器（活跃请求计数，供 hotswap 排空轮询）
}

// New 创建引擎：装配 easyserver + 注册 chain 适配器为 head 中间件。
// 订阅配置热更：默认 upstream 变化时热更新适配器（§2.4/§8.2），保证代理立即切换。
func New(cfgMgr conf.Manager, c *chain.Chain) *Engine {
	cfg := cfgMgr.Current()
	srv := easyserver.NewServer(cfg.ListenAddr)
	srv.SetQuiet(true) // 主引擎为纯反向代理（easyserver 层无路由），静默横幅与空路由警告
	e := &Engine{server: srv, chain: c, conf: cfgMgr, pool: newUpstreamPool()}
	e.adapter = chain.NewAdapter(c, cfg.DefaultUpstream, e.Forward)
	srv.AddMiddleHead(e.adapter)
	if cfgMgr != nil {
		cfgMgr.Watch(func(newCfg *conf.Config) {
			if newCfg != nil {
				e.adapter.SetDefaultUpstream(newCfg.DefaultUpstream)
			}
		})
	}
	return e
}

// ActiveCount 返回当前活跃请求数（供 hotswap 排空轮询）。
func (e *Engine) ActiveCount() int64 {
	return e.adapter.ActiveCount()
}

// ListenAndServe 启动 HTTP 监听（委托给内部 *easyserver.Server）。
func (e *Engine) ListenAndServe() error {
	return e.server.ListenAndServe()
}

// Shutdown 优雅停机：停止接收新连接，等待在途请求完成。
func (e *Engine) Shutdown(ctx context.Context) error {
	return e.server.Shutdown(ctx)
}

// Forward 将请求转发到目标 upstream，保留 Method/Header/Body。
// 自动追加 X-Forwarded-For、X-Trace-Id（始终从 DataFlow 读取，与 trace 挂件状态无关）。
//
// ★ 时间戳取点：收到上游响应（或判定 502/504 失败）后、写回客户端之前，
// 调用 df.SetDoneBizAt(time.Now())；成功与失败路径统一在此取点。
//
// 失败契约：先通过 w.WriteHeader + w.Write 写入完整错误响应，再返回 error，
// 保证 Adapter 缓冲路径下 ctx.RespCode/RespHeader/RespBody 可用。
//
// target 格式：http://host:port
func (e *Engine) Forward(tw http.ResponseWriter, r *http.Request, target string, df *dataflow.DataFlow) error {
	w := tw

	// WebSocket Upgrade 请求：走独立隧道分支（握手后双向字节对拷，见 forwardWebSocket）。
	// 判断复用 chain.IsWebSocketUpgrade（Adapter 据此绕过响应缓冲）。
	if chain.IsWebSocketUpgrade(r) {
		return e.forwardWebSocket(w, r, target, df)
	}

	// 解析目标 URL（格式：http://host:port）
	dst, err := url.Parse(target)
	if err != nil || dst.Host == "" {
		return writeError(w, http.StatusBadGateway, "invalid upstream target")
	}

	// 构造上游请求，保留 Method/Header/Body
	outReq := r.Clone(r.Context())
	outReq.URL.Scheme = "http"
	outReq.URL.Host = dst.Host
	outReq.RequestURI = ""
	outReq.Host = dst.Host

	// 自动追加头
	appendForwardedFor(outReq.Header, netutil.GetClientIP(r))
	outReq.Header.Set("X-Trace-Id", df.TraceID())

	// 转发超时控制
	ctx, cancel := context.WithTimeout(r.Context(), e.upstreamTimeout())
	defer cancel()
	outReq = outReq.WithContext(ctx)

	// 执行上游往返
	resp, err := e.pool.RoundTrip(outReq)
	if err != nil {
		// ★ 失败路径：写回前先取点，再写完整错误响应并返回 error
		df.SetDoneBizAt(now())
		if ctx.Err() == context.DeadlineExceeded {
			return writeError(w, http.StatusGatewayTimeout, "upstream timeout")
		}
		return writeError(w, http.StatusBadGateway, "upstream error: "+err.Error())
	}
	defer resp.Body.Close()

	// ★ 成功路径：收到上游响应后、写回客户端之前在取点
	df.SetDoneBizAt(now())

	// 复制上游响应头、写状态码、流式拷贝响应体（不缓存、不解析、不修改）
	copyRespHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return nil
}

// forwardWebSocket 代理 WebSocket：将 Upgrade 请求原样转发到后端，后端回 101 后
// 劫持客户端底层连接，与后端 TCP 双向字节对拷（不再解析 HTTP，ws 帧原样透传）。
// 非 101 响应（后端拒绝升级）按普通响应透传给客户端。
// 失败契约与 Forward 一致：先写完整错误响应，再返回 error。
func (e *Engine) forwardWebSocket(w http.ResponseWriter, r *http.Request, target string, df *dataflow.DataFlow) error {
	// 解析目标 URL（格式：http://host:port）
	dst, err := url.Parse(target)
	if err != nil || dst.Host == "" {
		df.SetDoneBizAt(now())
		return writeError(w, http.StatusBadGateway, "invalid upstream target")
	}

	// 构造上游请求（与普通转发一致：保留 method/header，改写 Host，追加链路头）
	outReq := r.Clone(r.Context())
	outReq.URL.Scheme = "http"
	outReq.URL.Host = dst.Host
	outReq.RequestURI = ""
	outReq.Host = dst.Host
	appendForwardedFor(outReq.Header, netutil.GetClientIP(r))
	outReq.Header.Set("X-Trace-Id", df.TraceID())

	// 直连后端 TCP（ws 需要原始连接，不走 http.Transport 连接池）
	backend, err := net.DialTimeout("tcp", dst.Host, upstreamDialTimeout)
	if err != nil {
		df.SetDoneBizAt(now())
		return writeError(w, http.StatusBadGateway, "upstream error: "+err.Error())
	}
	defer backend.Close()

	// 握手阶段设 deadline：防后端接受连接后挂起不回，导致握手永久阻塞
	//（拖住 Adapter activeCount，进而阻塞 hotswap 排空与优雅停机）。
	_ = backend.SetDeadline(time.Now().Add(upstreamDialTimeout))

	// 原样写入 Upgrade 请求（含 Upgrade/Connection/Sec-WebSocket-* 头）
	if err := outReq.Write(backend); err != nil {
		df.SetDoneBizAt(now())
		return writeError(w, http.StatusBadGateway, "upstream error: "+err.Error())
	}

	// 读后端响应（101 Switching Protocols 或拒绝）
	br := bufio.NewReader(backend)
	resp, err := http.ReadResponse(br, outReq)
	if err != nil {
		df.SetDoneBizAt(now())
		return writeError(w, http.StatusBadGateway, "upstream error: "+err.Error())
	}
	defer resp.Body.Close()

	// 后端拒绝升级：按普通响应透传（状态码/头/体）
	if resp.StatusCode != http.StatusSwitchingProtocols {
		df.SetDoneBizAt(now())
		copyRespHeader(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return nil
	}

	// 101：清除握手 deadline（隧道为长连接，不受超时限制），劫持客户端连接
	_ = backend.SetDeadline(time.Time{})
	hj, ok := w.(http.Hijacker)
	if !ok {
		df.SetDoneBizAt(now())
		return writeError(w, http.StatusInternalServerError, "hijack not supported")
	}
	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		df.SetDoneBizAt(now())
		return writeError(w, http.StatusBadGateway, "hijack failed: "+err.Error())
	}
	defer clientConn.Close()

	// 把 101 响应写回客户端（此后连接不再作为 HTTP 处理）
	if err := resp.Write(clientConn); err != nil {
		df.SetDoneBizAt(now())
		return err
	}
	df.SetDoneBizAt(now())

	// 双向对拷：backend ↔ clientConn。
	// clientBuf/br 分别带走「握手后已到达」的客户端/后端字节，避免数据滞留缓冲。
	errCh := make(chan error, 2)
	go func() { _, err := io.Copy(backend, clientBuf); errCh <- err }() // 客户端 → 后端
	go func() { _, err := io.Copy(clientConn, br); errCh <- err }()    // 后端 → 客户端
	<-errCh                                                          // 任一端关闭即结束（对端副本随连接关闭退出）
	return nil
}

// upstreamTimeout 返回转发超时配置；conf 未装配时回退默认 18s。
func (e *Engine) upstreamTimeout() time.Duration {
	if e.conf != nil {
		if c := e.conf.Current(); c != nil && c.UpstreamTimeout > 0 {
			return c.UpstreamTimeout
		}
	}
	return 18 * time.Second
}

func now() time.Time {
	return time.Now()
}

// appendForwardedFor 追加 X-Forwarded-For：取客户端 IP 追加到已有值末尾。
func appendForwardedFor(h http.Header, ip string) {
	if ip == "" {
		return
	}
	if existing := h.Get("X-Forwarded-For"); existing != "" {
		h.Set("X-Forwarded-For", existing+", "+ip)
		return
	}
	h.Set("X-Forwarded-For", ip)
}

// copyRespHeader 将 src 响应头复制到 dst（覆盖同名 key）。
func copyRespHeader(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// writeError 写入完整错误响应后返回错误描述。
// 必须先写完整响应再返回 error，保证 Adapter 缓冲路径下 ctx 字段可用。
func writeError(w http.ResponseWriter, code int, msg string) error {
	c := http.StatusText(code)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, c+": "+msg)
	return errors.New(msg)
}
