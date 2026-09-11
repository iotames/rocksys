// geoip_sync.go：GeoIP 历史数据回填（TRAFFIC_ANALYSIS 后续增量，2026-09-09 用户提出）。
//
// 背景：country/city 列为**写时解析**入库，上线前的历史行这两列为空串，统计时只能计「未知」。
// 本功能对 access_log / shield_event 两表中「有 IP 但 geo 缺失」的行做批量回填。
//
// 性能与超时（2026-09-11 优化）：
//   - 发现阶段不走 DISTINCT 全表扫描（百万行表实测分钟级，必超时），改为按 id 主键游标**分块增量
//     扫描**——每块 geoSyncScanChunk 行、单趟最多 geoSyncScanCap 行；游标记录在进程内
//     （表 → 已扫描到的最大 id），再次点击从断点续扫，不重扫已处理区间。游标**只在回填写全部
//     完成后才推进**：本趟被时间预算/错误中断时保持原值，下趟重扫同一区间（已回填行因 country
//     非空被发现阶段过滤，重扫代价可忽略），避免「已发现未回填」的行被游标跳过。
//   - 回填写侧沿用 CASE WHEN 批量 UPDATE（每条语句独立提交）：中途停下时已执行的批次依然生效，
//     反复点击直至扫完即全量回填（断点续填 + 部分进度保留）。
//   - ★服务端硬超时：整趟绑定「请求上下文 + 单趟时间预算」（固定常量 geoSyncBudget=20 秒，非配置项），
//     在每个扫描块/更新批边界检查，到点或客户端断开立即收工返回已完成进度——绝不出现
//     「客户端早已超时、服务端仍在长时间读写占锁」的情况（互斥锁随本趟退出立即释放）。
//     私网/回环/解析不出的 IP 跳过并计数（永远无 geo，游标越过不再重试）。
package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rocksys/internal/db"
	"rocksys/internal/geoip"
)

// geoLookup GeoIP 解析最小接口（*geoip.Resolver 天然满足；测试可注入桩）。
type geoLookup interface {
	Lookup(ip string) geoip.GeoInfo
	Ready() bool
}

// geoSyncReport 单表回填报告。
type geoSyncReport struct {
	Table       string   `json:"table"`
	IPs         int      `json:"ips"`            // 缺失 geo 的去重 IP 数（本趟处理）
	RowsUpdated int64    `json:"rows_updated"`   // 回填行数
	Skipped     int      `json:"skipped"`        // 解析不出地理位置的 IP 数（私网/回环/库外地址）
	IPSample    []string `json:"ip_sample"`      // 跳过的 IP 样本（最多 5 个，供排查）
	Scanned     int      `json:"scanned"`        // 本趟发现阶段实际扫描的缺失行数
	Done        bool     `json:"done"`           // 本表缺失区间是否已扫完（false=可再次点击续填）
	BudgetStop  bool     `json:"budget_stopped"` // 本趟是否因时间预算/客户端断开而提前收工
}

// 回填节奏参数（批次化：单趟有界、可断点续填，超时/失败已提交批次依然生效）。
var (
	geoipSyncTables = []string{db.TableAccessLog, db.TableShieldEvent}
	geoSyncBatchIPs = 5000 // 单趟处理的缺失 IP 上限：防超大表首趟过久；未完可再次执行续填

	geoSyncBatchStmt = 200    // 每条批量 UPDATE 携带的 IP 数：CASE WHEN 展开参数随 IP 数线性增长，分批兼顾效率与语句长度
	geoSyncScanChunk = 10000  // 发现阶段每块扫描的缺失行数（走 id 主键范围扫，单块毫秒级）
	geoSyncScanCap   = 200000 // 单趟发现阶段扫描缺失行数上限：即使缺失行极稀疏也保证单趟耗时可控
)

// geoSyncBudget 单趟时间预算（固定 20 秒，不做配置项）：回填是大表批量读写，必须给服务端硬上限，
// 到点即在块/批边界收工返回已完成进度；属实现细节而非用户可调策略，写死以免增加配置心智负担。
const geoSyncBudget = 20 * time.Second

// 回填运行态：id 游标（断点续填，进程内记录即可）与进行中互斥锁（防并发双趟重复扫描写库）。
var (
	geoSyncMu      sync.Mutex
	geoSyncCursors = map[string]int64{}
	geoSyncRunning atomic.Bool
)

// geoSyncCursorLoad/Store 游标读写（0 表示从头扫）。
func geoSyncCursorLoad(table string) int64 {
	geoSyncMu.Lock()
	defer geoSyncMu.Unlock()
	return geoSyncCursors[table]
}

