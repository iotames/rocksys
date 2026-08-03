package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/dataflow"

	"github.com/iotames/easyserver"
)

// Engine 反向代理引擎，封装 *easyserver.Server（即 *httpsvr.EasyServer）。
// 无任何业务逻辑，负责 HTTP 转发与生命周期管理。
type Engine struct {
	server *easyserver.Server // 底层 HTTP 服务器
	chain  *chain.Chain       // 转发链
	conf  conf.Manager        // 配置管理器
	pool   *UpstreamPool      // 上游连接池
}

// New 创建引擎：装配 easyserver + 注册 chain 适配器为 head 中间件。
func New(cfgMgr conf.Manager, c *chain.Chain) *Engine {
	cfg := cfgMgr.Current()
	srv := easyserver.NewServer(cfg.ListenAddr)
	e := &Engine{server: srv, chain: c, conf: cfgMgr, pool: newUpstreamPool()}
	adapter := chain.NewAdapter(c, cfg.DefaultUpstream, e.Forward)
	srv.AddMiddleHead(adapter)
	return e
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

	// WebSocket Upgrade 请求不支持代理 → 501 Not Implemented
	if isWebSocketUpgrade(r) {
		return writeError(w, http.StatusNotImplemented, "websocket upgrade not supported")
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
	appendForwardedFor(outReq.Header, clientIP(r))
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

// upstreamTimeout 返回转发超时配置；conf 未装配时回退默认 5s。
func (e *Engine) upstreamTimeout() time.Duration {
	if e.conf != nil {
		if c := e.conf.Current(); c != nil && c.UpstreamTimeout > 0 {
			return c.UpstreamTimeout
		}
	}
	return 5 * time.Second
}

func now() time.Time {
	return time.Now()
}

// isWebSocketUpgrade 判断请求是否为 WebSocket Upgrade 请求。
func isWebSocketUpgrade(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !strings.EqualFold(r.Header.Get("Connection"), "Upgrade") {
		return false
	}
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
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

// clientIP 从 RemoteAddr 提取客户端 IP（去除端口）。
func clientIP(r *http.Request) string {
	if r == nil || r.RemoteAddr == "" {
		return ""
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
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