package shield

import (
	"database/sql"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iotames/easydb"

	dbpkg "rocksys/internal/db"
)

// countingStore 计数包装：统计 QueryActive 调用次数（断言热路径零 DB 查询）。
type countingStore struct {
	*IPListStore
	queries atomic.Int64
}

func (c *countingStore) QueryActive(now time.Time) ([]ActiveIP, error) {
	c.queries.Add(1)
	return c.IPListStore.QueryActive(now)
}

// newFileListStore 建临时文件 sqlite 库 + 黑白名单表（Shield 集成测试用，避免 :memory: 连接歧义）。
func newFileListStore(t *testing.T, isBlack bool) (*IPListStore, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/list.db")
	if err != nil {
		t.Fatalf("打开 sqlite 失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	src, err := dbpkg.EmbeddedSQLSource("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	store := NewIPListStore(easydb.NewEasyDbBySqlDB(db), src, isBlack)
	if err := store.EnsureTable(); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return store, db
}

// dbShield 装配带 DB 黑白名单的 Shield（black/white 可 nil）。
func dbShield(t *testing.T, black, white *countingStore) *Shield {
	t.Helper()
	s, _ := newTestShield(t)
	s.enabled = true
	var b, w ipListStore
	if black != nil {
		b = black
	}
	if white != nil {
		w = white
	}
	s.SetIPListStores(b, w)
	t.Cleanup(func() { s.Stop() })
	return s
}

// ── DB 黑名单命中 + 白名单优先 ──────────────────────────────────────

func TestShield_DBBlacklistHit(t *testing.T) {
	black, db := newFileListStore(t, true)
	now := testNow()
	if _, err := black.Insert("10.0.0.5", "DB 条目", BlockIPBlacklist, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := black.Insert("10.0.0.0/8", "DB 段", BlockIPBlacklist, nil, now); err != nil {
		t.Fatal(err)
	}
	s := dbShield(t, &countingStore{IPListStore: black}, nil)

	// DB 精确 IP 命中 → 403 + 中断链
	ctx, w := newCtx("/api", "10.0.0.5:80", "curl/8.0")
	if next := s.Handle(ctx); next {
		t.Fatal("DB 黑名单精确 IP 应拦截")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("应 403, got %d", w.Code)
	}
	// DB CIDR 命中（子网内未精确收录）→ 403
	ctx2, _ := newCtx("/api", "10.0.0.99:80", "curl/8.0")
	if next := s.Handle(ctx2); next {
		t.Fatal("DB 黑名单 CIDR 应拦截")
	}
	// 未收录 IP → 放行
	ctx3, _ := newCtx("/api", "192.168.1.1:80", "curl/8.0")
	if next := s.Handle(ctx3); !next {
		t.Fatal("未收录 IP 应放行")
	}

	// hit_count 攒批：精确命中 1 次 + CIDR 命中 2 次（CIDR 不归因单条）
	for i := 0; i < 2; i++ {
		ctxC, _ := newCtx("/api", "10.0.0.6:80", "curl/8.0")
		s.Handle(ctxC)
	}
	s.flushHitCounts()
	var hit int64
	if err := db.QueryRow("SELECT hit_count FROM ip_blacklist WHERE ip = '10.0.0.5'").Scan(&hit); err != nil {
		t.Fatalf("查询 hit_count: %v", err)
	}
	if hit != 1 {
		t.Fatalf("精确条目 hit_count = %d, want 1（CIDR 命中不归因单条）", hit)
	}
}

// 白名单优先于黑名单（DB 白名单 + DB 黑名单同一 IP）。
func TestShield_DBWhitelistPriority(t *testing.T) {
	black, _ := newFileListStore(t, true)
	white, _ := newFileListStore(t, false)
	now := testNow()
	if _, err := black.Insert("10.0.0.5", "黑", 1, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := white.Insert("10.0.0.5", "白", 1, nil, now); err != nil {
		t.Fatal(err)
	}
	s := dbShield(t, &countingStore{IPListStore: black}, &countingStore{IPListStore: white})

	ctx, w := newCtx("/api", "10.0.0.5:80", "curl/8.0")
	if next := s.Handle(ctx); !next {
		t.Fatal("白名单优先：同 IP 应在黑名单前短路放行")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("放行不应写响应, got %d", w.Code)
	}
}

// 软删/过期条目重建快照后不拦截。
func TestShield_DBSoftDeleteExpiredNotBlock(t *testing.T) {
	black, _ := newFileListStore(t, true)
	now := testNow()
	if _, err := black.Insert("10.0.0.1", "永久", 1, nil, now); err != nil {
		t.Fatal(err)
	}
	idDel, err := black.Insert("10.0.0.2", "待删", 1, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	idExp, err := black.Insert("10.0.0.3", "过期", 1, timePtr(now.Add(-time.Hour)), now)
	if err != nil {
		t.Fatal(err)
	}
	s := dbShield(t, &countingStore{IPListStore: black}, nil)

	// 永久 + 未过期（待删）在快照内拦截；已过期条目快照加载时即被 QueryActive 过滤，不拦截
	for _, ip := range []string{"10.0.0.1", "10.0.0.2"} {
		ctx, _ := newCtx("/api", ip+":80", "curl/8.0")
		if next := s.Handle(ctx); next {
			t.Fatalf("%s 应拦截", ip)
		}
	}
	ctxExp, _ := newCtx("/api", "10.0.0.3:80", "curl/8.0")
	if next := s.Handle(ctxExp); !next {
		t.Fatal("已过期条目应在快照加载时被过滤，不拦截")
	}

	// 软删 + 过期（已过期条目本就生效于 QueryActive 过滤；此处模拟后台清理后重建）
	if err := black.SoftDelete(idDel, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	// 保留条目仍拦截
	ctx, _ := newCtx("/api", "10.0.0.1:80", "curl/8.0")
	if next := s.Handle(ctx); next {
		t.Fatal("永久条目应拦截")
	}
	// 软删条目不拦截
	ctx2, _ := newCtx("/api", "10.0.0.2:80", "curl/8.0")
	if next := s.Handle(ctx2); !next {
		t.Fatal("软删条目不应拦截")
	}
	// 过期条目不拦截（QueryActive 已过滤）
	ctx3, _ := newCtx("/api", "10.0.0.3:80", "curl/8.0")
	if next := s.Handle(ctx3); !next {
		t.Fatal("过期条目不应拦截")
	}
	_ = idExp
}

// ★ 性能红线：快照构建后处理 N 条请求期间 DB 查询次数 = 0。
func TestShield_DBHotPathNoQuery(t *testing.T) {
	black, _ := newFileListStore(t, true)
	white, _ := newFileListStore(t, false)
	now := testNow()
	for i := 1; i <= 5; i++ {
		if _, err := black.Insert("10.0.0."+string(rune('0'+i)), "t", 1, nil, now); err != nil {
			t.Fatal(err)
		}
		if _, err := white.Insert("192.168.1."+string(rune('0'+i)), "t", 1, nil, now); err != nil {
			t.Fatal(err)
		}
	}
	cb := &countingStore{IPListStore: black}
	cw := &countingStore{IPListStore: white}
	s := dbShield(t, cb, cw) // SetIPListStores 内部重建快照（DB 查询仅发生于此）

	q0 := cb.queries.Load() + cw.queries.Load()
	// 处理混合请求：命中黑名单 / 命中白名单 / 普通放行
	cases := []struct {
		ip   string
		want bool // 是否放行
	}{
		{"10.0.0.1:80", false},   // 黑名单命中
		{"10.0.0.2:80", false},   // 黑名单命中（CIDR/另一条）
		{"192.168.1.1:80", true}, // 白名单命中
		{"8.8.8.8:80", true},     // 普通放行
		{"10.0.0.1:80", false},   // 重复命中
	}
	for i, c := range cases {
		ctx, _ := newCtx("/api", c.ip, "curl/8.0")
		if next := s.Handle(ctx); next != c.want {
			t.Fatalf("case %d ip=%s: next=%v, want %v", i, c.ip, next, c.want)
		}
	}
	if got := cb.queries.Load() + cw.queries.Load(); got != q0 {
		t.Fatalf("热路径 DB 查询次数 = %d（快照后 +%d），要求 0（性能红线）", got, got-q0)
	}
}

// 管理面变更（导入/增删改）后 Rebuild → 立即生效。
func TestShield_DBImportThenRebuild(t *testing.T) {
	black, _ := newFileListStore(t, true)
	s := dbShield(t, &countingStore{IPListStore: black}, nil)

	// 初始不拦截
	ctx, _ := newCtx("/api", "10.9.9.9:80", "curl/8.0")
	if next := s.Handle(ctx); !next {
		t.Fatal("导入前应放行")
	}
	// 批量导入 + Rebuild（管理面变更 → 主动重建）
	imported, _, err := black.Import([]string{"10.9.9.9"}, "管理面导入", 1, testNow())
	if err != nil || imported != 1 {
		t.Fatalf("Import: %v imported=%d", err, imported)
	}
	if err := s.Rebuild(); err != nil {
		t.Fatal(err)
	}
	ctx2, w := newCtx("/api", "10.9.9.9:80", "curl/8.0")
	if next := s.Handle(ctx2); next {
		t.Fatal("导入 + Rebuild 后应拦截")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("应 403, got %d", w.Code)
	}
}

