// geoip_sync.go：GeoIP 关联表增量同步（geoip_list 构建/刷新，GEOIP_LIST_PLAN §4.5）。
//
// 背景：地理信息不再逐行存于 access_log / shield_event（原 country/city 两列已删），
// 改由一 IP 一行的 geoip_list 关联表承载；本同步器扫两表 client_ip 与 geoip_list 求差，
// 逐 IP 经 mmdb Lookup 后 upsert geoip_list（手动 POST /admin/db/geoip_sync + 定时自动共用本入口）。
//
// 性能与超时（沿用已验证的增量骨架）：
//   - 发现阶段不走 DISTINCT 全表扫描（百万行表实测分钟级，必超时），改为按 id 主键游标**分块增量
//     扫描**——每块 geoSyncScanChunk 行、单趟最多 geoSyncScanCap 行；块内对 geoip_list 做 IN 点查
//     内存求差（批量小查，禁无界 DISTINCT）；游标记录在进程内（表 → 已扫描到的最大 id），
//     游标**只在回写全部完成后才推进**：本趟被时间预算/错误中断时保持原值，下趟重扫同一区间
//     （已入表 IP 被求差过滤，重扫代价可忽略），避免「已发现未回写」的行被游标跳过。
//   - 回写侧逐 IP upsert（geoip_list_upsert.sql：冲突更新 geo 列与 updated_at，created_at 保持
//     首次入库值）：幂等可重入，反复执行直至扫完即全量构建。
//   - ★服务端硬超时：整趟绑定「调用方上下文 + 单趟时间预算」（固定常量 geoSyncBudget=20 秒，非配置项），
//     在每个扫描块/回写批边界检查，到点或调用方断开立即收工返回已完成进度——互斥锁随本趟退出立即释放。
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

// geoSyncReport 单表同步报告。
type geoSyncReport struct {
	Table      string   `json:"table"`
	IPs        int      `json:"ips"`            // 本趟处理的去重 IP 数（新入 geoip_list）
	RowsUpsert int64    `json:"rows_upserted"`  // upsert geoip_list 行数
	Skipped    int      `json:"skipped"`        // 解析不出地理位置的 IP 数（私网/回环/库外地址）
	IPSample   []string `json:"ip_sample"`      // 跳过的 IP 样本（最多 5 个，供排查）
	Scanned    int      `json:"scanned"`        // 本趟发现阶段实际扫描的日志行数
	Done       bool     `json:"done"`           // 本表缺失区间是否已扫完（false=可再次执行续扫）
	BudgetStop bool     `json:"budget_stopped"` // 本趟是否因时间预算/调用方断开而提前收工
}

// 同步节奏参数（批次化：单趟有界、可断点续扫，超时/失败已完成部分依然生效）。
var (
	geoipSyncTables = []string{db.TableAccessLog, db.TableShieldEvent}
	geoSyncBatchIPs = 5000 // 单趟处理的缺失 IP 上限：防超大表首趟过久；未完可再次执行续扫

	geoSyncProbeChunk = 500    // 求差点查每批携带的 IP 数（geoip_list IN 点查，主键命中毫秒级）
	geoSyncScanChunk  = 10000  // 发现阶段每块扫描的日志行数（走 id 主键范围扫，单块毫秒级）
	geoSyncScanCap    = 200000 // 单趟发现阶段扫描行数上限：即使缺失行极稀疏也保证单趟耗时可控
)

// geoSyncBudget 单趟时间预算（固定 20 秒，不做配置项）：同步是大表批量读写，必须给服务端硬上限，
// 到点即在块/批边界收工返回已完成进度；属实现细节而非用户可调策略，写死以免增加配置心智负担。
const geoSyncBudget = 20 * time.Second

// 回写运行态：id 游标（断点续扫，进程内记录即可）与进行中互斥锁（防并发双趟重复扫描写库）。
var (
	geoSyncMu      sync.Mutex
	geoSyncCursors = map[string]int64{}
	geoSyncRunning atomic.Bool
)

// geoipSyncIntervalMin GEOIP_SYNC_INTERVAL 当前值（main 装配期经 Register 绑定，热更直接写入；
// 定时器每轮触发时读取，运行中改值下一轮生效）。
var geoipSyncIntervalMin int

