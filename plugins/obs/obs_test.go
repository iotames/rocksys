package obs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/dataflow"
	"rocksys/internal/db"
	"rocksys/internal/hotswap"

	"github.com/iotames/easyserver/httpsvr"

	_ "modernc.org/sqlite"
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
func (f *fakeConfMgr) List() []conf.ConfigItem      { return nil }
func (f *fakeConfMgr) SyncDefaultFile() error       { return nil }

// newTestObs 构造使用 sqlite 临时库的 Obs（访问日志写库场景）。
func newTestObs(t *testing.T) (*Obs, *fakeConfMgr) {
	t.Helper()
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "obs.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	f := newFakeConf()
	o := New(f, d)
	if err := o.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 排空异步队列（Windows 上句柄不关则 TempDir 清理失败；Close 幂等，与测试内 Shutdown 不冲突）。
	t.Cleanup(func() { _ = o.sink.Load().(*AsyncStore).Close() })
	return o, f
}

// newTestObsDB 构造使用 sqlite 数据访问层的 Obs。
func newTestObsDB(t *testing.T) (*Obs, *db.DB) {
	t.Helper()
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "obs.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	f := newFakeConf()
	o := New(f, d)
	if err := o.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = o.sink.Load().(*AsyncStore).Close() })
	return o, d
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
	for _, n := range []string{"OBS_ENABLED", "OBS_LOG_PRUNE_ENABLED", "OBS_LOG_RETENTION_DAYS"} {
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

// OnResponse 后日志写入 access_log 表，平铺维度字段正确。
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

	rows, err := o.Query(Query{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("应有 1 条日志，实际 %d", len(rows))
	}
	m := rows[0]
	if m[DimTraceID] != "trace-001" || m[DimTenantID] != "tenant-1" {
		t.Errorf("trace/tenant = %q/%q", m[DimTraceID], m[DimTenantID])
	}
	if m[DimPath] != "/api/test" || m[DimMethod] != http.MethodPost {
		t.Errorf("path/method = %q/%q", m[DimPath], m[DimMethod])
	}
	if m[DimStatusCode] != int64(200) || m[DimUpstream] != "http://upstream:8080" {
		t.Errorf("status/upstream = %v/%q", m[DimStatusCode], m[DimUpstream])
	}
	if m[DimShieldMs] != int64(5) || m[DimBizMs] != int64(20) || m[DimTotalMs] != int64(25) {
		t.Errorf("耗时 = %v/%v/%v，期望 5/20/25", m[DimShieldMs], m[DimBizMs], m[DimTotalMs])
	}
	if m[DimReqBytes] != int64(5) || m[DimRespBytes] != int64(2) {
		t.Errorf("字节 = %v/%v，期望 5/2", m[DimReqBytes], m[DimRespBytes])
	}
}

// 多条请求各写一行记录。
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
	total, err := o.Count(Query{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Errorf("应有 5 条日志，实际 %d", total)
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

// Shutdown flush 内存缓冲 → 日志全部落库；重复调用幂等。
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
	total, err := o.Count(Query{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Errorf("flush 后应有 3 条日志，实际 %d", total)
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
	total, err := o.Count(Query{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("Stop 后应已落库 1 条，实际 %d", total)
	}
}

// 队列满时降级丢弃并计数，不阻塞请求（AsyncStore）。
func TestAsyncStoreQueueFullDrops(t *testing.T) {
	a := NewAsyncStore(discardStore{})
	a.mu.Lock()
	a.pending = make([]*AccessRecord, asyncCap)
	a.mu.Unlock()
	a.Write(&AccessRecord{TraceID: "dropped"})
	if a.DropCount() != 1 {
		t.Errorf("队列满应丢弃并计数，drop = %d", a.DropCount())
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

	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("logs 返回非 JSONL: %v, body=%q", err, rec.Body.String())
	}
	if m[DimTraceID] != "trace-001" {
		t.Errorf("trace_id = %q，期望 trace-001", m[DimTraceID])
	}
}

// /admin/logs 非法时间参数 → 400。
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

	req = httptest.NewRequest(http.MethodGet, "/admin/logs?from=2024-01-02T10:00&to=2024-01-02T09:00", nil)
	rec = httptest.NewRecorder()
	h.Logs(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("from 晚于 to 应返回 400，实际 %d", rec.Code)
	}
}

// /admin/logs 分钟级时间范围 + path 精确/模糊过滤。
func TestAdminLogsFilters(t *testing.T) {
	o, _ := newTestObs(t)
	// 两条不同路径的请求
	for _, p := range []string{"/api/order/1", "/api/user/list"} {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		if err := o.OnResponse(newCtx(r, 200, []byte("ok"), 1, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	mgr := hotswap.NewManager(chain.New(), nil)
	mgr.RegisterMiddleware(o)
	h := NewAdminHandler(mgr)

	now := time.Now()
	from := now.Format(timeFmtMinute)
	to := now.Format(timeFmtMinute)

	queryLogs := func(extra string) []map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/admin/logs", nil)
		q := req.URL.Query()
		q.Set("from", from)
		q.Set("to", to)
		for _, kv := range strings.Split(extra, "&") {
			if kv == "" {
				continue
			}
			parts := strings.SplitN(kv, "=", 2)
			q.Set(parts[0], parts[1])
		}
		req.URL.RawQuery = q.Encode()
		rec := httptest.NewRecorder()
		h.Logs(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("logs 状态码 = %d, body=%q", rec.Code, rec.Body.String())
		}
		var rows []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("坏行: %q", line)
			}
			rows = append(rows, m)
		}
		return rows
	}

	// 无 path 条件：2 条
	if got := len(queryLogs("")); got != 2 {
		t.Errorf("无 path 条件应有 2 条，实际 %d", got)
	}
	// path 精确
	rows := queryLogs("path=/api/order/1")
	if len(rows) != 1 || rows[0][DimPath] != "/api/order/1" {
		t.Errorf("path 精确过滤错误: %v", rows)
	}
	// path 模糊
	rows = queryLogs("path_like=/api/order")
	if len(rows) != 1 || rows[0][DimPath] != "/api/order/1" {
		t.Errorf("path_like 模糊过滤错误: %v", rows)
	}
	// trace_id 保留（API 层）
	rows = queryLogs("trace_id=trace-001")
	if len(rows) != 2 {
		t.Errorf("trace_id 过滤应有 2 条，实际 %d", len(rows))
	}
}

// DBStore 写读 + 过滤（sqlite 临时库）。
func TestDBStoreWriteQuery(t *testing.T) {
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "obs.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	st := NewDBStore(d, "")
	if err := st.EnsureTable(); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	base := time.Now()
	recs := []*AccessRecord{
		{Time: base.Add(-time.Minute), TraceID: "tid-1", Path: "/api/order/1", Method: http.MethodGet, StatusCode: 200, TotalMs: 5},
		{Time: base.Add(-time.Minute), TraceID: "tid-2", Path: "/api/user/list", Method: http.MethodPost, StatusCode: 500, TotalMs: 9, Extras: map[string]any{"request_body": "a=1"}},
	}
	if err := st.Write(recs); err != nil {
		t.Fatalf("Write: %v", err)
	}
	rows, err := st.Query(Query{From: base.Add(-2 * time.Hour), To: base.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应有 2 条，实际 %d", len(rows))
	}
	// 倒序：后写入的 tid-2 在前
	if rows[0][DimTraceID] != "tid-2" {
		t.Errorf("第一条应为 tid-2（倒序），实际 %v", rows[0][DimTraceID])
	}
	// path 精确 + 扩展字段平铺
	rows, _ = st.Query(Query{From: base.Add(-2 * time.Hour), To: base.Add(time.Hour), Path: "/api/user/list"})
	if len(rows) != 1 {
		t.Fatalf("path 过滤应有 1 条，实际 %d", len(rows))
	}
	if rows[0]["request_body"] != "a=1" {
		t.Errorf("extra 负载维度应平铺，request_body = %v", rows[0]["request_body"])
	}
	if _, ok := rows[0]["extra"]; ok {
		t.Error("extra 列不应暴露")
	}
	// 状态码列
	rows, _ = st.Query(Query{From: base.Add(-2 * time.Hour), To: base.Add(time.Hour), PathLike: "/api/"})
	if len(rows) != 2 {
		t.Errorf("path_like 应有 2 条，实际 %d", len(rows))
	}
}

// TestDBStoreTypeNormalize 字符串列内容为纯数字时（如 trace_id="123"）不得被底层
// decodeAny 强转成数值：查询返回的类型必须与维度注册表一致（DimString→string、DimInt→int64）。
func TestDBStoreTypeNormalize(t *testing.T) {
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "obs_type.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	st := NewDBStore(d, "")
	if err := st.EnsureTable(); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	base := time.Now()
	if err := st.Write([]*AccessRecord{{
		Time: base, TraceID: "123", TenantID: "42", Path: "/123",
		Method: "200", ClientIP: "9", Upstream: "8",
		StatusCode: 200, ShieldMs: 1, BizMs: 2, TotalMs: 3, ReqBytes: 4, RespBytes: 5,
	}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	rows, err := st.Query(Query{From: base.Add(-time.Hour), To: base.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(rows))
	}
	row := rows[0]
	// 字符串列保持 string（即使内容是纯数字）
	for _, k := range []string{DimTraceID, DimTenantID, DimPath, DimMethod, DimClientIP, DimUpstream} {
		if _, ok := row[k].(string); !ok {
			t.Errorf("%s 应为 string，got %T(%v)", k, row[k], row[k])
		}
	}
	// 数值列保持 int64
	for _, k := range []string{DimStatusCode, DimShieldMs, DimBizMs, DimTotalMs, DimReqBytes, DimRespBytes} {
		if _, ok := row[k].(int64); !ok {
			t.Errorf("%s 应为 int64，got %T(%v)", k, row[k], row[k])
		}
	}
	// 时间列经 normalizeRowTypes 归一为 RFC3339 字符串（DB 侧原生类型，读取时 toString 转换）
	if _, ok := row[DimTime].(string); !ok {
		t.Errorf("time 应为 string，got %T(%v)", row[DimTime], row[DimTime])
	}
}

// 数据访问层未就绪（dataDB=nil）时降级 discardStore：写入不 panic、查询恒空、统计为 0。
func TestDiscardStoreWhenDBNil(t *testing.T) {
	f := newFakeConf()
	o := New(f, nil)
	if err := o.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = o.sink.Load().(*AsyncStore).Close() })
	if got := o.sink.Load().(*AsyncStore).Name(); got != "discard" {
		t.Fatalf("dataDB=nil 应降级 discard 后端，实际 %s", got)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/one", nil)
	if err := o.OnResponse(newCtx(r, 200, []byte("ok"), 1, 1)); err != nil {
		t.Fatalf("OnResponse 不应报错: %v", err)
	}
	if err := o.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	rows, err := o.Query(Query{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if err != nil || len(rows) != 0 {
		t.Errorf("discard 后端查询应恒空，实际 %v (err=%v)", rows, err)
	}
	if v := o.StorageSize(); v != 0 {
		t.Errorf("discard 后端 StorageSize 应为 0，实际 %d", v)
	}
}

// /admin/logs/storage 返回日志存储总占用。
func TestAdminStorage(t *testing.T) {
	o, _ := newTestObs(t)
	// 写一条日志
	r := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	if err := o.OnResponse(newCtx(r, 200, []byte("ok"), 1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := o.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	mgr := hotswap.NewManager(chain.New(), nil)
	mgr.RegisterMiddleware(o)
	h := NewAdminHandler(mgr)

	rec := httptest.NewRecorder()
	h.Storage(rec, httptest.NewRequest(http.MethodGet, "/admin/logs/storage", nil))

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("storage 响应非 JSON: %v, body=%q", err, rec.Body.String())
	}
	if v := got["total_bytes"].(float64); v <= 0 {
		t.Errorf("total_bytes 应 > 0，实际 %v", v)
	}
}

// DBStore.SizeBytes 统计表 + 索引占用（sqlite dbstat）。
func TestDBStoreSizeBytes(t *testing.T) {
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "obs.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	st := NewDBStore(d, "")
	if err := st.EnsureTable(); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	// 空表：dbstat 有表页（建表即分配页），应 > 0
	v0, err := st.SizeBytes()
	if err != nil {
		t.Fatalf("SizeBytes(空): %v", err)
	}
	if v0 <= 0 {
		t.Errorf("建表后 SizeBytes 应 > 0，实际 %d", v0)
	}
	// 写 100 条后应更大（或至少不更小）
	recs := make([]*AccessRecord, 0, 100)
	for i := 0; i < 100; i++ {
		recs = append(recs, &AccessRecord{
			Time: time.Now(), TraceID: "tid", Path: "/api/x", Method: http.MethodGet,
			StatusCode: 200, RespBytes: 256, TotalMs: 1,
		})
	}
	if err := st.Write(recs); err != nil {
		t.Fatal(err)
	}
	v1, err := st.SizeBytes()
	if err != nil {
		t.Fatalf("SizeBytes(写后): %v", err)
	}
	if v1 < v0 {
		t.Errorf("写入后 SizeBytes 不应变小：%d → %d", v0, v1)
	}
}

// DBStore.Prune 保留期清理（常规日志 7 天机制底层）：
// 保留期外删除、保留期内保留、重复执行幂等。
func TestDBStorePrune(t *testing.T) {
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "obs_prune.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	st := NewDBStore(d, "")
	if err := st.EnsureTable(); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	old := time.Now().Add(-10 * 24 * time.Hour)
	now := time.Now()
	if err := st.Write([]*AccessRecord{
		{Time: old, TraceID: "old-1", Path: "/old", Method: http.MethodGet, StatusCode: 200, TotalMs: 1},
		{Time: old, TraceID: "old-2", Path: "/old", Method: http.MethodGet, StatusCode: 200, TotalMs: 1},
		{Time: now, TraceID: "new-1", Path: "/new", Method: http.MethodGet, StatusCode: 200, TotalMs: 1},
	}); err != nil {
		t.Fatal(err)
	}
	// 保留 1 天：10 天前的 2 条删除
	n, err := st.Prune(1)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 2 {
		t.Fatalf("应删除 2 条，实际 %d", n)
	}
	rows, err := st.Query(Query{From: old.Add(-time.Hour), To: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][DimTraceID] != "new-1" {
		t.Errorf("应剩 1 条 new-1，实际 %v", rows)
	}
	// 幂等：再次执行删除 0 条
	if n, _ := st.Prune(1); n != 0 {
		t.Errorf("重复 Prune 应删除 0 条，实际 %d", n)
	}
}
