// 拦截监控统计单元测试（docs/WAF_MONITOR_STATS.md）：
// 覆盖内存滑动窗口计数器、拦截类别枚举、记录器异步落库/查询/清理与降级丢弃。
package shield

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/dataflow"
	"rocksys/internal/db"

	"github.com/iotames/easyserver/httpsvr"

	_ "modernc.org/sqlite"
)

// ── 枚举 ────────────────────────────────────────────────────────────

// 拦截类别枚举：String/Valid 与注册表一致，越界值安全。
func TestBlockTypeEnum(t *testing.T) {
	if got := BlockIPBlacklist.String(); got != "IP黑名单" {
		t.Errorf("BlockIPBlacklist.String() = %q", got)
	}
	for bt := BlockType(1); int(bt) <= blockTypeCount; bt++ {
		if !bt.Valid() {
			t.Errorf("BlockType(%d) 应合法", bt)
		}
		if bt.String() == "未知" || bt.String() == "" {
			t.Errorf("BlockType(%d) 应有注册名", bt)
		}
	}
	for _, bt := range []BlockType{0, blockTypeCount + 1, -1} {
		if bt.Valid() {
			t.Errorf("BlockType(%d) 不应合法", bt)
		}
		if bt.String() != "未知" {
			t.Errorf("BlockType(%d) 越界应返回 未知", bt)
		}
	}
}

// ── 内存滑动窗口计数器 ──────────────────────────────────────────────

// 窗口内计数聚合：总数与按类别分布正确。
func TestEventCounterSnapshot(t *testing.T) {
	c := &eventCounter{}
	now := time.Now()
	c.Add(BlockSQLInjection, now)
	c.Add(BlockSQLInjection, now)
	c.Add(BlockRateLimit, now)
	s := c.Snapshot(now)
	if s.Total != 3 {
		t.Errorf("Total = %d，期望 3", s.Total)
	}
	if s.ByType["SQL注入"] != 2 || s.ByType["限流"] != 1 {
		t.Errorf("ByType = %v", s.ByType)
	}
}

// 窗口滑动：超过 1 分钟的旧桶不计入快照。
func TestEventCounterWindowSlides(t *testing.T) {
	c := &eventCounter{}
	now := time.Now()
	c.Add(BlockXSS, now.Add(-2*time.Minute)) // 旧桶：应滑出窗口
	c.Add(BlockXSS, now.Add(-10*time.Second))
	s := c.Snapshot(now)
	if s.Total != 1 {
		t.Errorf("旧桶数据不应计入，Total = %d", s.Total)
	}
	if s.ByType["XSS"] != 1 {
		t.Errorf("ByType = %v", s.ByType)
	}
}

// 无锁并发：多 goroutine 并发 Add（同 slot 原子自增 + 跨 slot 桶竞争）不 panic、计数不丢。
// 快照时刻距写入 ≤30s（全部桶在 1 分钟窗口内），故总数必须精确等于写入次数。
func TestEventCounterConcurrent(t *testing.T) {
	c := &eventCounter{}
	const workers = 8
	const perWorker = 2000
	base := time.Now().Truncate(time.Second)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			off := time.Duration(w%7) * time.Second // 7 个邻近 slot（跨桶竞争），同 slot 多 goroutine 竞争自增
			for i := 0; i < perWorker; i++ {
				c.Add(BlockSQLInjection, base.Add(off))
			}
		}(w)
	}
	wg.Wait()
	s := c.Snapshot(base.Add(30 * time.Second))
	if want := int64(workers * perWorker); s.Total != want {
		t.Errorf("并发总数 = %d，期望 %d（计数丢失）", s.Total, want)
	}
	if want := int64(workers * perWorker); s.ByType["SQL注入"] != want {
		t.Errorf("并发 SQL注入 = %d，期望 %d", s.ByType["SQL注入"], want)
	}
}

// ── 记录器（sqlite 临时库）────────────────────────────────────────

// newTestEventCtx 构造带 DataFlow 的请求上下文（Record → newEvent 依赖）。
func newEventCtx(r *http.Request) *chain.Context {
	inner := httpsvr.NewDataFlow()
	df := dataflow.New(inner, r)
	df.SetTraceID("trace-ev-1")
	return &chain.Context{R: r, DF: df}
}

