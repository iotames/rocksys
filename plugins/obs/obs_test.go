package obs

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/dataflow"
	"rocksys/internal/hotswap"

	"github.com/iotames/easyserver/httpsvr"
)

// fakeConfMgr 测试用假配置管理器：仅记录注册项，不触发真实重载。
type fakeConfMgr struct {
	regs map[string]any
}

func newFakeConf() *fakeConfMgr { return &fakeConfMgr{regs: make(map[string]any)} }

func (f *fakeConfMgr) Current() *conf.Config          { return nil }
func (f *fakeConfMgr) Watch(func(*conf.Config))       {}
func (f *fakeConfMgr) StartWatcher() error            { return nil }
func (f *fakeConfMgr) Shutdown(context.Context) error { return nil }
func (f *fakeConfMgr) Register(pval any, name, defval, title string, usage ...string) error {
	f.regs[name] = pval
	return nil
}
func (f *fakeConfMgr) Set(name, value string) error { return nil }

// newTestObs 构造写往 t.TempDir() 的 Obs。
func newTestObs(t *testing.T) (*Obs, *fakeConfMgr) {
	t.Helper()
	f := newFakeConf()
	o := New(f)
	o.logDir = t.TempDir()
	if err := o.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return o, f
}

// newCtx 构造携带指定三时间戳的 chain.Context。
func newCtx(r *http.Request, status int, body []byte, shieldMs, bizMs int64) *chain.Context {
	inner := httpsvr.NewDataFlow()
	df := dataflow.New(inner, r)
	begin := df.BeginAt()
	df.SetBeginBizAt(begin.Add(time.Duration(shieldMs) * time.Millisecond))
	df.SetDoneBizAt(begin.Add(time.Duration(shieldMs+bizMs) * time.Millisecond))
	df.SetTraceID("trace-001")
	df.SetTenantID("tenant-1")
	df.SetTarget("http://upstream:8080")
	return &chain.Context{
		R:        r,
		DF:       df,
		RespCode: status,
		RespBody: body,
	}
}

// New 应注册 §14 配置项；Name/Slot/Handle 符合接口约定。
func TestNewRegistersConfig(t *testing.T) {
	o, f := newTestObs(t)
	for _, n := range []string{"OBS_LOG_DIR", "OBS_RETENTION_DAYS"} {
		if _, ok := f.regs[n]; !ok {
			t.Errorf("应注册配置项 %s", n)
		}
	}
	if o.Name() != "obs" {
		t.Errorf("Name 应为 obs，实际 %q", o.Name())
	}
	if o.Slot() != chain.Tail {
		t.Errorf("Slot 应为 Tail，实际 %v", o.Slot())
	}
	if next := o.Handle(&chain.Context{}); next {
		t.Error("Handle 应返回 false（占位）")
	}
}

// OnResponse 后日志文件存在且 JSON 行可解析、字段正确。
func TestOnResponseWritesLog(t *testing.T) {
	o, _ := newTestObs(t)

	r := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader("hello"))
	r.RemoteAddr = "127.0.0.1:1234"
	ctx := newCtx(r, 200, []byte("ok"), 5, 20)
	if err := o.OnResponse(ctx); err != nil {
		t.Fatalf("OnResponse: %v", err)
	}
	if err := o.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	path := filepath.Join(o.logDir, "access-"+time.Now().Format("2006-01-02")+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("日志文件不存在: %v", err)
	}
	var al AccessLog
	if err := json.Unmarshal(data, &al); err != nil {
		t.Fatalf("JSON 行不可解析: %v, line=%q", err, data)
	}
	if al.TraceID != "trace-001" || al.TenantID != "tenant-1" {
		t.Errorf("trace/tenant = %q/%q", al.TraceID, al.TenantID)
	}
	if al.Path != "/api/test" || al.Method != http.MethodPost {
		t.Errorf("path/method = %q/%q", al.Path, al.Method)
	}
	if al.StatusCode != 200 || al.Upstream != "http://upstream:8080" {
		t.Errorf("status/upstream = %d/%q", al.StatusCode, al.Upstream)
	}
	if al.ShieldMs != 5 || al.BizMs != 20 || al.TotalMs != 25 {
		t.Errorf("耗时 = %d/%d/%d，期望 5/20/25", al.ShieldMs, al.BizMs, al.TotalMs)
	}
	if al.ReqBytes != 5 || al.RespBytes != 2 {
		t.Errorf("字节 = %d/%d，期望 5/2", al.ReqBytes, al.RespBytes)
	}
}

