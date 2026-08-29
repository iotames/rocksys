// 自动拉黑引擎（IP 黑名单增强 STEP A4；设计依据 docs/IP_BLACKLIST_PLAN.md §3.4 决策 11/12）。
//
// 职责：后台周期扫描近窗口内的 WAF 拦截事件（shield_event 表），按 IP 跨类别合计
// 拦截次数，达阈值（SHIELD_AUTO_BAN_THRESHOLD）自动写入黑名单（小黑屋封禁语义，
// 复用 A2 的 BanInsert/GetByIP/RestoreBan 三态）。纯后台批处理，拦截热路径零改动。
//
// 关键决策：
//   - 决策 11：候选查询排除 block_type=1（黑名单自我拦截事件），防止"已被拉黑 IP
//     继续撞黑名单产生事件 → 无限续封"的循环封禁；
//   - 决策 12：聚合在 Go 侧单处实现（SQL 不用窗口函数，老 MySQL 无 ROW_NUMBER，
//     三方言保持同构）：按 IP 跨类别合计判阈值；拉黑类别取该 IP 次数最多者，
//     并列取枚举值小者（规则定死可测）；
//   - 白名单过滤复用拦截快照 CIDR 匹配语义（InWhitelist，白名单可含网段，
//     不能只做精确 IP 比对）；
//   - 仅精确 IP 入库（拦截事件来源为精确 IP，无 CIDR）。
//
// 生命周期：参照 EventRecorder 的 ticker/Stop 模式（started atomic.Bool CAS +
// stopCh/doneCh），由 main.go 装配启动、随 shield 停机停止。
// ★ 每轮循环开始读取配置最新值（开关/阈值/窗口/TTL 均支持热更），
// 开关关闭则空转（仅读一个 bool，零 DB 开销）。
package shield

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iotames/easyserver/log"

	"rocksys/internal/conf"
)

// autoBanCandidatesSQL 自动拉黑候选查询脚本名（sql/<dbtype>/ 下三方言同构）。
const autoBanCandidatesSQL = "shield_event_auto_ban_candidates.sql"

// 自动拉黑运行周期下限：window/3 再小也不低于 1 分钟（避免高频扫库）。
const autoBanMinInterval = time.Minute

// AutoBanEngine 自动拉黑引擎：周期扫描拦截事件 → 达阈值自动封禁。
// 依赖注入：shield 提供快照白名单过滤与黑白名单 store；recorder 提供统一数据
// 访问层连接、SQL 脚本源与 shield_event 表名（复用 SHIELD_EVENT_TABLE 配置）。
type AutoBanEngine struct {
	shield *Shield        // 拦截快照（白名单过滤）+ 黑名单 store（封禁三态）
	rec    *EventRecorder // 数据访问层（edb/sqlText/tableName 均复用，nil 引擎不可用）

	// 配置项字段（构造时经 conf.Manager.Register 注册，热更直接写入；
	// 每轮循环开始读取最新值）。
	enabled   bool   // SHIELD_AUTO_BAN_ENABLED：引擎开关（false 空转）
	threshold int    // SHIELD_AUTO_BAN_THRESHOLD：窗口内拦截次数阈值
	window    string // SHIELD_AUTO_BAN_WINDOW：统计窗口（Go duration，如 10m）
	ttl       string // SHIELD_AUTO_BAN_TTL：封禁时长（如 24h；0=永久，续封取 10 倍）

	stopCh  chan struct{} // 停止信号（Stop 关闭）
	doneCh  chan struct{} // 循环退出应答（Stop 等待）
	started atomic.Bool
}

