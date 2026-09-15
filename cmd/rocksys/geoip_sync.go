// geoip_sync.go：GeoIP 关联表增量同步（geoip_list 构建/刷新）。
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
//   - ★无时间预算（设计约定）：任务中心承载的默认无超时（后台化的意义就是摆脱请求超时），
//     单趟仅在扫描块/回写批边界响应调用方取消（人工 Cancel），取消即收工返回已完成进度——
//     分组互斥锁随本趟退出立即释放。私网/回环/解析不出的 IP 跳过并计数（永远无 geo，游标越过不再重试）。
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rocksys/internal/db"
	"rocksys/internal/geoip"
	"rocksys/internal/taskcenter"

	"github.com/iotames/easyserver/log"
)

// geoLookup GeoIP 解析最小接口（*geoip.Resolver 天然满足；测试可注入桩）。
type geoLookup interface {
	Lookup(ip string) geoip.GeoInfo
	Ready() bool
}

// geoSyncReport 单表同步报告。
type geoSyncReport struct {
	Table       string   `json:"table"`
	IPs         int      `json:"ips"`           // 本趟处理的去重 IP 数（新入 geoip_list）
	RowsUpsert  int64    `json:"rows_upserted"` // upsert geoip_list 行数
	Skipped     int      `json:"skipped"`       // 解析不出地理位置的 IP 数（私网/回环/库外地址）
	IPSample    []string `json:"ip_sample"`     // 跳过的 IP 样本（最多 5 个，供排查）
	Scanned     int      `json:"scanned"`       // 本趟发现阶段实际扫描的日志行数
	Done        bool     `json:"done"`          // 本表缺失区间是否已扫完（false=可再次执行续扫）
	Interrupted bool     `json:"interrupted"`   // 本趟是否因调用方取消而提前收工（已扫描/已写入部分保留）
}

// 同步节奏参数（批次化：单趟有界、可断点续扫，超时/失败已完成部分依然生效）。
var (
	geoipSyncTables = []string{db.TableAccessLog, db.TableShieldEvent}
	geoSyncBatchIPs = 5000 // 单趟处理的缺失 IP 上限：防超大表首趟过久；未完可再次执行续扫

	geoSyncProbeChunk = 500    // 求差点查每批携带的 IP 数（geoip_list IN 点查，主键命中毫秒级）
	geoSyncScanChunk  = 10000  // 发现阶段每块扫描的日志行数（走 id 主键范围扫，单块毫秒级）
	geoSyncScanCap    = 200000 // 单趟发现阶段扫描行数上限：即使缺失行极稀疏也保证单趟耗时可控
)

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
// status = success / failed / partial（取值语义见 schedule.go 常量），message 为结果摘要。
// 回写点单一：手动与定时触发皆经 geoSyncAll 收口。
var geoSyncOnDone func(status, message string)

