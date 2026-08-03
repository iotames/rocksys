package chain

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rocksys/internal/dataflow"

	"github.com/iotames/easyserver/httpsvr"
)

// mockMiddleware 通用测试中间件：返回可配置的 next 结果。
type mockMiddleware struct {
	name   string
	next   bool
	caller *atomic.Int32
	write  func(w http.ResponseWriter)
}

func (m *mockMiddleware) Name() string { return m.name }
func (m *mockMiddleware) Handle(ctx *Context) bool {
	if m.caller != nil {
		m.caller.Add(1)
	}
	if m.write != nil {
		m.write(ctx.W)
	}
	return m.next
}

// blockingMiddleware 可阻塞的中间件：用于在途请求快照测试。
type blockingMiddleware struct {
	name    string
	gate    chan struct{}
	entered chan struct{} // 可选：进入 Handle 时通知（确保快照已取）
	write   func(w http.ResponseWriter)
}

func (m *blockingMiddleware) Name() string { return m.name }
func (m *blockingMiddleware) Handle(ctx *Context) bool {
	if m.entered != nil {
		close(m.entered)
	}
	if m.gate != nil {
		<-m.gate
	}
	if m.write != nil {
		m.write(ctx.W)
	}
	return true
}

// tailHook 实现 ResponseHook 的 Tail 中间件。
type tailHook struct {
	name    string
	onResp  func(ctx *Context) error
	gotCode int
	gotBody string
	mu      sync.Mutex
}

func (t *tailHook) Name() string { return t.name }
func (t *tailHook) Handle(ctx *Context) bool {
	return false
}
func (t *tailHook) OnResponse(ctx *Context) error {
	t.mu.Lock()
	t.gotCode = ctx.RespCode
	t.gotBody = string(ctx.RespBody)
	t.mu.Unlock()
	if t.onResp != nil {
		return t.onResp(ctx)
	}
	return nil
}

func newTestCtx() *Context {
	return &Context{
		W:     httptest.NewRecorder(),
		R:     httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil),
		DF:    dataflow.New(httpsvr.NewDataFlow(), httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)),
		RespW: httptest.NewRecorder(),
	}
}

// 场景1：空链 → Execute 返回 true（直通）。
func TestExecuteEmptyChain(t *testing.T) {
	c := New()
	if !c.Execute(newTestCtx()) {
		t.Fatal("空链 Execute 应返回 true")
	}
}

// 场景2：添加返回 false 的中间件（写 403）→ Execute 返回 false、响应 403。
func TestExecuteInterruptWrites403(t *testing.T) {
	c := New()
	c.Add(Head, &mockMiddleware{name: "blocker", next: false, write: func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("forbidden"))
	}})

	rec := httptest.NewRecorder()
	ctx := &Context{W: rec, R: httptest.NewRequest(http.MethodGet, "/", nil),
		DF: dataflow.New(httpsvr.NewDataFlow(), httptest.NewRequest(http.MethodGet, "/", nil)), RespW: rec}

	if c.Execute(ctx) {
		t.Fatal("返回 false 的中间件应中断链")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("期望 403，得到 %d", rec.Code)
	}
	if rec.Body.String() != "forbidden" {
		t.Fatalf("期望 forbidden，得到 %q", rec.Body.String())
	}
}

// 场景3：Replace 替换中间件 → 在途请求（旧快照）不受影响。
func TestReplaceInFlightSnapshot(t *testing.T) {
	c := New()

	block := &blockingMiddleware{name: "old-block", gate: make(chan struct{}), entered: make(chan struct{})}
	oldRan := &atomic.Int32{}
	c.Add(Middle, block)
	c.Add(Middle, &mockMiddleware{name: "old-tail", next: true, caller: oldRan})

	rec := httptest.NewRecorder()
	ctx := &Context{W: rec, R: httptest.NewRequest(http.MethodGet, "/", nil),
		DF: dataflow.New(httpsvr.NewDataFlow(), httptest.NewRequest(http.MethodGet, "/", nil)), RespW: rec}

	done := make(chan bool, 1)
	go func() {
		done <- c.Execute(ctx)
	}()

	// 等待 Execute 已进入旧快照的第一个中间件（阻塞在 gate 上）
	<-block.entered
	select {
	case <-done:
		t.Fatal("Execute 不应在阻塞中间件释放前返回")
	case <-time.After(50 * time.Millisecond):
	}

	// Replace 整个 Middle 槽位为新中间件
	newRan := &atomic.Int32{}
	c.Replace(Middle, []Middleware{&mockMiddleware{name: "new-mw", next: true, caller: newRan}})

	// 释放旧快照的阻塞中间件
	close(block.gate)

	if !<-done {
		t.Fatal("旧快照执行应返回 true")
	}
	if oldRan.Load() != 1 {
		t.Fatalf("旧中间件应被调用 1 次，得到 %d", oldRan.Load())
	}
	if newRan.Load() != 0 {
		t.Fatalf("新中间件不应被在途请求调用，得到 %d", newRan.Load())
	}
}

