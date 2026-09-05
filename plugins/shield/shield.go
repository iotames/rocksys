// Package shield L1 防护（转发链中间件）。
package shield

import (
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/hotswap"
	"rocksys/internal/netutil"

	"github.com/iotames/easyserver/log"
)

// bucketsMax 限流桶数量上限，LRU 淘汰防内存膨胀（§9.3）。
const bucketsMax = 10000

// PathRule 路径/UA 规则（glob 风格，* 通配任意串，? 通配单字符）。
type PathRule struct {
	Pattern string // 匹配目标：URL 路径或 User-Agent
	Action  string // "allow" | "deny"
}

// Shield L1 防护中间件：IP 黑白名单、路径/UA 规则、令牌桶限流。
//
// 运行态全部存于不可变快照（atomic.Value 承载，§6.2），Start 整体重建后原子替换，
// 保证与在途请求的 Handle 并发安全。
type Shield struct {
	cfg conf.Manager

	// 配置项挂件字段（构造时由 cfgMgr.Register 注册，Start 时读取重建快照）。
	// ★ conf.Manager.Register 仅支持 *string/*int/*bool，黑白名单用字符串逗号分隔承载。
	// ★ SHIELD_ENABLED 是挂载开关（配置中心唯一真源）：挂载即拦截，内部不再读取本字段。
	enabled     bool
	ipWhitelist string // 逗号分隔，支持精确 IP 与 CIDR
	rps         int    // 0 = 不限流
	burst       int
	limitBy     string // 限流维度，当前仅支持 "ip"

	// pathRules 路径/UA 规则：无配置项，代码注入（SetPathRules），Start 时并入快照。
	pathRules []PathRule

	// WAF 检测配置项（§9.6）：全部默认关闭，Start 时编译进快照。
	wafSQLEnabled  bool   // SHIELD_WAF_SQL_INJECTION（*bool）
	wafXSSEnabled  bool   // SHIELD_WAF_XSS（*bool）
	wafPathEnabled bool   // SHIELD_WAF_PATH_TRAVERSAL（*bool）
	wafRiskPathOn  bool   // SHIELD_WAF_RISK_PATH（*bool）风险路径检测开关
	wafCrawlerOn   bool   // SHIELD_WAF_CRAWLER_UA（*bool）爬虫 UA 拦截开关
	wafRiskPaths   string // SHIELD_WAF_RISK_PATHS（*string）追加风险路径
	allowMethods   string // SHIELD_ALLOW_METHODS（*string）方法白名单
	maxBodySize    int    // SHIELD_MAX_BODY_SIZE（*int）请求体上限字节

	mu       sync.Mutex   // 保护 pathRules 读写
	snapshot atomic.Value // *shieldSnapshot

	hub *hotswap.ScriptHub // 外挂规则统一内容中枢（nil = 未注入，规则回落 ScriptDir 直读）

	// WAF 拦截监控统计：
	// counter 常驻内存滑动窗口计数（无 DB 也工作，供实时看板）；
	// recorder 落库记录器（可选，setter 注入，nil 时 Record 静默 no-op）。
	counter  *eventCounter
	recorder *EventRecorder

	// 动态 IP 黑白名单（DB 持久化，WAF 方案 §5.3）：
	// ipBlackDB/ipWhiteDB 数据访问层（setter 注入，nil = DB 未配置，回落仅外挂/.env）；
	// dbHits 黑名单命中计数攒批（id → 原子增量，TTL 循环定时 flush 落库，热路径零 DB 查询）；
	// TTL 兜底刷新（默认 60s）：覆盖管理面变更通知缺失的异常场景，顺带 flush hit_count。
	ipBlackDB ipListStore // DB 黑名单数据访问（nil = 未注入，回落仅外挂文件）
	ipWhiteDB ipListStore // DB 白名单数据访问（nil = 未注入，回落仅 .env 配置）
	dbHits    sync.Map    // int64 条目 id → *atomic.Int64 增量
	ttlMu     sync.Mutex
	ttlStopCh chan struct{}
	ttlStart  bool
}

