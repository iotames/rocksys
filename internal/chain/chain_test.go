package chain

import (
	"fmt"
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

// 回归测试 BUG#2：上游设置了 Content-Length，但 Tail hook 用 WriteFinal 改写为
// 不同长度的 body 时，必须删除过期的 Content-Length，由 Go 按实际 body 重新计算，
// 否则客户端会因长度不匹配而截断响应（曾导致 0 字节接收）。
func TestWriteFinalDropsStaleContentLength(t *testing.T) {
	ch := New()
	ch.Add(Tail, &tailHook{name: "result", onResp: func(ctx *Context) error {
		// 上游 body 为 68 字节，改写为更短的 body，长度必然不一致。
		return ctx.WriteFinal(http.StatusOK, nil, []byte(`{"code":0,"msg":"ok"}`))
	}})

	adapter := NewAdapter(ch, "http://default", func(w http.ResponseWriter, r *http.Request, target string, df *dataflow.DataFlow) error {
		w.Header().Set("Content-Length", "68") // 模拟上游设置的过期长度
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream-body-with-a-longer-length-than-rewritten"))
		return nil
	})

	rec := httptest.NewRecorder()
	adapter.Handler(rec, httptest.NewRequest(http.MethodGet, "/", nil), httpsvr.NewDataFlow())

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d", rec.Code)
	}
	got := rec.Body.String()
	want := `{"code":0,"msg":"ok"}`
	if got != want {
		t.Fatalf("WriteFinal 改写后 body 应完整返回，得到 %q 期望 %q", got, want)
	}
	// 关键断言：Content-Length 必须与改写后的实际 body 长度一致。
	if cl := rec.Header().Get("Content-Length"); cl != "" && cl != fmt.Sprintf("%d", len(want)) {
		t.Fatalf("Content-Length 应与改写后 body 一致，得到 %q 期望 %d", cl, len(want))
	}
}

// 回归测试 BUG#1：Adapter 的默认 upstream 应支持运行时热更（SetDefaultUpstream），
// 而非构造时固定。热更后新请求必须转发到新 upstream。
func TestAdapterSetDefaultUpstreamHotReload(t *testing.T) {
	ch := New()
	var gotTarget atomic.Value
	adapter := NewAdapter(ch, "http://old", func(w http.ResponseWriter, r *http.Request, target string, df *dataflow.DataFlow) error {
		gotTarget.Store(target)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
		return nil
	})

	// 初始请求走旧 upstream
	rec := httptest.NewRecorder()
	adapter.Handler(rec, httptest.NewRequest(http.MethodGet, "/", nil), httpsvr.NewDataFlow())
	if gotTarget.Load() != "http://old" {
		t.Fatalf("初始应转发到 http://old，得到 %q", gotTarget.Load())
	}

	// 热更到新 upstream
	adapter.SetDefaultUpstream("http://new")

	rec2 := httptest.NewRecorder()
	adapter.Handler(rec2, httptest.NewRequest(http.MethodGet, "/", nil), httpsvr.NewDataFlow())
	if gotTarget.Load() != "http://new" {
		t.Fatalf("热更后应转发到 http://new，得到 %q", gotTarget.Load())
	}
}

// ---- P3：请求路径 panic 兜底（recover）----

// panicMiddleware 构造即 panic 的中间件。
type panicMiddleware struct{ name string }

func (m *panicMiddleware) Name() string             { return m.name }
func (m *panicMiddleware) Handle(ctx *Context) bool { panic("boom-" + m.name) }

// panicHook 实现 ResponseHook 的 panic Tail 中间件（Handle 不参与转发前）。
type panicHook struct {
	name string
}

func (h *panicHook) Name() string                  { return h.name }
func (h *panicHook) Handle(ctx *Context) bool      { return false }
func (h *panicHook) OnResponse(ctx *Context) error { panic("boom-hook-" + h.name) }

// panicAfterWriteHook 先 WriteFinal 再 panic 的 Tail 中间件。
type panicAfterWriteHook struct {
	name string
}

func (h *panicAfterWriteHook) Name() string             { return h.name }
func (h *panicAfterWriteHook) Handle(ctx *Context) bool { return false }
func (h *panicAfterWriteHook) OnResponse(ctx *Context) error {
	_ = ctx.WriteFinal(http.StatusCreated, nil, []byte("final"))
	panic("boom-after-write-" + h.name)
}

// TestExecuteHeadPanicRecovers Head 槽位 panic → safeHandle 写 500、Execute 返回 false、不 panic 上抛。
func TestExecuteHeadPanicRecovers(t *testing.T) {
	c := New()
	c.Add(Head, &panicMiddleware{name: "panicky"})

	rec := httptest.NewRecorder()
	ctx := &Context{W: rec, R: httptest.NewRequest(http.MethodGet, "/", nil),
		DF: dataflow.New(httpsvr.NewDataFlow(), httptest.NewRequest(http.MethodGet, "/", nil)), RespW: rec}

	if c.Execute(ctx) {
		t.Fatal("panic 中间件应中断链（Execute 返回 false）")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic 后客户端应收到 500，得到 %d", rec.Code)
	}
}

