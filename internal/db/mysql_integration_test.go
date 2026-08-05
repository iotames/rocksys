//go:build integration

// MySQL 方言脚本集成测试：对真实 MySQL/MariaDB 全量执行三组脚本（mq / access_log / admin_users），
// 并穿过组件层验证死信语义（A1 修复：MySQL 的 UPDATE SET 从左到右求值，status 必须先于 retry_count 赋值，
// 否则第 2 次失败即误转死信）。
//
// 通过环境变量 MYSQL_TEST_DSN 指定连接串（如 <USER>:<PASSWORD>@tcp(<HOST>:3306)/<DB>），
// 仅当该变量非空时才实际连接；否则跳过。运行：
//
//	MYSQL_TEST_DSN="<USER>:<PASSWORD>@tcp(<HOST>:3306)/<DB>" go test -tags integration -run TestMySQLDialect ./internal/db/
//
// 注意：测试只使用带 _mysqltest 后缀的临时表，结束后 DROP 清理，不触碰库内既有表。
package db_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"rocksys/internal/db"
	"rocksys/plugins/mq"
	"rocksys/plugins/obs"

	_ "github.com/go-sql-driver/mysql"
)

// execScript 读取脚本并替换表名后执行（单条语句，无参数）。
func execScript(t *testing.T, d *db.DB, table, name string) {
	t.Helper()
	txt, err := d.SQL(name)
	if err != nil {
		t.Fatalf("SQL(%s) err: %v", name, err)
	}
	if _, err := d.EasyDB().Exec(strings.ReplaceAll(txt, "{table}", table)); err != nil {
		t.Fatalf("执行 %s 失败: %v", name, err)
	}
}

// dropTable 清理测试临时表。
func dropTable(t *testing.T, d *db.DB, table string) {
	t.Helper()
	if _, err := d.EasyDB().Exec("DROP TABLE IF EXISTS " + table); err != nil {
		t.Errorf("清理表 %s 失败: %v", table, err)
	}
}