// newTestRecorder 构造写往临时 sqlite 库的记录器（自动建表 + 启动后台任务）。
// fakeConfMgr 只记录注册项不回填默认值（真实 conf.Manager 会写入 defval），
// 故此处显式开启落库开关，模拟生产默认 SHIELD_EVENT_LOG_ENABLED=true。
func newTestRecorder(t *testing.T) (*EventRecorder, *db.DB) {
	t.Helper()
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "shield.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	f := newFakeConf()
	r := NewEventRecorder(f, d)
	r.logEnabled = true
	r.pruneEnabled = false
	t.Cleanup(func() {
		r.Stop()
		_ = d.Close() // 释放文件句柄（Windows：句柄不关则 TempDir 清理失败）
	})
	return r, d
}

// NewEventRecorder 应注册全部 SHIELD_EVENT_* 配置项。
func TestNewEventRecorderRegistersConfig(t *testing.T) {
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "shield2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	f := newFakeConf()
	r := NewEventRecorder(f, d)
	r.Stop()
	for _, n := range []string{
		"SHIELD_EVENT_LOG_ENABLED", "SHIELD_EVENT_RETENTION_DAYS", "SHIELD_EVENT_PRUNE_ENABLED",
		"SHIELD_EVENT_TABLE", "SHIELD_EVENT_BUFFER", "SHIELD_EVENT_FLUSH_ROWS", "SHIELD_EVENT_FLUSH_INTERVAL",
	} {
		if _, ok := f.regs[n]; !ok {
			t.Errorf("应注册配置项 %s", n)
		}
	}
}

// Record → Stop 优雅停机 flush → QueryEvents 可查到全部事件（含过滤条件）。
func TestEventRecorderRecordAndQuery(t *testing.T) {
	r, _ := newTestRecorder(t)
	base := time.Now()
	// 3 条 SQL 注入 + 2 条限流
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/a?id=1%20OR%201=1", nil)
		req.RemoteAddr = "10.0.0.1:1000"
		r.Record(newEventCtx(req), BlockSQLInjection, "sql_pattern")
	}
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/b", nil)
		req.RemoteAddr = "10.0.0.2:2000"
		r.Record(newEventCtx(req), BlockRateLimit, "token_bucket")
	}
	r.Stop() // 优雅停机：排空通道并写出

	written, dropped := r.Stats()
	if written != 5 || dropped != 0 {
		t.Fatalf("written/dropped = %d/%d，期望 5/0", written, dropped)
	}

	rows, err := r.QueryEvents(EventQuery{From: base.Add(-time.Minute), To: base.Add(time.Minute)})
	if err != nil {
		t.Fatalf("QueryEvents: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("应有 5 条明细，实际 %d", len(rows))
	}
	// 倒序：最新在前；字段归一化（block_type 为 int64、time 为字符串）
	if bt := rows[0]["block_type"].(int64); bt != int64(BlockRateLimit) {
		t.Errorf("首行 block_type = %d，期望限流(%d)", bt, BlockRateLimit)
	}
	if _, ok := rows[0]["time"].(string); !ok {
		t.Errorf("time 应归一为字符串，实际 %T", rows[0]["time"])
	}
	if rows[0]["rule_hit"] != "token_bucket" {
		t.Errorf("rule_hit = %v", rows[0]["rule_hit"])
	}

	// 类别过滤
	rows, _ = r.QueryEvents(EventQuery{From: base.Add(-time.Minute), To: base.Add(time.Minute), BlockType: BlockSQLInjection})
	if len(rows) != 3 {
		t.Errorf("block_type 过滤应有 3 条，实际 %d", len(rows))
	}
	// IP 精确过滤
	rows, _ = r.QueryEvents(EventQuery{From: base.Add(-time.Minute), To: base.Add(time.Minute), ClientIP: "10.0.0.2"})
	if len(rows) != 2 {
		t.Errorf("client_ip 过滤应有 2 条，实际 %d", len(rows))
	}
	// limit
	rows, _ = r.QueryEvents(EventQuery{From: base.Add(-time.Minute), To: base.Add(time.Minute), Limit: 2})
	if len(rows) != 2 {
		t.Errorf("limit=2 应返回 2 条，实际 %d", len(rows))
	}
}

