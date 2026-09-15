// missing_columns_test.go：启动缺列检测（TRAFFIC_ANALYSIS D16）单测。
// 验证：旧版两表 → 报缺 user_agent（access_log；country/city 已随 GEOIP_LIST 方案删除）；
// 新版全列表 → 零缺列；缺表 → 不误报缺列（缺表交给既有建表流程）。
package main

import (
	"path/filepath"
	"strings"
	"testing"

	"rocksys/internal/db"
)

// openTestDB 临时 sqlite 库（脚本源走内嵌 sql/sqlite/）。
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "mcol.db"))
	if err != nil {
		t.Fatalf("Open err: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func exec(t *testing.T, d *db.DB, q string) {
	t.Helper()
	if _, err := d.EasyDB().Exec(q); err != nil {
		t.Fatalf("exec %q err: %v", q[:40], err)
	}
}

func TestMissingLogColumns(t *testing.T) {
	specs := buildTableSpecs("shield_event")

	t.Run("旧版两表报缺列", func(t *testing.T) {
		d := openTestDB(t)
		// 旧表 = 全量 DDL 剔除本期新增列（模拟升级前老库，其余列齐备；country/city 已随
		// GEOIP_LIST 方案从建表脚本删除，不再参与缺列检测）。
		exec(t, d, ddlWithout(d, t, "access_log_create_table.sql", "access_log", "user_agent"))
		exec(t, d, ddlWithout(d, t, "shield_event_create_table.sql", "shield_event"))
		got := missingLogColumns(d, specs)
		assertCols(t, got["access_log"], "user_agent")
		if got["shield_event"] != nil {
			t.Fatalf("shield_event 无新增列不应报缺列，got %v", got["shield_event"])
		}
	})

	t.Run("全列表零缺列", func(t *testing.T) {
		d := openTestDB(t)
		ddl, err := d.SQL("access_log_create_table.sql")
		if err != nil {
			t.Fatalf("读建表脚本 err: %v", err)
		}
		exec(t, d, strings.ReplaceAll(ddl, "{table}", "access_log"))
		ddl, err = d.SQL("shield_event_create_table.sql")
		if err != nil {
			t.Fatalf("读建表脚本 err: %v", err)
		}
		exec(t, d, strings.ReplaceAll(ddl, "{table}", "shield_event"))
		if got := missingLogColumns(d, specs); len(got) != 0 {
			t.Fatalf("全列表不应报缺列，got %v", got)
		}
	})

	t.Run("缺表不误报缺列", func(t *testing.T) {
		d := openTestDB(t)
		if got := missingLogColumns(d, specs); len(got) != 0 {
			t.Fatalf("缺表应交由建表流程处理，不应报缺列，got %v", got)
		}
	})
}

// ddlWithout 读建表脚本并剔除指定列的定义行（模拟老库 schema）。
func ddlWithout(d *db.DB, t *testing.T, script, table string, drop ...string) string {
	t.Helper()
	ddl, err := d.SQL(script)
	if err != nil {
		t.Fatalf("读建表脚本 err: %v", err)
	}
	ddl = strings.ReplaceAll(ddl, "{table}", table)
	var keep []string
	for _, ln := range strings.Split(ddl, "\n") {
		skip := false
		for _, c := range drop {
			if strings.HasPrefix(strings.TrimSpace(ln), c+" ") {
				skip = true
				break
			}
		}
		if !skip {
			keep = append(keep, ln)
		}
	}
	return strings.Join(keep, "\n")
}

func assertCols(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("缺列应为 %v，got %v", want, got)
	}
	set := map[string]bool{}
	for _, c := range got {
		set[c] = true
	}
	for _, c := range want {
		if !set[c] {
			t.Fatalf("缺列应含 %s，got %v", c, got)
		}
	}
}
