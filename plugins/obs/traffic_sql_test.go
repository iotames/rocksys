// traffic 统计 SQL 脚本（sql/<dbtype>/traffic_*.sql）单测：
// sqlite 临时库建 access_log / shield_event 两表，经 internal/db 的 SQLSource 读取内嵌脚本
// （不在测试内复制 SQL 文本），插入构造数据后跑 4 个脚本断言：
//   - UV 口径：同 IP 不同 UA 计 2；ua 空串退化为 IP 口径计 1；
//   - 对账：series 各桶之和（hour 与 day 两个粒度）= summary 总数；
//   - geo_top 计数与排序正确，空串 country 参与计数不丢量。
//
// MySQL / PostgreSQL 真库冒烟经 MYSQL_TEST_DSN / PG_TEST_DSN 环境变量门控（未设置即跳过）。
package obs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rocksys/internal/db"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// trafficTestBase 构造数据基准时刻（UTC）。
var trafficTestBase = time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)

// trafficScript 读取脚本并替换 {table}/{table2} 表名占位符（{table}=access 侧，{table2}=shield 侧）。
func trafficScript(t *testing.T, d *db.DB, name, accessTable, shieldTable string) string {
	t.Helper()
	txt, err := d.SQL(name)
	if err != nil {
		t.Fatalf("SQL(%s): %v", name, err)
	}
	txt = strings.ReplaceAll(txt, "{table}", accessTable)
	txt = strings.ReplaceAll(txt, "{table2}", shieldTable)
	return strings.ReplaceAll(txt, "{geo}", "geoip_list")
}

// trafficCreateTables 幂等建 access/shield 两表（直跑建表脚本）。
func trafficCreateTables(t *testing.T, d *db.DB, accessTable, shieldTable string) {
	t.Helper()
	for _, p := range []struct{ name, table string }{
		{"access_log_create_table.sql", accessTable},
		{"shield_event_create_table.sql", shieldTable},
		{"geoip_list_create_table.sql", "geoip_list"},
	} {
		// 建表脚本只有 {table} 单占位符，直接替换为本表名
		txt, err := d.SQL(p.name)
		if err != nil {
			t.Fatalf("SQL(%s): %v", p.name, err)
		}
		if _, err := d.EasyDB().Exec(strings.ReplaceAll(txt, "{table}", p.table)); err != nil {
			t.Fatalf("建表 %s: %v", p.table, err)
		}
	}
}