// geoSyncOnDone 同步结束回写钩子（装配期注入 schedule_list 登记器；nil=不回写）：
// status = success / failed，message 为结果摘要。回写点单一（D21）：手动与定时触发皆经 geoSyncAll 收口。
var geoSyncOnDone func(status, message string)

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

// geoVal 单个 IP 的解析结果（upsert 的输入单元）。
type geoVal struct{ ip, code, country, province, city string }

// emptyGeo 判断解析结果是否全空（无任何地理信息）。
func (v geoVal) emptyGeo() bool {
	return v.code == "" && v.country == "" && v.province == "" && v.city == ""
}

// geoSyncNextCursor 计算一趟结束后的游标与 Done 标记（纯函数，便于单测中断语义）：
//   - 回写被中断（时间预算/调用方断开/批失败）→ 保持原游标、Done=false，下趟重扫同一区间；
//   - 发现阶段已到表尾且回写完成 → 游标归零、Done=true（后续新增行可被下趟再发现）；
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

// geoSyncExistingIPs 查询 ipList 中已入 geoip_list 的 IP 集合（分批 IN 点查，禁无界 DISTINCT）。
func geoSyncExistingIPs(ctx context.Context, d *db.DB, ips []string) (map[string]bool, error) {
	sqlDB := d.EasyDB().GetSqlDB()
	exist := make(map[string]bool, len(ips))
	for start := 0; start < len(ips); start += geoSyncProbeChunk {
		end := min(start+geoSyncProbeChunk, len(ips))
		args := make([]any, 0, end-start)
		marks := make([]string, 0, end-start)
		for _, ip := range ips[start:end] {
			args = append(args, ip)
			marks = append(marks, "?")
		}
		q := "SELECT ip FROM " + db.TableGeoipList + " WHERE ip IN (" + strings.Join(marks, ",") + ")"
		rows, err := sqlDB.QueryContext(ctx, q, args...)
		if err != nil {
			if ctx.Err() != nil {
				return exist, ctx.Err()
			}
			return exist, fmt.Errorf("geoip: 查询已入表 IP 失败: %w", err)
		}
		for rows.Next() {
			var ip string
			if err := rows.Scan(&ip); err != nil {
				rows.Close()
				return exist, fmt.Errorf("geoip: 扫描已入表 IP 失败: %w", err)
			}
			exist[ip] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return exist, fmt.Errorf("geoip: 遍历已入表 IP 失败: %w", err)
		}
		rows.Close()
	}
	return exist, nil
}

// geoSyncUpsert 逐 IP upsert geoip_list（geoip_list_upsert.sql：三方言各一脚本，
// 冲突仅更新 geo 列与 updated_at，created_at 保持首次入库值）。ctx 在批边界检查。
func geoSyncUpsert(ctx context.Context, d *db.DB, vals []geoVal) (int64, error) {
	ins, err := d.SQL("geoip_list_upsert.sql")
	if err != nil {
		return 0, fmt.Errorf("geoip: 读取 upsert 脚本失败: %w", err)
	}
	ins = strings.ReplaceAll(ins, "{table}", db.TableGeoipList)
	sqlDB := d.EasyDB().GetSqlDB()
	var n int64
	now := time.Now().UTC()
	for start := 0; start < len(vals); start += geoSyncProbeChunk {
		if ctx.Err() != nil { // 批边界检查：到点/断开即收工，已完成部分不回滚
			return n, ctx.Err()
		}
		end := min(start+geoSyncProbeChunk, len(vals))
		for _, v := range vals[start:end] {
			res, err := sqlDB.ExecContext(ctx, ins, v.ip, v.code, v.country, v.province, v.city, now, now)
			if err != nil {
				if ctx.Err() != nil {
					return n, ctx.Err()
				}
				return n, fmt.Errorf("geoip: upsert %s 失败: %w", v.ip, err)
			}
			if m, err := res.RowsAffected(); err == nil {
				n += m
			}
		}
	}
	return n, nil
}

