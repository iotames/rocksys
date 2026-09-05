//go:build integration

// 真库封禁链路集成测试（A2 功能三方言验证）：BanInsert / RestoreBan 三分支（普通恢复·
// 续封·满 5 转永久）/ 小黑屋 Jail / {order} 排序白名单——sqlite 单测之外，对真实
// PostgreSQL / MySQL 验证方言 SQL（门控 PG_TEST_DSN / MYSQL_TEST_DSN，build tag integration）。
// 测试表名带 _<dialect>test 后缀隔离，Cleanup DROP TABLE。运行：
//
//	PG_TEST_DSN="postgresql://dev:dev123456@127.0.0.1:5432/devdb?sslmode=disable" \
//	MYSQL_TEST_DSN="dev:dev123456@tcp(127.0.0.1:3306)/devdb?parseTime=true" \
//	  go test -tags integration -run 'TestRealDBBan' ./plugins/shield/
package shield

import (
	"os"
	"testing"
	"time"

	"rocksys/internal/db"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

// runBanFlowTest 封禁链路端到端（driver 为方言目录名，suffix 为隔离表名后缀）。
func runBanFlowTest(t *testing.T, driver, dsn, suffix string) {
	t.Helper()
	d, err := db.Open(driver, dsn)
	if err != nil {
		t.Fatalf("Open(%s) err: %v", driver, err)
	}
	t.Cleanup(func() { _ = d.Close() })

	store := NewIPListStore(d.EasyDB(), d, true)
	tbl := store.Table() + suffix
	store.table = tbl
	t.Cleanup(func() { _, _ = d.EasyDB().Exec("DROP TABLE IF EXISTS " + tbl) })
	if err := store.EnsureTable(); err != nil {
		t.Fatalf("EnsureTable err: %v", err)
	}
	now := time.Now().UTC()

	// ① BanInsert：warn_times=1 起算
	id, err := store.BanInsert("10.9.0.1", "自动拉黑：10m内拦截≥50次", BlockSQLInjection, ptrTime(now.Add(24*time.Hour)), now)
	if err != nil {
		t.Fatalf("BanInsert: %v", err)
	}
	if id <= 0 {
		t.Fatalf("BanInsert 应返回有效 id，got %d", id)
	}
	e, err := store.GetByIP("10.9.0.1")
	if err != nil || e == nil || e.WarnTimes != 1 || e.Deleted {
		t.Fatalf("BanInsert 后 GetByIP 异常: e=%+v err=%v", e, err)
	}

	// ② RestoreBan 普通恢复：软删条目 → 恢复 + warn_times+1 + 所选时长
	if err := store.SoftDelete(id, now); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	perm, err := store.RestoreBan("10.9.0.1", ptrTime(now.Add(48*time.Hour)), now, banWarnTimesLimit)
	if err != nil || perm {
		t.Fatalf("RestoreBan 普通恢复应成功且不转永久: perm=%v err=%v", perm, err)
	}
	if e, _ = store.GetByIP("10.9.0.1"); e.Deleted || e.WarnTimes != 2 {
		t.Fatalf("恢复后应未删且 warn_times=2: %+v", e)
	}

	// ③ RestoreBan 满 5 转永久：连封 3 次（2→3→4→5），第 5 次转永久
	for i := 0; i < 2; i++ {
		if err := store.SoftDelete(id, now); err != nil {
			t.Fatalf("SoftDelete: %v", err)
		}
		if perm, err = store.RestoreBan("10.9.0.1", ptrTime(now.Add(24*time.Hour)), now, banWarnTimesLimit); err != nil || perm {
			t.Fatalf("第 %d 次恢复不应转永久: perm=%v err=%v", i+2, perm, err)
		}
	}
	if err := store.SoftDelete(id, now); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	perm, err = store.RestoreBan("10.9.0.1", ptrTime(now.Add(24*time.Hour)), now, banWarnTimesLimit)
	if err != nil || !perm {
		t.Fatalf("第 5 次恢复应转永久: perm=%v err=%v", perm, err)
	}
	if e, _ = store.GetByIP("10.9.0.1"); e.ExpiresAt != "" || e.WarnTimes != 5 {
		t.Fatalf("转永久后 expires_at 应为空且 warn_times=5: %+v", e)
	}

	// ④ 恢复永久封禁条目：warn_times 继续累加但保持永久（防重复追加转永久标记）
	if err := store.SoftDelete(id, now); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if perm, err = store.RestoreBan("10.9.0.1", ptrTime(now.Add(24*time.Hour)), now, banWarnTimesLimit); err != nil || perm {
		t.Fatalf("永久条目恢复不报转永久: perm=%v err=%v", perm, err)
	}
	if e, _ = store.GetByIP("10.9.0.1"); e.ExpiresAt != "" || e.WarnTimes != 6 {
		t.Fatalf("永久条目恢复后应仍永久且 warn_times=6: %+v", e)
	}

	// ⑤ 小黑屋：限时未删未过期条目才在押
	exp1 := now.Add(2 * time.Hour)
	if _, err := store.BanInsert("10.9.0.2", "jail-a", BlockCrawlerUA, &exp1, now); err != nil {
		t.Fatalf("BanInsert(10.9.0.2): %v", err)
	}
	if _, err := store.BanInsert("10.9.0.3", "jail-perm", BlockCrawlerUA, nil, now); err != nil { // 永久不在押
		t.Fatalf("BanInsert(10.9.0.3): %v", err)
	}
	rows, total, err := store.Jail(now, 20)
	if err != nil {
		t.Fatalf("Jail: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("小黑屋应只含 1 条限时在押（永久与已恢复满 5 的 10.9.0.1 排除），got total=%d rows=%d", total, len(rows))
	}
	if rows[0]["ip"] != "10.9.0.2" {
		t.Fatalf("在押条目应为 10.9.0.2，got %v", rows[0]["ip"])
	}
	if _, ok := rows[0]["warn_times"]; !ok {
		t.Errorf("小黑屋行应含 warn_times 列: %v", rows[0])
	}

	// ⑥ 排序：{order} 白名单注入（warn_times DESC → 10.9.0.1 的 6 次在前）
	if err := store.Update(id, "", 0, ptrTime(now.Add(24*time.Hour)), now); err != nil {
		t.Fatalf("Update: %v", err)
	}
	rows, _, err = store.List(ListFilter{Limit: 10, Sort: "warn_times"}, now)
	if err != nil {
		t.Fatalf("List(warn_times): %v", err)
	}
	if len(rows) < 2 || rows[0]["ip"] != "10.9.0.1" {
		t.Fatalf("warn_times DESC 排序首行应为 10.9.0.1（6 次），got %+v", firstOr(rows))
	}
	rows, _, err = store.List(ListFilter{Limit: 10, Sort: "DROP TABLE x;--"}, now) // 非法值回默认
	if err != nil {
		t.Fatalf("List(非法 sort): %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("非法 sort 应回默认排序正常返回，got %d 行", len(rows))
	}
}

func firstOr(rows []map[string]any) map[string]any {
	if len(rows) > 0 {
		return rows[0]
	}
	return nil
}

func ptrTime(t time.Time) *time.Time { return &t }

// getenvDefault 取环境变量（不存在返回空串）。
func getenvDefault(key string) string {
	v, _ := os.LookupEnv(key)
	return v
}

// TestRealDBBanPostgres 真库封禁链路（PG）。
func TestRealDBBanPostgres(t *testing.T) {
	dsn := getenvDefault("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN 未设置，跳过")
	}
	runBanFlowTest(t, "postgres", dsn, "_pgtest")
}

// TestRealDBBanMySQL 真库封禁链路（MySQL）。
func TestRealDBBanMySQL(t *testing.T) {
	dsn := getenvDefault("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN 未设置，跳过")
	}
	runBanFlowTest(t, "mysql", dsn, "_mytest")
}