func geoSyncCursorStore(table string, id int64) {
	geoSyncMu.Lock()
	defer geoSyncMu.Unlock()
	geoSyncCursors[table] = id
}

// geoVal 单个 IP 的解析结果（批量 UPDATE 的输入单元）。
type geoVal struct{ ip, country, city string }

// buildGeoBatchUpdate 组装单批回填 SQL 与实参（纯函数，便于单测占位符与实参一一对应）。
// 实参顺序固定为「全部 (ip, country) 对 → 全部 (ip, city) 对 → 全部 ip（IN 列表）」，
// 占位符按同一顺序生成（postgres 的 $n 递增编号、其余方言的 ? 文本位置），
// 保证两个 CASE 分支在三方言下都取到正确实参（曾因 placeholders 交错编号导致 PG 批 ≥2 时错位）。
func buildGeoBatchUpdate(driver, table string, batch []geoVal) (string, []any) {
	pg := driver == "postgres"
	pi := 1
	ph := func() string {
		if pg {
			p := fmt.Sprintf("$%d", pi)
			pi++
			return p
		}
		return "?"
	}
	var cCase, yCase, inList strings.Builder
	args := make([]any, 0, len(batch)*5)
	for i := range batch {
		if i > 0 {
			cCase.WriteString(" ")
		}
		cCase.WriteString("WHEN " + ph() + " THEN " + ph())
		args = append(args, batch[i].ip, batch[i].country)
	}
	for i := range batch {
		if i > 0 {
			yCase.WriteString(" ")
		}
		yCase.WriteString("WHEN " + ph() + " THEN " + ph())
		args = append(args, batch[i].ip, batch[i].city)
	}
	for i := range batch {
		if i > 0 {
			inList.WriteString(",")
		}
		inList.WriteString(ph())
		args = append(args, batch[i].ip)
	}
	u := fmt.Sprintf(
		"UPDATE %s SET country = CASE client_ip %s END, city = CASE client_ip %s END "+
			"WHERE client_ip IN (%s) AND (country = '' OR country IS NULL)",
		table, cCase.String(), yCase.String(), inList.String())
	return u, args
}

// geoSyncNextCursor 计算一趟结束后的游标与 Done 标记（纯函数，便于单测中断语义）：
//   - 回填被中断（时间预算/客户端断开/批失败）→ 保持原游标、Done=false，下趟重扫同一区间；
//   - 发现阶段已到表尾且回填完成 → 游标归零、Done=true（后续新增缺失行可被下趟再发现）；
//   - 其余（批次上限/扫描上限先到）→ 游标推进到已扫描最大 id。
func geoSyncNextCursor(prevCursor, maxID int64, exhausted, interrupted bool) (int64, bool) {
	if interrupted {
		return prevCursor, false
	}
	if exhausted {
		return 0, true
	}
	return maxID, false
}