// geoSyncTable 对单表执行一轮同步：id 游标分块增量发现「client_ip 未入 geoip_list」的 IP
// （单趟上限 geoSyncBatchIPs 个 IP / geoSyncScanCap 行）→ 解析 → upsert geoip_list。
// ctx 在块/批边界检查：到点或调用方断开即收工，并在报告中标记 BudgetStop。
func geoSyncTable(ctx context.Context, d *db.DB, table string, res geoLookup) (geoSyncReport, error) {
	rep := geoSyncReport{Table: table, IPSample: []string{}}
	sqlDB := d.EasyDB().GetSqlDB()
	prevCursor := geoSyncCursorLoad(table)
	cursor := prevCursor
	var uniq []string
	inUniq := map[string]bool{}
	var maxID int64
	exhausted := false
	interrupted := false
	for len(uniq) < geoSyncBatchIPs && rep.Scanned < geoSyncScanCap && !exhausted {
		if ctx.Err() != nil {
			rep.BudgetStop = true
			interrupted = true
			break
		}
		q := fmt.Sprintf(
			"SELECT id, client_ip FROM %s WHERE id > %d AND client_ip <> '' ORDER BY id LIMIT %d",
			table, cursor, geoSyncScanChunk)
		rows, err := sqlDB.QueryContext(ctx, q)
		if err != nil {
			if ctx.Err() != nil { // 超时/断开导致的取消不算错误，按提前收工处理
				rep.BudgetStop = true
				interrupted = true
				break
			}
			return rep, fmt.Errorf("geoip: 扫描 %s 缺失 IP 失败: %w", table, err)
		}
		var chunk []string
		n := 0
		for rows.Next() {
			var id int64
			var ip string
			if err := rows.Scan(&id, &ip); err != nil {
				rows.Close()
				return rep, fmt.Errorf("geoip: 扫描 %s 缺失 IP 失败: %w", table, err)
			}
			n++
			if id > maxID {
				maxID = id
			}
			if !inUniq[ip] {
				inUniq[ip] = true
				chunk = append(chunk, ip)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			if ctx.Err() != nil {
				rep.BudgetStop = true
				interrupted = true
				break
			}
			return rep, fmt.Errorf("geoip: 扫描 %s 缺失 IP 失败: %w", table, err)
		}
		if n == 0 {
			exhausted = true // 到表尾：本趟扫完，游标归零（后续新增行可被下一趟发现）
			break
		}
		rep.Scanned += n
		cursor = maxID
		// 块内求差：过滤已入 geoip_list 的 IP（IN 点查），剩余即本块缺失 IP。
		exist, err := geoSyncExistingIPs(ctx, d, chunk)
		if err != nil {
			if ctx.Err() != nil {
				rep.BudgetStop = true
				interrupted = true
				break
			}
			return rep, err
		}
		for _, ip := range chunk {
			if !exist[ip] && len(uniq) < geoSyncBatchIPs {
				uniq = append(uniq, ip)
			}
		}
	}
	rep.IPs = len(uniq)

	// 解析 + upsert：解析不出任何 geo 的 IP 跳过并计数（游标越过不再重试）。
	var vals []geoVal
	for _, ip := range uniq {
		gi := res.Lookup(ip)
		v := geoVal{ip: ip, code: gi.Code, country: gi.Country, province: gi.Province, city: gi.City}
		if v.emptyGeo() {
			rep.Skipped++
			if len(rep.IPSample) < 5 {
				rep.IPSample = append(rep.IPSample, ip)
			}
			continue
		}
		vals = append(vals, v)
	}
	upserted, err := geoSyncUpsert(ctx, d, vals)
	rep.RowsUpsert = upserted
	if err != nil {
		if ctx.Err() != nil { // 取消导致的失败同样按提前收工处理（此前批次已提交）
			rep.BudgetStop = true
			interrupted = true
		} else {
			return rep, err
		}
	}
	// 游标只在回写未被中断时推进：中断即保持原值，下趟从同一 id 区间重扫。
	// 已入表 IP 会被求差阶段过滤，重扫代价可忽略；若提前推进，本趟「已发现但未回写」的
	// 行会被永久跳过（要等游标绕回表尾才可能再被发现）。
	next, done := geoSyncNextCursor(prevCursor, maxID, exhausted, interrupted)
	geoSyncCursorStore(table, next)
	rep.Done = done
	return rep, nil
}

// geoSyncAll 对全部参与表执行增量同步（geoip_list 构建/刷新唯一入口，D31）：
// 手动端点与定时触发皆调它。geo 未就绪直接报错（无 mmdb 时同步无从谈起）；
// 上一趟仍在进行时拒绝并发（不排队）。ctx 为调用方上下文（客户端断开会取消本趟），
// budget 为单趟时间预算（≤0 表示不限，仅受 ctx 约束）——两者共同保证服务端不会脱离
// 调用方监督长时间空转：到点/断开即在块边界收工，已完成部分保留，互斥锁立即释放。
// 结束后经 geoSyncOnDone 回写 schedule_list 状态（D21，注入方决定落点；nil 不回写）。
func geoSyncAll(ctx context.Context, d *db.DB, res geoLookup, budget time.Duration) ([]geoSyncReport, error) {
	if !res.Ready() {
		return nil, fmt.Errorf("geoip: mmdb 未加载，无法同步；请先放置 GeoLite2 mmdb 并重启服务")
	}
	if !geoSyncRunning.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("geoip: 上一轮同步仍在进行中，请稍候再触发（已完成部分不受影响）")
	}
	defer geoSyncRunning.Store(false)
	if budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	var out []geoSyncReport
	var syncErr error
	for _, table := range geoipSyncTables {
		rep, err := geoSyncTable(ctx, d, table, res)
		if rep.Table != "" {
			out = append(out, rep)
		}
		if err != nil {
			syncErr = err
			break
		}
	}
	// 状态回写（单点收口）：成功/失败皆落 schedule_list（geoip_sync 行），报告摘要作 message。
	if geoSyncOnDone != nil {
		status := "success"
		msg := geoSyncReportText(out)
		if syncErr != nil {
			status = "failed"
			msg = syncErr.Error()
		}
		geoSyncOnDone(status, msg)
	}
	return out, syncErr
}

