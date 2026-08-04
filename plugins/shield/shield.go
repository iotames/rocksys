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
)

// bucketsMax 限流桶数量上限，LRU 淘汰防内存膨胀（§9.3）。
const bucketsMax = 10000

// defaultRulesDir WAF 规则外置目录默认值（相对工作目录；缺失时回退内嵌 rules/）。
const defaultRulesDir = "rules"

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
	enabled     bool
	ipBlacklist string // 逗号分隔，支持精确 IP 与 CIDR
	ipWhitelist string // 逗号分隔，支持精确 IP 与 CIDR
	rps         int    // 0 = 不限流
	burst       int
	limitBy     string // 限流维度，当前仅支持 "ip"

	// pathRules 路径/UA 规则：无配置项，代码注入（SetPathRules），Start 时并入快照。
	pathRules []PathRule

	// WAF 检测配置项（§9.6）：全部默认关闭，Start 时编译进快照。
	wafSQLEnabled     bool   // SHIELD_WAF_SQL_INJECTION（*bool）
	wafXSSEnabled     bool   // SHIELD_WAF_XSS（*bool）
	wafPathEnabled    bool   // SHIELD_WAF_PATH_TRAVERSAL（*bool）
	wafRiskPathOn     bool   // SHIELD_WAF_RISK_PATH（*bool）风险路径检测开关
	wafCrawlerOn      bool   // SHIELD_WAF_CRAWLER_UA（*bool）爬虫 UA 拦截开关
	wafRiskPaths      string // SHIELD_WAF_RISK_PATHS（*string）追加风险路径
	allowMethods      string // SHIELD_ALLOW_METHODS（*string）方法白名单
	maxBodySize       int    // SHIELD_MAX_BODY_SIZE（*int）请求体上限字节
	rulesDir          string // SHIELD_RULES_DIR（*string）WAF 规则外置目录（优先加载，嵌入兜底）

	mu       sync.Mutex   // 保护 pathRules 读写
	snapshot atomic.Value // *shieldSnapshot
}

