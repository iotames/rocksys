// /admin/log/* 端点验收测试：鉴权 401、
// 级别/文件通道热切、tail 增量与 reset、SSE 实时推送与断连无泄漏。
package adminapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iotames/easyserver/httpsvr"
	"github.com/iotames/easyserver/log"
)

// syncRecorder 线程安全的 ResponseRecorder（SSE 推送与测试读 body 并发，避免 -race 竞争）。
// 同时实现 http.Flusher 以满足 handleLogStream 的流式刷新；notify 在首次 Flush 时关闭，
// 供测试等待「SSE 连接已建立（首帧已发、since 快照已取）」。
type syncRecorder struct {
	mu     sync.Mutex
	rec    *httptest.ResponseRecorder
	notify chan struct{}
	once   sync.Once
}

func (r *syncRecorder) Header() http.Header { return r.rec.Header() }

func (r *syncRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.WriteHeader(code)
}

func (r *syncRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Write(p)
}

func (r *syncRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.Flush()
	r.once.Do(func() { close(r.notify) })
}

func (r *syncRecorder) Body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Body.String()
}

func (r *syncRecorder) Code() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Code
}

// tailResp /admin/log/tail 的响应结构。
type tailResp struct {
	Lines      []string `json:"lines"`
	NextOffset int64    `json:"next_offset"`
	EOF        bool     `json:"eof"`
	Reset      bool     `json:"reset"`
}

func decodeTail(t *testing.T, rec *httptest.ResponseRecorder) tailResp {
	t.Helper()
	var out tailResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析 tail 响应失败: %v, body=%s", err, rec.Body.String())
	}
	return out
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if strings.Contains(x, s) {
			return true
		}
	}
	return false
}

func TestParseN(t *testing.T) {
	cases := []struct {
		in   string
		def  int
		want int
	}{
		{"", 100, 100},      // 缺省 → 回退 def
		{"10", 100, 10},     // 正常
		{"0", 100, 1},       // 下限夹取
		{"-1", 100, 1},      // 负数夹取
		{"5000", 100, 1000}, // 上限夹取
		{"abc", 50, 50},     // 非法 → 回退 def
		{"1000", 100, 1000}, // 上限边界合法
	}
	for _, c := range cases {
		if got := parseN(c.in, c.def); got != c.want {
			t.Errorf("parseN(%q, %d)=%d, want %d", c.in, c.def, got, c.want)
		}
	}
}

func TestParseSince(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", -1},         // 缺省 → 尾部首拉
		{"12345", 12345}, // 正常增量游标
		{"0", 0},         // since=0 合法增量游标（窗口最旧 n 行）
		{"-1", -1},       // 任意负数 → 尾部首拉
		{"-5", -1},       // 任意负数 → 尾部首拉
		{"abc", -1},      // 非法 → 尾部首拉
	}
	for _, c := range cases {
		if got := parseSince(c.in); got != c.want {
			t.Errorf("parseSince(%q)=%d, want %d", c.in, got, c.want)
		}
	}
}

func TestSlogLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"INFO", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"verbose", slog.LevelInfo},
	}
	for _, c := range cases {
		if got := slogLevel(c.in); got != c.want {
			t.Errorf("slogLevel(%q)=%v, want %v", c.in, got, c.want)
		}
	}
}

func TestValidLevel(t *testing.T) {
	for _, s := range []string{"debug", "info", "warn", "warning", "error", "DEBUG", "Warn"} {
		if !validLevel(s) {
			t.Errorf("validLevel(%q)=false, want true", s)
		}
	}
	for _, s := range []string{"", "verbose", "fatal", "123"} {
		if validLevel(s) {
			t.Errorf("validLevel(%q)=true, want false", s)
		}
	}
}

// TestLogEndpointsRequireAuth 未认证访问 5 个 /admin/log/* 端点 → 401（走真实 HTTP 路由链，
// 同时验证 5 条精确路由已注册——未注册会 404 而非 401）。
func TestLogEndpointsRequireAuth(t *testing.T) {
	// New(nil) 时静态 token 未配置（token 指针为 nil），非回环地址必须鉴权。

	// 非回环地址绑定 → 必须鉴权；未初始化且无用户 → 一律 401。
	s := New("0.0.0.0:19527", nil, nil, nil)
	s.srv.SetQuiet(true)
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/admin/log/info"},
		{http.MethodPost, "/admin/log/level"},
		{http.MethodPost, "/admin/log/output"},
		{http.MethodGet, "/admin/log/tail"},
		{http.MethodGet, "/admin/log/stream"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		rec := httptest.NewRecorder()
		s.srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 未认证应 401，got %d", c.method, c.path, rec.Code)
		}
	}
}

