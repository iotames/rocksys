// 自动拉黑引擎（IP 黑名单增强 STEP A4；设计依据 docs/IP_BLACKLIST_PLAN.md §3.4 决策 11/12；
// 风险分档升级：按 block_type 三档差异化处置，见 banTierOf）。
//
// 职责：后台周期扫描近窗口内的 WAF 拦截事件（shield_event 表），按 IP 分档合计
// 拦截次数，达对应档位阈值自动写入黑名单（小黑屋封禁语义，复用 A2 的
// BanInsert/GetByIP/RestoreBan 三态）。纯后台批处理，拦截热路径零改动。
//
// 风险分档策略（档位映射定死在 block_type.go，攻击档阈值定死在代码）：
//   - 攻击档（风险路径/遍历/SQL注入/XSS）：真实攻击不存在误报合理场景，
//     窗口内命中 attackBanThreshold（1）次即直接永久封禁（宁可误杀绝不放过）；
//   - 爬虫档（爬虫/扫描器 UA）：UA 属君子协议伪造成本低，但按流量计费场景爬虫
//     烧钱，独立低阈值（SHIELD_AUTO_BAN_CRAWLER_THRESHOLD，默认 20）限时封禁；
//   - 通用档（限流/方法/体积/规则 deny）：通用阈值（SHIELD_AUTO_BAN_THRESHOLD）限时封禁；
//   - 同一 IP 多档同时达标取最严档处置；跨档次数不合并（各判各的，SQLi 3 次 +
//     限流 47 次不等于攻击档达标之外的通用档达标口径混淆）；
//   - 累犯升级：限时封禁累计入狱达 SHIELD_AUTO_BAN_REPEAT_LIMIT（默认 5，0=永不
//     自动转永久）次转永久，替代原硬编码 banWarnTimesLimit。
//
// 关键决策：
//   - 决策 11：候选查询排除 block_type=1（黑名单自我拦截事件），防止"已被拉黑 IP
//     继续撞黑名单产生事件 → 无限续封"的循环封禁；
//   - 决策 12：聚合在 Go 侧单处实现（SQL 不用窗口函数，老 MySQL 无 ROW_NUMBER，
//     三方言保持同构）：按 IP 分档合计判阈值；拉黑类别取该 IP 档内次数最多者，
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
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iotames/easyserver/log"

	"rocksys/internal/conf"
)

// autoBanCandidatesSQL 自动拉黑候选查询脚本名（sql/<dbtype>/ 下三方言同构）。
const autoBanCandidatesSQL = "shield_event_auto_ban_candidates.sql"

// autoBanMinInterval 运行周期下限：window/3 再小也不低于 1 分钟（避免高频扫库）。
const autoBanMinInterval = time.Minute

// attackBanThreshold 攻击档直永久阈值：窗口内攻击类（风险路径/遍历/SQL注入/XSS）
// 命中达该次数即直接永久封禁。安全策略定死在代码不开放配置——攻击类请求不存在
// 误报合理场景，放开口子（如设 0 关闭）等于默认放过攻击器，属误操作陷阱。
const attackBanThreshold = 1

