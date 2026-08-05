//go:build integration

// PostgreSQL 方言脚本集成测试：对真实 PG 全量执行三组脚本（mq / access_log / admin_users），
// 并穿过组件层（obs.DBStore / mq.OutboxStore）验证生产路径（RETURNING 自增、死信边界、查询归一化）。
// 通过环境变量 PG_TEST_DSN 指定连接串（如 host=<HOST> port=5432 user=<USER> password=<PASSWORD> dbname=postgres sslmode=disable），
// 仅当该变量非空时才实际连接；否则跳过。运行：
//
//	PG_TEST_DSN="host=... user=... password=... dbname=... sslmode=disable" go test -tags integration -run TestPostgresDialect ./internal/db/
package db_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"rocksys/internal/db"
	"rocksys/plugins/mq"
	"rocksys/plugins/obs"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// TestPostgresDialect 在真实 PG 上验证 postgres 方言脚本 + 组件层生产路径。
func TestPostgresDialect(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN 未设置，跳过 PostgreSQL 集成测试")
	}

	d, err := db.Open("postgres", dsn, "sql")
	if err != nil {
		t.Fatalf("Open(postgres) err: %v", err)
	}
	defer d.Close()

	run := func(name, table string, fn func(t *testing.T, d *db.DB, table string)) {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(func() { _, _ = d.EasyDB().Exec("DROP TABLE IF EXISTS " + table) })
			fn(t, d, table)
		})
	}

	// obs 走组件层：EnsureTable（含索引幂等）、Write、Query（含 CONCAT/|| 与 normalizeRowTypes）、SizeBytes。
	run("obs_access_log", "obs_access_log_pgtest", func(t *testing.T, d *db.DB, table string) {
		st := obs.NewDBStore(d, table)
		if err := st.EnsureTable(); err != nil {
			t.Fatalf("EnsureTable err: %v", err)
		}
		// 重复 EnsureTable：建表 IF NOT EXISTS + 建索引 IF NOT EXISTS 均幂等
		if err := st.EnsureTable(); err != nil {
			t.Fatalf("重复 EnsureTable 应幂等: %v", err)
		}

		base := time.Now()
		if err := st.Write([]*obs.AccessRecord{{
			Time: base.Add(-time.Minute), TraceID: "123", TenantID: "42", Path: "/a/b",
			Method: "GET", ClientIP: "127.0.0.1", StatusCode: 200,
			Upstream: "http://up", ShieldMs: 1, BizMs: 2, TotalMs: 3, ReqBytes: 4, RespBytes: 5,
			Extras: map[string]any{"k": "v"},
		}}); err != nil {
			t.Fatalf("Write err: %v", err)
		}

		rows, err := st.Query(obs.Query{
			From: base.Add(-2 * time.Hour), To: base.Add(time.Hour), PathLike: "/a",
		})
		if err != nil {
			t.Fatalf("Query err: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("查询应返回 1 行, got %d", len(rows))
		}
		// 字符串数字列保持 string（normalizeRowTypes 生效），数值列保持 int64
		if v, ok := rows[0][obs.DimTraceID].(string); !ok || v != "123" {
			t.Errorf("trace_id 应为 string \"123\"，got %T(%v)", rows[0][obs.DimTraceID], rows[0][obs.DimTraceID])
		}
		if _, ok := rows[0][obs.DimStatusCode].(int64); !ok {
			t.Errorf("status_code 应为 int64，got %T(%v)", rows[0][obs.DimStatusCode], rows[0][obs.DimStatusCode])
		}
		if n, err := st.SizeBytes(); err != nil || n <= 0 {
			t.Fatalf("SizeBytes err=%v n=%d（应 >0）", err, n)
		}
	})

	// mq 走组件层：Insert（PG 分支 RETURNING）、FetchPending、MarkDone/MarkFailed/MarkDead。
	run("mq_outbox", "mq_outbox_pgtest", func(t *testing.T, d *db.DB, table string) {
		store := mq.NewOutboxStore(d.EasyDB().GetSqlDB(), table)
		store.SetSQLSource(d)
		store.SetMaxRetries(3)
		if err := store.EnsureTable(); err != nil {
			t.Fatalf("EnsureTable err: %v", err)
		}
		// 重复 EnsureTable → 建表+索引均幂等
		if err := store.EnsureTable(); err != nil {
			t.Fatalf("重复 EnsureTable 应幂等: %v", err)
		}

		// PostgreSQL 分支：Driver()=="postgres" → mq_insert_returning_id.sql（RETURNING id）
		id, err := store.Insert("order.created", `{"id":1}`)
		if err != nil {
			t.Fatalf("Insert(RETURNING) err: %v", err)
		}
		if id == 0 {
			t.Fatal("Insert 应返回自增 id")
		}

		msgs, err := store.FetchPending(10)
		if err != nil {
			t.Fatalf("FetchPending err: %v", err)
		}
		if len(msgs) != 1 || msgs[0].ID != id || msgs[0].Status != "pending" {
			t.Fatalf("FetchPending 应返回 1 条 pending，got %+v", msgs)
		}

		if err := store.MarkDone(id); err != nil {
			t.Fatalf("MarkDone err: %v", err)
		}
		if err := store.MarkDead(id); err != nil {
			t.Fatalf("MarkDead err: %v", err)
		}
	})

	// mq 死信边界：第 1、2 次失败 failed，第 3 次才 dead（maxRetries=3）。
	run("mq_dead_letter", "mq_outbox_dead_pgtest", func(t *testing.T, d *db.DB, table string) {
		store := mq.NewOutboxStore(d.EasyDB().GetSqlDB(), table)
		store.SetSQLSource(d)
		store.SetMaxRetries(3)
		if err := store.EnsureTable(); err != nil {
			t.Fatalf("EnsureTable err: %v", err)
		}
		id, err := store.Insert("t", "p")
		if err != nil {
			t.Fatalf("Insert err: %v", err)
		}
		status := func() string {
			var s string
			if err := d.EasyDB().GetSqlDB().QueryRow("SELECT status FROM "+table+" WHERE id = $1", id).Scan(&s); err != nil {
				t.Fatalf("查询 status err: %v", err)
			}
			return s
		}
		for i := 1; i <= 2; i++ {
			c, err := store.MarkFailed(id, "boom")
			if err != nil || c != i {
				t.Fatalf("第 %d 次 MarkFailed c=%d err=%v", i, c, err)
			}
			if s := status(); s != "failed" {
				t.Fatalf("第 %d 次失败后 status=%q，want failed", i, s)
			}
		}
		c, err := store.MarkFailed(id, "boom")
		if err != nil || c != 3 {
			t.Fatalf("第 3 次 MarkFailed c=%d err=%v", c, err)
		}
		if s := status(); s != "dead" {
			t.Fatalf("第 3 次失败后 status=%q，want dead", s)
		}
	})

	// admin_users 脚本全执行（补 get：空表→无行、插入后→首行）。
	run("admin_users", "admin_users_pgtest", func(t *testing.T, d *db.DB, table string) {
		for _, name := range []string{
			"admin_users_create_table.sql", "admin_users_count.sql",
			"admin_users_get.sql", "admin_users_get_by_username.sql",
			"admin_users_update.sql", "admin_users_insert.sql",
		} {
			if _, err := d.SQL(name); err != nil {
				t.Fatalf("SQL(%s) err: %v", name, err)
			}
		}
		exec := func(name string, args ...any) {
			t.Helper()
			txt, err := d.SQL(name)
			if err != nil {
				t.Fatalf("SQL(%s) err: %v", name, err)
			}
			if _, err := d.EasyDB().Exec(strings.ReplaceAll(txt, "{table}", table), args...); err != nil {
				t.Fatalf("执行 %s 失败: %v", name, err)
			}
		}
		exec("admin_users_create_table.sql")
		// 重复建表 → 幂等
		exec("admin_users_create_table.sql")

		// 空表 get：无返回行，不报错
		var u struct {
			ID           int64  `db:"id"`
			Username     string `db:"username"`
			PasswordHash string `db:"password_hash"`
			CreatedAt    string `db:"created_at"`
			UpdatedAt    string `db:"updated_at"`
		}
		get, _ := d.SQL("admin_users_get.sql")
		if err := d.EasyDB().GetOneData(strings.ReplaceAll(get, "{table}", table), &u); err != nil {
			t.Fatalf("空表 admin get 不应报错: %v", err)
		}
		if u.ID != 0 {
			t.Fatalf("空表 get 应无行（ID=0），got %+v", u)
		}

		exec("admin_users_insert.sql", "admin", "hash-1", "2026-08-06T00:00:00Z", "2026-08-06T00:00:00Z")

		cnt, _ := d.SQL("admin_users_count.sql")
		row := make(map[string]any, 1)
		if err := d.EasyDB().GetOneData(strings.ReplaceAll(cnt, "{table}", table), &row); err != nil {
			t.Fatalf("admin count 失败: %v", err)
		}
		n, _ := row["n"].(int64)
		if n != 1 {
			t.Fatalf("admin count 应为 1, got %d", n)
		}

		getByUser, _ := d.SQL("admin_users_get_by_username.sql")
		if err := d.EasyDB().GetOneData(strings.ReplaceAll(getByUser, "{table}", table), &u, "admin"); err != nil {
			t.Fatalf("admin getByUsername 失败: %v", err)
		}
		if u.Username != "admin" {
			t.Fatalf("getByUsername 返回异常: %+v", u)
		}

		upd, _ := d.SQL("admin_users_update.sql")
		if _, err := d.EasyDB().Exec(strings.ReplaceAll(upd, "{table}", table),
			"admin2", "hash-2", "2026-08-06T01:00:00Z", u.ID); err != nil {
			t.Fatalf("admin update 失败: %v", err)
		}
		// 首行 get：返回更新后的唯一行
		if err := d.EasyDB().GetOneData(strings.ReplaceAll(get, "{table}", table), &u); err != nil {
			t.Fatalf("admin get 失败: %v", err)
		}
		if u.Username != "admin2" {
			t.Fatalf("get 应返回更新后的行，got %+v", u)
		}
	})
}
