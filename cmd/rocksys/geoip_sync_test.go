// geoip_sync_test.go：GeoIP 关联表增量同步单测（桩 Resolver，sqlite 临时库，不依赖真实 mmdb）。
// 覆盖：缺失 IP 构建 geoip_list（geo 落值）、已入表 IP 不重扫（幂等）、私网 IP 跳过、
// geo 未就绪拒绝、断点续扫、超时收工、并发互斥、间隔归一。
package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"rocksys/internal/db"
	"rocksys/internal/geoip"
)

// stubResolver 桩解析器：公网 IP 返回固定地理，其他返回空（模拟私网/库外地址）。
// Ready（自动同步门控=开关+库）与 SyncReady（手动同步门控=仅库）独立可调，
// 覆盖「GEOIP_ENABLED=false 但 mmdb 就绪」的开关/就绪组合。
type stubResolver struct{}

func (stubResolver) Ready() bool { return stubReady }

func (stubResolver) SyncReady() bool { return stubSyncReady }

func (stubResolver) LookupSync(ip string) geoip.GeoInfo {
	if ip == "8.8.8.8" {
		return geoip.GeoInfo{Code: "US", Country: "美国", Province: "加利福尼亚州", City: "山景城"}
	}
	if ip == "1.2.3.4" {
		return geoip.GeoInfo{Code: "US"}
	}
	return geoip.GeoInfo{}
}

var (
	stubReady     = true // 自动同步门控（GEOIP_ENABLED && mmdb）
	stubSyncReady = true // 手动同步门控（仅 mmdb）
)

// mustDDL 读建表脚本并替换占位符后返回（{table}/{geo} 均替换）。
func mustDDL(t *testing.T, d *db.DB, script string, tables ...string) string {
	t.Helper()
	ddl, err := d.SQL(script)
	if err != nil {
		t.Fatalf("读建表脚本 err: %v", err)
	}
	marks := []string{"{table}", "{table2}", "{geo}"}
	for i, m := range marks {
		if i < len(tables) {
			ddl = strings.ReplaceAll(ddl, m, tables[i])
		}
	}
	return ddl
}

