// geoip_sync_test.go：GeoIP 历史回填单测（桩 Resolver，sqlite 临时库，不依赖真实 mmdb）。
// 覆盖：缺失行回填（country/city 落值）、已有值行不动、私网 IP 跳过、geo 未就绪拒绝、续填幂等。
package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"rocksys/internal/db"
	"rocksys/internal/geoip"
)

// stubResolver 桩解析器：公网 IP 返回固定地理，其他返回空（模拟私网/库外地址）。
type stubResolver struct{}

func (stubResolver) Ready() bool { return stubReady }

func (stubResolver) Lookup(ip string) geoip.GeoInfo {
	if ip == "8.8.8.8" {
		return geoip.GeoInfo{Code: "US", Country: "美国", City: "加利福尼亚州/山景城"}
	}
	if ip == "1.2.3.4" {
		return geoip.GeoInfo{Code: "US"}
	}
	return geoip.GeoInfo{}
}

var stubReady = true

// mustDDL 读建表脚本并替换 {table} 后返回。
func mustDDL(t *testing.T, d *db.DB, script, table string) string {
	t.Helper()
	ddl, err := d.SQL(script)
	if err != nil {
		t.Fatalf("读建表脚本 err: %v", err)
	}
	return strings.ReplaceAll(ddl, "{table}", table)
}