// 多条请求写入同一文件，每行一条 JSON。
func TestOnResponseMultipleLines(t *testing.T) {
	o, _ := newTestObs(t)
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		ctx := newCtx(r, 200, []byte("ok"), 1, 2)
		if err := o.OnResponse(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(o.logDir, "access-"+time.Now().Format("2006-01-02")+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var n int
	for sc.Scan() {
		if !json.Valid(sc.Bytes()) {
			t.Errorf("第 %d 行非合法 JSON: %q", n+1, sc.Bytes())
		}
		n++
	}
	if n != 5 {
		t.Errorf("应有 5 行日志，实际 %d", n)
	}
}

// Metrics 聚合：注入若干耗时/状态码，校验 QPS/分位/错误率。
func TestMetricsAggregation(t *testing.T) {
	m := NewMetrics()
	base := time.Now()
	for i := 0; i < 10; i++ {
		code := 200
		if i == 0 {
			code = 500
		}
		m.Add(base.Add(time.Duration(i)*time.Millisecond), int64((i+1)*10), code)
	}
	s := m.Snapshot(base.Add(time.Second))
	// 10 条 / 60s
	if got := s.QPS; got < 0.16 || got > 0.17 {
		t.Errorf("QPS = %v，期望 ≈ 0.1667", got)
	}
	// 1 条 500 / 10 条
	if s.ErrorRate != 0.1 {
		t.Errorf("错误率 = %v，期望 0.1", s.ErrorRate)
	}
	// 耗时 [10..100]：P50=60, P95=100, P99=100
	if s.P50 != 60 {
		t.Errorf("P50 = %d，期望 60", s.P50)
	}
	if s.P95 != 100 || s.P99 != 100 {
		t.Errorf("P95/P99 = %d/%d，期望 100/100", s.P95, s.P99)
	}
}

// 窗口滑动：超过 60s 的旧桶数据不参与聚合。
func TestMetricsWindowSlides(t *testing.T) {
	m := NewMetrics()
	old := time.Now().Add(-2 * time.Minute)
	m.Add(old, 10, 200)
	m.Add(old.Add(time.Second), 10, 200)
	now := time.Now()
	m.Add(now, 50, 200)
	s := m.Snapshot(now)
	if s.QPS < 1.0/60.0-1e-9 {
		t.Errorf("旧窗口数据不应计入，QPS = %v", s.QPS)
	}
	if s.P50 != 50 {
		t.Errorf("P50 = %d，期望 50（仅当前窗口）", s.P50)
	}
}

// Shutdown flush 内存缓冲 → 日志全部落盘；重复调用幂等。
func TestShutdownFlush(t *testing.T) {
	o, _ := newTestObs(t)
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		if err := o.OnResponse(newCtx(r, 200, []byte("ok"), 1, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := o.Shutdown(context.Background()); err != nil {
		t.Fatalf("重复 Shutdown: %v", err)
	}
	path := filepath.Join(o.logDir, "access-"+time.Now().Format("2006-01-02")+".jsonl")
	data, _ := os.ReadFile(path)
	if got := strings.Count(string(data), "\n"); got != 3 {
		t.Errorf("flush 后应有 3 行日志，实际 %d", got)
	}
}

// Stop() 内部桥接 Shutdown(context.Background())（MiddlewareLifecycle 无 context 参数）。
func TestStopBridgesShutdown(t *testing.T) {
	o, _ := newTestObs(t)
	r := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	if err := o.OnResponse(newCtx(r, 200, []byte("ok"), 1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := o.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	path := filepath.Join(o.logDir, "access-"+time.Now().Format("2006-01-02")+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Stop 后日志应已落盘: %v", err)
	}
}

// 留存清理：超过保留天数的日志被删除，当日保留。
func TestRetentionCleanup(t *testing.T) {
	dir := t.TempDir()
	s := NewFileSink(dir, 30)

	old := filepath.Join(dir, "access-2020-01-01.jsonl")
	if err := os.WriteFile(old, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "access-"+time.Now().Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(keep, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	s.cleanupOld()
	s.mu.Unlock()

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("超过留存天数的日志应被清理")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("当日日志不应被清理")
	}
}

// 队列满时降级丢弃并计数，不阻塞请求。
func TestSinkQueueFullDrops(t *testing.T) {
	s := NewFileSink(t.TempDir(), 30)
	s.mu.Lock()
	s.pending = make([]*AccessLog, pendingCap)
	s.mu.Unlock()
	s.Write(&AccessLog{TraceID: "dropped"})
	if s.DropCount() != 1 {
		t.Errorf("队列满应丢弃并计数，drop = %d", s.DropCount())
	}
}

// /admin/metrics 输出 qps/p95_ms/error_rate。
func TestAdminMetrics(t *testing.T) {
	o, _ := newTestObs(t)
	now := time.Now()
	o.metrics.Add(now, 100, 200)
	o.metrics.Add(now.Add(time.Millisecond), 200, 500)

	mgr := hotswap.NewManager(chain.New(), nil)
	mgr.RegisterMiddleware(o)
	h := NewAdminHandler(mgr)

	rec := httptest.NewRecorder()
	h.Metrics(rec, httptest.NewRequest(http.MethodGet, "/admin/metrics", nil))

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("metrics 响应非 JSON: %v, body=%q", err, rec.Body.String())
	}
	if got["qps"] == nil || got["p95_ms"] == nil || got["error_rate"] == nil {
		t.Errorf("metrics 应含 qps/p95_ms/error_rate，实际 %v", got)
	}
	if v := got["error_rate"].(float64); v != 0.5 {
		t.Errorf("error_rate = %v，期望 0.5", v)
	}
}

// /admin/logs?from=&to= 返回 JSONL。
func TestAdminLogs(t *testing.T) {
	o, _ := newTestObs(t)
	r := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader("hi"))
	ctx := newCtx(r, 200, []byte("ok"), 5, 5)
	if err := o.OnResponse(ctx); err != nil {
		t.Fatal(err)
	}
	if err := o.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	mgr := hotswap.NewManager(chain.New(), nil)
	mgr.RegisterMiddleware(o)
	h := NewAdminHandler(mgr)

	today := time.Now().Format("2006-01-02")
	req := httptest.NewRequest(http.MethodGet, "/admin/logs", nil)
	q := req.URL.Query()
	q.Set("from", today)
	q.Set("to", today)
	req.URL.RawQuery = q.Encode()

	rec := httptest.NewRecorder()
	h.Logs(rec, req)

	var al AccessLog
	if err := json.Unmarshal(rec.Body.Bytes(), &al); err != nil {
		t.Fatalf("logs 返回非 JSONL: %v, body=%q", err, rec.Body.String())
	}
	if al.TraceID != "trace-001" {
		t.Errorf("trace_id = %q，期望 trace-001", al.TraceID)
	}
}

// /admin/logs 非法日期参数 → 400。
func TestAdminLogsBadParams(t *testing.T) {
	o, _ := newTestObs(t)
	mgr := hotswap.NewManager(chain.New(), nil)
	mgr.RegisterMiddleware(o)
	h := NewAdminHandler(mgr)

	req := httptest.NewRequest(http.MethodGet, "/admin/logs?from=bad", nil)
	rec := httptest.NewRecorder()
	h.Logs(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法 from 应返回 400，实际 %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/logs?from=2024-01-02&to=2024-01-01", nil)
	rec = httptest.NewRecorder()
	h.Logs(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("from 晚于 to 应返回 400，实际 %d", rec.Code)
	}
}