// ipListStore 动态黑白名单数据访问接口（实现见 IPListStore；测试可注入计数包装
// 断言热路径零 DB 查询——WAF 方案 §5.3 验证要求）。管理面 CRUD 亦经本接口。
type ipListStore interface {
	Table() string
	EnsureTable() error
	QueryActive(now time.Time) ([]ActiveIP, error)
	AddHitCount(id int64, delta int) error
	Insert(ip, title string, blockType BlockType, expiresAt *time.Time, now time.Time) (int64, error)
	Update(id int64, title string, blockType BlockType, expiresAt *time.Time, now time.Time) error
	SoftDelete(id int64, now time.Time) error
	Restore(id int64, now time.Time) error
	Import(ips []string, title string, blockType BlockType, now time.Time) (imported, skipped int, err error)
	List(f ListFilter, now time.Time) (rows []map[string]any, total int64, err error)
	// 封禁语义（黑名单专属；IP_BLACKLIST_PLAN §3.4/§3.7，白名单实现返回错误）。
	BanInsert(ip, title string, blockType BlockType, expiresAt *time.Time, now time.Time) (int64, error)
	GetByIP(ip string) (*BanEntry, error)
	// repeatLimit 累计入狱转永久阈值（人工=banWarnTimesLimit；自动=SHIELD_AUTO_BAN_REPEAT_LIMIT，0=永不转永久）。
	RestoreBan(ip string, expiresAt *time.Time, now time.Time, repeatLimit int) (toPermanent bool, err error)
	Jail(now time.Time, limit int) (rows []map[string]any, total int64, err error)
}

// dbTTLInterval DB 黑白名单快照兜底刷新间隔（WAF 方案 §5.3；§8 记后续参数化）。
const dbTTLInterval = 60 * time.Second

// shieldSnapshot 不可变运行态快照（整体重建后原子替换）。
// dbBlackIDs 精确 IP → DB 黑名单条目 id（命中时异步累加 hit_count；
// 仅精确 IP 命中可归因单条，CIDR 子网命中不累加）。
type shieldSnapshot struct {
	ipBlacklist *ipSet
	ipWhitelist *ipSet
	dbBlackIDs  map[string]int64
	pathRules   []PathRule
	limitBy     string
	limiter     *RateLimiter
	waf         *wafSnapshot // WAF 检测编译态（§9.6）
}

// ipSet 编译后的 IP 匹配集：精确 + CIDR。
type ipSet struct {
	exact map[string]struct{}
	nets  []net.IPNet
}

func newIPSet(list []string) *ipSet {
	s := &ipSet{exact: make(map[string]struct{})}
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ipnet, err := net.ParseCIDR(item); err == nil {
			s.nets = append(s.nets, *ipnet)
			continue
		}
		if net.ParseIP(item) != nil {
			s.exact[item] = struct{}{}
		}
	}
	return s
}

func (s *ipSet) contains(ip string) bool {
	if _, ok := s.exact[ip]; ok {
		return true
	}
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	for i := range s.nets {
		if s.nets[i].Contains(p) {
			return true
		}
	}
	return false
}

// RateLimiter 令牌桶限流器：每 key 独立桶，桶数超过上限时按 LRU 批量淘汰（§9.1）。
type RateLimiter struct {
	rps     int
	burst   int
	buckets sync.Map     // key → *tokenBucket
	count   atomic.Int64 // 当前桶数
}

type tokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	lastRefill time.Time
	lastAccess atomic.Int64 // unix 纳秒，供 LRU 淘汰
}

func newRateLimiter(rps, burst int) *RateLimiter {
	if burst <= 0 {
		burst = 1
	}
	return &RateLimiter{rps: rps, burst: burst}
}

// Allow 检查 key 是否放行（消费 1 个 token）；超限返回 false。
func (rl *RateLimiter) Allow(key string) bool {
	ok, _ := rl.allow(key, time.Now())
	return ok
}

// allow 内部实现，返回是否放行及重新放行前的等待时长（超限时）。
func (rl *RateLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	if rl.rps <= 0 {
		return true, 0
	}
	tb := rl.loadOrStore(key, now)
	tb.mu.Lock()
	defer tb.mu.Unlock()
	if elapsed := now.Sub(tb.lastRefill).Seconds(); elapsed > 0 {
		tb.tokens += elapsed * float64(rl.rps)
		if tb.tokens > float64(rl.burst) {
			tb.tokens = float64(rl.burst)
		}
		tb.lastRefill = now
	}
	if tb.tokens < 1 {
		need := 1 - tb.tokens
		wait := time.Duration(need / float64(rl.rps) * float64(time.Second))
		if wait < time.Second {
			wait = time.Second
		}
		return false, wait
	}
	tb.tokens--
	return true, 0
}

