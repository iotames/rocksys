// Package dispatch L2 路由分发（转发链中间件）。
//
// 依据 DEV_HANDBOOK.md 第 10 章实现：URI 前缀路由表 → 目标 upstream；
// 未命中 → 不写入 Target（Adapter 回退默认 upstream）。
// v2 增强（批次10）：前缀可指向【节点组】——多节点 + 平滑加权轮询 + 主动健康检查 + 高优节点优先。
package dispatch

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/hotswap"

	"github.com/iotames/easyserver/log"
)

// 编译期断言：Dispatch 实现 hotswap.MiddlewareLifecycle。
var _ hotswap.MiddlewareLifecycle = (*Dispatch)(nil)

// Rule 一条路由规则：Prefix 匹配的 URI 前缀（支持 :param / * 通配），Nodes 为转发目标节点组。
type Rule struct {
	Prefix      string       // 匹配 pattern，必须以 "/" 开头（如 /api/order/、/api/order/:id、/api/*）
	Nodes       []*Node      // 上游节点组（≥1）
	HealthCheck *HealthCheck // 主动健康检查（nil = 不探活，所有节点视为健康）
	Algo        string       // 负载均衡算法：roundrobin（默认）| chash
	ChashKey    string       // chash key 提取方式（$remote_addr 默认 / $http_<h> / $cookie_<c>）
	rr          *rrState     // 平滑加权轮询状态（与 Nodes 等长）
}

// RouteTable 前缀路由表：有序列表（最长前缀优先）+ Radix Tree 路由引擎（子组件）。
// router 为 nil 时回退线性前缀扫描（向后兼容）。
type RouteTable struct {
	rules  []*Rule
	router *Router
}

// keyPathParams DataFlow 中路径参数（:param 捕获）的 KV key。
const keyPathParams = "rocksys:path_params"

// MatchParams 匹配路径，返回命中的规则与捕获的参数（:param）。未命中返回 (nil, nil)。
func (rt *RouteTable) MatchParams(path string) (*Rule, map[string]string) {
	if rt.router != nil {
		return rt.router.match(path)
	}
	// 回退线性前缀扫描：无参数捕获。
	rule, ok := rt.Match(path)
	if !ok {
		return nil, nil
	}
	return rule, nil
}

// Dispatch L2 路由分发中间件（chain.Middle 槽位）。
// 运行态（RouteTable）存于不可变快照，经 atomic.Value 原子替换，保证 Start 与在途 Handle 并发安全。
type Dispatch struct {
	cfg   conf.Manager
	rules string       // DISPATCH_RULES 配置字符串（*string 注册，easyconf 自动写入）
	rt    atomic.Value // 持有 *RouteTable 不可变快照
}

// New 创建路由分发中间件并注册 DISPATCH_RULES 配置项。
func New(cfg conf.Manager) *Dispatch {
	d := &Dispatch{cfg: cfg}
	d.rt.Store(&RouteTable{})
	if cfg != nil {
		// 注册字符串指针：easyconf 写入配置值时自动更新 d.rules。
		_ = cfg.Register(&d.rules, "DISPATCH_RULES", "",
			"路由规则（<Prefix>=<spec>，逗号分隔。spec=<node>[;<node>...][@interval@timeout@path]，"+
				"node=http(s)://host[:port][|w=权重][|p=0高优/1备份]）",
			"示例：/api/order/=http://o1:9001;http://o2:9001|w=2@10s@2s@/healthz",
			"旧格式 /api/=http://host:port 仍兼容（单节点）")
	}
	return d
}

// Name 返回中间件名称。
func (d *Dispatch) Name() string { return "dispatch" }

// Slot 挂载位置：路由分发在防护之后、转发之前执行。
func (d *Dispatch) Slot() chain.Slot { return chain.Middle }

// Start 从配置字符串解析并重建 RouteTable，原子替换内部快照（§10.3 流程 C）。
// 解析失败时保留旧快照并返回 error；健康检查随新旧表启停，避免 goroutine 泄漏。
func (d *Dispatch) Start(_ any) error {
	rt, err := parseRules(d.rules)
	if err != nil {
		return err
	}
	old := d.rt.Load().(*RouteTable)
	old.stopHealthChecks()
	d.rt.Store(rt)
	rt.startHealthChecks()
	return nil
}

// Stop 停止当前路由表的健康检查探活 goroutine。
func (d *Dispatch) Stop() error {
	d.rt.Load().(*RouteTable).stopHealthChecks()
	return nil
}