// trafficPh 按方言生成第 i 个（从 1 起）占位符（sqlite/mysql=?，postgres=$N）。
func trafficPh(driver string, i int) string {
	if driver == "postgres" {
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}

// trafficSeed 插入构造数据，覆盖：同 IP 不同 UA、同 IP 同 UA、ua 空串、静态资源
// （含大写后缀与带查询串路径）、4xx/5xx、shield 各 block_type、country 空串与非空。
// access 侧期望：req_ok=7（含 2 条静态）、uv=6、ip_all=4、err4xx=1、err5xx=2；
// hour 桶 10 点：ok=3 blocked=2；11 点：ok=4 blocked=2；合计 7/4。
func trafficSeed(t *testing.T, d *db.DB, accessTable, shieldTable string) {
	t.Helper()
	edb := d.EasyDB()
	ph := func(i int) string { return trafficPh(d.Driver(), i) }

	// 列：time(1) trace_id(2) path(3) method(4) client_ip(5) status_code(6) user_agent(7)
	// （geo 不再逐行落列，由 geoip_list 按 client_ip 关联）
	accIns := fmt.Sprintf(
		"INSERT INTO %s (time, trace_id, path, method, client_ip, status_code, user_agent, extra) VALUES (%s,%s,%s,%s,%s,%s,%s,'{}')",
		accessTable, ph(1), ph(2), ph(3), ph(4), ph(5), ph(6), ph(7))
	accRows := []struct {
		at    time.Time
		trace string
		path  string
		ip    string
		code  int
		ua    string
	}{
		{trafficTestBase.Add(5 * time.Minute), "t1", "/api/order/1", "1.1.1.1", 200, "UA-A"},
		{trafficTestBase.Add(15 * time.Minute), "t2", "/api/user/list", "1.1.1.1", 404, "UA-B"},
		{trafficTestBase.Add(65 * time.Minute), "t3", "/api/z", "1.1.1.1", 500, ""},
		{trafficTestBase.Add(20 * time.Minute), "t4", "/static/app.js", "2.2.2.2", 200, "UA-A"},
		{trafficTestBase.Add(70 * time.Minute), "t5", "/img/logo.png", "2.2.2.2", 200, "UA-A"},
		{trafficTestBase.Add(90 * time.Minute), "t6", "/api/w", "3.3.3.3", 502, "UA-C"},
		// 静态资源：大写后缀 + 查询串（验证 LOWER 与 '?' 前路径段匹配）
		{trafficTestBase.Add(110 * time.Minute), "t7", "/assets/app.CSS?ver=1", "4.4.4.4", 200, "UA-D"},
	}
	for _, r := range accRows {
		if _, err := edb.Exec(accIns, r.at, r.trace, r.path, "GET", r.ip, r.code, r.ua); err != nil {
			t.Fatalf("插入 access 行 %s: %v", r.trace, err)
		}
	}

	// 列：time(1) block_type(2) client_ip(3) status_code(4)
	// （path/extra 在 MySQL 方言无默认值，必须显式给值）
	shIns := fmt.Sprintf("INSERT INTO %s (time, block_type, client_ip, path, status_code, extra) VALUES (%s,%s,%s,'/x',%s,'{}')",
		shieldTable, ph(1), ph(2), ph(3), ph(4))
	shRows := []struct {
		at   time.Time
		btyp int
		ip   string
		code int
	}{
		{trafficTestBase.Add(10 * time.Minute), 1, "9.9.9.9", 403},
		{trafficTestBase.Add(80 * time.Minute), 7, "9.9.9.9", 403},
		{trafficTestBase.Add(100 * time.Minute), 2, "8.8.8.8", 429},
		{trafficTestBase.Add(40 * time.Minute), 3, "7.7.7.7", 403},
	}
	for _, r := range shRows {
		if _, err := edb.Exec(shIns, r.at, r.btyp, r.ip, r.code); err != nil {
			t.Fatalf("插入 shield 行 %s: %v", r.ip, err)
		}
	}

	// geoip_list：一 IP 一行（4.4.4.4 / 8.8.8.8 不入表——读侧 LEFT JOIN 空串计「未知」不丢量）
	if _, err := edb.Exec("DELETE FROM geoip_list"); err != nil {
		t.Fatalf("清理 geoip_list 残留: %v", err)
	}
	geoIns := fmt.Sprintf("INSERT INTO geoip_list (ip, country_code, country_name, province, city, created_at, updated_at) VALUES (%s,%s,%s,%s,%s,%s,%s)",
		ph(1), ph(2), ph(3), ph(4), ph(5), ph(6), ph(7))
	now := trafficTestBase
	geoRows := []struct{ ip, code, name, prov, city string }{
		{"1.1.1.1", "CN", "中国", "广东省", "深圳市"},
		{"2.2.2.2", "US", "美国", "加利福尼亚州", "尔湾"},
		{"3.3.3.3", "US", "美国", "", ""},
		{"9.9.9.9", "CN", "中国", "四川省", "成都市"},
		{"7.7.7.7", "US", "美国", "", ""},
	}
	for _, g := range geoRows {
		if _, err := edb.Exec(geoIns, g.ip, g.code, g.name, g.prov, g.city, now, now); err != nil {
			t.Fatalf("插入 geoip_list 行 %s: %v", g.ip, err)
		}
	}
}

// trafficAsInt 断言值转 int64（兼容 int64/int/浮点/字符串编码）。
func trafficAsInt(t *testing.T, v any) int64 {
	t.Helper()
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case []byte:
		var out int64
		if _, err := fmt.Sscanf(string(n), "%d", &out); err != nil {
			t.Fatalf("值 %q 转整数失败: %v", n, err)
		}
		return out
	case string:
		var out int64
		if _, err := fmt.Sscanf(n, "%d", &out); err != nil {
			t.Fatalf("值 %q 转整数失败: %v", n, err)
		}
		return out
	default:
		t.Fatalf("不支持的数值类型 %T: %v", v, v)
		return 0
	}
}

