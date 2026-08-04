package mq

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	dbpkg "rocksys/internal/db"
	"rocksys/internal/hotswap"
)

// newTestStore 建内存 SQLite 库并建 outbox 表（:memory: 需限制单连接保证同一库）。
func newTestStore(t *testing.T) (*OutboxStore, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	store := NewOutboxStore(db, "outbox")
	src, err := dbpkg.EmbeddedSQLSource("sqlite")
	if err != nil {
		t.Fatalf("EmbeddedSQLSource(sqlite) 失败: %v", err)
	}
	store.SetSQLSource(src)
	if err := store.EnsureTable(); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return store, db
}

// statusOf 直接查询消息当前状态（跨包同包测试访问 db）。
func statusOf(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var st string
	if err := db.QueryRow("SELECT status FROM outbox WHERE id = ?", id).Scan(&st); err != nil {
		t.Fatalf("查询状态失败: %v", err)
	}
	return st
}

// TestOutboxInsertFetchPending Insert → FetchPending 返回，字段与顺序正确。
func TestOutboxInsertFetchPending(t *testing.T) {
	store, _ := newTestStore(t)

	id1, err := store.Insert("user.created", `{"uid":1}`)
	if err != nil {
		t.Fatalf("Insert 1 err: %v", err)
	}
	id2, err := store.Insert("order.paid", `{"oid":9}`)
	if err != nil {
		t.Fatalf("Insert 2 err: %v", err)
	}
	if id1 >= id2 {
		t.Errorf("id1=%d 应小于 id2=%d", id1, id2)
	}

	msgs, err := store.FetchPending(10)
	if err != nil {
		t.Fatalf("FetchPending err: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("FetchPending 返回 %d 条，want 2", len(msgs))
	}
	if msgs[0].Topic != "user.created" || msgs[0].Payload != `{"uid":1}` {
		t.Errorf("msgs[0]=%+v，want topic=user.created", msgs[0])
	}
	if msgs[0].Status != statusPending || msgs[0].RetryCount != 0 {
		t.Errorf("msgs[0] status=%q retry=%d，want pending/0", msgs[0].Status, msgs[0].RetryCount)
	}
	if msgs[0].CreatedAt.IsZero() {
		t.Error("CreatedAt 应为非零值")
	}
}

// TestOutboxMarkDone 投递成功 MarkDone 后不再出现在 FetchPending。
func TestOutboxMarkDone(t *testing.T) {
	store, db := newTestStore(t)
	id, err := store.Insert("t", "p")
	if err != nil {
		t.Fatalf("Insert err: %v", err)
	}
	if err := store.MarkDone(id); err != nil {
		t.Fatalf("MarkDone err: %v", err)
	}
	if got := statusOf(t, db, id); got != statusDone {
		t.Errorf("status=%q，want done", got)
	}
	msgs, _ := store.FetchPending(10)
	if len(msgs) != 0 {
		t.Errorf("MarkDone 后仍有待投递: %+v", msgs)
	}
}

// TestOutboxMarkFailedToDead 失败达最大次数自动转死信。
func TestOutboxMarkFailedToDead(t *testing.T) {
	store, db := newTestStore(t)
	id, _ := store.Insert("t", "p")

	for i := 1; i <= 2; i++ {
		if c, err := store.MarkFailed(id, "boom"); err != nil || c != i {
			t.Fatalf("第 %d 次 MarkFailed: c=%d err=%v", i, c, err)
		}
		if got := statusOf(t, db, id); got != statusFailed {
			t.Errorf("第 %d 次失败后 status=%q，want failed", i, got)
		}
	}
	if c, err := store.MarkFailed(id, "boom"); err != nil || c != 3 {
		t.Fatalf("第 3 次 MarkFailed: c=%d err=%v", c, err)
	}
	if got := statusOf(t, db, id); got != statusDead {
		t.Errorf("第 3 次失败后 status=%q，want dead（死信）", got)
	}
}

// echoHandler 记录请求体并返回固定状态码。
func echoHandler(status int, body *string, mu *sync.Mutex) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		*body = string(b)
		mu.Unlock()
		w.WriteHeader(status)
	}
}