// AutoBanEngine 自动拉黑引擎：周期扫描拦截事件 → 达阈值自动封禁。
// 依赖注入：shield 提供快照白名单过滤与黑白名单 store；recorder 提供统一数据
// 访问层连接、SQL 脚本源与 shield_event 表名（复用 SHIELD_EVENT_TABLE 配置）。
type AutoBanEngine struct {
	shield *Shield        // 拦截快照（白名单过滤）+ 黑名单 store（封禁三态）
	rec    *EventRecorder // 数据访问层（edb/sqlText/tableName 均复用，nil 引擎不可用）
	cfgMgr conf.Manager   // 配置管理器（readConfig 经 List() 持锁快照读，nil 回落注册指针字段）

	// 配置项注册存储（构造时经 conf.Manager.Register 注册，热更由配置中心直写；
	// ★ 运行期读取一律走 readConfig() 的 List() 持锁快照——配置中心写指针与引擎
	// goroutine 读指针之间无同步，直读构成 data race，故本组字段仅作注册目标与测试注入口）。
	enabled          bool   // SHIELD_AUTO_BAN_ENABLED：引擎开关（false 空转）
	threshold        int    // SHIELD_AUTO_BAN_THRESHOLD：通用档窗口内拦截次数阈值
	crawlerThreshold int    // SHIELD_AUTO_BAN_CRAWLER_THRESHOLD：爬虫档阈值（默认 20）
	repeatLimit      int    // SHIELD_AUTO_BAN_REPEAT_LIMIT：累计入狱转永久阈值（0=永不自动转永久）
	window           string // SHIELD_AUTO_BAN_WINDOW：统计窗口（Go duration，如 10m）
	ttl              string // SHIELD_AUTO_BAN_TTL：封禁时长（如 24h；0=永久，续封取 10 倍）

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
		cfgMgr: cfgMgr,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	items := []struct {
		pval   any
		name   string
		defval string
		title  string
		usage  []string
	}{
		{&e.enabled, "SHIELD_AUTO_BAN_ENABLED", "false",
			"是否开启自动拉黑引擎（周期扫描拦截事件，按风险分档：攻击类命中 1 次直接永久封禁；爬虫 UA/通用类窗口内达各自阈值封禁）",
			[]string{"开启后按风险分档自动封禁：攻击档（风险路径/路径遍历/SQL注入/XSS）命中 1 次直接永久；爬虫档与通用档达阈值限时封禁",
				"适用场景：公网网关无人值守时的自动攻击防护与爬虫治理；误封兜底=白名单+人工解封",
				"开关改动需重启服务生效"},
		},
		{&e.threshold, "SHIELD_AUTO_BAN_THRESHOLD", "50",
			"自动拉黑通用阈值：统计窗口内单 IP 通用类（限流/方法/体积/规则deny）拦截次数≥该值触发封禁；攻击类不适用（命中 1 次即永久）",
			[]string{"仅约束通用档拦截类别（限流=异常高并发判断，误伤面较大故维持宽松）",
				"调大更宽容（防业务高峰误封），调小更敏感；每轮扫描读配置，热更"},
		},
		{&e.crawlerThreshold, "SHIELD_AUTO_BAN_CRAWLER_THRESHOLD", "20",
			"自动拉黑爬虫阈值：统计窗口内单 IP 爬虫/扫描器 UA 拦截次数≥该值触发封禁（低于通用阈值：按流量计费场景爬虫烧钱优先拦）",
			[]string{"仅约束爬虫/扫描器 UA 拦截类别；UA 属君子协议有误杀可能，故不直接永久封禁、走限时+累犯升级",
				"按流量计费场景建议保持低于 SHIELD_AUTO_BAN_THRESHOLD；每轮扫描读配置，热更"},
		},
		{&e.repeatLimit, "SHIELD_AUTO_BAN_REPEAT_LIMIT", "5",
			"自动拉黑累犯转永久阈值：限时封禁条目累计入狱达该次数转永久封禁（0=永不自动转永久）",
			[]string{"条目被软删/过期后再次达阈值即入狱 1 次（续封时长=TTL×10），累计入狱达本值转永久——屡教不改者升级为永久封禁",
				"0=关闭累犯升级（限时封禁到期自动解封，永不自动转永久）；每轮扫描读配置，热更"},
		},
		{&e.window, "SHIELD_AUTO_BAN_WINDOW", "10m",
			"自动拉黑统计窗口（Go duration，如 10m），各档阈值均按本窗口计数；引擎运行周期=窗口/3（下限 1 分钟）",
			[]string{"所有档位阈值均为本窗口内的拦截次数；窗口调大则同等阈值更易触发（灵敏度线性变宽）",
				"每轮循环重新读取并按 窗口/3 重算运行周期，热更"},
		},
		{&e.ttl, "SHIELD_AUTO_BAN_TTL", "24h",
			"自动拉黑封禁时长（Go duration，如 24h；0=永久）；软删/过期条目恢复续封时长为该值的 10 倍",
			[]string{"仅约束爬虫档/通用档的限时封禁；攻击档直永久与本值 0=永久均不受限",
				"软删/过期条目恢复续封时长=本值×10；每轮扫描读配置，热更"},
		},
	}
	for _, it := range items {
		if err := cfgMgr.Register(it.pval, it.name, it.defval, it.title, it.usage...); err != nil {
			log.Warn("shield: 注册自动拉黑配置项失败", "name", it.name, "err", err.Error())
		}
	}
	return e
}