// TestExecuteMiddlePanicRecovers Middle 槽位 panic → 同样兜底为 500。
func TestExecuteMiddlePanicRecovers(t *testing.T) {
	c := New()
	c.Add(Middle, &panicMiddleware{name: "panicky"})

	rec := httptest.NewRecorder()
	ctx := &Context{W: rec, R: httptest.NewRequest(http.MethodGet, "/", nil),
		DF: dataflow.New(httpsvr.NewDataFlow(), httptest.NewRequest(http.MethodGet, "/", nil)), RespW: rec}

	if c.Execute(ctx) {
		t.Fatal("panic 中间件应中断链（Execute 返回 false）")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic 后客户端应收到 500，得到 %d", rec.Code)
	}
}

// TestExecutePanicDoesNotKillChain panic 请求后，同一 Chain 后续正常请求不受影响。
func TestExecutePanicDoesNotKillChain(t *testing.T) {
	c := New()
	c.Add(Middle, &panicMiddleware{name: "panicky"})
	c.Add(Tail, &tailHook{name: "obs"}) // Tail 挂 hook 验证链结构未污染

	rec := httptest.NewRecorder()
	ctx := &Context{W: rec, R: httptest.NewRequest(http.MethodGet, "/", nil),
		DF: dataflow.New(httpsvr.NewDataFlow(), httptest.NewRequest(http.MethodGet, "/", nil)), RespW: rec}
	_ = c.Execute(ctx) // panic 请求

	// 移除 panic 中间件后恢复正常
	if err := c.Remove("panicky"); err != nil {
		t.Fatalf("Remove err: %v", err)
	}
	if !c.Execute(ctx) {
		t.Fatal("移除 panic 中间件后 Execute 应返回 true")
	}
}

// TestResponseHookPanicContinues Tail hook panic → 仅记录日志，后续 hook 继续执行，客户端响应正常。
// 注意：ResponseHooks(Tail) 返回注册逆序（impl.go），后注册者先执行。
// 此处 tailHook 先注册（执行顺序第二）、panicHook 后注册（执行顺序第一）。
func TestResponseHookPanicContinues(t *testing.T) {
	ch := New()
	hook := &tailHook{name: "obs"}
	ch.Add(Tail, hook)                        // 注册顺序 1 → 执行顺序第二
	ch.Add(Tail, &panicHook{name: "panicky"}) // 注册顺序 2 → 执行顺序第一（先 panic）

	adapter := NewAdapter(ch, "http://default", func(w http.ResponseWriter, r *http.Request, target string, df *dataflow.DataFlow) error {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
		return nil
	})

	rec := httptest.NewRecorder()
	adapter.Handler(rec, httptest.NewRequest(http.MethodGet, "/", nil), httpsvr.NewDataFlow())

	if rec.Code != http.StatusOK {
		t.Fatalf("客户端响应应为 200，得到 %d", rec.Code)
	}
	hook.mu.Lock()
	defer hook.mu.Unlock()
	if hook.gotCode != http.StatusOK {
		t.Fatalf("panic hook 之后的 hook 仍应执行且拿到上游码 200，gotCode = %d", hook.gotCode)
	}
}

// TestResponseHookPanicAfterWriteFinal 执行顺序第一的 hook WriteFinal 后 panic：
// 客户端收到改写响应，第二个 hook 仍执行。
func TestResponseHookPanicAfterWriteFinal(t *testing.T) {
	ch := New()
	hook := &tailHook{name: "obs"}
	ch.Add(Tail, hook)                            // 注册顺序 1 → 执行顺序第二
	ch.Add(Tail, &panicAfterWriteHook{name: "w"}) // 注册顺序 2 → 执行顺序第一（WriteFinal 后 panic）

	adapter := NewAdapter(ch, "http://default", func(w http.ResponseWriter, r *http.Request, target string, df *dataflow.DataFlow) error {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream"))
		return nil
	})

	rec := httptest.NewRecorder()
	adapter.Handler(rec, httptest.NewRequest(http.MethodGet, "/", nil), httpsvr.NewDataFlow())

	if rec.Code != http.StatusCreated {
		t.Fatalf("WriteFinal 改写响应应生效，客户端收到 201，得到 %d", rec.Code)
	}
	if rec.Body.String() != "final" {
		t.Fatalf("WriteFinal 改写 body 应生效，得到 %q", rec.Body.String())
	}
	hook.mu.Lock()
	defer hook.mu.Unlock()
	if hook.gotCode == 0 {
		t.Fatal("WriteFinal 后 panic 不应中断后续 hook，第二个 hook 仍应执行")
	}
}
