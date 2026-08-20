package shield

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/iotames/easydb"

	dbpkg "rocksys/internal/db"
)

// newTestListStore 建内存 SQLite 库并建黑白名单表（:memory: 需限制单连接保证同一库）。
func newTestListStore(t *testing.T, isBlack bool) (*IPListStore, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	src, err := dbpkg.EmbeddedSQLSource("sqlite")
	if err != nil {
		t.Fatalf("EmbeddedSQLSource(sqlite) 失败: %v", err)
	}
	store := NewIPListStore(easydb.NewEasyDbBySqlDB(db), src, isBlack)
	if err := store.EnsureTable(); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	// 幂等：重复建表 + 索引不应报错
	if err := store.EnsureTable(); err != nil {
		t.Fatalf("重复建表失败: %v", err)
	}
	return store, db
}

func testNow() time.Time {
	return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
}

func timePtr(t time.Time) *time.Time { return &t }

// ── 黑名单 CRUD + 快照加载 ──────────────────────────────────────────

func TestIPListStore_BlacklistCRUD(t *testing.T) {
	s, _ := newTestListStore(t, true)
	now := testNow()

	// 新增
	id1, err := s.Insert("192.168.1.100", "扫描器", BlockSQLInjection, nil, now)
	if err != nil {
		t.Fatalf("Insert 失败: %v", err)
	}
	if id1 <= 0 {
		t.Fatalf("Insert 返回 id = %d, want > 0", id1)
	}
	// 唯一约束：重复 ip 报 ErrIPExists
	if _, err := s.Insert("192.168.1.100", "重复", BlockIPBlacklist, nil, now); err != ErrIPExists {
		t.Fatalf("重复 Insert err = %v, want ErrIPExists", err)
	}

	// 快照加载：有效条目可见
	active, err := s.QueryActive(now)
	if err != nil {
		t.Fatalf("QueryActive: %v", err)
	}
	if len(active) != 1 || active[0].IP != "192.168.1.100" {
		t.Fatalf("QueryActive = %+v, want [192.168.1.100]", active)
	}

	// 更新
	if err := s.Update(id1, "改备注", BlockPathTraversal, timePtr(now.Add(time.Hour)), now); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// 软删后：快照不再包含
	if err := s.SoftDelete(id1, now); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	active, _ = s.QueryActive(now)
	if len(active) != 0 {
		t.Fatalf("软删后 QueryActive = %+v, want 空", active)
	}
	// 恢复后：快照重新包含
	if err := s.Restore(id1, now); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	active, _ = s.QueryActive(now)
	if len(active) != 1 {
		t.Fatalf("恢复后 QueryActive = %+v, want 1 条", active)
	}
}