// Enabled 返回引擎开关当前值（诊断用；引擎自身每轮经 readConfig 读取，不依赖本方法）。
func (e *AutoBanEngine) Enabled() bool { return e.readConfig().enabled }

// autoBanCfg 一轮扫描的配置快照（readConfig 产出，runOnce 单轮内使用同一份）。
type autoBanCfg struct {
	enabled          bool
	threshold        int
	crawlerThreshold int
	repeatLimit      int
	window           string
	ttl              string
}

// readConfig 读取配置快照：优先经 conf.Manager.List()（内部持锁，与热更写入同步，
// 消除直读注册指针的 data race）；List 未覆盖的项（如测试 fake 不实现 List）回落
// 注册指针字段。
func (e *AutoBanEngine) readConfig() autoBanCfg {
	c := autoBanCfg{
		enabled:          e.enabled,
		threshold:        e.threshold,
		crawlerThreshold: e.crawlerThreshold,
		repeatLimit:      e.repeatLimit,
		window:           e.window,
		ttl:              e.ttl,
	}
	if e.cfgMgr == nil {
		return c
	}
	for _, it := range e.cfgMgr.List() {
		switch it.Key {
		case "SHIELD_AUTO_BAN_ENABLED":
			c.enabled = strings.EqualFold(strings.TrimSpace(it.Current), "true")
		case "SHIELD_AUTO_BAN_THRESHOLD":
			if n, err := strconv.Atoi(strings.TrimSpace(it.Current)); err == nil && n > 0 {
				c.threshold = n
			}
		case "SHIELD_AUTO_BAN_CRAWLER_THRESHOLD":
			if n, err := strconv.Atoi(strings.TrimSpace(it.Current)); err == nil && n > 0 {
				c.crawlerThreshold = n
			}
		case "SHIELD_AUTO_BAN_REPEAT_LIMIT":
			// 允许显式 0（=永不自动转永久）；非法值保持注册默认。
			if n, err := strconv.Atoi(strings.TrimSpace(it.Current)); err == nil && n >= 0 {
				c.repeatLimit = n
			}
		case "SHIELD_AUTO_BAN_WINDOW":
			c.window = it.Current
		case "SHIELD_AUTO_BAN_TTL":
			c.ttl = it.Current
		}
	}
	return c
}

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
		cfg := e.readConfig()
		window, _ := parseAutoBanDuration(cfg.window) // 非法回落 0 → autoBanInterval 回落 1 分钟
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

// banCandidate 达到档位阈值的候选拉黑 IP（聚合结果；同一 IP 多档达标取最严档，
// 攻击档直永久已覆盖该 IP，无需再出低档候选）。
type banCandidate struct {
	IP        string    // 精确 IP
	Total     int64     // 窗口内该档拦截合计
	TopType   BlockType // 拉黑类别：档内次数最多者（并列取枚举值小者）
	Tier      banTier   // 达标档位（处置策略依据）
	Threshold int64     // 达标档位阈值（title 展示用）
}