// TestHandleLogLevel 级别热改：合法 200、非法 400、空 body 400、缺 level 400。
func TestHandleLogLevel(t *testing.T) {
	s := New("127.0.0.1:19527", nil, nil, nil)

	// 保存原始级别，测试后恢复（log 包为进程级全局状态）。
	before := log.GetInfo().Level
	t.Cleanup(func() { log.SetLevel(slogLevel(before)) })

	// 合法值 → 200 且立即生效
	ctx := newCtx(http.MethodPost, "/admin/log/level", `{"level":"debug"}`)
	s.handleLogLevel(ctx)
	if got := ctx.Writer.(*httptest.ResponseRecorder).Code; got != http.StatusOK {
		t.Fatalf("合法级别应 200，got %d", got)
	}
	if out := decode(t, ctx); out["ok"] != true {
		t.Fatalf("应 ok:true，got %v", out)
	}
	if got := log.GetInfo().Level; got != "DEBUG" {
		t.Fatalf("级别未生效，got %s, want DEBUG", got)
	}

	// 非法值 → 400 且级别不变
	ctx = newCtx(http.MethodPost, "/admin/log/level", `{"level":"verbose"}`)
	s.handleLogLevel(ctx)
	if got := ctx.Writer.(*httptest.ResponseRecorder).Code; got != http.StatusBadRequest {
		t.Fatalf("非法级别应 400，got %d", got)
	}

	// 空 body → 400
	ctx = newCtx(http.MethodPost, "/admin/log/level", "")
	s.handleLogLevel(ctx)
	if got := ctx.Writer.(*httptest.ResponseRecorder).Code; got != http.StatusBadRequest {
		t.Fatalf("空 body 应 400，got %d", got)
	}

	// 缺 level 字段 → 400
	ctx = newCtx(http.MethodPost, "/admin/log/level", `{}`)
	s.handleLogLevel(ctx)
	if got := ctx.Writer.(*httptest.ResponseRecorder).Code; got != http.StatusBadRequest {
		t.Fatalf("缺 level 应 400，got %d", got)
	}
}

// TestHandleLogOutput 文件通道热改：file:true 开通道 + 持久化，file:false 关通道 + 持久化。
func TestHandleLogOutput(t *testing.T) {
	cfgMgr, _, _ := setup(t)
	s := New("127.0.0.1:19527", cfgMgr, nil, nil)

	// 用临时目录作为日志文件路径与 5MB 上限，避免污染仓库。
	logPath := filepath.Join(t.TempDir(), "output.log")
	if err := cfgMgr.Set("ROCKSYS_LOG_FILE", logPath); err != nil {
		t.Fatalf("Set ROCKSYS_LOG_FILE: %v", err)
	}
	if err := cfgMgr.Set("ROCKSYS_LOG_MAX_SIZE", "5"); err != nil {
		t.Fatalf("Set ROCKSYS_LOG_MAX_SIZE: %v", err)
	}
	// 测试结束后关闭文件通道，避免异步 fileWriter 污染后续测试。
	t.Cleanup(func() { _ = log.SetFileWriter(false) })

	// file:true → 开文件通道 + 持久化
	ctx := newCtx(http.MethodPost, "/admin/log/output", `{"file":true}`)
	s.handleLogOutput(ctx)
	if got := ctx.Writer.(*httptest.ResponseRecorder).Code; got != http.StatusOK {
		t.Fatalf("file:true 应 200，got %d", got)
	}
	if out := decode(t, ctx); out["ok"] != true {
		t.Fatalf("file:true 应 ok:true，got %v", out)
	}
	info := log.GetInfo()
	if !info.FileOn {
		t.Fatal("file:true 后 FileOn 应为 true")
	}
	if info.FilePath != logPath {
		t.Fatalf("FilePath=%q, want %q", info.FilePath, logPath)
	}
	if info.MaxSizeMB != 5 {
		t.Fatalf("MaxSizeMB=%d, want 5", info.MaxSizeMB)
	}
	if !cfgMgr.Current().LogToFile {
		t.Fatal("ROCKSYS_LOG_TO_FILE 未持久化为 true")
	}
	// 文件通道已开，写日志不阻塞主通道（红线：文件异步落盘）。
	log.Info("output-test-write")

	// file:false → 关文件通道 + 持久化
	ctx = newCtx(http.MethodPost, "/admin/log/output", `{"file":false}`)
	s.handleLogOutput(ctx)
	if got := ctx.Writer.(*httptest.ResponseRecorder).Code; got != http.StatusOK {
		t.Fatalf("file:false 应 200，got %d", got)
	}
	if out := decode(t, ctx); out["ok"] != true {
		t.Fatalf("file:false 应 ok:true，got %v", out)
	}
	if log.GetInfo().FileOn {
		t.Fatal("file:false 后 FileOn 应为 false")
	}
	if cfgMgr.Current().LogToFile {
		t.Fatal("ROCKSYS_LOG_TO_FILE 未持久化为 false")
	}
}

