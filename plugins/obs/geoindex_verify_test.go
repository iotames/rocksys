// geoindex_verify_test.go：验证 obs EnsureTable 随建表一并创建 geoip_list 关联表及其索引
// （GEOIP_LIST 方案回归：防止"只建表不建索引"回退）。
package obs

import (
	"path/filepath"
	"strings"
	"testing"

	"rocksys/internal/db"
)

func TestEnsureTableCreatesGeoipListIndex(t *testing.T) {
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "geo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	st := NewDBStore(d, "")
	if err := st.EnsureTable(); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	rows, err := d.EasyDB().GetSqlDB().Query(
		"SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='geoip_list' AND name NOT LIKE 'sqlite_autoindex%'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	found := false
	for _, n := range names {
		if strings.Contains(n, "idx_geoip_list_country") {
			found = true
		}
	}
	if !found {
		t.Fatalf("EnsureTable 应一并创建 idx_geoip_list_country 索引，实际索引: %v", names)
	}
}