// runOnce 执行一轮扫描：读配置 → 查候选 → 聚合 → 白名单过滤 → 四态处理 → 有变更重建快照。
// 开关关闭直接返回（空转，零 DB 开销）；各环节失败仅告警，下轮重试。
func (e *AutoBanEngine) runOnce() {
	cfg := e.readConfig()
	if !cfg.enabled {
		return // 开关关闭：空转（热更开启后下一轮自动生效）
	}
	threshold := int64(cfg.threshold)
	if threshold <= 0 {
		threshold = 50 // 配置异常兜底（与注册默认值一致）
	}
	crawler := int64(cfg.crawlerThreshold)
	if crawler <= 0 {
		crawler = 20 // 配置异常兜底（与注册默认值一致）
	}
	window, err := parseAutoBanDuration(cfg.window)
	if err != nil || window <= 0 {
		log.Warn("shield: 自动拉黑窗口配置非法，本轮跳过", "window", cfg.window)
		return
	}
	ttl, err := parseAutoBanDuration(cfg.ttl)
	if err != nil || ttl < 0 {
		log.Warn("shield: 自动拉黑封禁时长配置非法，本轮跳过", "ttl", cfg.ttl)
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
	cands := aggregateAutoBanCandidates(rows, threshold, crawler)
	if len(cands) == 0 {
		return
	}
	// title 按设计定式：临时封禁=「自动拉黑：{window}内拦截≥{threshold}次」（截断至
	// 列宽内，见 truncateTitle）；攻击档直永久单独定式。
	tempTitleFmt := "自动拉黑：%s内拦截≥%d次"
	attackTitle := truncateTitle("自动拉黑：高危攻击直接永久封禁", 0)
	changed := false
	now := time.Now()
	for _, c := range cands {
		// 白名单过滤：复用拦截快照 CIDR 匹配语义（白名单可含网段，不能只精确比对）。
		if e.shield.InWhitelist(c.IP) {
			continue
		}
		// 新增封禁到期时间：攻击档直接永久（TTL 不适用，宁可误杀绝不放过）；
		// 其余档 TTL=0 → 永久（nil）。
		title := truncateTitle(fmt.Sprintf(tempTitleFmt, strings.TrimSpace(cfg.window), c.Threshold), 0)
		var expInsert *time.Time
		if c.Tier == banTierAttack {
			title = attackTitle
		} else if ttl > 0 {
			t := now.Add(ttl)
			expInsert = &t
		}
		cur, err := st.GetByIP(c.IP)
		switch {
		case errors.Is(err, ErrIPNotExists):
			// 态一：无记录 → 新增入库（warn_times=1 起算，block_type=档内真实拦截类别）。
			if _, err := st.BanInsert(c.IP, title, c.TopType, expInsert, now); err != nil {
				log.Warn("shield: 自动拉黑入库失败", "ip", c.IP, "err", err.Error())
				continue
			}
			changed = true
			log.Info("shield: 自动拉黑新增封禁", "ip", c.IP,
				"block_type", int(c.TopType), "tier", int(c.Tier), "hits", c.Total, "ttl", cfg.ttl)
		case err != nil:
			log.Warn("shield: 自动拉黑查询条目失败", "ip", c.IP, "err", err.Error())
		case banEntryActive(cur, now):
			// 态二：活跃条目（未删未过期）→ 跳过（已在封禁中，不重复计数）。
		default:
			// 态三/四：软删/已过期条目 → 恢复续封（warn_times+1）。攻击档直永久策略
			// 覆盖续封时长：恢复即转永久（expires_at 置 NULL、title 改攻击档定式），
			// 防止真实攻击者蹭旧过期条目只拿到 TTL×10 限时续封逃过永久封禁；
			// 其余档解封时间=TTL×10，累计入狱达 SHIELD_AUTO_BAN_REPEAT_LIMIT 的
			// 限时封禁转永久（RestoreBan 内聚，TTL=0 恢复后仍永久）。
			if c.Tier == banTierAttack {
				toPerm, err := st.RestoreBanToPermanent(c.IP, attackTitle, now)
				if err != nil {
					log.Warn("shield: 自动拉黑攻击档转永久失败", "ip", c.IP, "err", err.Error())
					continue
				}
				changed = true
				log.Info("shield: 自动拉黑恢复续封（攻击档直永久）", "ip", c.IP,
					"block_type", int(c.TopType), "tier", int(c.Tier), "warn_times", cur.WarnTimes+1, "to_permanent", toPerm)
				continue
			}
			var expRestore *time.Time
			if ttl > 0 {
				t := now.Add(ttl * 10)
				expRestore = &t
			}
			perm, err := st.RestoreBan(c.IP, expRestore, now, cfg.repeatLimit)
			if err != nil {
				log.Warn("shield: 自动拉黑续封失败", "ip", c.IP, "err", err.Error())
				continue
			}
			changed = true
			log.Info("shield: 自动拉黑恢复续封", "ip", c.IP,
				"block_type", int(c.TopType), "tier", int(c.Tier), "warn_times", cur.WarnTimes+1, "to_permanent", perm)
		}
	}
	// 有变更才重建拦截快照（一次整轮重建，与管理面 rebuildAfter 同语义）。
	if changed {
		e.shield.rebuildAfter("auto_ban")
	}
}

// aggregateAutoBanCandidates Go 侧聚合（决策 12 分档版，纯函数可测）：
// 输入候选 SQL 行（client_ip/block_type/cnt），按 IP 分档（banTierOf）合计拦截次数，
// 各档判各自阈值（攻击档=attackBanThreshold，爬虫档=crawlerThreshold，
// 通用档=genericThreshold）；跨档次数不合并。同一 IP 多档达标取最严档
// （攻击 > 爬虫 > 通用）。拉黑类别取该 IP 达标档内次数最多者，并列取枚举值小者。
// 过滤口径：
//   - 仅精确 IP（net.ParseIP 校验；CIDR/非法串排除——拦截事件来源本无 CIDR，双保险）；
//   - 仅真实拦截类别 2-10（决策 11：排除 block_type=1 黑名单自我拦截，防循环封禁；
//     SQL 已排除，此处 Go 侧双保险）；0/11 等黑名单语境枚举不参与聚合。
func aggregateAutoBanCandidates(rows []map[string]any, genericThreshold, crawlerThreshold int64) []banCandidate {
	// tiers[ip][tier] = 档内合计；counts[ip][tier][bt] = 档内类别计数。
	totals := make(map[string]map[banTier]int64)
	counts := make(map[string]map[banTier]map[BlockType]int64)
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
		tier := banTierOf(bt)
		if tier == 0 {
			continue // 1 自我拦截（决策 11）与 0/11 非拦截语境不参与聚合
		}
		if totals[ip] == nil {
			totals[ip] = make(map[banTier]int64)
			counts[ip] = make(map[banTier]map[BlockType]int64)
		}
		totals[ip][tier] += cnt
		if counts[ip][tier] == nil {
			counts[ip][tier] = make(map[BlockType]int64)
		}
		counts[ip][tier][bt] += cnt
	}
	tierThreshold := func(t banTier) int64 {
		switch t {
		case banTierAttack:
			return attackBanThreshold
		case banTierCrawler:
			return crawlerThreshold
		default:
			return genericThreshold
		}
	}
	out := make([]banCandidate, 0, len(totals))
	for ip, byTier := range totals {
		// 达标档位取最严（枚举值小者优先：攻击 1 < 爬虫 2 < 通用 3）。
		hit := banTier(0)
		for t, total := range byTier {
			if total >= tierThreshold(t) && (hit == 0 || t < hit) {
				hit = t
			}
		}
		if hit == 0 {
			continue // 所有档均未达阈值
		}
		// 档内次数最多者；并列取枚举值小者（规则定死可测）。
		top := BlockType(0)
		var maxCnt int64 = -1
		for bt, n := range counts[ip][hit] {
			if n > maxCnt || (n == maxCnt && bt < top) {
				top, maxCnt = bt, n
			}
		}
		out = append(out, banCandidate{
			IP: ip, Total: byTier[hit], TopType: top,
			Tier: hit, Threshold: tierThreshold(hit),
		})
	}
	// 按 IP 排序：输出与处理顺序确定（可测、日志可读）。
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}