func (rl *RateLimiter) loadOrStore(key string, now time.Time) *tokenBucket {
	if v, ok := rl.buckets.Load(key); ok {
		tb := v.(*tokenBucket)
		tb.lastAccess.Store(now.UnixNano())
		return tb
	}
	tb := &tokenBucket{tokens: float64(rl.burst), lastRefill: now}
	tb.lastAccess.Store(now.UnixNano())
	if actual, loaded := rl.buckets.LoadOrStore(key, tb); loaded {
		actual.(*tokenBucket).lastAccess.Store(now.UnixNano())
		return actual.(*tokenBucket)
	}
	if rl.count.Add(1) > bucketsMax {
		rl.evict()
	}
	return tb
}

// evict LRU 批量淘汰：超出上限时淘汰最久未访问的桶，回收至上限的 3/4。
// 仅在桶数超限时触发，O(n log n) 成本可摊薄到各请求。
func (rl *RateLimiter) evict() {
	target := int64(bucketsMax) * 3 / 4
	type entry struct {
		key        string
		lastAccess int64
	}
	var entries []entry
	rl.buckets.Range(func(k, v any) bool {
		tb := v.(*tokenBucket)
		entries = append(entries, entry{k.(string), tb.lastAccess.Load()})
		return true
	})
	if int64(len(entries)) <= target {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].lastAccess < entries[j].lastAccess })
	toRemove := int64(len(entries)) - target
	for i := int64(0); i < toRemove; i++ {
		rl.buckets.Delete(entries[i].key)
	}
	rl.count.Add(-toRemove)
}

