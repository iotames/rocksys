package obs

import (
	"path/filepath"
	"testing"
	"time"

	"rocksys/internal/db"
)

// TestStatusBounds 状态过滤区间合成：不过滤 / 分组 / 仅异常 / 组合，均输出闭区间供 SQL BETWEEN。
func TestStatusBounds(t *testing.T) {
	cases := []struct {
		name   string
		q      Query
		wantLo int
		wantHi int
	}{
		{"不过滤", Query{}, 0, 999999},
		{"分组4xx", Query{StatusGroup: "4"}, 400, 499},
		{"分组2xx", Query{StatusGroup: "2"}, 200, 299},
		{"仅异常", Query{OnlyError: true}, 400, 999999},
		{"分组5xx加仅异常", Query{StatusGroup: "5", OnlyError: true}, 500, 599},
		{"分组4xx加仅异常", Query{StatusGroup: "4", OnlyError: true}, 400, 499},
		{"契约外组值1xx不过滤", Query{StatusGroup: "1"}, 0, 999999},
		{"契约外组值9xx不过滤", Query{StatusGroup: "9"}, 0, 999999},
		{"非法组值不过滤", Query{StatusGroup: "abc"}, 0, 999999},
	}
	for _, c := range cases {
		lo, hi := statusBounds(c.q)
		if lo != c.wantLo || hi != c.wantHi {
			t.Errorf("%s: got [%d,%d], want [%d,%d]", c.name, lo, hi, c.wantLo, c.wantHi)
		}
	}
}

// TestDBStoreQuerySortAndStatus 真库回归：time_desc 走专用脚本（主键倒序）、耗时排序走 CASE 脚本、
// 状态分组/仅异常合成区间过滤、Count 与 Query 口径一致。
// 回归背景：缺省排序曾用 ORDER BY CASE 致大范围查询退化为临时排序超时，现拆分双脚本。
func TestDBStoreQuerySortAndStatus(t *testing.T) {
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "obs_sort.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	st := NewDBStore(d, "")
	if err := st.EnsureTable(); err != nil {
		t.Fatalf("EnsureTable: %v", err)
	}
	base := time.Now()
	recs := []*AccessRecord{
		{Time: base.Add(-3 * time.Minute), TraceID: "a", Path: "/api/x", StatusCode: 200, TotalMs: 10, EgressMs: 8},
		{Time: base.Add(-2 * time.Minute), TraceID: "b", Path: "/api/y", StatusCode: 404, TotalMs: 30, EgressMs: 2},
		{Time: base.Add(-time.Minute), TraceID: "c", Path: "/api/z", StatusCode: 500, TotalMs: 20, EgressMs: 5},
	}
	if err := st.Write(recs); err != nil {
		t.Fatalf("Write: %v", err)
	}
	rng := Query{From: base.Add(-time.Hour), To: base.Add(time.Hour)}

	// time_desc（缺省）：最新在前（主键倒序）
	rows, err := st.Query(rng)
	if err != nil {
		t.Fatalf("Query time_desc: %v", err)
	}
	if rows[0][DimTraceID] != "c" || rows[2][DimTraceID] != "a" {
		t.Errorf("time_desc 顺序应为 c,b,a，实际 %v,%v,%v", rows[0][DimTraceID], rows[1][DimTraceID], rows[2][DimTraceID])
	}
	// 耗排序脚本：total_desc 首条应为 b（30ms）
	rows, err = st.Query(Query{From: rng.From, To: rng.To, Sort: "total_desc"})
	if err != nil {
		t.Fatalf("Query total_desc: %v", err)
	}
	if rows[0][DimTraceID] != "b" {
		t.Errorf("total_desc 首条应为 b，实际 %v", rows[0][DimTraceID])
	}
	// 仅异常：404+500 两条
	rows, _ = st.Query(Query{From: rng.From, To: rng.To, OnlyError: true})
	if len(rows) != 2 {
		t.Errorf("仅异常应有 2 条，实际 %d", len(rows))
	}
	// 状态分组 5xx：仅 500 一条
	rows, _ = st.Query(Query{From: rng.From, To: rng.To, StatusGroup: "5"})
	if len(rows) != 1 || rows[0][DimTraceID] != "c" {
		t.Errorf("5xx 应仅 c 一条，实际 %d 条", len(rows))
	}
	// Count 与 Query 全量口径一致
	total, err := st.Count(rng)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if total != 3 {
		t.Errorf("Count 应为 3，实际 %d", total)
	}
	if n, _ := st.Count(Query{From: rng.From, To: rng.To, OnlyError: true}); n != 2 {
		t.Errorf("仅异常 Count 应为 2，实际 %d", n)
	}
}