// 场景4：挂 ResponseHook Tail 中间件 → Adapter.Handler 全流程后 OnResponse 被调用、
//
//	ctx.RespBody 为上游响应体；未调 WriteFinal 时缓冲内容原样写回客户端。
func TestAdapterTailHookPassthrough(t *testing.T) {
	ch := New()
	hook := &tailHook{name: "obs"}
	ch.Add(Tail, hook)

	adapter := NewAdapter(ch, "http://default", func(w http.ResponseWriter, r *http.Request, target string, df *dataflow.DataFlow) error {
		if target != "http://default" {
			t.Fatalf("期望默认 upstream，得到 %q", target)
		}
		w.Header().Set("X-Upstream", "up-1")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream-body"))
		return nil
	})

	rec := httptest.NewRecorder()
	next := adapter.Handler(rec, httptest.NewRequest(http.MethodGet, "/", nil), httpsvr.NewDataFlow())

	if next {
		t.Fatal("Adapter.Handler 应返回 false")
	}
	hook.mu.Lock()
	code, body := hook.gotCode, hook.gotBody
	hook.mu.Unlock()
	if code != http.StatusOK {
		t.Fatalf("OnResponse 应看到 200，得到 %d", code)
	}
	if body != "upstream-body" {
		t.Fatalf("OnResponse 应看到上游响应体，得到 %q", body)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "upstream-body" {
		t.Fatalf("未调 WriteFinal 时缓冲内容应原样回写，得到 %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Upstream") != "up-1" {
		t.Fatalf("上游响应头应回写，得到 %q", rec.Header().Get("X-Upstream"))
	}
}

// 场景4b：Tail hook 调用 WriteFinal 改写响应 → 客户端收到改写后的内容。
func TestAdapterTailHookWriteFinal(t *testing.T) {
	ch := New()
	ch.Add(Tail, &tailHook{name: "result", onResp: func(ctx *Context) error {
		return ctx.WriteFinal(http.StatusCreated, http.Header{"X-Rewritten": {"1"}}, []byte("rewritten"))
	}})

	adapter := NewAdapter(ch, "http://default", func(w http.ResponseWriter, r *http.Request, target string, df *dataflow.DataFlow) error {
		w.Header().Set("X-Upstream", "up-1")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream-body"))
		return nil
	})

	rec := httptest.NewRecorder()
	adapter.Handler(rec, httptest.NewRequest(http.MethodGet, "/", nil), httpsvr.NewDataFlow())

	if rec.Code != http.StatusCreated {
		t.Fatalf("WriteFinal 后应返回 201，得到 %d", rec.Code)
	}
	if rec.Body.String() != "rewritten" {
		t.Fatalf("WriteFinal 后应返回 rewritten，得到 %q", rec.Body.String())
	}
	if rec.Header().Get("X-Rewritten") != "1" {
		t.Fatalf("WriteFinal 的响应头应写入，得到 %q", rec.Header().Get("X-Rewritten"))
	}
}

// 场景4c：无 Tail ResponseHook → 不缓冲、直接流式写回。
func TestAdapterNoHookDirectStream(t *testing.T) {
	ch := New()
	adapter := NewAdapter(ch, "http://default", func(w http.ResponseWriter, r *http.Request, target string, df *dataflow.DataFlow) error {
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte("direct"))
		return nil
	})

	rec := httptest.NewRecorder()
	adapter.Handler(rec, httptest.NewRequest(http.MethodGet, "/", nil), httpsvr.NewDataFlow())

	if rec.Code != http.StatusAccepted || rec.Body.String() != "direct" {
		t.Fatalf("无 hook 应直写，得到 %d %q", rec.Code, rec.Body.String())
	}
}

// respBufferWriter：重复 WriteHeader 忽略 + 截断直写。
func TestRespBufferWriterTruncate(t *testing.T) {
	rec := httptest.NewRecorder()
	b := newRespBufferWriter(rec)

	b.WriteHeader(200)
	b.WriteHeader(500) // 重复调用应忽略
	if b.Status() != 200 {
		t.Fatalf("重复 WriteHeader 不应覆盖，得到 %d", b.Status())
	}

	big := make([]byte, respBufferLimit+10)
	for i := range big {
		big[i] = 'x'
	}
	b.Write(big)

	if !b.truncated {
		t.Fatal("超出缓冲上限应置截断标记")
	}
	if rec.Code != 200 {
		t.Fatalf("截断直写时应保留状态码 200，得到 %d", rec.Code)
	}
	if len(rec.Body.Bytes()) != len(big) {
		t.Fatalf("截断后全部数据应直写底层，得到 %d 期望 %d", len(rec.Body.Bytes()), len(big))
	}
}