// New 构造 Shield 并注册全部配置项（§9.4）。
// hub 为可选参数（variadic）：装配方注入 ScriptHub 统一内容中枢后，
// 规则子目录（rules/）注册进中枢并订阅，外挂规则文件变更 ≤3s 自动重建快照；
// 未注入（nil/缺省）时规则读取回落 ScriptDir 直读（与旧行为一致，测试兼容）。
func New(cfgMgr conf.Manager, hubs ...*hotswap.ScriptHub) (*Shield, error) {
	s := &Shield{cfg: cfgMgr, counter: &eventCounter{}}
	if len(hubs) > 0 {
		s.hub = hubs[0]
	}
	items := []struct {
		pval   any
		name   string
		defval string
		title  string
	}{
		{&s.enabled, "SHIELD_ENABLED", "false", "是否启用 L1 防护（false=不挂载；true=挂载并拦截）"},
		{&s.ipWhitelist, "SHIELD_IP_WHITELIST", "", "IP 白名单（逗号分隔，支持 CIDR）"},
		{&s.rps, "SHIELD_RATE_LIMIT_RPS", "0", "限流速率（每秒请求数，0=不限流）"},
		{&s.burst, "SHIELD_RATE_LIMIT_BURST", "0", "限流突发容量"},
		{&s.limitBy, "SHIELD_RATE_LIMIT_BY", "ip", "限流维度（当前仅支持 ip）"},
		{&s.wafSQLEnabled, "SHIELD_WAF_SQL_INJECTION", "false", "SQL 注入检测（URL 路径/查询串，组合特征，默认关闭）"},
		{&s.wafXSSEnabled, "SHIELD_WAF_XSS", "false", "XSS 检测（URL 查询串，默认关闭）"},
		{&s.wafPathEnabled, "SHIELD_WAF_PATH_TRAVERSAL", "false", "路径遍历检测（默认关闭）"},
		{&s.wafRiskPathOn, "SHIELD_WAF_RISK_PATH", "false", "风险路径检测（内置 + SHIELD_WAF_RISK_PATHS 追加，默认关闭）"},
		{&s.wafCrawlerOn, "SHIELD_WAF_CRAWLER_UA", "false", "UA黑名单开关（拦截爬虫/扫描器 UA 与空 UA，特征见规则文件 crawler_ua.txt，默认关闭；命中 ua_whitelist.txt 白名单的 UA 在黑名单判定前放行）"},
		{&s.wafRiskPaths, "SHIELD_WAF_RISK_PATHS", "", "追加风险路径（逗号分隔，需先开启 SHIELD_WAF_RISK_PATH）"},
		{&s.allowMethods, "SHIELD_ALLOW_METHODS", "", "HTTP 方法白名单（逗号分隔，空=不限）"},
		{&s.maxBodySize, "SHIELD_MAX_BODY_SIZE", "0", "请求体大小上限（字节，0=不限）"},
	}
	for _, it := range items {
		if err := cfgMgr.Register(it.pval, it.name, it.defval, it.title); err != nil {
			return nil, err
		}
	}
	if s.hub != nil {
		if err := s.registerRulesHub(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// registerRulesHub 将规则子目录（rules/）注册进 ScriptHub 并订阅：
// 规则文件增/删/改 → 回调重建快照（复用 Start 重建逻辑，清空限流状态）。
// ★ shield 无论是否启用都注册（挂载前保证规则已是最新），无害。
func (s *Shield) registerRulesHub() error {
	loader, err := newRuleLoader(s.hub)
	if err != nil {
		return err
	}
	if err := s.hub.Register(ruleSubDir, loader.sd); err != nil {
		return err
	}
	return s.hub.Subscribe(ruleSubDir, func(relPath string) {
		if err := s.Start(nil); err != nil {
			log.Warn("shield: 外挂规则热更重建快照失败，保留旧快照", "file", relPath, "err", err.Error())
		}
	})
}

// SetPathRules 注入路径/UA 规则并立即重建快照生效（规则无配置项，代码注入）。
func (s *Shield) SetPathRules(rules []PathRule) {
	s.mu.Lock()
	s.pathRules = append([]PathRule(nil), rules...)
	s.mu.Unlock()
	_ = s.Start(nil)
}

// SetEventRecorder 注入拦截事件记录器（DB 就绪后由 main.go 装配调用，
// 未注入时拦截照常，只不落库（Record nil 安全 no-op）。
// 装配期调用（监听启动前），运行期只读，无需加锁。
func (s *Shield) SetEventRecorder(r *EventRecorder) { s.recorder = r }

// Recorder 返回拦截事件记录器（未注入时为 nil，admin 端点据此返回 503）。
func (s *Shield) Recorder() *EventRecorder { return s.recorder }

// Counter 返回内存滑动窗口计数器（常驻，无 DB 也工作）。
func (s *Shield) Counter() *eventCounter { return s.counter }

// Name 返回中间件名称。
func (s *Shield) Name() string { return "shield" }

// Slot 挂载位置：L1 防护在转发前最先执行（§9.1）。
func (s *Shield) Slot() chain.Slot { return chain.Head }

// Start 从配置项字段读取最新值，重建不可变快照并原子替换（§6.2/§6.3）。
// 规则文件（SQL/XSS/路径遍历/风险路径/爬虫 UA）在 Start 时经 ruleLoader 加载：
// 加载失败返回 error 并保留旧快照（实例继续以旧规则服务），符合"Start 失败保留旧快照"约定。
func (s *Shield) Start(cfg any) error {
	s.mu.Lock()
	rules := append([]PathRule(nil), s.pathRules...)
	s.mu.Unlock()

	limitBy := s.limitBy
	if limitBy == "" {
		limitBy = "ip"
	}

	// 加载 WAF 规则文件（注入 hub 时经统一缓存读取；否则 ScriptDir 直读。
	// 外置 HOT_SCRIPTS_DIR/rules 优先、嵌入兜底）。
	loader, err := newRuleLoader(s.hub)
	if err != nil {
		return err
	}
	rs, err := loader.load()
	if err != nil {
		return err
	}

	// 动态黑白名单合并：DB 表 ∪ 外挂/.env（WAF 方案 §5.3）。
	// ★ DB 查询仅在快照重建时发生（启动/管理面变更/TTL 兜底），请求热路径零 DB 查询。
	// DB 未注入或查询失败：回退仅外挂/.env，告警不阻断（防护不因 DB 异常降级为无）。
	blackList := rs.IPBlacklist
	dbBlackIDs := map[string]int64{}
	if s.ipBlackDB != nil {
		if actives, err := s.ipBlackDB.QueryActive(time.Now()); err != nil {
			log.Warn("shield: 加载 DB 黑名单失败，仅外挂文件生效", "err", err.Error())
		} else {
			for _, a := range actives {
				blackList = append(blackList, a.IP)
				dbBlackIDs[a.IP] = a.ID
			}
		}
	}
	whiteList := splitList(s.ipWhitelist)
	if s.ipWhiteDB != nil {
		if actives, err := s.ipWhiteDB.QueryActive(time.Now()); err != nil {
			log.Warn("shield: 加载 DB 白名单失败，仅 .env 配置生效", "err", err.Error())
		} else {
			for _, a := range actives {
				whiteList = append(whiteList, a.IP)
			}
		}
	}

	snap := &shieldSnapshot{
		ipBlacklist: newIPSet(blackList), // 外挂 rules/ip_blacklist.txt ∪ DB 表（精确 IP/CIDR）
		ipWhitelist: newIPSet(whiteList), // .env SHIELD_IP_WHITELIST ∪ DB 表
		dbBlackIDs:  dbBlackIDs,
		pathRules:   rules,
		limitBy:     limitBy,
		limiter:     newRateLimiter(s.rps, s.burst),
		waf: &wafSnapshot{
			sqlEnabled:      s.wafSQLEnabled,
			xssEnabled:      s.wafXSSEnabled,
			pathTravEnabled: s.wafPathEnabled,
			riskPathEnabled: s.wafRiskPathOn,
			crawlerEnabled:  s.wafCrawlerOn,
			allowMethods:    newMethodSet(s.allowMethods),
			maxBodySize:     int64(s.maxBodySize),
			sqlPatterns:     rs.SQLPatterns,
			xssPatterns:     rs.XSSPatterns,
			pathPatterns:    rs.PathTraversal,
			crawlerUAs:      rs.CrawlerUA,
			uaWhitelist:     rs.UAWhitelist,
			riskPaths:       mergeRiskPaths(rs.RiskPaths, s.wafRiskPaths),
		},
	}
	s.snapshot.Store(snap)
	return nil
}

// SetIPListStores 注入动态黑白名单数据访问层（main.go 装配，DB 就绪后调用）。
// nil 安全：任一 store 为 nil 表示该表未配置（回落仅外挂/.env）。
// 注入后立即重建一次快照（DB 数据即时生效，不等 TTL），并启动 TTL 兜底刷新循环。
// ★ 请求热路径零 DB 查询：store 仅在快照重建（本方法/管理面变更/TTL）与 hit_count flush 时访问。
func (s *Shield) SetIPListStores(black, white ipListStore) {
	s.ipBlackDB = black
	s.ipWhiteDB = white
	if black == nil && white == nil {
		return
	}
	// 幂等建表：失败告警不阻断（表缺失时查询失败 → 快照回退外挂/.env，防护不降级）。
	for _, st := range []ipListStore{black, white} {
		if st == nil {
			continue
		}
		if err := st.EnsureTable(); err != nil {
			log.Warn("shield: 黑白名单表初始化失败（拦截仍正常，仅外挂/.env 生效）", "table", st.Table(), "err", err.Error())
		}
	}
	if err := s.Start(nil); err != nil {
		log.Warn("shield: 注入 DB 黑白名单后重建快照失败", "err", err.Error())
	}
	s.startTTLLoop()
}

// Rebuild 主动重建快照（管理面增删改/导入成功后调用，DB 数据立即生效）。
func (s *Shield) Rebuild() error { return s.Start(nil) }

// bumpHit 黑名单精确命中 → 内存攒批计数（热路径，非阻塞；TTL 循环定时 flush 落库）。
func (s *Shield) bumpHit(id int64) {
	v, _ := s.dbHits.LoadOrStore(id, &atomic.Int64{})
	v.(*atomic.Int64).Add(1)
}

// startTTLLoop 启动 TTL 兜底刷新循环（幂等）：定时 flush hit_count + 重建快照。
func (s *Shield) startTTLLoop() {
	s.ttlMu.Lock()
	defer s.ttlMu.Unlock()
	if s.ttlStart {
		return
	}
	s.ttlStart = true
	s.ttlStopCh = make(chan struct{})
	go s.dbTTLLoop(s.ttlStopCh)
}

// dbTTLLoop 兜底刷新：覆盖"管理面变更未主动重建"的异常场景，顺带 flush hit_count。
func (s *Shield) dbTTLLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(dbTTLInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.flushHitCounts()
			if err := s.Start(nil); err != nil {
				log.Warn("shield: TTL 刷新 DB 黑白名单快照失败，保留旧快照", "err", err.Error())
			}
		}
	}
}

// flushHitCounts 将攒批的黑名单命中计数批量落库（Swap 归零防重复累加；
// 写失败回补计数，下轮再试——统计类指标容忍极小误差）。
func (s *Shield) flushHitCounts() {
	if s.ipBlackDB == nil {
		return
	}
	s.dbHits.Range(func(k, v any) bool {
		id := k.(int64)
		c := v.(*atomic.Int64)
		delta := c.Swap(0)
		if delta == 0 {
			return true
		}
		if err := s.ipBlackDB.AddHitCount(id, int(delta)); err != nil {
			log.Warn("shield: 黑名单命中计数落库失败，回补计数下轮重试", "id", id, "err", err.Error())
			c.Add(delta)
		}
		return true
	})
}

// Stop 清理资源：停止 TTL 兜底刷新循环（mgr.Shutdown 统一调用）。
func (s *Shield) Stop() error {
	s.ttlMu.Lock()
	ch := s.ttlStopCh
	s.ttlStopCh = nil
	s.ttlMu.Unlock()
	if ch != nil {
		close(ch)
	}
	return nil
}

// Handle 处理请求（§9.2 流程）。
// 返回 true 表示继续转发链；返回 false 表示已写入响应并中断链。
func (s *Shield) Handle(ctx *chain.Context) (next bool) {
	snap := s.current()
	if snap == nil {
		return true
	}
	ip := netutil.GetClientIP(ctx.R)
	if ip == "" {
		return true
	}
	if snap.ipWhitelist.contains(ip) {
		return true
	}
	if snap.ipBlacklist.contains(ip) {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		if id, ok := snap.dbBlackIDs[ip]; ok { // DB 精确条目命中 → 异步累加 hit_count（攒批，不阻塞）
			s.bumpHit(id)
		}
		s.recordEvent(ctx, BlockIPBlacklist, "ip_blacklist") // 拦截监控：记录后中断链
		return false
	}
	// ★ WAF 安全检测（§9.6，默认全部关闭；开启后位于 IP 检查之后、路径/UA 规则之前）
	if !s.runWAF(ctx, snap.waf) {
		return false
	}
	deny, allow := snap.matchRules(ctx.R.URL.Path, ctx.R.UserAgent())
	if deny {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		s.recordEvent(ctx, BlockPathRuleDeny, "path_rule") // 拦截监控：记录后中断链
		return false
	}
	if allow {
		return true
	}
	ok, wait := snap.limiter.allow(snap.limitKey(ip), time.Now())
	if !ok {
		ctx.W.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(wait.Seconds())), 10))
		http.Error(ctx.W, "too many requests", http.StatusTooManyRequests)
		s.recordEvent(ctx, BlockRateLimit, "rate_limit") // 拦截监控：记录后中断链
		return false
	}
	return true
}