// countScalar 执行标量计数查询。
func countScalar(t *testing.T, d *db.DB, q string) int {
	t.Helper()
	rows, err := d.EasyDB().GetSqlDB().Query(q)
	if err != nil {
		t.Fatalf("查询 %q 失败: %v", q[:50], err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("查询 %q 无结果行", q[:50])
	}
	var n int
	if err := rows.Scan(&n); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return n
}

// resetGeoSyncState 重置回填全局运行态（游标/进行中标记），避免用例间相互串扰。
func resetGeoSyncState() {
	geoSyncMu.Lock()
	geoSyncCursors = map[string]int64{}
	geoSyncMu.Unlock()
	geoSyncRunning.Store(false)
}

func TestGeoipSyncBackfill(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	// 全量新表结构（回填发生在结构同步之后；旧库场景由 TestMissingLogColumns 覆盖）
	exec(t, d, mustDDL(t, d, "access_log_create_table.sql", "access_log"))
	exec(t, d, mustDDL(t, d, "shield_event_create_table.sql", "shield_event"))
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	insAcc := `INSERT INTO access_log (time, trace_id, path, method, client_ip, status_code, user_agent, country, city, extra)
		VALUES ('%s','t1','/a','GET','%s',200,'UA','%s','%s','{}')`
	// access_log：8.8.8.8×2（缺失→应回填）、已回填行（不动）、127.0.0.1（私网→跳过）
	exec(t, d, fmt.Sprintf(insAcc, now, "8.8.8.8", "", ""))
	exec(t, d, fmt.Sprintf(insAcc, now, "8.8.8.8", "", ""))
	exec(t, d, fmt.Sprintf(insAcc, now, "9.9.9.9", "JP", "东京"))
	exec(t, d, fmt.Sprintf(insAcc, now, "127.0.0.1", "", ""))
	// shield_event：country 已有值 city 空（WHERE country='' 不命中 → 验证"只补缺失行"边界）
	exec(t, d, `INSERT INTO shield_event (time, trace_id, block_type, client_ip, method, path, user_agent, host, country, city, status_code, rule_hit, req_bytes, extra)
		VALUES ('`+now+`','s1',7,'1.2.3.4','GET','/x','UA','h','US','','403','test',0,'{}')`)

	reps, err := geoSyncAll(context.Background(), d, stubResolver{}, 0)
	if err != nil {
		t.Fatalf("geoSyncAll err: %v", err)
	}
	if len(reps) != 2 {
		t.Fatalf("应输出 2 张表报告，got %d", len(reps))
	}
	if got := countScalar(t, d, "SELECT COUNT(*) FROM access_log WHERE country = 'US'"); got != 2 {
		t.Errorf("access_log 应回填 2 行（8.8.8.8 两笔），实际 %d", got)
	}
	if got := countScalar(t, d, "SELECT COUNT(*) FROM access_log WHERE client_ip = '127.0.0.1' AND country <> ''"); got != 0 {
		t.Errorf("私网 IP 应跳过保持空串，实际 %d 行被改动", got)
	}
	if got := countScalar(t, d, "SELECT COUNT(*) FROM shield_event WHERE country = 'US' AND city = ''"); got != 1 {
		t.Errorf("shield_event country 已有值的行不应被覆盖（city 空也不动）")
	}
	for _, r := range reps {
		switch r.Table {
		case "access_log":
			if r.IPs != 2 || r.RowsUpdated != 2 || r.Skipped != 1 {
				t.Errorf("access_log 报告 = %+v，期望 ips=2 rows=2 skipped=1", r)
			}
		case "shield_event":
			if r.IPs != 0 || r.RowsUpdated != 0 {
				t.Errorf("shield_event 应无缺失 IP（country 非空不选），报告 %+v", r)
			}
		}
	}
	// 续填幂等：再跑一次应 0 行回填
	reps2, err := geoSyncAll(context.Background(), d, stubResolver{}, 0)
	if err != nil {
		t.Fatalf("二次回填 err: %v", err)
	}
	for _, r := range reps2 {
		if r.RowsUpdated != 0 {
			t.Errorf("二次回填应 0 行（幂等），%s updated=%d", r.Table, r.RowsUpdated)
		}
	}
}

func TestGeoipSyncNotReady(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	stubReady = false
	defer func() { stubReady = true }()
	if _, err := geoSyncAll(context.Background(), d, stubResolver{}, 0); err == nil {
		t.Fatal("mmdb 未就绪应拒绝并报错")
	}
}

func TestGeoipSyncBatchChunks(t *testing.T) {
	resetGeoSyncState()
	// 批量 UPDATE 分批边界：把单语句 IP 容量压到 2，插入 5 个可解析 IP，验证跨批全量回填不丢行
	d := openTestDB(t)
	exec(t, d, mustDDL(t, d, "access_log_create_table.sql", "access_log"))
	// 索引脚本是多条语句，exec 不拆句，这里只建本优化涉及的 client_ip 索引
	exec(t, d, "CREATE INDEX idx_access_log_client_ip ON access_log(client_ip)")
	exec(t, d, mustDDL(t, d, "shield_event_create_table.sql", "shield_event"))
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	for i := 1; i <= 5; i++ {
		exec(t, d, fmt.Sprintf(
			`INSERT INTO access_log (time, trace_id, path, method, client_ip, status_code, user_agent, country, city, extra)
			VALUES ('%s','t%d','/a','GET','8.8.8.%d',200,'UA','','','{}')`, now, i, i))
	}
	oldBatch := geoSyncBatchStmt
	geoSyncBatchStmt = 2
	defer func() { geoSyncBatchStmt = oldBatch }()

	// 段内全解析桩：8.8.8.x 一律返回 US，便于构造 >1 批的可回填 IP
	rep, err := geoSyncTable(context.Background(), d, "access_log", stubFlexResolver{})
	if err != nil {
		t.Fatalf("geoSyncTable err: %v", err)
	}
	if rep.IPs != 5 || rep.RowsUpdated != 5 || rep.Skipped != 0 {
		t.Errorf("报告 = %+v，期望 ips=5 rows=5 skipped=0", rep)
	}
	if got := countScalar(t, d, "SELECT COUNT(*) FROM access_log WHERE country = 'US'"); got != 5 {
		t.Errorf("5 个 IP 跨 3 批应全部回填，实际 %d 行", got)
	}
}

// stubFlexResolver 段内全解析桩：8.8.8.x 一律返回 US，其他返回空。
type stubFlexResolver struct{}

func (stubFlexResolver) Ready() bool { return true }
func (stubFlexResolver) Lookup(ip string) geoip.GeoInfo {
	if strings.HasPrefix(ip, "8.8.8.") {
		return geoip.GeoInfo{Code: "US", City: "加利福尼亚州/山景城"}
	}
	return geoip.GeoInfo{}
}

// TestGeoipSyncCursorResume 断点续填：单趟只处理部分 IP（批次上限 + 扫描块大小共同约束），
// 未扫完时 Done=false 且游标前移，再次调用从断点继续，多趟累计完成全量回填。
func TestGeoipSyncCursorResume(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	exec(t, d, mustDDL(t, d, "access_log_create_table.sql", "access_log"))
	exec(t, d, "CREATE INDEX idx_access_log_client_ip ON access_log(client_ip)")
	exec(t, d, mustDDL(t, d, "shield_event_create_table.sql", "shield_event"))
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	for i := 1; i <= 4; i++ {
		exec(t, d, fmt.Sprintf(
			`INSERT INTO access_log (time, trace_id, path, method, client_ip, status_code, user_agent, country, city, extra)
			VALUES ('%s','t%d','/a','GET','8.8.8.%d',200,'UA','','','{}')`, now, i, i))
	}
	oldIPs, oldChunk, oldStmt := geoSyncBatchIPs, geoSyncScanChunk, geoSyncBatchStmt
	geoSyncBatchIPs, geoSyncScanChunk, geoSyncBatchStmt = 2, 1, 10
	defer func() { geoSyncBatchIPs, geoSyncScanChunk, geoSyncBatchStmt = oldIPs, oldChunk, oldStmt }()

	var total int64
	for pass := 1; pass <= 2; pass++ {
		rep, err := geoSyncTable(context.Background(), d, "access_log", stubFlexResolver{})
		if err != nil {
			t.Fatalf("第 %d 趟 err: %v", pass, err)
		}
		total += rep.RowsUpdated
		if rep.Done {
			t.Errorf("第 %d 趟不应判定扫完（还剩缺失行），报告 %+v", pass, rep)
		}
	}
	if total != 4 {
		t.Errorf("两趟累计应回填 4 行，实际 %d", total)
	}
	if got := countScalar(t, d, "SELECT COUNT(*) FROM access_log WHERE country = 'US'"); got != 4 {
		t.Errorf("断点续填后应 4 行全回填，实际 %d", got)
	}
	// 扫完一趟：Done=true 且游标归零，再次执行仍幂等 0 行
	rep3, err := geoSyncTable(context.Background(), d, "access_log", stubFlexResolver{})
	if err != nil {
		t.Fatalf("收尾趟 err: %v", err)
	}
	if !rep3.Done || rep3.RowsUpdated != 0 {
		t.Errorf("收尾趟应 Done=true 且 0 行，报告 %+v", rep3)
	}
}

// TestGeoipSyncBudgetStop 服务端硬超时：上下文取消（客户端断开/单趟到点）时立即收工，
// 返回已完成进度且标记 BudgetStop，并释放进行中互斥锁——不允许脱离调用方继续长时间读写。
func TestGeoipSyncBudgetStop(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	exec(t, d, mustDDL(t, d, "access_log_create_table.sql", "access_log"))
	exec(t, d, mustDDL(t, d, "shield_event_create_table.sql", "shield_event"))
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	exec(t, d, fmt.Sprintf(
		`INSERT INTO access_log (time, trace_id, path, method, client_ip, status_code, user_agent, country, city, extra)
		VALUES ('%s','t1','/a','GET','8.8.8.8',200,'UA','','','{}')`, now))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 模拟客户端已断开 / 单趟时间已到
	reps, err := geoSyncAll(ctx, d, stubFlexResolver{}, 0)
	if err != nil {
		t.Fatalf("取消后应正常返回已完成进度而非报错，err: %v", err)
	}
	for _, r := range reps {
		if !r.BudgetStop || r.Done || r.RowsUpdated != 0 {
			t.Errorf("%s 应标记提前收工（BudgetStop=true, done=false, 0 行），报告 %+v", r.Table, r)
		}
	}
	if geoSyncRunning.Load() {
		t.Error("收工后应释放进行中互斥锁")
	}
}

// TestGeoipSyncBusyGuard 并发互斥：上一趟进行中时拒绝再次触发，避免双趟重复扫描写库。
func TestGeoipSyncBusyGuard(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	exec(t, d, mustDDL(t, d, "access_log_create_table.sql", "access_log"))
	exec(t, d, mustDDL(t, d, "shield_event_create_table.sql", "shield_event"))
	geoSyncRunning.Store(true)
	defer geoSyncRunning.Store(false)
	if _, err := geoSyncAll(context.Background(), d, stubResolver{}, 0); err == nil {
		t.Fatal("上一趟进行中应拒绝并发触发")
	}
}

// TestBuildGeoBatchUpdate 批量 UPDATE 组装：占位符顺序必须与实参顺序严格一一对应。
// 回归背景：曾按「country CASE → city CASE → IN 列表」交错递增 $n，而实参是分组追加的，
// postgres 下批 ≥2 时第 2 个 IP 起编号错位（CASE 匹配不到 → 已有 city 被置 NULL）。
func TestBuildGeoBatchUpdate(t *testing.T) {
	batch := []geoVal{
		{ip: "1.1.1.1", country: "US", city: "加州/洛杉矶"},
		{ip: "2.2.2.2", country: "CN", city: "广东/深圳"},
	}
	wantArgs := []any{
		"1.1.1.1", "US", "2.2.2.2", "CN", // 全部 (ip,country) 对
		"1.1.1.1", "加州/洛杉矶", "2.2.2.2", "广东/深圳", // 全部 (ip,city) 对
		"1.1.1.1", "2.2.2.2", // IN 列表
	}
	cases := []struct {
		driver string
		want   string
	}{
		{"sqlite", "UPDATE access_log SET country = CASE client_ip WHEN ? THEN ? WHEN ? THEN ? END, " +
			"city = CASE client_ip WHEN ? THEN ? WHEN ? THEN ? END " +
			"WHERE client_ip IN (?,?) AND (country = '' OR country IS NULL)"},
		{"postgres", "UPDATE access_log SET country = CASE client_ip WHEN $1 THEN $2 WHEN $3 THEN $4 END, " +
			"city = CASE client_ip WHEN $5 THEN $6 WHEN $7 THEN $8 END " +
			"WHERE client_ip IN ($9,$10) AND (country = '' OR country IS NULL)"},
	}
	for _, c := range cases {
		got, args := buildGeoBatchUpdate(c.driver, "access_log", batch)
		if got != c.want {
			t.Errorf("%s SQL 不符：\n got %s\nwant %s", c.driver, got, c.want)
		}
		if len(args) != len(wantArgs) {
			t.Fatalf("%s 实参个数 = %d，期望 %d", c.driver, len(args), len(wantArgs))
		}
		for i := range wantArgs {
			if args[i] != wantArgs[i] {
				t.Errorf("%s 实参[%d] = %v，期望 %v", c.driver, i, args[i], wantArgs[i])
			}
		}
	}
}

// TestBuildGeoBatchUpdateSingle 单 IP 批（批边界下限）：占位符连续且与实参对齐。
func TestBuildGeoBatchUpdateSingle(t *testing.T) {
	got, args := buildGeoBatchUpdate("postgres", "shield_event",
		[]geoVal{{ip: "8.8.8.8", country: "US", city: "加州/山景城"}})
	want := "UPDATE shield_event SET country = CASE client_ip WHEN $1 THEN $2 END, " +
		"city = CASE client_ip WHEN $3 THEN $4 END " +
		"WHERE client_ip IN ($5) AND (country = '' OR country IS NULL)"
	if got != want {
		t.Errorf("SQL 不符：\n got %s\nwant %s", got, want)
	}
	if len(args) != 5 {
		t.Errorf("实参个数 = %d，期望 5", len(args))
	}
}

// TestGeoSyncNextCursor 游标推进语义：回填被中断时保持原游标（下趟重扫同一区间，不跳过未回填行）；
// 只有发现到表尾且回填完成才归零并标记 Done。
func TestGeoSyncNextCursor(t *testing.T) {
	cases := []struct {
		name                   string
		prev, maxID            int64
		exhausted, interrupted bool
		wantCursor             int64
		wantDone               bool
	}{
		{"中断保持原游标", 1234, 9999, false, true, 1234, false},
		{"中断时即使发现到表尾也不归零", 1234, 9999, true, true, 1234, false},
		{"未中断未扫完推进到已扫描最大 id", 1234, 9999, false, false, 9999, false},
		{"扫到表尾且回填完成归零", 1234, 9999, true, false, 0, true},
	}
	for _, c := range cases {
		gotCursor, gotDone := geoSyncNextCursor(c.prev, c.maxID, c.exhausted, c.interrupted)
		if gotCursor != c.wantCursor || gotDone != c.wantDone {
			t.Errorf("%s：cursor=%d done=%v，期望 cursor=%d done=%v",
				c.name, gotCursor, gotDone, c.wantCursor, c.wantDone)
		}
	}
}
