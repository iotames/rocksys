package engine

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/dataflow"

	"github.com/iotames/easyserver/httpsvr"
)

// testDF 构造携带 trace_id 的 DataFlow。
func testDF(traceID string) *dataflow.DataFlow {
	inner := httpsvr.NewDataFlow()
	df := dataflow.New(inner, httptest.NewRequest(http.MethodGet, "http://client/", nil))
	if traceID != "" {
		df.SetTraceID(traceID)
	}
	return df
}

func TestForwardNormal(t *testing.T) {
	const wantBody = "hello from upstream"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(wantBody))
	}))
	defer upstream.Close()

	e := &Engine{pool: newUpstreamPool()}
	df := testDF("trace-001")

	r := httptest.NewRequest(http.MethodGet, "http://client/hello?x=1", nil)
	w := httptest.NewRecorder()

	if err := e.Forward(w, r, upstream.URL, df); err != nil {
		t.Fatalf("Forward 返回错误: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 %d", w.Code, http.StatusOK)
	}
	if body := w.Body.String(); body != wantBody {
		t.Fatalf("响应体 = %q, 期望 %q", body, wantBody)
	}
	if df.DoneBizAt().IsZero() {
		t.Error("DoneBizAt 未被设置")
	}
}

func TestForwardTraceIDPropagation(t *testing.T) {
	const wantTrace = "trace-abc123"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Trace-Id"); got != wantTrace {
			t.Errorf("上游收到的 X-Trace-Id = %q, 期望 %q", got, wantTrace)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	e := &Engine{pool: newUpstreamPool()}
	df := testDF(wantTrace)

	r := httptest.NewRequest(http.MethodGet, "http://client/", nil)
	w := httptest.NewRecorder()

	if err := e.Forward(w, r, upstream.URL, df); err != nil {
		t.Fatalf("Forward 返回错误: %v", err)
	}
	if w.Code != http.StatusNoContent {
		t.Fatalf("状态码 = %d, 期望 %d", w.Code, http.StatusNoContent)
	}
}

func TestForwardXForwardedFor(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Forwarded-For"); got != "192.168.1.10" {
			t.Errorf("X-Forwarded-For = %q, 期望含客户端 IP", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	e := &Engine{pool: newUpstreamPool()}
	df := testDF("")

	r := httptest.NewRequest(http.MethodGet, "http://client/", nil)
	r.RemoteAddr = "192.168.1.10:5555"
	w := httptest.NewRecorder()

	if err := e.Forward(w, r, upstream.URL, df); err != nil {
		t.Fatalf("Forward 返回错误: %v", err)
	}
}

func TestForwardWebSocketEcho(t *testing.T) {
	// 端到端：客户端 → Forward → ws echo 后端，握手后双向字节透传。
	backend := newWebSocketEchoServer(t)
	defer backend.Close()

	e := &Engine{pool: newUpstreamPool()}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		df := testDF("ws-001")
		if err := e.Forward(w, r, backend.URL, df); err != nil {
			t.Errorf("Forward: %v", err)
		}
	}))
	defer proxy.Close()

	conn, br := wsUpgrade(t, proxy.Listener.Addr().String(), "/chat")
	defer conn.Close()

	// 后端在 101 后立即发送的帧（验证 br 带走握手期间缓冲字节，无滞留）
	greeting := make([]byte, len(wsGreeting))
	if _, err := io.ReadFull(br, greeting); err != nil {
		t.Fatalf("读取后端即时帧失败: %v", err)
	}
	if string(greeting) != wsGreeting {
		t.Fatalf("即时帧不匹配: got %q want %q", greeting, wsGreeting)
	}

	// 客户端 → 后端 → echo 回客户端
	payload := []byte("hello rocksys ws")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("读取 echo 失败: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo 不匹配: got %q want %q", got, payload)
	}
}