func TestIPListStore_BlacklistExpiresFilter(t *testing.T) {
	s, _ := newTestListStore(t, true)
	now := testNow()

	// 永久 + 未过期 + 已过期
	if _, err := s.Insert("1.0.0.1", "永久", 1, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Insert("1.0.0.2", "未过期", 1, timePtr(now.Add(24*time.Hour)), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Insert("1.0.0.3", "已过期", 1, timePtr(now.Add(-time.Hour)), now); err != nil {
		t.Fatal(err)
	}

	active, err := s.QueryActive(now.Add(time.Minute))
	if err != nil {
		t.Fatalf("QueryActive: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("QueryActive = %+v, want 2 条（永久 + 未过期；已过期排除）", active)
	}
}

// ── 批量导入幂等 ─────────────────────────────────────────────────────

func TestIPListStore_ImportIdempotent(t *testing.T) {
	s, _ := newTestListStore(t, true)
	now := testNow()

	// 含注释、空行、重复项
	imported, skipped, err := s.Import([]string{
		"10.0.0.1",
		"",          // 空行跳过
		"# 注释",      // 注释跳过
		"10.0.0.1",  // 重复
		"10.0.0.0/8", // CIDR
	}, "批量来源", 1, now)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported != 2 || skipped != 1 {
		t.Fatalf("imported = %d skipped = %d, want 2/1（skipped 仅计重复项；空行/注释非条目不计数）", imported, skipped)
	}
	active, _ := s.QueryActive(now)
	if len(active) != 2 {
		t.Fatalf("导入后 QueryActive = %+v, want 2 条", active)
	}

	// 再导一遍：全部跳过（幂等）
	imported, skipped, err = s.Import([]string{"10.0.0.1", "10.0.0.0/8"}, "再导", 1, now)
	if err != nil {
		t.Fatalf("二次 Import: %v", err)
	}
	if imported != 0 || skipped != 2 {
		t.Fatalf("二次导入 imported = %d skipped = %d, want 0/2", imported, skipped)
	}
}

// ── 列表分页 + 过滤 ──────────────────────────────────────────────────

func TestIPListStore_ListFilterPaging(t *testing.T) {
	s, _ := newTestListStore(t, true)
	now := testNow()

	for i, ip := range []string{"10.0.0.1", "10.0.0.2", "192.168.1.10", "192.168.1.20"} {
		bt := BlockIPBlacklist
		if i >= 2 {
			bt = BlockSQLInjection
		}
		if _, err := s.Insert(ip, "t", bt, nil, now); err != nil {
			t.Fatal(err)
		}
	}
	// 软删一条
	active, _ := s.QueryActive(now)
	_ = s.SoftDelete(active[0].ID, now)

	// 全量（含软删）
	rows, total, err := s.List(ListFilter{Limit: 10}, now)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 4 || len(rows) != 4 {
		t.Fatalf("全量 total = %d rows = %d, want 4/4", total, len(rows))
	}
	// 仅有效（软删排除）
	rows, total, _ = s.List(ListFilter{Limit: 10, ValidOnly: true}, now)
	if total != 3 || len(rows) != 3 {
		t.Fatalf("仅有效 total = %d rows = %d, want 3/3", total, len(rows))
	}
	// ip 模糊
	rows, total, _ = s.List(ListFilter{IP: "192.168", Limit: 10}, now)
	if total != 2 {
		t.Fatalf("ip 模糊 total = %d, want 2", total)
	}
	// block_type 过滤
	rows, total, _ = s.List(ListFilter{BlockType: BlockSQLInjection, Limit: 10}, now)
	if total != 2 {
		t.Fatalf("block_type 过滤 total = %d, want 2", total)
	}
	// 分页：limit 2 offset 2 → 第 3、4 条（id 倒序）
	rows, total, _ = s.List(ListFilter{Limit: 2, Offset: 2}, now)
	if total != 4 || len(rows) != 2 {
		t.Fatalf("分页 total = %d rows = %d, want 4/2", total, len(rows))
	}
	// 行归一化：id/block_type 为 int64，时间为 RFC3339
	first := rows[0]
	if _, ok := first["id"].(int64); !ok {
		t.Fatalf("id 未归一化: %T", first["id"])
	}
	if _, ok := first["block_type"].(int64); !ok {
		t.Fatalf("block_type 未归一化: %T", first["block_type"])
	}
}

// ── hit_count 累加 ───────────────────────────────────────────────────

func TestIPListStore_HitCount(t *testing.T) {
	s, db := newTestListStore(t, true)
	now := testNow()
	id, err := s.Insert("1.1.1.1", "t", 1, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddHitCount(id, 3); err != nil {
		t.Fatalf("AddHitCount: %v", err)
	}
	if err := s.AddHitCount(id, 2); err != nil {
		t.Fatalf("AddHitCount: %v", err)
	}
	var hit int64
	if err := db.QueryRow("SELECT hit_count FROM ip_blacklist WHERE id = ?", id).Scan(&hit); err != nil {
		t.Fatal(err)
	}
	if hit != 5 {
		t.Fatalf("hit_count = %d, want 5", hit)
	}
}

// ── 白名单（无 block_type/expires_at/hit_count）─────────────────────

func TestIPListStore_Whitelist(t *testing.T) {
	s, _ := newTestListStore(t, false)
	now := testNow()

	if _, err := s.Insert("10.0.0.5", "办公出口", BlockIPBlacklist, timePtr(now), now); err != nil {
		t.Fatalf("白名单 Insert: %v", err)
	}
	if _, err := s.Insert("10.0.0.6", "办公2", 0, nil, now); err != nil {
		t.Fatal(err)
	}
	// 白名单忽略 expires_at：插入后仍有效（QueryActive 不过滤过期）
	active, err := s.QueryActive(now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("白名单 QueryActive = %+v, want 2 条", active)
	}
	// 软删/恢复
	if err := s.SoftDelete(active[0].ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(active[0].ID, now); err != nil {
		t.Fatal(err)
	}
	// 白名单 AddHitCount 应报错
	if err := s.AddHitCount(active[0].ID, 1); err == nil || !strings.Contains(err.Error(), "hit_count") {
		t.Fatalf("白名单 AddHitCount err = %v, want hit_count 错误", err)
	}
	// 列表无 block_type 字段
	rows, total, _ := s.List(ListFilter{Limit: 10}, now)
	if total != 2 || len(rows) != 2 {
		t.Fatalf("白名单列表 total = %d, want 2", total)
	}
	if _, ok := rows[0]["block_type"]; ok {
		t.Fatal("白名单行不应含 block_type")
	}
}

// attack_archive 建表装配函数（WAF 方案 §4.3：本期仅建表）幂等。
func TestEnsureAttackArchiveTable(t *testing.T) {
	s, db := newTestListStore(t, true) // 复用内存库
	_ = db
	if err := EnsureAttackArchiveTable(s.edb, s.sqls); err != nil {
		t.Fatalf("EnsureAttackArchiveTable: %v", err)
	}
	if err := EnsureAttackArchiveTable(s.edb, s.sqls); err != nil {
		t.Fatalf("重复建表应幂等: %v", err)
	}
	var n int
	if err := s.edb.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='attack_archive'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("attack_archive 表 = %d, want 1", n)
	}
}