// recordEvent 记录一次拦截：内存滑动窗口计数常开（无 DB 也工作，供实时看板），
// 落库经 recorder（nil 安全 no-op，未注入 DB 时不记录）。热路径调用：
// 内部只做计数 + 非阻塞入队（通道满丢弃），绝不阻塞转发。
func (s *Shield) recordEvent(ctx *chain.Context, bt BlockType, ruleHit string) {
	s.counter.Add(bt, time.Now())
	s.recorder.Record(ctx, bt, ruleHit)
}

func (s *Shield) current() *shieldSnapshot {
	if v := s.snapshot.Load(); v != nil {
		return v.(*shieldSnapshot)
	}
	return nil
}

// InBlacklist IP 是否命中当前生效黑名单（外挂 rules/ip_blacklist.txt ∪ DB 活跃条目，
// 与 Handle 拦截判定同源）。供管理端点标注 Top 攻击源 IP「是否在黑名单」用。
func (s *Shield) InBlacklist(ip string) bool {
	snap := s.current()
	return snap != nil && snap.ipBlacklist.contains(ip)
}

// InWhitelist IP 是否命中当前生效白名单（.env SHIELD_IP_WHITELIST ∪ DB 活跃条目，
// 支持精确 IP 与 CIDR 网段匹配，与 Handle 放行判定同源）。
// 供自动拉黑引擎入库前过滤白名单 IP 用（IP_BLACKLIST_PLAN §3.4）。
func (s *Shield) InWhitelist(ip string) bool {
	snap := s.current()
	return snap != nil && snap.ipWhitelist.contains(ip)
}