// geoSyncTable 对单表执行一轮回填：id 游标分块增量发现缺失 IP（单趟上限 geoSyncBatchIPs 个 IP /
// geoSyncScanCap 行）→ 解析 → 按 IP 分组批量 UPDATE（CASE client_ip WHEN 语法三方言统一，配合
// client_ip 索引点查；每条语句独立提交，中途停下已提交批次依然生效）。
// ctx 在块/批边界检查：到点或客户端断开即收工，并在报告中标记 BudgetStop。
func geoSyncTable(ctx context.Context, d *db.DB, table string, res geoLookup) (geoSyncReport, error) {
	rep := geoSyncReport{Table: table, IPSample: []string{}}
	sqlDB := d.EasyDB().GetSqlDB()
	prevCursor := geoSyncCursorLoad(table)
	cursor := prevCursor
	uniq := map[string]bool{}
	var maxID int64
	exhausted := false
	for len(uniq) < geoSyncBatchIPs && rep.Scanned < geoSyncScanCap && !exhausted {
		if ctx.Err() != nil {
			rep.BudgetStop = true
			break
		}
		q := fmt.Sprintf(
			"SELECT id, client_ip FROM %s WHERE id > %d AND (country = '' OR country IS NULL) AND client_ip <> '' "+
				"ORDER BY id LIMIT %d", table, cursor, geoSyncScanChunk)
		rows, err := sqlDB.QueryContext(ctx, q)
		if err != nil {
			if ctx.Err() != nil { // 超时/断开导致的取消不算错误，按提前收工处理
				rep.BudgetStop = true
				break
			}
			return rep, fmt.Errorf("geoip: 查询缺失 IP 失败: %w", err)
		}
		n := 0
		for rows.Next() {
			var id int64
			var ip string
			if err := rows.Scan(&id, &ip); err != nil {
				rows.Close()
				return rep, fmt.Errorf("geoip: 扫描缺失 IP 失败: %w", err)
			}
			n++
			if id > maxID {
				maxID = id
			}
			if !uniq[ip] {
				uniq[ip] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			if ctx.Err() != nil {
				rep.BudgetStop = true
				break
			}
			return rep, fmt.Errorf("geoip: 扫描缺失 IP 失败: %w", err)
		}
		if n == 0 {
			exhausted = true // 到表尾：本趟扫完，游标归零（后续新增缺失行可被下一趟发现）
			break
		}
		rep.Scanned += n
		cursor = maxID
	}
	rep.IPs = len(uniq)

	var vals []geoVal
	for ip := range uniq {
		gi := res.Lookup(ip)
		country, city := gi.Code, gi.City
		if country == "" && city == "" {
			rep.Skipped++
			if len(rep.IPSample) < 5 {
				rep.IPSample = append(rep.IPSample, ip)
			}
			continue
		}
		vals = append(vals, geoVal{ip, country, city})
	}
	// 分批组装批量 UPDATE（country/city 各一个 CASE client_ip WHEN ? THEN ?，三方言语法一致）。
	batchInterrupted := false
	for start := 0; start < len(vals); start += geoSyncBatchStmt {
		if ctx.Err() != nil { // 批边界检查：到点/断开即收工，已完成批次不回滚
			rep.BudgetStop = true
			batchInterrupted = true
			break
		}
		end := min(start+geoSyncBatchStmt, len(vals))
		u, args := buildGeoBatchUpdate(d.Driver(), table, vals[start:end])
		res2, err := sqlDB.ExecContext(ctx, u, args...)
		if err != nil {
			if ctx.Err() != nil { // 取消导致的失败同样按提前收工处理（此前批次已提交）
				rep.BudgetStop = true
				batchInterrupted = true
				break
			}
			return rep, fmt.Errorf("geoip: 批量回填 %s 失败: %w", table, err)
		}
		if n, err := res2.RowsAffected(); err == nil {
			rep.RowsUpdated += n
		}
	}
	// 游标只在回填未被中断时推进：中断即保持原值，下趟从同一 id 区间重扫。
	// 已回填行 country 非空会被发现阶段的 WHERE 过滤，重扫代价可忽略；若提前推进，
	// 本趟「已发现但未回填」的行会被永久跳过（要等游标绕回表尾才可能再被发现）。
	next, done := geoSyncNextCursor(prevCursor, maxID, exhausted, batchInterrupted)
	geoSyncCursorStore(table, next)
	rep.Done = done
	return rep, nil
}

// geoSyncAll 对全部参与表执行回填。geo 未就绪直接报错（无 mmdb 时回填无从谈起）；
// 上一趟仍在进行时拒绝并发。ctx 为 HTTP 请求上下文（客户端断开会取消本趟），
// budget 为单趟时间预算（≤0 表示不限，仅受 ctx 约束）——两者共同保证服务端不会脱离
// 调用方监督长时间空转：到点/断开即在块边界收工，已提交批次保留，互斥锁立即释放。
func geoSyncAll(ctx context.Context, d *db.DB, res geoLookup, budget time.Duration) ([]geoSyncReport, error) {
	if !res.Ready() {
		return nil, fmt.Errorf("geoip: mmdb 未加载，无法回填；请先放置 GeoLite2 mmdb 并重启服务")
	}
	if !geoSyncRunning.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("geoip: 上一轮回填仍在进行中，请稍候再点（已提交的批次不受影响）")
	}
	defer geoSyncRunning.Store(false)
	if budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	var out []geoSyncReport
	for _, table := range geoipSyncTables {
		rep, err := geoSyncTable(ctx, d, table, res)
		if err != nil {
			return out, err
		}
		out = append(out, rep)
	}
	return out, nil
}

// geoSyncReportText 报告转人读文案（日志/前端展示共用）。
func geoSyncReportText(reps []geoSyncReport) string {
	var b strings.Builder
	for _, r := range reps {
		fmt.Fprintf(&b, "%s：回填 %d 行（处理 %d 个 IP，跳过 %d 个无地理信息 IP）", r.Table, r.RowsUpdated, r.IPs, r.Skipped)
		switch {
		case r.BudgetStop:
			b.WriteString("；本轮已达单趟时间上限，已完成部分已写入，再次点击从断点继续")
		case !r.Done:
			b.WriteString("；仍有缺失行未扫完，可再次点击继续")
		}
		b.WriteString("；")
	}
	return strings.TrimSuffix(b.String(), "；")
}