// Handle 前缀路由分发：命中则写入 Target；未命中不动（Adapter 用默认 upstream）。
// 命中但节点组全部不可达（已配置健康检查）→ 写 503 并中断链，避免错误转发。
func (d *Dispatch) Handle(ctx *chain.Context) bool {
	rt := d.rt.Load().(*RouteTable)
	rule, params := rt.MatchParams(ctx.R.URL.Path)
	if rule == nil {
		return true
	}
	// 参数捕获（:param）：存入 DataFlow（供日志/后续中间件）并注入请求头（透传给上游）。
	if len(params) > 0 {
		ctx.DF.Set(keyPathParams, params)
		for k, v := range params {
			ctx.R.Header.Set("X-Route-Param-"+k, v)
		}
	}
	target, ok := rule.Select(ctx.R)
	if !ok {
		log.Warn("dispatch: no healthy node in upstream group", "prefix", rule.Prefix)
		http.Error(ctx.W, "no healthy upstream node", http.StatusServiceUnavailable)
		return false
	}
	ctx.DF.SetTarget(target)
	return true
}

// Match 匹配 path，返回命中的最长前缀所对应的规则，未命中返回 ok=false。
//
// 优先使用 Radix Tree 路由引擎（支持参数/通配）；引擎未构建时回退线性前缀扫描。
// 1. path 末尾补 "/"（统一处理）；例外：根路径 "/" 不补。
// 2. Prefix 也要求以 "/" 结尾才算完整段；例外：Prefix "/" 为兜底路由。
// 3. 从 rules 中找到匹配的最长前缀。
func (rt *RouteTable) Match(path string) (*Rule, bool) {
	if rt.router != nil {
		rule, _ := rt.router.match(path)
		return rule, rule != nil
	}
	p := path
	if p != "/" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	var best *Rule
	bestLen := -1
	for _, rule := range rt.rules {
		prefix := rule.Prefix
		if prefix != "/" && !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		if strings.HasPrefix(p, prefix) && len(prefix) > bestLen {
			bestLen = len(prefix)
			best = rule
		}
	}
	return best, best != nil
}

// parseRules 解析 DISPATCH_RULES：逗号分隔，每条 <Prefix>=<spec>。
// 空字符串或格式错误 → 返回空路由表（所有请求走默认 upstream）。
func parseRules(s string) (*RouteTable, error) {
	rt := &RouteTable{}
	if strings.TrimSpace(s) == "" {
		return rt, nil
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idx := strings.Index(part, "=")
		if idx <= 0 {
			return nil, fmt.Errorf("DISPATCH_RULES 条目格式错误（应为 <Prefix>=<spec>）: %q", part)
		}
		prefix := strings.TrimSpace(part[:idx])
		spec := strings.TrimSpace(part[idx+1:])
		if !strings.HasPrefix(prefix, "/") {
			return nil, fmt.Errorf("DISPATCH_RULES 前缀必须以 / 开头: %q", prefix)
		}
		rule, err := parseRule(prefix, spec)
		if err != nil {
			return nil, err
		}
		rt.rules = append(rt.rules, rule)
	}
	// 最长前缀优先：按归一化前缀长度降序排列（供线性回退与健康检查遍历）。
	sort.SliceStable(rt.rules, func(i, j int) bool {
		return normLen(rt.rules[i].Prefix) > normLen(rt.rules[j].Prefix)
	})
	// 构建 Radix Tree 路由引擎（子组件）：参数/通配/前缀匹配统一由引擎接管。
	rt.router = newRouter()
	for _, rule := range rt.rules {
		rt.router.insert(rule.Prefix, rule)
	}
	return rt, nil
}