// shieldSnapshot 不可变运行态快照（整体重建后原子替换）。
type shieldSnapshot struct {
	enabled     bool
	ipBlacklist *ipSet
	ipWhitelist *ipSet
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
func New(cfgMgr conf.Manager) (*Shield, error) {
	s := &Shield{cfg: cfgMgr}
	items := []struct {
		pval   any
		name   string
		defval string
		title  string
	}{
		{&s.enabled, "SHIELD_ENABLED", "true", "是否启用 L1 防护"},
		{&s.ipBlacklist, "SHIELD_IP_BLACKLIST", "", "IP 黑名单（逗号分隔，支持 CIDR）"},
		{&s.ipWhitelist, "SHIELD_IP_WHITELIST", "", "IP 白名单（逗号分隔，支持 CIDR）"},
		{&s.rps, "SHIELD_RATE_LIMIT_RPS", "0", "限流速率（每秒请求数，0=不限流）"},
		{&s.burst, "SHIELD_RATE_LIMIT_BURST", "0", "限流突发容量"},
		{&s.limitBy, "SHIELD_RATE_LIMIT_BY", "ip", "限流维度（当前仅支持 ip）"},
		{&s.wafSQLEnabled, "SHIELD_WAF_SQL_INJECTION", "false", "SQL 注入检测（URL 路径/查询串，组合特征，默认关闭）"},
		{&s.wafXSSEnabled, "SHIELD_WAF_XSS", "false", "XSS 检测（URL 查询串，默认关闭）"},
		{&s.wafPathEnabled, "SHIELD_WAF_PATH_TRAVERSAL", "false", "路径遍历检测（默认关闭）"},
		{&s.wafRiskPathOn, "SHIELD_WAF_RISK_PATH", "false", "风险路径检测（内置 + SHIELD_WAF_RISK_PATHS 追加，默认关闭）"},
		{&s.wafCrawlerOn, "SHIELD_WAF_CRAWLER_UA", "false", "爬虫/扫描器 UA 拦截（特征见规则文件 crawler_ua.txt，默认关闭）"},
		{&s.wafRiskPaths, "SHIELD_WAF_RISK_PATHS", "", "追加风险路径（逗号分隔，需先开启 SHIELD_WAF_RISK_PATH）"},
		{&s.allowMethods, "SHIELD_ALLOW_METHODS", "", "HTTP 方法白名单（逗号分隔，空=不限）"},
		{&s.maxBodySize, "SHIELD_MAX_BODY_SIZE", "0", "请求体大小上限（字节，0=不限）"},
		{&s.rulesDir, "SHIELD_RULES_DIR", "rules", "WAF 规则外置目录（优先加载，缺失回退内嵌 rules/）"},
	}
	for _, it := range items {
		if err := cfgMgr.Register(it.pval, it.name, it.defval, it.title); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// SetPathRules 注入路径/UA 规则并立即重建快照生效（规则无配置项，代码注入）。
func (s *Shield) SetPathRules(rules []PathRule) {
	s.mu.Lock()
	s.pathRules = append([]PathRule(nil), rules...)
	s.mu.Unlock()
	_ = s.Start(nil)
}

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

	// 加载 WAF 规则文件（外置目录优先、嵌入兜底）。
	rulesDir := s.rulesDir
	if rulesDir == "" {
		rulesDir = defaultRulesDir
	}
	loader, err := newRuleLoader(rulesDir)
	if err != nil {
		return err
	}
	rs, err := loader.load()
	if err != nil {
		return err
	}

	snap := &shieldSnapshot{
		enabled:     s.enabled,
		ipBlacklist: newIPSet(splitList(s.ipBlacklist)),
		ipWhitelist: newIPSet(splitList(s.ipWhitelist)),
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
			riskPaths:       mergeRiskPaths(rs.RiskPaths, s.wafRiskPaths),
		},
	}
	s.snapshot.Store(snap)
	return nil
}

// Stop 清理资源（本挂件无特别资源）。
func (s *Shield) Stop() error { return nil }

// Handle 处理请求（§9.2 流程）。
// 返回 true 表示继续转发链；返回 false 表示已写入响应并中断链。
func (s *Shield) Handle(ctx *chain.Context) (next bool) {
	snap := s.current()
	if snap == nil || !snap.enabled {
		return true
	}
	ip := clientIP(ctx.R)
	if ip == "" {
		return true
	}
	if snap.ipWhitelist.contains(ip) {
		return true
	}
	if snap.ipBlacklist.contains(ip) {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		return false
	}
	// ★ WAF 安全检测（§9.6，默认全部关闭；开启后位于 IP 检查之后、路径/UA 规则之前）
	if !runWAF(ctx, snap.waf) {
		return false
	}
	deny, allow := snap.matchRules(ctx.R.URL.Path, ctx.R.UserAgent())
	if deny {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		return false
	}
	if allow {
		return true
	}
	ok, wait := snap.limiter.allow(snap.limitKey(ip), time.Now())
	if !ok {
		ctx.W.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(wait.Seconds())), 10))
		http.Error(ctx.W, "too many requests", http.StatusTooManyRequests)
		return false
	}
	return true
}

func (s *Shield) current() *shieldSnapshot {
	if v := s.snapshot.Load(); v != nil {
		return v.(*shieldSnapshot)
	}
	return nil
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

// clientIP 从 RemoteAddr 提取客户端 IP。
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
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
func runWAF(ctx *chain.Context, waf *wafSnapshot) bool {
	if waf == nil {
		return true
	}
	// 1. 方法白名单（空 = 不限）
	if len(waf.allowMethods) > 0 {
		if _, ok := waf.allowMethods[strings.ToUpper(ctx.R.Method)]; !ok {
			http.Error(ctx.W, "method not allowed", http.StatusForbidden)
			return false
		}
	}
	// 2. 请求体大小预检（仅 ContentLength；-1 表示 chunked/未知，跳过，见 §9.6 边界）
	if waf.maxBodySize > 0 && ctx.R.ContentLength > waf.maxBodySize {
		http.Error(ctx.W, "request body too large", http.StatusRequestEntityTooLarge)
		return false
	}
	// 3. 风险路径（文件风险路径 + 配置追加）
	if waf.riskPathEnabled && len(waf.riskPaths) > 0 && waf.matchRiskPath(ctx.R.URL.Path) {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		return false
	}
	// 4. 路径遍历（原始转义路径 + 解码路径双路）
	if waf.pathTravEnabled && waf.hasPathTraversal(ctx.R.URL.EscapedPath(), ctx.R.URL.Path) {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		return false
	}
	// 5. SQL 注入
	if waf.sqlEnabled && waf.hasSQL(ctx.R.URL.Path, ctx.R.URL.RawQuery) {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		return false
	}
	// 6. XSS
	if waf.xssEnabled && waf.hasXSS(ctx.R.URL.RawQuery) {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		return false
	}
	// 7. 爬虫/扫描器 UA
	if waf.crawlerEnabled && waf.hasCrawlerUA(ctx.R.UserAgent()) {
		http.Error(ctx.W, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}