// TestMySQLDialect 在真实 MySQL 上验证三组方言脚本（obs / mq / admin_users）全流程。
func TestMySQLDialect(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN 未设置，跳过 MySQL 集成测试")
	}
	d, err := db.Open("mysql", dsn, "sql")
	if err != nil {
		t.Fatalf("Open(mysql) err: %v", err)
	}
	defer d.Close()
	if d.Driver() != "mysql" {
		t.Fatalf("Driver=%q，want mysql", d.Driver())
	}

	t.Run("obs_access_log", func(t *testing.T) {
		const table = "access_log_mysqltest"
		t.Cleanup(func() { dropTable(t, d, table) })

		// 走 obs 组件层：建表 + 逐条建索引（SplitSQLStatements）+ 幂等容错。
		st := obs.NewDBStore(d, table)
		if err := st.EnsureTable(); err != nil {
			t.Fatalf("EnsureTable err: %v", err)
		}
		// 重复 EnsureTable：建表 IF NOT EXISTS 幂等；建索引报 Duplicate key name 被容错。
		if err := st.EnsureTable(); err != nil {
			t.Fatalf("重复 EnsureTable 应幂等（Duplicate key name 容错）: %v", err)
		}

		base := time.Now()
		if err := st.Write([]*obs.AccessRecord{{
			Time: base.Add(-time.Minute), TraceID: "trace-1", Path: "/a/b",
			Method: "GET", ClientIP: "127.0.0.1", StatusCode: 200,
			Upstream: "http://up", ShieldMs: 1, BizMs: 2, TotalMs: 3, ReqBytes: 4, RespBytes: 5,
			Extras: map[string]any{"k": "v"},
		}}); err != nil {
			t.Fatalf("Write err: %v", err)
		}

		// CONCAT('%', ?, '%') 模糊查询在 MySQL 上的参数化验证（组件层 Query）。
		rows, err := st.Query(obs.Query{
			From: base.Add(-2 * time.Hour), To: base.Add(time.Hour),
			PathLike: "/a",
		})
		if err != nil {
			t.Fatalf("Query err: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("查询应返回 1 行, got %d", len(rows))
		}

		// information_schema 返回 DECIMAL → SizeBytes 归一化为 int64。
		if n, err := st.SizeBytes(); err != nil || n <= 0 {
			t.Fatalf("SizeBytes err=%v n=%d（应 >0）", err, n)
		}
	})

	t.Run("mq_outbox", func(t *testing.T) {
		const table = "outbox_mysqltest"
		t.Cleanup(func() { dropTable(t, d, table) })

		execScript(t, d, table, "mq_create_table.sql")
		execScript(t, d, table, "mq_create_index.sql")
		// 重复建表+索引 → 表 IF NOT EXISTS 幂等；索引名重复报 Duplicate key name（组件容错）。
		execScript(t, d, table, "mq_create_table.sql")

		// 组件层：EnsureTable 应能容错重复建索引（MySQL 无 IF NOT EXISTS）。
		store := mq.NewOutboxStore(d.EasyDB().GetSqlDB(), table)
		store.SetSQLSource(d)
		store.SetMaxRetries(3)
		if err := store.EnsureTable(); err != nil {
			t.Fatalf("EnsureTable 重复调用应幂等: %v", err)
		}

		// MySQL 驱动支持 LastInsertId → 普通 INSERT 路径
		id, err := store.Insert("order.created", `{"id":1}`)
		if err != nil {
			t.Fatalf("Insert err: %v", err)
		}
		if id == 0 {
			t.Fatal("Insert 应返回自增 id")
		}

		msgs, err := store.FetchPending(10)
		if err != nil {
			t.Fatalf("FetchPending err: %v", err)
		}
		if len(msgs) != 1 || msgs[0].ID != id {
			t.Fatalf("FetchPending 应返回 1 条, got %d", len(msgs))
		}

		if err := store.MarkDone(id); err != nil {
			t.Fatalf("MarkDone err: %v", err)
		}
		if err := store.MarkDead(id); err != nil {
			t.Fatalf("MarkDead err: %v", err)
		}
	})

	// TestMySQLDialect/mq_dead_letter：A1 修复验证——死信必须发生在第 maxRetries(=3) 次失败，
	// 而非第 2 次。这是 MySQL/MariaDB UPDATE SET 左→右求值语义的关键回归测试。
	t.Run("mq_dead_letter", func(t *testing.T) {
		const table = "outbox_dead_mysqltest"
		t.Cleanup(func() { dropTable(t, d, table) })

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
			if err := d.EasyDB().GetSqlDB().QueryRow("SELECT status FROM "+table+" WHERE id = ?", id).Scan(&s); err != nil {
				t.Fatalf("查询 status err: %v", err)
			}
			return s
		}

		// 第 1、2 次失败：retry=1/2，status=failed（死信必须等第 3 次）
		for i := 1; i <= 2; i++ {
			c, err := store.MarkFailed(id, "boom")
			if err != nil {
				t.Fatalf("第 %d 次 MarkFailed err: %v", i, err)
			}
			if c != i {
				t.Fatalf("第 %d 次 MarkFailed retry=%d，want %d", i, c, i)
			}
			if s := status(); s != "failed" {
				t.Fatalf("第 %d 次失败后 status=%q，want failed（A1：不得提前转死信）", i, s)
			}
		}
		// 第 3 次失败：retry=3，status=dead
		c, err := store.MarkFailed(id, "boom")
		if err != nil {
			t.Fatalf("第 3 次 MarkFailed err: %v", err)
		}
		if c != 3 {
			t.Fatalf("第 3 次 MarkFailed retry=%d，want 3", c)
		}
		if s := status(); s != "dead" {
			t.Fatalf("第 3 次失败后 status=%q，want dead", s)
		}
	})

	t.Run("admin_users", func(t *testing.T) {
		const table = "admin_users_mysqltest"
		t.Cleanup(func() { dropTable(t, d, table) })

		execScript(t, d, table, "admin_users_create_table.sql")
		// 重复建表 → 幂等
		execScript(t, d, table, "admin_users_create_table.sql")

		// 空表 get：无返回行，不报错
		var u struct {
			ID           int64  `db:"id"`
			Username     string `db:"username"`
			PasswordHash string `db:"password_hash"`
			CreatedAt    string `db:"created_at"`
			UpdatedAt    string `db:"updated_at"`
		}
		getOne, _ := d.SQL("admin_users_get.sql")
		if err := d.EasyDB().GetOneData(strings.ReplaceAll(getOne, "{table}", table), &u); err != nil {
			t.Fatalf("空表 admin get 不应报错: %v", err)
		}
		if u.ID != 0 {
			t.Fatalf("空表 get 应无行（ID=0），got %+v", u)
		}

		// 带参数插入（占位符 ? 在 MySQL 上验证）
		ins, _ := d.SQL("admin_users_insert.sql")
		if _, err := d.EasyDB().Exec(strings.ReplaceAll(ins, "{table}", table),
			"admin", "hash-1", "2026-08-06T00:00:00Z", "2026-08-06T00:00:00Z"); err != nil {
			t.Fatalf("admin 插入失败: %v", err)
		}

		cnt, _ := d.SQL("admin_users_count.sql")
		row := make(map[string]any, 1)
		if err := d.EasyDB().GetOneData(strings.ReplaceAll(cnt, "{table}", table), &row); err != nil {
			t.Fatalf("admin count 失败: %v", err)
		}
		n, _ := row["n"].(int64)
		if n != 1 {
			t.Fatalf("admin count 应为 1, got %d", n)
		}

		get, _ := d.SQL("admin_users_get_by_username.sql")
		if err := d.EasyDB().GetOneData(strings.ReplaceAll(get, "{table}", table), &u, "admin"); err != nil {
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
		if err := d.EasyDB().GetOneData(strings.ReplaceAll(getOne, "{table}", table), &u); err != nil {
			t.Fatalf("admin get 失败: %v", err)
		}
		if u.Username != "admin2" {
			t.Fatalf("get 应返回更新后的行，got %+v", u)
		}
	})
}