func TestForwardWebSocketHijackUnsupported(t *testing.T) {
	// ResponseWriter 不支持 Hijack（如 httptest.Recorder）→ 500。
	backend := newWebSocketEchoServer(t)
	defer backend.Close()

	e := &Engine{pool: newUpstreamPool()}
	df := testDF("ws-no-hijack")
	r := httptest.NewRequest(http.MethodGet, "http://client/chat", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()

	if err := e.Forward(w, r, backend.URL, df); err == nil {
		t.Fatal("hijack 不支持应返回 error")
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, 期望 %d", w.Code, http.StatusInternalServerError)
	}
}

func TestForwardWebSocketUpgradeRejected(t *testing.T) {
	// 后端拒绝升级（400 + body）→ 按普通响应完整透传给客户端。
	const rejectBody = "upgrade rejected by backend"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(rejectBody))
	}))
	defer backend.Close()

	e := &Engine{pool: newUpstreamPool()}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		df := testDF("ws-rej")
		_ = e.Forward(w, r, backend.URL, df)
	}))
	defer proxy.Close()

	conn, br := wsUpgradeExpect(t, proxy.Listener.Addr().String(), "/chat", http.StatusBadRequest)
	defer conn.Close()
	body := make([]byte, len(rejectBody))
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatalf("读取透传响应体失败: %v", err)
	}
	if string(body) != rejectBody {
		t.Fatalf("响应体不匹配: got %q want %q", body, rejectBody)
	}
}

func TestForwardWebSocketBackendDown(t *testing.T) {
	// 后端不可达 → 502，且失败路径同样取点。
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	deadAddr := l.Addr().String()
	l.Close()

	e := &Engine{pool: newUpstreamPool()}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		df := testDF("ws-down")
		if err := e.Forward(w, r, "http://"+deadAddr, df); err == nil {
			t.Error("后端不可达应返回 error")
		}
		if df.DoneBizAt().IsZero() {
			t.Error("失败路径 DoneBizAt 未被设置")
		}
	}))
	defer proxy.Close()

	conn, _ := wsUpgradeExpect(t, proxy.Listener.Addr().String(), "/chat", http.StatusBadGateway)
	defer conn.Close()
}

// wsGreeting 后端 101 后立即发送的即时帧（验证代理侧握手缓冲不滞留）。
const wsGreeting = "greeting-after-101"

func TestForwardWebSocketHandshakeTimeout(t *testing.T) {
	// 后端 accept 连接但不响应（挂起）→ 握手 deadline 触发，客户端收到 502。
	old := upstreamDialTimeout
	upstreamDialTimeout = 300 * time.Millisecond
	defer func() { upstreamDialTimeout = old }()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // 不读不写：模拟挂起的后端
		}
	}()

	e := &Engine{pool: newUpstreamPool()}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		df := testDF("ws-hang")
		if err := e.Forward(w, r, "http://"+ln.Addr().String(), df); err == nil {
			t.Error("握手超时应返回 error")
		}
		if df.DoneBizAt().IsZero() {
			t.Error("失败路径 DoneBizAt 未被设置")
		}
	}))
	defer proxy.Close()

	conn, _ := wsUpgradeExpect(t, proxy.Listener.Addr().String(), "/chat", http.StatusBadGateway)
	defer conn.Close()
}

// newWebSocketEchoServer 构造手动处理 Upgrade 的 ws 后端：
// 101 后先发送即时帧 wsGreeting，再把随后收到的字节原样 echo。
func newWebSocketEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("后端 ResponseWriter 不支持 Hijack")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("后端 Hijack 失败: %v", err)
			return
		}
		defer conn.Close()
		resp := &http.Response{
			StatusCode: http.StatusSwitchingProtocols,
			Status:     "101 Switching Protocols",
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header: http.Header{
				"Upgrade":              {"websocket"},
				"Connection":           {"Upgrade"},
				"Sec-WebSocket-Accept": {wsAcceptKey(r.Header.Get("Sec-WebSocket-Key"))},
			},
			Body: http.NoBody,
		}
		if err := resp.Write(conn); err != nil {
			t.Errorf("后端写 101 失败: %v", err)
			return
		}
		_, _ = conn.Write([]byte(wsGreeting)) // 101 后立即发帧
		_, _ = io.Copy(conn, buf)             // echo：读到的原样写回
	}))
}