// trafficQuery 执行脚本并返回 map 切片。
func trafficQuery(t *testing.T, d *db.DB, script string, args ...any) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := d.EasyDB().GetMany(script, &rows, args...); err != nil {
		t.Fatalf("执行脚本失败: %v\n脚本: %s", err, script)
	}
	return rows
}

// TestTrafficRateDenominator 锁定错误率/拦截率分母口径（回归防护）：
// 错误率分母必须是 req_ok（仅入网数据），拦截率分母必须是请求次数（req_ok+block_total）。
// 历史缺陷：错误率分母曾误用请求次数，把拦截数算进分母而分子只数放行侧，
// 导致拦截越多错误率越低的相反方向；block4xx_rate 又因分子分母同源（拦截码全是 4xx）恒为 1，
// 被当成「拦截率」展示（当前系统显示 100% 即此）。
func TestTrafficRateDenominator(t *testing.T) {
	reqOK, blockTotal, err4xx, err5xx, block4xx := 1000.0, 30.0, 20.0, 2.0, 30.0
	total := reqOK + blockTotal

	// 错误率：分母仅入网侧 req_ok，不含拦截。
	if got, want := safeRate(err4xx, reqOK), 0.02; got != want {
		t.Errorf("err4xx_rate = %v, want %v（分母必须是 req_ok=%v，非请求次数 %v）", got, want, reqOK, total)
	}
	if got, want := safeRate(err5xx, reqOK), 0.002; got != want {
		t.Errorf("err5xx_rate = %v, want %v", got, want)
	}
	// 拦截混入分母会稀释错误率（正是历史缺陷的表现），断言二者不相等以固化口径。
	if safeRate(err4xx, reqOK) == safeRate(err4xx, total) {
		t.Error("错误率分母疑似仍含拦截：req_ok 与请求次数两种分母结果相同，无法区分口径")
	}

	// 拦截率：分母为请求次数，取值应显著低于 1（拦截占比），不再恒为 100%。
	got := safeRate(blockTotal, total)
	if want := 30.0 / 1030.0; got != want {
		t.Errorf("block_rate = %v, want %v", got, want)
	}
	if got >= 1 {
		t.Errorf("block_rate = %v，拦截率不应恒为 100%%（那是 block4xx_rate 的分子分母同源缺陷）", got)
	}

	// 拦截内部构成率：分子分母同源故恒为 1，保留作趋势位但不得当作拦截率展示。
	if got := safeRate(block4xx, blockTotal); got != 1 {
		t.Errorf("block4xx_rate = %v, want 1（拦截码恒为 403/413/429）", got)
	}

	// 分母 0 防护。
	if got := safeRate(5, 0); got != 0 {
		t.Errorf("分母 0 应输出 0，实际 %v", got)
	}
}