// StatsDaily / StatsTopIP 查询时聚合正确。
func TestEventRecorderStats(t *testing.T) {
	r, _ := newTestRecorder(t)
	base := time.Now()
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "/a", nil)
		req.RemoteAddr = "10.0.0.1:1000"
		r.Record(newEventCtx(req), BlockXSS, "xss_pattern")
	}
	req := httptest.NewRequest(http.MethodGet, "/b", nil)
	req.RemoteAddr = "10.0.0.9:9000"
	r.Record(newEventCtx(req), BlockXSS, "xss_pattern")
	r.Stop()

	daily, err := r.StatsDaily(base.Add(-time.Minute))
	if err != nil {
		t.Fatalf("StatsDaily: %v", err)
	}
	var cnt int64
	for _, row := range daily {
		if row["day"] != base.Format("2006-01-02") {
			continue
		}
		if row["block_type"].(int64) == int64(BlockXSS) {
			cnt = row["cnt"].(int64)
		}
	}
	if cnt != 5 {
		t.Errorf("按日聚合 cnt = %d，期望 5", cnt)
	}

	top, err := r.StatsTopIP(base.Add(-time.Minute), 10)
	if err != nil {
		t.Fatalf("StatsTopIP: %v", err)
	}
	if len(top) != 2 {
		t.Fatalf("Top IP 应有 2 个，实际 %d", len(top))
	}
	if top[0]["client_ip"] != "10.0.0.1" || top[0]["cnt"].(int64) != 4 {
		t.Errorf("Top1 应为 10.0.0.1×4，实际 %v", top[0])
	}
}