// TestHandleLogTail tail 增量读取与 reset：首次尾部首拉 → 无新日志 EOF → 增量续读 → 过期游标 Reset。
func TestHandleLogTail(t *testing.T) {
	s := New("127.0.0.1:19527", nil, nil, nil)

	// 写入两条日志作为增量数据源。
	base := time.Now().UnixNano()
	msg1 := fmt.Sprintf("tail-test-line-%d", base)
	msg2 := fmt.Sprintf("tail-test-line-%d", base+1)
	log.Info(msg1)
	log.Info(msg2)

	// 首次拉取（since 缺省 → 尾部首拉）
	ctx := newCtx(http.MethodGet, "/admin/log/tail?n=10", "")
	s.handleLogTail(ctx)
	rec := ctx.Writer.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusOK {
		t.Fatalf("tail 应 200，got %d", rec.Code)
	}
	first := decodeTail(t, rec)
	if first.Reset {
		t.Fatalf("首次拉取不应 reset: %+v", first)
	}
	if len(first.Lines) < 2 {
		t.Fatalf("应至少返回 2 行，got %d", len(first.Lines))
	}
	if !containsStr(first.Lines, msg1) || !containsStr(first.Lines, msg2) {
		t.Fatalf("首次拉取应包含刚写入的日志: %v", first.Lines)
	}
	if first.NextOffset <= 0 {
		t.Fatalf("next_offset 应为正数，got %d", first.NextOffset)
	}

	// 增量：since=next_offset 且无新日志 → eof=true、lines 序列化为空数组 []（非 null）
	ctx = newCtx(http.MethodGet, fmt.Sprintf("/admin/log/tail?since=%d", first.NextOffset), "")
	s.handleLogTail(ctx)
	rec = ctx.Writer.(*httptest.ResponseRecorder)
	second := decodeTail(t, rec)
	if !second.EOF {
		t.Fatalf("无新日志应 eof=true: %+v", second)
	}
	if len(second.Lines) != 0 {
		t.Fatalf("无新日志应无行: %+v", second)
	}
	if !strings.Contains(rec.Body.String(), `"lines":[]`) {
		t.Fatalf("空行应序列化为 [] 而非 null: %s", rec.Body.String())
	}

	// 再写一条 → 增量续读可读到新行，且不重复旧行
	msg3 := fmt.Sprintf("tail-test-line-%d", base+2)
	log.Info(msg3)
	ctx = newCtx(http.MethodGet, fmt.Sprintf("/admin/log/tail?since=%d", first.NextOffset), "")
	s.handleLogTail(ctx)
	rec = ctx.Writer.(*httptest.ResponseRecorder)
	third := decodeTail(t, rec)
	if third.EOF || third.Reset {
		t.Fatalf("有增量应非 EOF/reset: %+v", third)
	}
	if !containsStr(third.Lines, msg3) {
		t.Fatalf("增量应读到新行 %s: %v", msg3, third.Lines)
	}
	if containsStr(third.Lines, msg1) || containsStr(third.Lines, msg2) {
		t.Fatalf("增量续读不应重复旧行: %v", third.Lines)
	}

	// 过期游标（since 大于当前 total）→ reset=true、next_offset=当前 total
	ctx = newCtx(http.MethodGet, "/admin/log/tail?since=999999999999", "")
	s.handleLogTail(ctx)
	rec = ctx.Writer.(*httptest.ResponseRecorder)
	stale := decodeTail(t, rec)
	if !stale.Reset {
		t.Fatalf("过期游标应 reset=true: %+v", stale)
	}
	if stale.NextOffset != log.GetInfo().RingTotal {
		t.Fatalf("reset 的 next_offset 应为当前 total=%d, got %d", log.GetInfo().RingTotal, stale.NextOffset)
	}
}

