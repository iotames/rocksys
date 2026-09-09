// geoip_sync_test.go：GeoIP 历史回填单测（桩 Resolver，sqlite 临时库，不依赖真实 mmdb）。
// 覆盖：缺失行回填（country/city 落值）、已有值行不动、私网 IP 跳过、geo 未就绪拒绝、续填幂等。
package main

import (
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

func TestGeoipSyncBackfill(t *testing.T) {
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

	reps, err := geoSyncAll(d, stubResolver{})
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
	reps2, err := geoSyncAll(d, stubResolver{})
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
	d := openTestDB(t)
	stubReady = false
	defer func() { stubReady = true }()
	if _, err := geoSyncAll(d, stubResolver{}); err == nil {
		t.Fatal("mmdb 未就绪应拒绝并报错")
	}
}

func TestGeoipSyncBatchChunks(t *testing.T) {
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
	rep, err := geoSyncTable(d, "access_log", stubFlexResolver{})
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