// geoSyncReportText 报告转人读文案（日志/前端展示共用）。
func geoSyncReportText(reps []geoSyncReport) string {
	var b strings.Builder
	for _, r := range reps {
		fmt.Fprintf(&b, "%s：新同步 %d 个 IP（扫描 %d 行，跳过 %d 个无地理信息 IP）",
			r.Table, r.RowsUpsert, r.Scanned, r.Skipped)
		switch {
		case r.BudgetStop:
			b.WriteString("；本轮已达单趟时间上限，已完成部分已写入，再次执行从断点继续")
		case !r.Done:
			b.WriteString("；仍有未同步行未扫完，可再次执行继续")
		}
		b.WriteString("；")
	}
	return strings.TrimSuffix(b.String(), "；")
}

// normalizeGeoSyncInterval 归一 GEOIP_SYNC_INTERVAL 配置值（D34）：
// 0 = 关闭自动同步（手动不受影响）；有效最小 10 分钟（防设置过小耗尽资源）；
// <10（非 0）或负数等非法值回落默认 60。
func normalizeGeoSyncInterval(v int) int {
	switch {
	case v == 0:
		return 0
	case v < 10:
		return 60
	default:
		return v
	}
}

// startGeoSyncTimer 启动自动同步定时器（GEOIP_LIST D29/D34，装配层调用）：
// 分钟粒度 tick，每轮触发时重读间隔当前值（运行中改值下一轮生效，含 0=关闭的动态判定）；
// 首轮在启动约 1 分钟后执行（存量库冷启动多轮逐步追平）。返回停止通道，进程停机时关闭。
// mmdb 未就绪时返回 nil（不启动定时器——同步无从谈起，配置任意值均不生效）。
func startGeoSyncTimer(res geoLookup, run func(ctx context.Context)) chan struct{} {
	if !res.Ready() {
		return nil
	}
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		var lastRun time.Time
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				iv := normalizeGeoSyncInterval(geoipSyncIntervalMin)
				if iv <= 0 {
					continue // 0=关闭（运行期动态判定）
				}
				if !lastRun.IsZero() && time.Since(lastRun) < time.Duration(iv)*time.Minute {
					continue
				}
				lastRun = time.Now()
				// 单趟绑定固定时间预算（geoSyncBudget）：脱离调用方监督也不会长时间读写。
				ctx, cancel := context.WithTimeout(context.Background(), geoSyncBudget)
				run(ctx)
				cancel()
			}
		}
	}()
	return stop
}