// wsUpgrade 裸 TCP 建立 WebSocket 握手，期望 101，返回连接与缓冲 reader。
func wsUpgrade(t *testing.T, addr, path string) (net.Conn, *bufio.Reader) {
	t.Helper()
	return wsUpgradeExpect(t, addr, path, http.StatusSwitchingProtocols)
}

// wsUpgradeExpect 裸 TCP 发送 Upgrade 请求，断言响应状态码。
func wsUpgradeExpect(t *testing.T, addr, path string, wantStatus int) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("连接 %s 失败: %v", addr, err)
	}
	key := "dGhlIHNhbXBsZSBub25jZQ==" // RFC 6455 示例 key
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		path, addr, key)
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		t.Fatalf("写握手请求失败: %v", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		t.Fatalf("读握手响应失败: %v", err)
	}
	if resp.StatusCode != wantStatus {
		conn.Close()
		t.Fatalf("响应状态码 = %d, 期望 %d", resp.StatusCode, wantStatus)
	}
	return conn, br
}

// wsAcceptKey 计算 RFC 6455 的 Sec-WebSocket-Accept。
func wsAcceptKey(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}

func TestForwardUnreachableGateway(t *testing.T) {
	// 关闭 listener 使其端口不可达
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	target := srv.URL
	srv.Close()

	e := &Engine{pool: newUpstreamPool()}
	df := testDF("")

	r := httptest.NewRequest(http.MethodGet, "http://client/", nil)
	w := httptest.NewRecorder()

	if err := e.Forward(w, r, target, df); err == nil {
		t.Fatal("上游不可达应返回 error")
	}
	if w.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d, 期望 %d", w.Code, http.StatusBadGateway)
	}
	// 失败路径同样取点
	if df.DoneBizAt().IsZero() {
		t.Error("失败路径 DoneBizAt 未被设置")
	}
}

func TestForwardTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer upstream.Close()

	e := &Engine{pool: newUpstreamPool()}
	df := testDF("")

	r := httptest.NewRequest(http.MethodGet, "http://client/", nil)
	// 请求自身携带更短的超时，驱动 context.WithTimeout 提前到期
	reqCtx, cancel := context.WithTimeout(r.Context(), 50*time.Millisecond)
	defer cancel()
	r = r.WithContext(reqCtx)

	w := httptest.NewRecorder()
	if err := e.Forward(w, r, upstream.URL, df); err == nil {
		t.Fatal("超时应返回 error")
	}
	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("状态码 = %d, 期望 %d", w.Code, http.StatusGatewayTimeout)
	}
}

func TestNewEngine(t *testing.T) {
	mgr, err := conf.Load([]string{"--listen", "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("conf.Load 失败: %v", err)
	}
	e := New(mgr, chain.New())
	if e == nil || e.server == nil || e.pool == nil {
		t.Fatal("New 返回的引擎未完整初始化")
	}
	// New 不应启动真实 listener，仅验证装配成功
}

func TestEngineShutdownNoListen(t *testing.T) {
	mgr, err := conf.Load([]string{"--listen", "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("conf.Load 失败: %v", err)
	}
	e := New(mgr, chain.New())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// 未启动时 Shutdown 应安全返回（不 panic）
	if err := e.Shutdown(ctx); err != nil {
		t.Logf("Shutdown 返回（未监听）：%v", err)
	}
}

func TestEngineForwardMethodHeaderBody(t *testing.T) {
	const wantBody = "payload-data"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("上游 Method = %s, 期望 POST", r.Method)
		}
		if got := r.Header.Get("X-Custom"); got != "custom-value" {
			t.Errorf("上游 X-Custom = %q", got)
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("读取请求体失败: %v", err)
		}
		if string(b) != wantBody {
			t.Errorf("上游 Body = %q, 期望 %q", string(b), wantBody)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	e := &Engine{pool: newUpstreamPool()}
	df := testDF("")

	r := httptest.NewRequest(http.MethodPost, "http://client/submit", strings.NewReader(wantBody))
	r.Header.Set("X-Custom", "custom-value")
	w := httptest.NewRecorder()

	if err := e.Forward(w, r, upstream.URL, df); err != nil {
		t.Fatalf("Forward 返回错误: %v", err)
	}
}