// TestTrafficBlockRateUnavailable 锁定拦截侧率的降级口径：SHIELD_EVENT_LOG_ENABLED=false 时
// 拦截事件未落库，计数与率一律输出 null（前端显示「—」），不得输出 0——
// 0 会把「无拦截数据」误报成「零拦截」，与 block_total 等计数字段的 null 口径相左。
func TestTrafficBlockRateUnavailable(t *testing.T) {
	defer SetBlockAvailable(true)

	SetBlockAvailable(false)
	if v := trafficBlockRate(30, 1030); v != nil {
		t.Errorf("记录关闭时 block_rate 应为 null，实际 %v", v)
	}
	if v := trafficBlockRate(4, 4); v != nil {
		t.Errorf("记录关闭时 block4xx_rate 应为 null，实际 %v", v)
	}
	if v := trafficBlockField(4); v != nil {
		t.Errorf("记录关闭时 block4xx 应为 null，实际 %v", v)
	}

	SetBlockAvailable(true)
	if v := trafficBlockRate(30, 1030); v != 30.0/1030.0 {
		t.Errorf("记录开启时 block_rate = %v, want %v", v, 30.0/1030.0)
	}
	if v := trafficBlockRate(30, 0); v != 0.0 {
		t.Errorf("记录开启但分母 0 时应输出 0，实际 %v", v)
	}
}

