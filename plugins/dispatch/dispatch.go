// Package dispatch L2 路由分发（转发链中间件）。
//
// 依据 DEV_HANDBOOK.md 第 10 章实现：URI 前缀路由表 → 目标 upstream；
// 未命中 → 不写入 Target（Adapter 回退默认 upstream）。
package dispatch

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/hotswap"
)

// 编译期断言：Dispatch 实现 hotswap.MiddlewareLifecycle。
var _ hotswap.MiddlewareLifecycle = (*Dispatch)(nil)

// Rule 一条路由规则：Prefix 匹配的 URI 前缀，Upstream 转发目标。
type Rule struct {
	Prefix   string // 前缀路径，必须以 "/" 开头
	Upstream string // 目标地址 http://host:port
}

// RouteTable 前缀路由表：有序列表（最长前缀优先）。
type RouteTable struct {
	rules []Rule
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
			"路由规则（<Prefix>=<Upstream>，逗号分隔，如 /api/order/=http://order-svc:9001）")
	}
	return d
}

// Name 返回中间件名称。
func (d *Dispatch) Name() string { return "dispatch" }

// Slot 挂载位置：路由分发在防护之后、转发之前执行。
func (d *Dispatch) Slot() chain.Slot { return chain.Middle }

// Start 从配置字符串解析并重建 RouteTable，原子替换内部快照（§10.3 流程 C）。
// 解析失败时保留旧快照并返回 error。
func (d *Dispatch) Start(_ any) error {
	rt, err := parseRules(d.rules)
	if err != nil {
		return err
	}
	d.rt.Store(rt)
	return nil
}

// Stop 无资源需要清理。
func (d *Dispatch) Stop() error { return nil }

// Handle 前缀路由分发：命中则写入 Target；未命中不动（Adapter 用默认 upstream）。
// 返回 true 表示不中断链、不写响应。
func (d *Dispatch) Handle(ctx *chain.Context) bool {
	up, ok := d.rt.Load().(*RouteTable).Match(ctx.R.URL.Path)
	if ok {
		ctx.DF.SetTarget(up)
	}
	return true
}

// Match 匹配 path，返回命中的最长前缀所对应的 upstream，未命中返回 ok=false。
//
// 1. path 末尾补 "/"（统一处理）；例外：根路径 "/" 不补。
// 2. Prefix 也要求以 "/" 结尾才算完整段；例外：Prefix "/" 为兜底路由。
// 3. 从 rules 中找到匹配的最长前缀。
func (rt *RouteTable) Match(path string) (upstream string, ok bool) {
	p := path
	if p != "/" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	var best string
	bestLen := -1
	for _, rule := range rt.rules {
		prefix := rule.Prefix
		if prefix != "/" && !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		if strings.HasPrefix(p, prefix) && len(prefix) > bestLen {
			bestLen = len(prefix)
			best = rule.Upstream
			ok = true
		}
	}
	return best, ok
}

// parseRules 解析 DISPATCH_RULES：逗号分隔，每条 <Prefix>=<Upstream>。
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
			return nil, fmt.Errorf("DISPATCH_RULES 条目格式错误（应为 <Prefix>=<Upstream>）: %q", part)
		}
		prefix := strings.TrimSpace(part[:idx])
		upstream := strings.TrimSpace(part[idx+1:])
		if !strings.HasPrefix(prefix, "/") {
			return nil, fmt.Errorf("DISPATCH_RULES 前缀必须以 / 开头: %q", prefix)
		}
		if upstream == "" {
			return nil, fmt.Errorf("DISPATCH_RULES upstream 不能为空: %q", part)
		}
		rt.rules = append(rt.rules, Rule{Prefix: prefix, Upstream: upstream})
	}
	// 最长前缀优先：按归一化前缀长度降序排列。
	sort.SliceStable(rt.rules, func(i, j int) bool {
		return normLen(rt.rules[i].Prefix) > normLen(rt.rules[j].Prefix)
	})
	return rt, nil
}

// normLen 返回前缀归一化（补 "/"）后的长度，用于最长前缀排序。
func normLen(prefix string) int {
	if prefix != "/" && !strings.HasSuffix(prefix, "/") {
		return len(prefix) + 1
	}
	return len(prefix)
}