// NewAutoBanEngine 构造自动拉黑引擎并注册配置项（遵循配置中心红线：一律经 Register）。
// 注册失败仅告警不阻断（与 EventRecorder 一致），缺省回落代码默认值。
func NewAutoBanEngine(cfgMgr conf.Manager, s *Shield, rec *EventRecorder) *AutoBanEngine {
	e := &AutoBanEngine{
		shield: s,
		rec:    rec,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	items := []struct {
		pval   any
		name   string
		defval string
		title  string
	}{
		{&e.enabled, "SHIELD_AUTO_BAN_ENABLED", "false",
			"是否开启自动拉黑引擎（周期扫描拦截事件，窗口内拦截达阈值自动封禁）"},
		{&e.threshold, "SHIELD_AUTO_BAN_THRESHOLD", "50",
			"自动拉黑阈值：统计窗口内单 IP 拦截次数≥该值触发封禁"},
		{&e.window, "SHIELD_AUTO_BAN_WINDOW", "10m",
			"自动拉黑统计窗口（Go duration，如 10m）；引擎运行周期=窗口/3（下限 1 分钟）"},
		{&e.ttl, "SHIELD_AUTO_BAN_TTL", "24h",
			"自动拉黑封禁时长（Go duration，如 24h；0=永久）；软删/过期条目恢复续封时长为该值的 10 倍"},
	}
	for _, it := range items {
		if err := cfgMgr.Register(it.pval, it.name, it.defval, it.title); err != nil {
			log.Warn("shield: 注册自动拉黑配置项失败", "name", it.name, "err", err.Error())
		}
	}
	return e
}

// Enabled 返回引擎开关当前值（main.go 装配判断是否启动用；随配置热更实时反映）。
func (e *AutoBanEngine) Enabled() bool { return e.enabled }

// Start 启动引擎循环（幂等）。循环先立即执行一轮（便于装配后尽快生效），
// 此后按"窗口/3（下限 1 分钟）"周期执行，每轮开始读取配置最新值。
func (e *AutoBanEngine) Start() {
	if !e.started.CompareAndSwap(false, true) {
		return
	}
	go e.runLoop()
}

// Stop 优雅停机：通知循环退出并等待本轮处理结束（幂等）。
func (e *AutoBanEngine) Stop() {
	if !e.started.CompareAndSwap(true, false) {
		return
	}
	close(e.stopCh)
	<-e.doneCh
}

// parseAutoBanDuration 解析时长配置："0" 特判为 0（永久），其余走 time.ParseDuration。
func parseAutoBanDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "0" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

// autoBanInterval 计算引擎运行周期：窗口/3，下限 1 分钟；窗口非法/非正回落 1 分钟。
func autoBanInterval(window time.Duration) time.Duration {
	if window <= 0 {
		return autoBanMinInterval
	}
	if iv := window / 3; iv >= autoBanMinInterval {
		return iv
	}
	return autoBanMinInterval
}

// runLoop 引擎主循环：周期随窗口配置变化（每轮重新计算，窗口热更即改周期）。
func (e *AutoBanEngine) runLoop() {
	defer close(e.doneCh)
	e.runOnce() // 启动即执行一轮（装配后尽快生效）
	for {
		window, _ := parseAutoBanDuration(e.window) // 非法回落 0 → autoBanInterval 回落 1 分钟
		timer := time.NewTimer(autoBanInterval(window))
		select {
		case <-e.stopCh:
			timer.Stop()
			return
		case <-timer.C:
			e.runOnce()
		}
	}
}

// banCandidate 达到阈值的候选拉黑 IP（聚合结果）。
type banCandidate struct {
	IP      string    // 精确 IP
	Total   int64     // 窗口内跨类别拦截合计
	TopType BlockType // 拉黑类别：次数最多者（并列取枚举值小者）
}

// runOnce 执行一轮扫描：读配置 → 查候选 → 聚合 → 白名单过滤 → 四态处理 → 有变更重建快照。
// 开关关闭直接返回（空转，零 DB 开销）；各环节失败仅告警，下轮重试。
func (e *AutoBanEngine) runOnce() {
	if !e.enabled {
		return // 开关关闭：空转（热更开启后下一轮自动生效）
	}
	threshold := int64(e.threshold)
	if threshold <= 0 {
		threshold = 50 // 配置异常兜底（与注册默认值一致）
	}
	window, err := parseAutoBanDuration(e.window)
	if err != nil || window <= 0 {
		log.Warn("shield: 自动拉黑窗口配置非法，本轮跳过", "window", e.window)
		return
	}
	ttl, err := parseAutoBanDuration(e.ttl)
	if err != nil || ttl < 0 {
		log.Warn("shield: 自动拉黑封禁时长配置非法，本轮跳过", "ttl", e.ttl)
		return
	}
	if e.rec == nil || e.shield == nil {
		return // 未装配数据层/拦截器（不应发生，防御性跳过）
	}
	st, err := e.shield.ipStore(true) // 封禁仅黑名单语境（store 未注入则整轮跳过）
	if err != nil {
		return
	}
	sel, err := e.rec.sqlText(autoBanCandidatesSQL)
	if err != nil {
		log.Warn("shield: 自动拉黑候选查询脚本读取失败，本轮跳过", "err", err.Error())
		return
	}
	var rows []map[string]any
	since := time.Now().Add(-window).UTC()
	if err := e.rec.edb.GetMany(sel, &rows, since); err != nil {
		log.Warn("shield: 自动拉黑候选查询失败，本轮跳过", "err", err.Error())
		return
	}
	cands := aggregateAutoBanCandidates(rows, threshold)
	if len(cands) == 0 {
		return
	}
	// title 按设计定式：自动拉黑：{window}内拦截≥{threshold}次
	title := fmt.Sprintf("自动拉黑：%s内拦截≥%d次", strings.TrimSpace(e.window), threshold)
	changed := false
	now := time.Now()
	for _, c := range cands {
		// 白名单过滤：复用拦截快照 CIDR 匹配语义（白名单可含网段，不能只精确比对）。
		if e.shield.InWhitelist(c.IP) {
			continue
		}
		// 新增封禁到期时间：TTL=0 → 永久（nil）。
		var expInsert *time.Time
		if ttl > 0 {
			t := now.Add(ttl)
			expInsert = &t
		}
		cur, err := st.GetByIP(c.IP)
		switch {
		case errors.Is(err, ErrIPNotExists):
			// 态一：无记录 → 新增入库（warn_times=1 起算，block_type=真实拦截类别 1-10）。
			if _, err := st.BanInsert(c.IP, title, c.TopType, expInsert, now); err != nil {
				log.Warn("shield: 自动拉黑入库失败", "ip", c.IP, "err", err.Error())
				continue
			}
			changed = true
			log.Info("shield: 自动拉黑新增封禁", "ip", c.IP,
				"block_type", int(c.TopType), "hits", c.Total, "ttl", e.ttl)
		case err != nil:
			log.Warn("shield: 自动拉黑查询条目失败", "ip", c.IP, "err", err.Error())
		case banEntryActive(cur, now):
			// 态二：活跃条目（未删未过期）→ 跳过（已在封禁中，不重复计数）。
		default:
			// 态三/四：软删/已过期条目 → 恢复续封（warn_times+1），解封时间=TTL×10；
			// 累计达 5 次的限时封禁转永久（RestoreBan 内聚，TTL=0 恢复后仍永久）。
			var expRestore *time.Time
			if ttl > 0 {
				t := now.Add(ttl * 10)
				expRestore = &t
			}
			perm, err := st.RestoreBan(c.IP, expRestore, now)
			if err != nil {
				log.Warn("shield: 自动拉黑续封失败", "ip", c.IP, "err", err.Error())
				continue
			}
			changed = true
			log.Info("shield: 自动拉黑恢复续封", "ip", c.IP,
				"block_type", int(c.TopType), "warn_times", cur.WarnTimes+1, "to_permanent", perm)
		}
	}
	// 有变更才重建拦截快照（一次整轮重建，与管理面 rebuildAfter 同语义）。
	if changed {
		e.shield.rebuildAfter("auto_ban")
	}
}

// aggregateAutoBanCandidates Go 侧聚合（决策 12，纯函数可测）：
// 输入候选 SQL 行（client_ip/block_type/cnt），按 IP 跨类别合计判阈值；
// 拉黑类别取该 IP 次数最多者，并列取枚举值小者。
// 过滤口径：
//   - 仅精确 IP（net.ParseIP 校验；CIDR/非法串排除——拦截事件来源本无 CIDR，双保险）；
//   - 仅真实拦截类别 2-10（决策 11：排除 block_type=1 黑名单自我拦截，防循环封禁；
//     SQL 已排除，此处 Go 侧双保险）；0/11 等黑名单语境枚举不参与聚合。
func aggregateAutoBanCandidates(rows []map[string]any, threshold int64) []banCandidate {
	totals := make(map[string]int64)
	counts := make(map[string]map[BlockType]int64)
	for _, row := range rows {
		ip := eventToString(row["client_ip"])
		bt := BlockType(eventToInt64(row["block_type"]))
		cnt := eventToInt64(row["cnt"])
		if ip == "" || cnt <= 0 {
			continue
		}
		if net.ParseIP(ip) == nil {
			continue // 仅精确 IP 入库
		}
		if bt < BlockRateLimit || bt > BlockPathRuleDeny {
			continue // 仅 2-10（排除 1 自我拦截；0/11 非拦截语境）
		}
		totals[ip] += cnt
		if counts[ip] == nil {
			counts[ip] = make(map[BlockType]int64)
		}
		counts[ip][bt] += cnt
	}
	out := make([]banCandidate, 0, len(totals))
	for ip, total := range totals {
		if total < threshold {
			continue // 跨类别合计未达阈值
		}
		// 次数最多者；并列取枚举值小者（规则定死可测）。
		top := BlockType(0)
		var maxCnt int64 = -1
		for bt, n := range counts[ip] {
			if n > maxCnt || (n == maxCnt && bt < top) {
				top, maxCnt = bt, n
			}
		}
		out = append(out, banCandidate{IP: ip, Total: total, TopType: top})
	}
	// 按 IP 排序：输出与处理顺序确定（可测、日志可读）。
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}