// trafficAssert 跑 4 个脚本并做全部断言（UV、对账、geo）。from/to 取基准时刻两侧整点。
func trafficAssert(t *testing.T, d *db.DB, accessTable, shieldTable string) {
	t.Helper()
	from := trafficTestBase
	to := trafficTestBase.Add(2 * time.Hour)

	// ① summary：期望值见 trafficSeed 注释（CTE 单次扫描版：4 参 = from,to ×2）。
	sum := trafficQuery(t, d, trafficScript(t, d, "traffic_summary.sql", accessTable, shieldTable),
		from, to, from, to)
	if len(sum) != 1 {
		t.Fatalf("summary 应返回 1 行，实际 %d", len(sum))
	}
	want := map[string]int64{
		"req_ok": 7, "req_pv": 4, "uv": 6, "ip_all": 4,
		"block_total": 4, "attack_ips": 3, "err4xx": 1, "err5xx": 2, "block4xx": 4,
	}
	for col, w := range want {
		if got := trafficAsInt(t, sum[0][col]); got != w {
			t.Errorf("summary.%s = %d, want %d", col, got, w)
		}
	}

	// ② series（hour 与 day 两个粒度）：各桶求和 = summary 总数（对账断言）。
	type bucketSum struct {
		ok, blocked int64
		buckets     map[string][2]int64
	}
	checkSeries := func(name, wantFirstBucket string, wantOK, wantBlocked int64, wantNBucket int) {
		rows := trafficQuery(t, d, trafficScript(t, d, name, accessTable, shieldTable), from, to, from, to)
		var s bucketSum
		s.buckets = map[string][2]int64{}
		for _, r := range rows {
			b, _ := r["bucket"].(string)
			ok := trafficAsInt(t, r["ok_count"])
			blocked := trafficAsInt(t, r["blocked_count"])
			s.ok += ok
			s.blocked += blocked
			s.buckets[b] = [2]int64{ok, blocked}
		}
		if s.ok != wantOK || s.blocked != wantBlocked {
			t.Errorf("%s 桶求和 = ok %d/blocked %d, want %d/%d", name, s.ok, s.blocked, wantOK, wantBlocked)
		}
		if len(s.buckets) != wantNBucket {
			t.Errorf("%s 应有 %d 个桶，实际 %d（%v）", name, wantNBucket, len(s.buckets), s.buckets)
		}
		if len(rows) > 0 {
			first, _ := rows[0]["bucket"].(string)
			if first != wantFirstBucket {
				t.Errorf("%s 首桶 = %q, want %q", name, first, wantFirstBucket)
			}
		}
	}
	// hour 粒度：sqlite 桶标签为 'YYYY-MM-DDTHH:00:00'（RFC3339 前缀），MySQL/PG 为空格分隔。
	// hour 桶标签实测：sqlite 驱动将 time.Time 存为 'YYYY-MM-DD HH:MM:SS' 空格分隔（非 RFC3339 的 T），
	// 与 MySQL/PG 标签一致，读侧无需按方言区分。
	checkSeries("traffic_series_hour.sql", "2026-09-09 10:00:00", 7, 4, 2)
	checkSeries("traffic_series_day.sql", "2026-09-09", 7, 4, 1)

	// ③ geo_top（geoip_list 关联后按 IP 归一）：
	// access 侧 CN=3（1.1.1.1×3）/ US=3（2.2.2.2×2 + 3.3.3.3）/ ""=1（4.4.4.4 未入表，不丢量）；
	// CN 与 US 并列 3，同计数排序不稳定，首行允许任一；blocked 侧 CN=2（9.9.9.9×2）/ US=1 / ""=1。
	runGeo := func(source string, wantTotal int64, wantFirstAny []string, wantFirstCnt int64) {
		// PG 占位符可复用（$3/$4 同值传两次）；sqlite/mysql 的 ? 不可复用，传 7 个。
		geoArgs := []any{from, to, source, from, to, source, 10}
		if d.Driver() == "postgres" {
			geoArgs = []any{from, to, source, source, 10}
		}
		rows := trafficQuery(t, d, trafficScript(t, d, "traffic_geo_top.sql", accessTable, shieldTable),
			geoArgs...)
		var total int64
		for i, r := range rows {
			ctry, _ := r["country"].(string)
			cnt := trafficAsInt(t, r["cnt"])
			total += cnt
			if i == 0 {
				okFirst := false
				for _, w := range wantFirstAny {
					if ctry == w && cnt == wantFirstCnt {
						okFirst = true
						break
					}
				}
				if !okFirst {
					t.Errorf("geo(%s) 首行 = (%q,%d), want %v×%d", source, ctry, cnt, wantFirstAny, wantFirstCnt)
				}
			}
			if i > 0 && cnt > trafficAsInt(t, rows[i-1]["cnt"]) {
				t.Errorf("geo(%s) 排序错误：第 %d 行 cnt=%d 大于前一行", source, i, cnt)
			}
		}
		if total != wantTotal {
			t.Errorf("geo(%s) 各行计数之和 = %d, want %d", source, total, wantTotal)
		}
	}
	runGeo("access", 7, []string{"CN", "US"}, 3)
	runGeo("blocked", 4, []string{"CN"}, 2)
	// 空串 country 必须出现在结果里（映射「未知」由读侧做，SQL 侧原样输出不丢量）
	geoAccessArgs := []any{from, to, "access", from, to, "access", 10}
	if d.Driver() == "postgres" { // PG 占位符可复用
		geoAccessArgs = []any{from, to, "access", "access", 10}
	}
	geoAccess := trafficQuery(t, d, trafficScript(t, d, "traffic_geo_top.sql", accessTable, shieldTable),
		geoAccessArgs...)
	hasEmpty := false
	for _, r := range geoAccess {
		if c, _ := r["country"].(string); c == "" {
			hasEmpty = true
		}
	}
	if !hasEmpty {
		t.Error("geo(access) 结果应包含空串 country（计「未知」，不丢量）")
	}

	// ④ geo_province_top：只统计 country_code='CN'，按 geoip_list.province（zh-CN 全称）聚合。
	// access 侧 CN=3：广东省 3（1.1.1.1×3；t3 原行 country 为空但按 IP 关联归入广东省）；
	// blocked 侧 CN=2：四川省 2（9.9.9.9×2）。
	runProv := func(source string, wantTotal int64, wantFirst string, wantFirstCnt int64) {
		provArgs := []any{from, to, source, from, to, source, 10}
		if d.Driver() == "postgres" {
			provArgs = []any{from, to, source, source, 10}
		}
		rows := trafficQuery(t, d, trafficScript(t, d, "traffic_geo_province_top.sql", accessTable, shieldTable),
			provArgs...)
		var total int64
		for i, r := range rows {
			region, _ := r["region"].(string)
			cnt := trafficAsInt(t, r["cnt"])
			total += cnt
			if i == 0 && (region != wantFirst || cnt != wantFirstCnt) {
				t.Errorf("geo_province(%s) 首行 = (%q,%d), want (%q,%d)", source, region, cnt, wantFirst, wantFirstCnt)
			}
		}
		if total != wantTotal {
			t.Errorf("geo_province(%s) 各行计数之和 = %d, want %d", source, total, wantTotal)
		}
	}
	runProv("access", 3, "广东省", 3)
	runProv("blocked", 2, "四川省", 2)
}