// ensureGeoipList 幂等创建 geoip_list 表（测试库无 schema 同步，须显式建表）。
func ensureGeoipList(t *testing.T, d *db.DB) {
	t.Helper()
	exec(t, d, mustDDL(t, d, "geoip_list_create_table.sql", db.TableGeoipList))
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

// geoRowCount 统计 geoip_list 满足条件的行数。
func geoRowCount(t *testing.T, d *db.DB, where string) int {
	t.Helper()
	return countScalar(t, d, "SELECT COUNT(*) FROM "+db.TableGeoipList+" WHERE "+where)
}

// resetGeoSyncState 重置同步全局运行态（游标/进行中标记/回写钩子），避免用例间相互串扰。
func resetGeoSyncState() {
	geoSyncMu.Lock()
	geoSyncCursors = map[string]int64{}
	geoSyncMu.Unlock()
	geoSyncRunning.Store(false)
	geoSyncOnDone = nil
}

// insAccess 插入一条访问日志（新结构：无 country/city 列）。
func insAccess(t *testing.T, d *db.DB, now, ip string) {
	t.Helper()
	exec(t, d, fmt.Sprintf(
		`INSERT INTO access_log (time, trace_id, path, method, client_ip, status_code, user_agent, extra)
		VALUES ('%s','t1','/a','GET','%s',200,'UA','{}')`, now, ip))
}

func TestGeoipSyncBuild(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	// 全量新表结构（同步发生在结构同步之后）
	exec(t, d, mustDDL(t, d, "access_log_create_table.sql", "access_log"))
	exec(t, d, mustDDL(t, d, "shield_event_create_table.sql", "shield_event"))
	ensureGeoipList(t, d)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	// access_log：8.8.8.8×2（缺失→应入表）、127.0.0.1（私网→跳过）
	insAccess(t, d, now, "8.8.8.8")
	insAccess(t, d, now, "8.8.8.8")
	insAccess(t, d, now, "127.0.0.1")
	// shield_event：1.2.3.4（只有国码，province/city 空——数据不造假，该空则空）
	exec(t, d, fmt.Sprintf(
		`INSERT INTO shield_event (time, trace_id, block_type, client_ip, method, path, user_agent, host, status_code, rule_hit, req_bytes, extra)
		VALUES ('%s','s1',7,'1.2.3.4','GET','/x','UA','h',403,'test',0,'{}')`, now))

	reps, err := geoSyncAll(context.Background(), d, stubResolver{}, nil)
	if err != nil {
		t.Fatalf("geoSyncAll err: %v", err)
	}
	if len(reps) != 2 {
		t.Fatalf("应输出 2 张表报告，got %d", len(reps))
	}
	// geoip_list：8.8.8.8 全字段、1.2.3.4 仅国码、私网不入表
	if got := geoRowCount(t, d, "ip='8.8.8.8' AND country_code='US' AND country_name='美国' AND province='加利福尼亚州' AND city='山景城'"); got != 1 {
		t.Errorf("8.8.8.8 应以完整 geo 入表一行，实际匹配 %d 行", got)
	}
	if got := geoRowCount(t, d, "ip='1.2.3.4' AND country_code='US' AND province='' AND city=''"); got != 1 {
		t.Errorf("1.2.3.4 应仅国码入表（该空则空），实际匹配 %d 行", got)
	}
	if got := geoRowCount(t, d, "ip='127.0.0.1'"); got != 0 {
		t.Errorf("私网 IP 不应入表，实际 %d 行", got)
	}
	if got := geoRowCount(t, d, "1=1"); got != 2 {
		t.Errorf("geoip_list 应共 2 行，实际 %d 行", got)
	}
	for _, r := range reps {
		if r.Table != "access_log" {
			continue
		}
		// ips=2（8.8.8.8 + 127.0.0.1），其中私网 1 个跳过、1 个入表
		if r.IPs != 2 || r.Skipped != 1 || r.RowsUpsert != 1 {
			t.Errorf("access_log 报告 = %+v，期望 ips=2 skipped=1 upsert=1", r)
		}
	}
	// 幂等：再跑一次应 0 行新增（127.0.0.1 永远无 geo，每趟被重发现但跳过，不计入 upsert）
	reps2, err := geoSyncAll(context.Background(), d, stubResolver{}, nil)
	if err != nil {
		t.Fatalf("二次同步 err: %v", err)
	}
	for _, r := range reps2 {
		if r.RowsUpsert != 0 {
			t.Errorf("二次同步应 0 行新增（幂等），%s ips=%d upsert=%d", r.Table, r.IPs, r.RowsUpsert)
		}
	}
}

func TestGeoipSyncNotReady(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	stubSyncReady = false
	defer func() { stubSyncReady = true }()
	if _, err := geoSyncAll(context.Background(), d, stubResolver{}, nil); err == nil {
		t.Fatal("mmdb 未加载应拒绝并报错")
	}
}

// TestGeoipSyncDisabledButSyncable 手动同步不受 GEOIP_ENABLED 限制：功能关闭（Ready=false，
// 自动同步停摆）但 mmdb 已加载（SyncReady=true）时，手动同步照常执行并正常落库。
func TestGeoipSyncDisabledButSyncable(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	ensureGeoipList(t, d)
	exec(t, d, mustDDL(t, d, "access_log_create_table.sql", "access_log"))
	exec(t, d, mustDDL(t, d, "shield_event_create_table.sql", "shield_event"))
	exec(t, d, fmt.Sprintf(
		`INSERT INTO access_log (time, trace_id, path, method, client_ip, status_code, user_agent, extra)
		VALUES ('%s','t1','/a','GET','8.8.8.8',200,'UA','{}')`, time.Now().UTC().Format(time.RFC3339)))
	stubReady = false // 功能关闭
	defer func() { stubReady = true }()
	reps, err := geoSyncAll(context.Background(), d, stubResolver{}, nil)
	if err != nil {
		t.Fatalf("开关关闭但 mmdb 就绪时手动同步不应被拒绝，err: %v", err)
	}
	for _, r := range reps {
		if r.Table == db.TableAccessLog && r.RowsUpsert != 1 {
			t.Errorf("开关关闭时手动同步应正常落库，报告 = %+v", r)
		}
	}
}

// stubFlexResolver 段内全解析桩：8.8.8.x 一律返回 US，其他返回空。
type stubFlexResolver struct{}

func (stubFlexResolver) Ready() bool     { return true }
func (stubFlexResolver) SyncReady() bool { return true }
func (stubFlexResolver) LookupSync(ip string) geoip.GeoInfo {
	if strings.HasPrefix(ip, "8.8.8.") {
		return geoip.GeoInfo{Code: "US", Country: "美国", Province: "加利福尼亚州", City: "山景城"}
	}
	return geoip.GeoInfo{}
}

// TestGeoipSyncUpsertConflict upsert 冲突语义：冲突更新仅刷新 geo 列与 updated_at，created_at
// 保持首次入库值。（增量求差下已入表 IP 不会被重发现，冲突路径仅防御同 IP 并发来源；直接单测脚本语义。）
func TestGeoipSyncUpsertConflict(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	ensureGeoipList(t, d)
	old := []geoVal{{ip: "8.8.8.7", code: "US", country: "美国", province: "加利福尼亚州", city: "山景城"}}
	if _, err := geoSyncUpsert(context.Background(), d, old); err != nil {
		t.Fatalf("首次 upsert err: %v", err)
	}
	exec(t, d, `UPDATE geoip_list SET created_at='2000-01-01 00:00:00' WHERE ip='8.8.8.7'`)
	newer := []geoVal{{ip: "8.8.8.7", code: "JP", country: "日本", province: "东京都", city: "东京"}}
	if _, err := geoSyncUpsert(context.Background(), d, newer); err != nil {
		t.Fatalf("冲突 upsert err: %v", err)
	}
	if got := countScalar(t, d, `SELECT COUNT(*) FROM geoip_list WHERE ip='8.8.8.7' AND country_code='JP' AND created_at='2000-01-01 00:00:00'`); got != 1 {
		t.Errorf("冲突更新应刷新 geo 列且保持 created_at，实际匹配 %d 行", got)
	}
}

// TestGeoipSyncCursorResume 断点续扫：单趟只处理部分 IP（批次上限 + 扫描块大小共同约束），
// 未扫完时 Done=false 且游标前移，再次调用从断点继续，多趟累计完成全量构建。
func TestGeoipSyncCursorResume(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	exec(t, d, mustDDL(t, d, "access_log_create_table.sql", "access_log"))
	exec(t, d, "CREATE INDEX idx_access_log_client_ip ON access_log(client_ip)")
	ensureGeoipList(t, d)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	for i := 1; i <= 4; i++ {
		insAccess(t, d, now, fmt.Sprintf("8.8.8.%d", i))
	}
	oldIPs, oldChunk := geoSyncIPsPerRun, geoSyncScanChunk
	geoSyncIPsPerRun, geoSyncScanChunk = 2, 1
	defer func() { geoSyncIPsPerRun, geoSyncScanChunk = oldIPs, oldChunk }()

	for pass := 1; pass <= 2; pass++ {
		rep, err := geoSyncTable(context.Background(), d, "access_log", stubFlexResolver{}, nil)
		if err != nil {
			t.Fatalf("第 %d 趟 err: %v", pass, err)
		}
		if rep.Done {
			t.Errorf("第 %d 趟不应判定扫完（还剩缺失 IP），报告 %+v", pass, rep)
		}
	}
	if got := geoRowCount(t, d, "country_code='US'"); got != 4 {
		t.Errorf("断点续扫后应 4 个 IP 全入表，实际 %d", got)
	}
	// 扫完一趟：Done=true 且游标归零，再次执行仍幂等 0 行
	rep3, err := geoSyncTable(context.Background(), d, "access_log", stubFlexResolver{}, nil)
	if err != nil {
		t.Fatalf("收尾趟 err: %v", err)
	}
	if !rep3.Done || rep3.RowsUpsert != 0 {
		t.Errorf("收尾趟应 Done=true 且 0 行，报告 %+v", rep3)
	}
}

// TestGeoipSyncInterruptOnCancel 调用方取消（人工经任务中心取消/进程收尾）时立即在块边界收工，
// 返回已完成进度且标记 Interrupted，并释放进行中互斥锁。无时间预算（后台任务默认无超时），
// 停止只有取消一条路。
func TestGeoipSyncInterruptOnCancel(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	exec(t, d, mustDDL(t, d, "access_log_create_table.sql", "access_log"))
	exec(t, d, mustDDL(t, d, "shield_event_create_table.sql", "shield_event"))
	ensureGeoipList(t, d)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	insAccess(t, d, now, "8.8.8.8")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 模拟调用方已断开 / 单趟时间已到
	reps, err := geoSyncAll(ctx, d, stubFlexResolver{}, nil)
	if err != nil {
		t.Fatalf("取消后应正常返回已完成进度而非报错，err: %v", err)
	}
	for _, r := range reps {
		if !r.Interrupted || r.Done || r.RowsUpsert != 0 {
			t.Errorf("%s 应标记提前收工（Interrupted=true, done=false, 0 行），报告 %+v", r.Table, r)
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
	ensureGeoipList(t, d)
	geoSyncRunning.Store(true)
	defer geoSyncRunning.Store(false)
	if _, err := geoSyncAll(context.Background(), d, stubResolver{}, nil); err == nil {
		t.Fatal("上一趟进行中应拒绝并发触发")
	}
}

// TestGeoSyncOutcome 取消与正常完成的登记状态必须可分辨（无时间预算，不存在到点分支）：
//   - 取消（Cancel）：人工终止（本轮未跑完）→ partial；
//   - 真错误 → failed。
//
// 三者混记会让定时任务页把「被取消」显示成「成功」，用户误以为数据已补齐。
func TestGeoSyncOutcome(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus string
		wantStop   bool
	}{
		{"无错误", nil, "success", false},
		{"任务被取消", context.Canceled, "partial", true},
		{"包装后的取消", fmt.Errorf("geoip: upsert 失败: %w", context.Canceled), "partial", true},
		{"真错误", fmt.Errorf("geoip: 扫描缺失 IP 失败: disk I/O error"), "failed", false},
	}
	for _, c := range cases {
		status, stop := geoipSyncOutcome(c.err)
		if status != c.wantStatus || stop != c.wantStop {
			t.Errorf("%s：geoipSyncOutcome(%v) = (%s, %v)，期望 (%s, %v)",
				c.name, c.err, status, stop, c.wantStatus, c.wantStop)
		}
	}
}

// TestGeoSyncOnDone 状态回写钩子：成功与失败路径都应以单点收口方式回调（nil 不回写）。
func TestGeoSyncOnDone(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	exec(t, d, mustDDL(t, d, "access_log_create_table.sql", "access_log"))
	exec(t, d, mustDDL(t, d, "shield_event_create_table.sql", "shield_event"))
	ensureGeoipList(t, d)

	var status, msg string
	called := false
	geoSyncOnDone = func(s, m string) { called, status, msg = true, s, m }
	if _, err := geoSyncAll(context.Background(), d, stubResolver{}, nil); err != nil {
		t.Fatalf("geoSyncAll err: %v", err)
	}
	if !called || status != "success" || msg == "" {
		t.Errorf("成功路径应回写 success 非空摘要，got called=%v status=%q msg=%q", called, status, msg)
	}

	resetGeoSyncState()
	called = false
	stubSyncReady = false
	geoSyncOnDone = func(s, m string) { called, status, msg = true, s, m }
	if _, err := geoSyncAll(context.Background(), d, stubResolver{}, nil); err == nil {
		t.Fatal("mmdb 未加载应报错")
	}
	if called {
		t.Error("未就绪拒绝（未进入同步）不应触发回写钩子")
	}
	stubSyncReady = true
}

// TestNormalizeGeoSyncInterval 间隔归一（D34）：0=关闭；<10（非 0）回落 60；合法值原样保留。
func TestNormalizeGeoSyncInterval(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 0}, {5, 60}, {9, 60}, {-3, 60}, {10, 10}, {60, 60}, {1440, 1440},
	}
	for _, c := range cases {
		if got := normalizeGeoSyncInterval(c.in); got != c.want {
			t.Errorf("normalize(%d) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

// TestGeoSyncNextCursor 游标推进语义：同步被中断时保持原游标（下趟重扫同一区间，不跳过未回写行）；
// 只有发现到表尾且回写完成才归零并标记 Done。
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
		{"扫到表尾且回写完成归零", 1234, 9999, true, false, 0, true},
	}
	for _, c := range cases {
		gotCursor, gotDone := geoSyncNextCursor(c.prev, c.maxID, c.exhausted, c.interrupted)
		if gotCursor != c.wantCursor || gotDone != c.wantDone {
			t.Errorf("%s：cursor=%d done=%v，期望 cursor=%d done=%v",
				c.name, gotCursor, gotDone, c.wantCursor, c.wantDone)
		}
	}
}

// TestNormalizeGeoSyncQuota 配额归一：0=不限；负数回落默认；合法值原样保留。
func TestNormalizeGeoSyncQuota(t *testing.T) {
	cases := []struct{ in, def, want int }{
		{0, 50000, math.MaxInt}, // 0=不限
		{-1, 50000, 50000},      // 负数回落默认
		{1, 50000, 1},
		{50000, 50000, 50000},
	}
	for _, c := range cases {
		if got := normalizeGeoSyncQuota(c.in, c.def); got != c.want {
			t.Errorf("normalizeQuota(%d, %d) = %d，期望 %d", c.in, c.def, got, c.want)
		}
	}
}

// TestGeoipSyncUnlimitedQuotaDrains 0=不限：一趟扫到表尾为止，Done=true 且游标归零
// （流式逐块回写，最终全部入库）。
func TestGeoipSyncUnlimitedQuotaDrains(t *testing.T) {
	resetGeoSyncState()
	d := openTestDB(t)
	exec(t, d, mustDDL(t, d, "access_log_create_table.sql", "access_log"))
	ensureGeoipList(t, d)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	for i := 1; i <= 3; i++ {
		insAccess(t, d, now, fmt.Sprintf("8.8.8.%d", i))
	}
	oldIPs, oldScan, oldChunk := geoSyncIPsPerRun, geoSyncScanCapRows, geoSyncScanChunk
	geoSyncIPsPerRun, geoSyncScanCapRows, geoSyncScanChunk = 0, 0, 1 // 双 0 = 全不限
	defer func() { geoSyncIPsPerRun, geoSyncScanCapRows, geoSyncScanChunk = oldIPs, oldScan, oldChunk }()

	rep, err := geoSyncTable(context.Background(), d, "access_log", stubFlexResolver{}, nil)
	if err != nil {
		t.Fatalf("不限配额趟 err: %v", err)
	}
	if !rep.Done || rep.RowsUpsert != 3 {
		t.Errorf("不限配额应一趟扫完（Done=true, 3 行），报告 %+v", rep)
	}
	if got := geoSyncCursorLoad("access_log"); got != 0 {
		t.Errorf("扫完游标应归零，实际 %d", got)
	}
	if got := geoRowCount(t, d, "country_code='US'"); got != 3 {
		t.Errorf("应 3 个 IP 全入表，实际 %d", got)
	}
}

// TestGeoSyncReportTextQuotaCopy 配额截断文案必须写明「自动续扫」，不得再出现
// 「可再次执行继续」这类误导人工干预的旧表述（实测踩坑：用户误以为同步失败）。
func TestGeoSyncReportTextQuotaCopy(t *testing.T) {
	reps := []geoSyncReport{
		{Table: "access_log", RowsUpsert: 5, Scanned: 10, Skipped: 0, Done: false},
		{Table: "shield_event", RowsUpsert: 0, Scanned: 3, Skipped: 1, Done: true},
	}
	text := geoSyncReportText(reps)
	if !strings.Contains(text, "下轮同步自动从断点续扫") {
		t.Errorf("配额截断应写明自动续扫，实际：%s", text)
	}
	if strings.Contains(text, "可再次执行继续") || strings.Contains(text, "仍有未同步行") {
		t.Errorf("不应残留误导性旧文案，实际：%s", text)
	}
	if strings.Contains(text, "本轮被取消") {
		t.Errorf("未取消的趟不应带取消文案，实际：%s", text)
	}
}
