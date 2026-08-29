//go:build integration

// PostgreSQL 方言动态黑白名单集成测试：对真实 PG 验证 ip_blacklist/ip_whitelist/attack_archive
// 建表 + 索引幂等 + store 全流程（CRUD / 软删恢复 / 过期过滤 / 幂等导入 / 分页过滤 / hit_count）。
// 门控环境变量 PG_TEST_DSN（同 internal/db/pg_integration_test.go）；测试表名带 _pgtest 后缀隔离，
// Cleanup DROP TABLE，不触碰业务表。运行：
//
//	PG_TEST_DSN="host=... port=5432 user=... password=... dbname=... sslmode=disable" \
//	  go test -tags integration -run TestPostgresIPList ./plugins/shield/
package shield

import (
	"os"
	"strings"
	"testing"
	"time"

	"rocksys/internal/db"

	_ "github.com/lib/pq"
)

// pgTestDB 打开真实 PG 连接（PG_TEST_DSN 门控；空则跳过）。
func pgTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN 未设置，跳过 PostgreSQL 集成测试")
	}
	d, err := db.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("Open(postgres) err: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// pgListStore 建隔离表（表名 _pgtest 后缀）的 store，Cleanup 删表。
func pgListStore(t *testing.T, d *db.DB, isBlack bool) *IPListStore {
	t.Helper()
	store := NewIPListStore(d.EasyDB(), d, isBlack)
	tbl := store.Table() + "_pgtest"
	store.table = tbl // 同包测试：替换为隔离表名
	t.Cleanup(func() { _, _ = d.EasyDB().Exec("DROP TABLE IF EXISTS " + tbl) })
	if err := store.EnsureTable(); err != nil {
		t.Fatalf("EnsureTable(%s) err: %v", tbl, err)
	}
	if err := store.EnsureTable(); err != nil { // 幂等重执行
		t.Fatalf("重复 EnsureTable 应幂等: %v", err)
	}
	return store
}

// TestPostgresIPList 黑名单：建表/CRUD/软删恢复/过期过滤/幂等导入/分页过滤/hit_count。
func TestPostgresIPList(t *testing.T) {
	d := pgTestDB(t)
	s := pgListStore(t, d, true)
	now := time.Now().UTC()

	// 新增 + 快照加载
	idKeep, err := s.Insert("10.1.1.1", "pg-永久", 1, nil, now)
	if err != nil {
		t.Fatalf("Insert err: %v", err)
	}
	idExp, err := s.Insert("10.1.1.2", "pg-未过期", 7, timePtr(now.Add(time.Hour)), now)
	if err != nil {
		t.Fatalf("Insert(exp) err: %v", err)
	}
	if _, err := s.Insert("10.1.1.3", "pg-已过期", 7, timePtr(now.Add(-time.Hour)), now); err != nil {
		t.Fatalf("Insert(expired) err: %v", err)
	}
	// 唯一约束
	if _, err := s.Insert("10.1.1.1", "重复", 1, nil, now); err != ErrIPExists {
		t.Fatalf("重复 Insert err = %v, want ErrIPExists", err)
	}

	active, err := s.QueryActive(now)
	if err != nil {
		t.Fatalf("QueryActive err: %v", err)
	}
	if len(active) != 2 { // 已过期被过滤
		t.Fatalf("QueryActive = %d 条, want 2（已过期过滤）", len(active))
	}

	// 更新（title/block_type/expires_at）
	if err := s.Update(idKeep, "pg-改备注", 6, nil, now); err != nil {
		t.Fatalf("Update err: %v", err)
	}

	// 列表分页过滤（pg 的 || LIKE 与 LIMIT/OFFSET）
	rows, total, err := s.List(ListFilter{IP: "10.1.1", Limit: 10}, now)
	if err != nil {
		t.Fatalf("List err: %v", err)
	}
	if total != 3 || len(rows) != 3 {
		t.Fatalf("List total = %d rows = %d, want 3/3", total, len(rows))
	}
	rows, total, _ = s.List(ListFilter{BlockType: BlockSQLInjection, Limit: 10}, now)
	if total != 2 {
		t.Fatalf("block_type 过滤 total = %d, want 2", total)
	}
	rows, total, _ = s.List(ListFilter{Limit: 2, Offset: 2}, now)
	if total != 3 || len(rows) != 1 { // 3 条数据 offset=2 → 越界剩 1 条
		t.Fatalf("分页 total = %d rows = %d, want 3/1", total, len(rows))
	}
	if _, ok := rows[0]["id"].(int64); !ok {
		t.Fatalf("id 未归一化: %T", rows[0]["id"])
	}

	// hit_count 累加
	if err := s.AddHitCount(idKeep, 3); err != nil {
		t.Fatalf("AddHitCount err: %v", err)
	}
	if err := s.AddHitCount(idExp, 2); err != nil {
		t.Fatalf("AddHitCount err: %v", err)
	}

	// 软删 + 恢复
	if err := s.SoftDelete(idKeep, now); err != nil {
		t.Fatalf("SoftDelete err: %v", err)
	}
	active, _ = s.QueryActive(now)
	if len(active) != 1 {
		t.Fatalf("软删后 QueryActive = %d, want 1", len(active))
	}
	rows, total, _ = s.List(ListFilter{Limit: 10, ValidOnly: true}, now)
	if total != 1 {
		t.Fatalf("仅有效 total = %d, want 1", total)
	}
	if err := s.Restore(idKeep, now); err != nil {
		t.Fatalf("Restore err: %v", err)
	}
	active, _ = s.QueryActive(now)
	if len(active) != 2 {
		t.Fatalf("恢复后 QueryActive = %d, want 2", len(active))
	}

	// 幂等导入（含注释/空行/重复/CIDR）
	imported, skipped, err := s.Import([]string{
		"10.2.0.1", "", "# 注释", "10.2.0.1", "10.2.0.0/24",
	}, "pg-导入", 1, now)
	if err != nil {
		t.Fatalf("Import err: %v", err)
	}
	if imported != 2 || skipped != 1 {
		t.Fatalf("imported = %d skipped = %d, want 2/1", imported, skipped)
	}
	imported, skipped, _ = s.Import([]string{"10.2.0.1", "10.2.0.0/24"}, "再导", 1, now)
	if imported != 0 || skipped != 2 {
		t.Fatalf("二次导入 imported = %d skipped = %d, want 0/2", imported, skipped)
	}
}

// TestPostgresIPListWhitelist 白名单同构（无 block_type/expires_at/hit_count）。
func TestPostgresIPListWhitelist(t *testing.T) {
	d := pgTestDB(t)
	s := pgListStore(t, d, false)
	now := time.Now().UTC()

	if _, err := s.Insert("10.3.0.1", "pg-白1", 0, nil, now); err != nil {
		t.Fatalf("Insert err: %v", err)
	}
	id2, err := s.Insert("10.3.0.2", "pg-白2", 0, nil, now)
	if err != nil {
		t.Fatalf("Insert err: %v", err)
	}
	// 白名单忽略 expires_at：插入时传过期时间仍有效
	if _, err := s.Insert("10.3.0.3", "pg-白3", 0, timePtr(now.Add(-time.Hour)), now); err != nil {
		t.Fatalf("Insert(白名单带过期) err: %v", err)
	}
	active, err := s.QueryActive(now.Add(time.Hour))
	if err != nil {
		t.Fatalf("QueryActive err: %v", err)
	}
	if len(active) != 3 {
		t.Fatalf("白名单 QueryActive = %d, want 3（不按 expires_at 过滤）", len(active))
	}
	if err := s.SoftDelete(id2, now); err != nil {
		t.Fatalf("SoftDelete err: %v", err)
	}
	if err := s.Restore(id2, now); err != nil {
		t.Fatalf("Restore err: %v", err)
	}
	// 白名单 AddHitCount 报错
	if err := s.AddHitCount(id2, 1); err == nil || !strings.Contains(err.Error(), "hit_count") {
		t.Fatalf("白名单 AddHitCount err = %v, want hit_count 错误", err)
	}
	// 列表无 block_type 列
	rows, total, err := s.List(ListFilter{Limit: 10}, now)
	if err != nil || total != 3 || len(rows) != 3 {
		t.Fatalf("List total = %d rows = %d err = %v, want 3/3", total, len(rows), err)
	}
	if _, ok := rows[0]["block_type"]; ok {
		t.Fatal("白名单行不应含 block_type")
	}
}

// TestPostgresAttackArchive 攻击证据归档表：本期仅建表 + 索引幂等（走生产装配函数）。
func TestPostgresAttackArchive(t *testing.T) {
	lockSharedDevDB(t) // devdb 共享库互斥：固定表名 attack_archive 与 internal/db 全清单验收测试互踩
	d := pgTestDB(t)
	t.Cleanup(func() { _, _ = d.EasyDB().Exec("DROP TABLE IF EXISTS attack_archive_pgtest") })

	// 生产路径经 EnsureAttackArchiveTable 建固定表名；测试隔离：手动 Exec 同脚本到 pgtest 表。
	ddl, err := d.SQL("attack_archive_create_table.sql")
	if err != nil {
		t.Fatalf("SQL(create_table) err: %v", err)
	}
	if _, err := d.EasyDB().Exec(strings.ReplaceAll(ddl, "{table}", "attack_archive_pgtest")); err != nil {
		t.Fatalf("建表 err: %v", err)
	}
	if _, err := d.EasyDB().Exec(strings.ReplaceAll(ddl, "{table}", "attack_archive_pgtest")); err != nil {
		t.Fatalf("重复建表应幂等: %v", err)
	}
	idx, err := d.SQL("attack_archive_create_index.sql")
	if err != nil {
		t.Fatalf("SQL(create_index) err: %v", err)
	}
	for _, stmt := range db.SplitSQLStatements(strings.ReplaceAll(idx, "{table}", "attack_archive_pgtest")) {
		if _, err := d.EasyDB().Exec(stmt); err != nil {
			t.Fatalf("建索引 err: %v（SQL: %s）", err, stmt)
		}
	}
	// 生产装配函数本身（固定表名）——先 DROP 再调用验证幂等建表
	_, _ = d.EasyDB().Exec("DROP TABLE IF EXISTS attack_archive")
	if err := EnsureAttackArchiveTable(d.EasyDB(), d); err != nil {
		t.Fatalf("EnsureAttackArchiveTable err: %v", err)
	}
	if err := EnsureAttackArchiveTable(d.EasyDB(), d); err != nil {
		t.Fatalf("重复 EnsureAttackArchiveTable 应幂等: %v", err)
	}
	t.Cleanup(func() { _, _ = d.EasyDB().Exec("DROP TABLE IF EXISTS attack_archive") })
}