// TestDelivererSuccess 消费方 200 → 消息被 MarkDone。
func TestDelivererSuccess(t *testing.T) {
	store, db := newTestStore(t)
	id, _ := store.Insert("user.created", `{"uid":1}`)

	var body string
	var mu sync.Mutex
	srv := httptest.NewServer(echoHandler(http.StatusOK, &body, &mu))
	defer srv.Close()

	d := NewPollingDeliverer(store, time.Hour) // 手动 pollOnce，不启动定时器
	d.SetBaseBackoff(time.Millisecond)
	d.SetConsumerBaseURL(srv.URL)

	d.pollOnce()

	var req struct {
		Topic   string `json:"topic"`
		Payload string `json:"payload"`
	}
	mu.Lock()
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("消费方收到非法 JSON %q: %v", body, err)
	}
	mu.Unlock()
	if req.Topic != "user.created" || req.Payload != `{"uid":1}` {
		t.Errorf("消费方收到 %+v，want {user.created {\"uid\":1}}", req)
	}
	if got := statusOf(t, db, id); got != statusDone {
		t.Errorf("投递成功后 status=%q，want done", got)
	}
}

// TestDelivererFailToDead 消费方连续 500，3 次失败后消息转死信。
func TestDelivererFailToDead(t *testing.T) {
	store, db := newTestStore(t)
	id, _ := store.Insert("order.paid", `{"oid":9}`)

	var mu sync.Mutex
	srv := httptest.NewServer(echoHandler(http.StatusInternalServerError, new(string), &mu))
	defer srv.Close()

	d := NewPollingDeliverer(store, time.Hour)
	d.SetBaseBackoff(time.Millisecond)
	d.SetConsumerBaseURL(srv.URL)

	d.pollOnce()
	if got := statusOf(t, db, id); got != statusFailed {
		t.Fatalf("第 1 次失败后 status=%q，want failed", got)
	}
	d.pollOnce()
	d.pollOnce()
	if got := statusOf(t, db, id); got != statusDead {
		t.Errorf("第 3 次失败后 status=%q，want dead", got)
	}
}

// TestDelivererRoute 按 topic 路由到指定消费方（无默认地址时也不报错）。
func TestDelivererRoute(t *testing.T) {
	store, db := newTestStore(t)
	id, _ := store.Insert("biz.event", "x")

	var got string
	var mu sync.Mutex
	srv := httptest.NewServer(echoHandler(http.StatusOK, &got, &mu))
	defer srv.Close()

	d := NewPollingDeliverer(store, time.Hour)
	d.SetRoute("biz.event", srv.URL) // 仅路由，无 ConsumerBaseURL
	d.pollOnce()
	if statusOf(t, db, id) != statusDone {
		t.Fatalf("路由投递后 status=%q，want done", statusOf(t, db, id))
	}
	mu.Lock()
	if !strings.Contains(got, "biz.event") {
		t.Errorf("消费方收到 %q，want 含 topic", got)
	}
	mu.Unlock()
}

// TestMQComponent MQ 组件生命周期（hotswap.Component）。
func TestMQComponent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	db.SetMaxOpenConns(1)

	var mu sync.Mutex
	srv := httptest.NewServer(echoHandler(http.StatusOK, new(string), &mu))
	defer srv.Close()

	m := New(db, "outbox")
	if m.Name() != "mq" {
		t.Errorf("Name=%q，want mq", m.Name())
	}
	src, err := dbpkg.EmbeddedSQLSource("sqlite")
	if err != nil {
		t.Fatalf("EmbeddedSQLSource(sqlite) 失败: %v", err)
	}
	m.SetSQLSource(src)
	if m.State() != hotswap.StateDisabled {
		t.Errorf("初始 State=%v，want disabled", m.State())
	}
	if m.Store() != nil {
		t.Error("未启动时 Store 应为 nil")
	}

	opts := &Options{
		Interval:        time.Hour, // 大间隔避免测试期间触发轮询
		ConsumerBaseURL: srv.URL,
		BaseBackoff:     time.Millisecond,
	}
	if err := m.Start(opts); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	if m.State() != hotswap.StateEnabled {
		t.Errorf("Start 后 State=%v，want enabled", m.State())
	}
	if m.Store() == nil {
		t.Error("Start 后 Store 应非 nil")
	}
	if err := m.Start(opts); err != nil {
		t.Errorf("重复 Start err: %v", err)
	}

	if err := m.Stop(); err != nil {
		t.Fatalf("Stop err: %v", err)
	}
	if m.State() != hotswap.StateDisabled {
		t.Errorf("Stop 后 State=%v，want disabled", m.State())
	}
	if err := m.Stop(); err != nil {
		t.Errorf("重复 Stop err: %v", err)
	}
}