// TestHandleLogStream SSE 直接 handler：连接建立后写入一条日志应收到 data 帧；
// 客户端断开（context 取消）后 goroutine 退出（无泄漏）。
func TestHandleLogStream(t *testing.T) {
	s := New("127.0.0.1:19527", nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/admin/log/stream", nil).WithContext(ctx)
	rec := &syncRecorder{rec: httptest.NewRecorder(), notify: make(chan struct{})}
	hctx := httpsvr.Context{Writer: rec, Request: req}

	done := make(chan struct{})
	go func() {
		s.handleLogStream(hctx)
		close(done)
	}()

	// 等待首帧（连接建立、since 快照已取）后再写日志，避免快照竞态漏推。
	select {
	case <-rec.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE 连接未建立（未收到首帧）")
	}
	if rec.Code() != http.StatusOK {
		t.Fatalf("SSE 应 200，got %d", rec.Code())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type=%q, want text/event-stream", ct)
	}

	// 连接建立后写入一条日志，应收到 data: 帧。
	msg := fmt.Sprintf("sse-test-message-%d", time.Now().UnixNano())
	log.Info(msg)
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(rec.Body(), msg) {
		if time.Now().After(deadline) {
			t.Fatalf("3s 内未收到 data 帧，body=%q", rec.Body())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(rec.Body(), "data: ") {
		t.Fatalf("缺少 data: 帧: %q", rec.Body())
	}

	// 客户端断开 → goroutine 退出（无泄漏）。
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 SSE goroutine 未退出（存在泄漏）")
	}
}

// TestHandleLogStreamHeartbeat SSE 心跳帧断言：
// 无新日志时每 500ms 发送注释行 `: ping`（防代理空闲断开），不依赖是否有日志。
func TestHandleLogStreamHeartbeat(t *testing.T) {
	s := New("127.0.0.1:19527", nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/admin/log/stream", nil).WithContext(ctx)
	rec := &syncRecorder{rec: httptest.NewRecorder(), notify: make(chan struct{})}
	hctx := httpsvr.Context{Writer: rec, Request: req}

	done := make(chan struct{})
	go func() {
		s.handleLogStream(hctx)
		close(done)
	}()

	// 等待连接建立（首帧已发）后，不写任何日志，只等心跳帧。
	select {
	case <-rec.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE 连接未建立（未收到首帧）")
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(rec.Body(), ": ping") {
		if time.Now().After(deadline) {
			t.Fatalf("3s 内未收到心跳帧 `: ping`，body=%q", rec.Body())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(rec.Body(), "data: ") {
		t.Fatalf("无日志输入时不应出现 data 帧，body=%q", rec.Body())
	}

	// 客户端断开 → goroutine 退出（无泄漏）。
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 SSE goroutine 未退出（存在泄漏）")
	}
}

// TestHandleLogStreamHTTP SSE 走真实 HTTP 链（路由 + 回环免鉴权 + 流式响应）：
// 收到 data 帧后断开连接，服务端 handler 正常退出。
func TestHandleLogStreamHTTP(t *testing.T) {
	s := New("127.0.0.1:19527", nil, nil, nil)
	s.srv.SetQuiet(true)
	ts := httptest.NewServer(s.srv)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/admin/log/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE 请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE 应 200，got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type=%q, want text/event-stream", ct)
	}

	// 周期性写入同一条日志，保证至少一次写入落在 SSE 连接建立（since 快照）之后，
	// 避免「客户端收到响应头早于服务端 since 快照」的竞态漏推。
	msg := fmt.Sprintf("sse-http-message-%d", time.Now().UnixNano())
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				log.Info(msg)
			}
		}
	}()
	// 注意：stop 在下方 select 的两分支均显式关闭，此处不再 defer（避免 double-close panic）。

	br := bufio.NewReader(resp.Body)
	resCh := make(chan struct{}, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if strings.Contains(line, msg) {
				resCh <- struct{}{}
				return
			}
			if err != nil {
				return // 连接断开（取消后），未收到目标帧由主测试判定超时
			}
		}
	}()

	select {
	case <-resCh:
		// 收到 data 帧，停止周期性写入。
		close(stop)
	case <-time.After(3 * time.Second):
		close(stop)
		t.Fatal("3s 内未收到 SSE data 帧")
	}

	cancel() // 断开连接 → 服务端 handler 在 500ms 内退出
	// 等待 handler goroutine 退出，避免 httptest.Server.Close 长时间等待。
	time.Sleep(600 * time.Millisecond)
}