// Prune：保留期外删除、保留期内保留；重复执行幂等。
func TestEventRecorderPrune(t *testing.T) {
	r, _ := newTestRecorder(t)
	old := time.Now().Add(-10 * 24 * time.Hour)
	// 直接走 writeBatch 写入历史时间的事件（绕过 Record 的 time.Now()）
	r.writeBatch([]*ShieldEvent{
		{Time: old, TraceID: "old-1", BlockType: BlockRiskPath, ClientIP: "1.1.1.1", Method: "GET", Path: "/old", StatusCode: 403, RuleHit: "risk"},
		{Time: old, TraceID: "old-2", BlockType: BlockRiskPath, ClientIP: "1.1.1.1", Method: "GET", Path: "/old2", StatusCode: 403, RuleHit: "risk"},
	})
	req := httptest.NewRequest(http.MethodGet, "/new", nil)
	r.Record(newEventCtx(req), BlockIPBlacklist, "ip_blacklist")
	r.Stop()

	// 保留 1 天：10 天前的 2 条删除，1 条新事件保留
	n, err := r.Prune(1)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 2 {
		t.Fatalf("应删除 2 条，实际 %d", n)
	}
	rows, _ := r.QueryEvents(EventQuery{From: old.Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if len(rows) != 1 {
		t.Errorf("应剩 1 条新事件，实际 %d", len(rows))
	}
	// 幂等：再次执行删除 0 条
	if n, _ := r.Prune(1); n != 0 {
		t.Errorf("重复 Prune 应删除 0 条，实际 %d", n)
	}
}

// 通道满降级：Record 丢弃并计数，绝不阻塞。
func TestEventRecorderDropOnFull(t *testing.T) {
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "shield_full.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	f := newFakeConf()
	// 用极小缓冲构造：Register 失败/成功不影响（fake 恒成功），缓冲取注册后兜底
	r := NewEventRecorder(f, d)
	// 手工填满通道（后台 flushLoop 持续消费，直接灌满并立刻断言有竞争；
	// 故改为停机后灌通道：Stop 后 flushLoop 已退出，通道状态稳定可控）
	r.Stop()
	// 重新手工构造小通道并直接灌满
	r.ch = make(chan *ShieldEvent, 1)
	r.ch <- &ShieldEvent{Time: time.Now(), BlockType: BlockXSS}
	r.logEnabled = true
	r.started.Store(true) // 防 Record 无关；Record 不检查 started
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Record(newEventCtx(req), BlockXSS, "xss_pattern")
	_, dropped := r.Stats()
	if dropped < 1 {
		t.Errorf("通道满应丢弃并计数，dropped = %d", dropped)
	}
}

// Record 的 nil 安全：未注入记录器（DB 未配置）时静默 no-op，不 panic。
func TestEventRecorderNilSafe(t *testing.T) {
	var r *EventRecorder
	r.Record(newEventCtx(httptest.NewRequest(http.MethodGet, "/x", nil)), BlockXSS, "r")
}

// ── 管理端点（构造 Shield 直连，不经 hotswap 装配）──────────────────

// Metrics：实时计数（内存窗口）+ 落库计数；recorder 未注入时 written/dropped 为 0。
func TestAdminShieldMetrics(t *testing.T) {
	s := &Shield{counter: &eventCounter{}}
	now := time.Now()
	s.counter.Add(BlockSQLInjection, now)
	s.counter.Add(BlockCrawlerUA, now)
	h := &AdminHandler{shield: s}

	rec := httptest.NewRecorder()
	h.Metrics(rec, httptest.NewRequest(http.MethodGet, PathShieldMetrics, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，body=%q", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("metrics 响应非 JSON: %v", err)
	}
	if got["total"].(float64) != 2 {
		t.Errorf("total = %v，期望 2", got["total"])
	}
	byType := got["by_type"].(map[string]any)
	if byType["SQL注入"].(float64) != 1 || byType["爬虫UA"].(float64) != 1 {
		t.Errorf("by_type = %v", byType)
	}
	if got["written"].(float64) != 0 || got["dropped"].(float64) != 0 {
		t.Errorf("recorder 未注入时 written/dropped 应为 0，实际 %v/%v", got["written"], got["dropped"])
	}
}

// Events/Stats：recorder 未注入（DB 未配置）时返回 503 降级提示。
func TestAdminShieldDegraded(t *testing.T) {
	h := &AdminHandler{shield: &Shield{counter: &eventCounter{}}}
	for _, fn := range []func(http.ResponseWriter, *http.Request){h.Events, h.Stats, h.Prune} {
		rec := httptest.NewRecorder()
		fn(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("recorder 未注入应返回 503，实际 %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "未启用") && !strings.Contains(rec.Body.String(), "未注册") {
			t.Errorf("503 响应应包含降级说明，body=%q", rec.Body.String())
		}
	}
}

// Events 参数校验：非法 block_type / limit / 时间格式 / from 晚于 to → 400。
func TestAdminShieldEventsBadParams(t *testing.T) {
	r, _ := newTestRecorder(t)
	r.Stop()
	h := &AdminHandler{shield: &Shield{counter: &eventCounter{}, recorder: r}}

	cases := []string{
		"?block_type=99",
		"?block_type=abc",
		"?limit=0",
		"?limit=-1",
		"?from=bad",
		"?to=bad",
		"?from=2024-01-02T10:00&to=2024-01-02T09:00",
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, PathShieldEvents+c, nil)
		rec := httptest.NewRecorder()
		h.Events(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s 应返回 400，实际 %d（body=%q）", c, rec.Code, rec.Body.String())
		}
	}
}

// Prune 端点：空请求体合法（用配置保留天数），响应 ok/deleted。
func TestAdminShieldPrune(t *testing.T) {
	r, _ := newTestRecorder(t)
	r.writeBatch([]*ShieldEvent{
		{Time: time.Now().Add(-100 * 24 * time.Hour), BlockType: BlockRiskPath, ClientIP: "1.1.1.1", Method: "GET", Path: "/old", StatusCode: 403, RuleHit: "risk"},
	})
	r.Stop()
	h := &AdminHandler{shield: &Shield{counter: &eventCounter{}, recorder: r}}

	req := httptest.NewRequest(http.MethodPost, PathShieldPrune, nil)
	rec := httptest.NewRecorder()
	h.Prune(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，body=%q", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("prune 响应非 JSON: %v", err)
	}
	if got["ok"] != true || got["deleted"].(float64) != 1 {
		t.Errorf("ok/deleted = %v/%v，期望 true/1", got["ok"], got["deleted"])
	}
}