// parseRule 解析单条规则：spec = <node>[;<node>...][@interval@timeout@path][|alg=<algo>][|key=<chash_key>]。
func parseRule(prefix, spec string) (*Rule, error) {
	if spec == "" {
		return nil, fmt.Errorf("DISPATCH_RULES 条目 %q 缺少 spec", prefix)
	}
	// 剥离规则级参数（从右往左）：|key= 在 |alg= 之后。
	algo := AlgoRoundRobin
	chashKey := defaultChashKey
	if i := strings.LastIndex(spec, "|key="); i >= 0 {
		chashKey = strings.TrimSpace(spec[i+len("|key="):])
		spec = spec[:i]
	}
	if i := strings.LastIndex(spec, "|alg="); i >= 0 {
		algo = strings.TrimSpace(spec[i+len("|alg="):])
		spec = spec[:i]
	}
	if algo != AlgoRoundRobin && algo != AlgoCHash {
		return nil, fmt.Errorf("DISPATCH_RULES 不支持的负载均衡算法 %q（仅支持 roundrobin/chash）: %q", algo, prefix)
	}
	nodesPart := spec
	var hc *HealthCheck
	if at := strings.Index(spec, "@"); at >= 0 {
		nodesPart = spec[:at]
		hcSpec := spec[at+1:]
		segs := strings.Split(hcSpec, "@")
		if len(segs) != 3 {
			return nil, fmt.Errorf("DISPATCH_RULES 健康检查格式错误（应为 @interval@timeout@path，分隔符@不可转义）: %q", hcSpec)
		}
		var err error
		hc, err = parseHealthCheck(segs[0], segs[1], segs[2])
		if err != nil {
			return nil, err
		}
	}
	nodes, err := parseNodes(nodesPart)
	if err != nil {
		return nil, err
	}
	rule := &Rule{Prefix: prefix, Nodes: nodes, HealthCheck: hc, Algo: algo, ChashKey: chashKey, rr: newRRState(len(nodes))}
	if hc == nil {
		// 未配置健康检查：全部节点视为健康（兼容旧版单节点语义）。
		for _, n := range nodes {
			n.healthy.Store(true)
		}
	}
	return rule, nil
}

// parseNodes 解析节点组（分号分隔）。
func parseNodes(s string) ([]*Node, error) {
	var nodes []*Node
	for _, p := range strings.Split(s, ";") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := parseNode(p)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("DISPATCH_RULES 节点组为空: %q", s)
	}
	return nodes, nil
}

// parseNode 解析单节点：node = <url>[|w=<weight>][|p=<priority>]。
func parseNode(s string) (*Node, error) {
	segs := strings.Split(s, "|")
	urlStr := strings.TrimSpace(segs[0])
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		return nil, fmt.Errorf("DISPATCH_RULES 节点必须以 http(s):// 开头: %q", urlStr)
	}
	n := &Node{URL: urlStr, Weight: 1, Priority: 0}
	for _, seg := range segs[1:] {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		kv := strings.SplitN(seg, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("DISPATCH_RULES 节点参数格式错误（应为 key=value）: %q", seg)
		}
		switch strings.ToLower(kv[0]) {
		case "w":
			w, err := strconv.Atoi(strings.TrimSpace(kv[1]))
			if err != nil || w <= 0 {
				return nil, fmt.Errorf("DISPATCH_RULES 节点权重必须为正整数: %q", seg)
			}
			n.Weight = w
		case "p":
			p, err := strconv.Atoi(strings.TrimSpace(kv[1]))
			if err != nil || (p != 0 && p != 1) {
				return nil, fmt.Errorf("DISPATCH_RULES 节点优先级仅支持 0(高优)/1(备份): %q", seg)
			}
			n.Priority = p
		default:
			return nil, fmt.Errorf("DISPATCH_RULES 不支持的节点参数: %q", seg)
		}
	}
	return n, nil
}

// parseHealthCheck 解析健康检查参数：interval/timeout 为 time.ParseDuration 可解析格式（如 10s/500ms），path 必须以 / 开头。
func parseHealthCheck(intervalStr, timeoutStr, path string) (*HealthCheck, error) {
	interval, err := time.ParseDuration(strings.TrimSpace(intervalStr))
	if err != nil || interval <= 0 {
		return nil, fmt.Errorf("DISPATCH_RULES 健康检查 interval 无效（应为 10s/1m 等）: %q", intervalStr)
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(timeoutStr))
	if err != nil || timeout <= 0 {
		return nil, fmt.Errorf("DISPATCH_RULES 健康检查 timeout 无效（应为 2s/500ms 等）: %q", timeoutStr)
	}
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("DISPATCH_RULES 健康检查 path 必须以 / 开头: %q", path)
	}
	return &HealthCheck{Interval: interval, Timeout: timeout, Path: path}, nil
}

// normLen 返回前缀归一化（补 "/"）后的长度，用于最长前缀排序。
func normLen(prefix string) int {
	if prefix != "/" && !strings.HasSuffix(prefix, "/") {
		return len(prefix) + 1
	}
	return len(prefix)
}