// geoSyncOnSkip 定时轮被跳过（互斥组被占，任务未提交）的登记钩子（nil=不登记）：
// 只记「该轮未执行」（skipped）与原因，不改最近执行时间（其语义 = 执行结束时刻，跳过非执行）。
var geoSyncOnSkip func(message string)

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
// ctx 在块/批边界检查：调用方取消即收工，并在报告中标记 Interrupted；
// setProgress 在每块边界回传进度（后台任务页实时可见）。
func geoSyncTable(ctx context.Context, d *db.DB, table string, res geoLookup, setProgress func(string)) (geoSyncReport, error) {
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
			rep.Interrupted = true
			interrupted = true
			break
		}
		q := fmt.Sprintf(
			"SELECT id, client_ip FROM %s WHERE id > %d AND client_ip <> '' ORDER BY id LIMIT %d",
			table, cursor, geoSyncScanChunk)
		rows, err := sqlDB.QueryContext(ctx, q)
		if err != nil {
			if ctx.Err() != nil { // 超时/断开导致的取消不算错误，按提前收工处理
				rep.Interrupted = true
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
				rep.Interrupted = true
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
		if setProgress != nil {
			setProgress(fmt.Sprintf("%s：已扫描 %d 行，发现 %d 个待同步 IP", table, rep.Scanned, len(uniq)))
		}
		// 块内求差：过滤已入 geoip_list 的 IP（IN 点查），剩余即本块缺失 IP。
		exist, err := geoSyncExistingIPs(ctx, d, chunk)
		if err != nil {
			if ctx.Err() != nil {
				rep.Interrupted = true
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
			rep.Interrupted = true
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

// geoSyncAll 对全部参与表执行增量同步（geoip_list 构建/刷新唯一入口）：
// 手动端点与定时触发皆经任务中心提交后调它（同互斥组天然串行）。geo 未就绪直接报错
// （无 mmdb 时同步无从谈起）；上一趟仍在进行时拒绝并发（不排队，防御性兜底——正常路径
// 互斥由任务中心分组承担）。ctx 为调用方上下文（人工取消即本趟收工；无时间预算——
// 后台任务默认无超时，跑多久由数据量决定）：取消在块/批边界生效，已完成部分保留。
// 结束后经 geoSyncOnDone 回写 schedule_list 状态（注入方决定落点；nil 不回写）；
// setProgress 在每块边界回传进度（后台任务页实时可见，可 nil）。
func geoSyncAll(ctx context.Context, d *db.DB, res geoLookup, setProgress func(text string)) ([]geoSyncReport, error) {
	if !res.Ready() {
		return nil, fmt.Errorf("geoip: mmdb 未加载，无法同步；请先放置 GeoLite2 mmdb 并重启服务")
	}
	if !geoSyncRunning.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("geoip: 上一轮同步仍在进行中，请稍候再触发（已完成部分不受影响）")
	}
	defer geoSyncRunning.Store(false)
	var out []geoSyncReport
	var syncErr error
	for _, table := range geoipSyncTables {
		rep, err := geoSyncTable(ctx, d, table, res, setProgress)
		if rep.Table != "" {
			out = append(out, rep)
		}
		if setProgress != nil {
			setProgress(geoSyncReportText(out))
		}
		if err != nil {
			syncErr = err
			break
		}
	}
	// 状态回写（单点收口）：结果落 schedule_list（geoip_sync 行），报告摘要作 message。
	// 取值全部是「轮次执行结果」域词汇（schedule 是任务工厂的登记面，实例运行时状态只在
	// 任务中心；取值权威定义见 schedule.go 常量 + sql 建表脚本注释 + docs/DATA_DICT.md §3.5）：
	//   - 正常跑完：记 success（分趟上限先到的轮次亦然——下次执行从断点续接，属设计内节奏）；
	//   - 被取消（任务取消端点 / 进程收尾）：本轮未跑完 ≠ 失败也 ≠ 成功，记 partial——
	//     记 success 会谎报「上轮已跑完」，用户误以为数据已补齐；
	//   - 其余错误：真失败，记 failed。
	// 原因判定以「ctx 停止原因」为准：表的读写路径在 ctx 取消时已按协作式收工转成
	// Interrupted 且 err=nil，只看 syncErr 会把「被取消」误判成正常完成（实测踩坑）。
	stopErr := syncErr
	if stopErr == nil {
		stopErr = ctx.Err()
	}
	status, stopped := geoipSyncOutcome(stopErr)
	msg := geoSyncReportText(out)
	if stopped && hasInterrupted(out) {
		msg += "；收工原因：任务被取消（已完成部分已写入，再次执行从断点续接）"
	} else if status == ScheduleStatusFailed {
		msg = syncErr.Error()
	}
	if geoSyncOnDone != nil {
		geoSyncOnDone(status, msg)
	}
	if stopped {
		// 协作式收工（取消）不上抛错误：任务中心已按取消请求裁定终态（取消先到 → cancelled），
		// 上抛会把「中断」记成任务失败。
		return out, nil
	}
	return out, syncErr
}

// geoipSyncOutcome 同步收口的结果裁定（纯函数，便于穷尽断言）：
//   - nil      → success（正常完成；含分趟上限先到、下轮续接的轮次）
//   - Canceled → partial（本轮未跑完：人工经任务取消端点终止，或进程收尾中止；
//     已完成部分已写入，下轮从断点续接）
//   - 其他     → failed（真错误：库不可用、SQL 出错等）
//
// 返回的 stopped 为 true 表示「协作式收工」（取消），属中断而非失败，不上抛错误。
// 传入的 err 应为「真实的停止原因」：正常结束传 nil；协作式收工场景须传 ctx.Err()
// （内层把 ctx 取消转为优雅收工、err 为 nil，只传 err 会把取消误判为成功完成）。
// 注：任务中心默认无超时（ctx 无 deadline），不存在「到点收工」分支。
func geoipSyncOutcome(err error) (status string, stopped bool) {
	switch {
	case err == nil:
		return ScheduleStatusSuccess, false
	case errors.Is(err, context.Canceled):
		return ScheduleStatusPartial, true
	default:
		return ScheduleStatusFailed, false
	}
}

// hasInterrupted 本趟是否存在提前收工的表（用于只在确有中断时追加收工原因文案）。
func hasInterrupted(reps []geoSyncReport) bool {
	for _, r := range reps {
		if r.Interrupted {
			return true
		}
	}
	return false
}

// geoSyncReportText 报告转人读文案（日志/前端展示共用）。
func geoSyncReportText(reps []geoSyncReport) string {
	var b strings.Builder
	for _, r := range reps {
		fmt.Fprintf(&b, "%s：新同步 %d 个 IP（扫描 %d 行，跳过 %d 个无地理信息 IP）",
			r.Table, r.RowsUpsert, r.Scanned, r.Skipped)
		switch {
		case r.Interrupted:
			b.WriteString("；本轮提前收工，已完成部分已写入，再次执行从断点继续")
		case !r.Done:
			b.WriteString("；仍有未同步行未扫完，可再次执行继续")
		}
		b.WriteString("；")
	}
	return strings.TrimSuffix(b.String(), "；")
}

// geoSyncTaskSpec 构造 GeoIP 同步任务（手动端点与定时触发共用同一执行体，仅标题区分来源）：
// 经任务中心提交执行（无时间预算——后台任务默认无超时，取消经统一取消端点送达）；
// 互斥由任务中心规则承载（geoip_sync 标签在公共互斥集内，与迁移/结构对齐/SQL 后台执行互斥）。
func geoSyncTaskSpec(title string, d *db.DB, res geoLookup, purgeCache func()) taskcenter.Spec {
	return taskcenter.Spec{
		CreatedBy: "geoip_sync",
		Title:     title,
		Run: func(ctx context.Context, setProgress taskcenter.SetProgressFn) error {
			reps, err := geoSyncAll(ctx, d, res, func(text string) {
				setProgress(&taskcenter.Progress{Text: text})
			})
			if err != nil {
				log.Error("geoip: 同步失败", "err", err.Error())
				return err
			}
			setProgress(&taskcenter.Progress{Text: geoSyncReportText(reps), Detail: reps})
			if purgeCache != nil {
				purgeCache() // 同步成功清流量统计缓存：聚合视图下次查询重新计算，立即见新数据
			}
			return nil
		},
	}
}

// normalizeGeoSyncInterval 归一 GEOIP_SYNC_INTERVAL 配置值：
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

// startGeoSyncTimer 启动自动同步定时器（装配层调用）：
// 分钟粒度 tick，每轮触发时重读间隔当前值（运行中改值下一轮生效，含 0=关闭的动态判定）；
// 首轮在启动约 1 分钟后执行（存量库冷启动多轮逐步追平）。返回停止通道，进程停机时关闭。
// mmdb 未就绪时返回 nil（不启动定时器——同步无从谈起，配置任意值均不生效）。
//
// 执行模型：定时同步与手动同步同为任务中心实例（run 由装配期注入「提交任务」闭包），
// 受任务中心分组互斥统一管控——同互斥组已有任务在跑（人工长任务/上一轮未完）时提交被拒，
// 该轮跳过（经 geoSyncOnSkip 登记，下轮到点自动续接，游标增量无数据丢失）。
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
				// 无时间预算（后台任务默认无超时）：互斥与生命周期由任务中心统一管控。
				run(context.Background())
			}
		}
	}()
	return stop
}
