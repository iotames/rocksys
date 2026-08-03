package engine

import (
	"context"
	"io"
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

func TestForwardWebSocketNotImplemented(t *testing.T) {
	e := &Engine{pool: newUpstreamPool()}
	df := testDF("")

	r := httptest.NewRequest(http.MethodGet, "http://client/", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()

	if err := e.Forward(w, r, "http://127.0.0.1:1", df); err == nil {
		t.Fatal("WebSocket 请求应返回 error")
	}
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("状态码 = %d, 期望 %d", w.Code, http.StatusNotImplemented)
	}
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