// dropTrafficTables 建表前先 DROP 残留（历史失败运行可能遗留带数据的表，污染计数断言）。
// geoip_list 为固定表名（脚本 {geo} 占位符不随后缀），一并列 入 DROP 清理范围。
func dropTrafficTables(t *testing.T, d *db.DB, accessTable, shieldTable string) {
	t.Helper()
	for _, tb := range []string{accessTable, shieldTable, "geoip_list"} {
		if _, err := d.EasyDB().Exec("DROP TABLE IF EXISTS " + tb); err != nil {
			t.Fatalf("DROP 残留表 %s: %v", tb, err)
		}
	}
}

// TestTrafficScriptsSQLite sqlite 临时库跑 4 个 traffic 脚本（UV 断言 + 对账断言 + geo 断言）。
func TestTrafficScriptsSQLite(t *testing.T) {
	d, err := db.Open("sqlite", filepath.Join(t.TempDir(), "traffic.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	trafficCreateTables(t, d, "access_log", "shield_event")
	trafficSeed(t, d, "access_log", "shield_event")
	trafficAssert(t, d, "access_log", "shield_event")
}

// TestTrafficScriptsMySQL MySQL 真库方言冒烟（环境变量 MYSQL_TEST_DSN 门控）。
// 只使用带 _traffic_mysqltest 后缀的临时表，结束 DROP 清理。
func TestTrafficScriptsMySQL(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN 未设置，跳过 MySQL traffic 脚本集成测试")
	}
	d, err := db.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("db.Open(mysql): %v", err)
	}
	defer d.Close()
	const acc, sh = "obs_access_log_traffic_mysqltest", "shield_event_traffic_mysqltest"
	// defer 而非 t.Cleanup（LIFO）：t.Cleanup 晚于 defer d.Close() 执行，
	// DROP 会落在已关闭连接上被静默吞掉 → 残留表污染共享库（F 级「库中多余表」）。
	dropTrafficTablesLater := func() { dropTrafficTables(t, d, acc, sh) } // t 在测试体内已不可用于 defer 后，直接闭包
	defer dropTrafficTablesLater()
	// 建表前先 DROP：防上一次失败运行残留行导致计数翻倍
	dropTrafficTables(t, d, acc, sh)
	trafficCreateTables(t, d, acc, sh)
	trafficSeed(t, d, acc, sh)
	trafficAssert(t, d, acc, sh)
}

// TestTrafficScriptsPostgres PostgreSQL 真库方言冒烟（环境变量 PG_TEST_DSN 门控）。
// 只使用带 _traffic_pgtest 后缀的临时表，结束 DROP 清理。
func TestTrafficScriptsPostgres(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN 未设置，跳过 PostgreSQL traffic 脚本集成测试")
	}
	d, err := db.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("db.Open(postgres): %v", err)
	}
	defer d.Close()
	const acc, sh = "obs_access_log_traffic_pgtest", "shield_event_traffic_pgtest"
	// defer 而非 t.Cleanup（LIFO）：t.Cleanup 晚于 defer d.Close() 执行，
	// DROP 会落在已关闭连接上被静默吞掉 → 残留表污染共享库（F 级「库中多余表」）。
	dropTrafficTablesLater := func() { dropTrafficTables(t, d, acc, sh) } // t 在测试体内已不可用于 defer 后，直接闭包
	defer dropTrafficTablesLater()
	// 建表前先 DROP：防上一次失败运行残留行导致计数翻倍
	dropTrafficTables(t, d, acc, sh)
	trafficCreateTables(t, d, acc, sh)
	trafficSeed(t, d, acc, sh)
	trafficAssert(t, d, acc, sh)
}