// splitList 逗号分隔解析：去空白、去空项。
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// matchRules 规则匹配：Pattern 依次与 URL 路径、User-Agent 匹配；deny 优先于 allow。
func (s *shieldSnapshot) matchRules(path, ua string) (deny, allow bool) {
	for _, r := range s.pathRules {
		if !matchGlob(r.Pattern, path) && !matchGlob(r.Pattern, ua) {
			continue
		}
		switch r.Action {
		case "deny":
			deny = true
		case "allow":
			allow = true
		}
	}
	return
}

// limitKey 限流键：当前仅支持按 IP 维度。
func (s *shieldSnapshot) limitKey(ip string) string {
	if s.limitBy != "ip" {
		return ip
	}
	return ip
}

// matchGlob glob 风格匹配：* 通配任意串（含空串），? 通配单字符。
func matchGlob(pattern, s string) bool {
	px, sx, star, mark := 0, 0, -1, 0
	for sx < len(s) {
		if px < len(pattern) && (pattern[px] == '?' || pattern[px] == s[sx]) {
			px++
			sx++
			continue
		}
		if px < len(pattern) && pattern[px] == '*' {
			star, mark = px, sx
			px++
			continue
		}
		if star >= 0 {
			px, mark = star+1, mark+1
			sx = mark
			continue
		}
		return false
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

// 编译期断言：Shield 满足 hotswap.MiddlewareLifecycle。
var _ hotswap.MiddlewareLifecycle = (*Shield)(nil)

// runWAF 执行 WAF 检测链（§9.6）。返回 true 放行；false 表示已写拦截响应。
// 检测顺序：方法白名单 → 体积限制 → 风险路径 → 路径遍历 → SQL 注入 → XSS → 爬虫 UA。
// 除方法白名单/体积限制按配置值决定外，其余检测项各自独立开关（全部默认关闭）。
// waf 为 nil（快照未构建）时直接放行。
// 拦截监控：每个拦截点在写响应后调用 recordEvent 记录（不改拦截判定，仅追加记录）。
func (s *Shield) runWAF(ctx *chain.Context, waf *wafSnapshot) bool {
	if waf == nil {
		return true
	}
	// 1. 方法白名单（空 = 不限）
	if len(waf.allowMethods) > 0 {
		if _, ok := waf.allowMethods[strings.ToUpper(ctx.R.Method)]; !ok {
			http.Error(ctx.W, "method not allowed", http.StatusForbidden)
			s.recordEvent(ctx, BlockMethodNotAllowed, "method_whitelist")
			return false
		}
	}
	// 2. 请求体大小预检（仅 ContentLength；-1 表示 chunked/未知，跳过，见 §9.6 边界）
	if waf.maxBodySize > 0 && ctx.R.ContentLength > waf.maxBodySize {
		http.Error(ctx.W, "request body too large", http.StatusRequestEntityTooLarge)
		s.recordEvent(ctx, BlockBodyTooLarge, "max_body_size")
		return false
	}
	// 3. 风险路径（文件风险路径 + 配置追加）
	if waf.riskPathEnabled && len(waf.riskPaths) > 0 && waf.matchRiskPath(ctx.R.URL.Path) {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		s.recordEvent(ctx, BlockRiskPath, "risk_path")
		return false
	}
	// 4. 路径遍历（原始转义路径 + 解码路径双路）
	if waf.pathTravEnabled && waf.hasPathTraversal(ctx.R.URL.EscapedPath(), ctx.R.URL.Path) {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		s.recordEvent(ctx, BlockPathTraversal, "path_traversal")
		return false
	}
	// 5. SQL 注入
	if waf.sqlEnabled && waf.hasSQL(ctx.R.URL.Path, ctx.R.URL.RawQuery) {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		s.recordEvent(ctx, BlockSQLInjection, "sql_pattern")
		return false
	}
	// 6. XSS
	if waf.xssEnabled && waf.hasXSS(ctx.R.URL.RawQuery) {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		s.recordEvent(ctx, BlockXSS, "xss_pattern")
		return false
	}
	// 7. 爬虫/扫描器 UA（UA黑名单）。白名单（rules/ua_whitelist.txt）优先：
	// 命中白名单的 UA 在黑名单判定前放行，仅豁免本步（其余检测与 IP 黑白名单照常）；
	// 空 UA 不命中任何非空白名单模式，仍照拦。
	if waf.crawlerEnabled && !waf.uaWhitelisted(ctx.R.UserAgent()) && waf.hasCrawlerUA(ctx.R.UserAgent()) {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		s.recordEvent(ctx, BlockCrawlerUA, "crawler_ua")
		return false
	}
	return true
}
