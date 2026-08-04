// Package rewrite 转发前改写挂件（L2 增强，默认关闭）。
//
// 在转发前改写请求的 URI 前缀与请求头，供网关做路径归一化 / 版本剥离 / 注入标记头。
// 借鉴 easywaf before_proxy 阶段能力，作为独立挂件挂 chain.Middle 槽位，
// 不改主架构（engine 转发逻辑零改动）。
//
// 配置项：REWRITE_RULES（字符串，逗号分隔多条规则）
//
//	<prefix>=<spec>[;<spec>...]
//	  prefix  匹配的 URI 前缀（以 / 开头，前缀匹配）
//	  spec    动作，支持：
//	           uri|<new_prefix>            改写 URI 前缀（new_prefix 替换 prefix）
//	           header=<name>:<value>       设置请求头（Set，覆盖同名）
//
// 示例：
//
//	REWRITE_RULES=/api/v1/=uri|/api/[;header=X-Proxy-Tag:rewrite
//
// 说明：不支持改写 Host——engine 转发时强制使用目标节点 host（见 engine.Forward），
// 改 Host 属路由职责（由 dispatch 的 Target 决定），避免与 L2 职责重叠。
package rewrite

import (
	"fmt"
	"strings"
	"sync/atomic"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/hotswap"
)

// rewriteRule 一条改写规则。
type rewriteRule struct {
	prefix     string            // 匹配前缀（以 / 开头）
	rewriteURI string            // 改写后的前缀（空 = 不改写 URI）
	headers    map[string]string // 要设置的请求头
}

// rewriteTable 不可变改写规则快照。
type rewriteTable struct {
	rules []*rewriteRule
}

// Rewrite 转发前改写中间件（chain.Middle 槽位）。
// 运行态存于不可变快照，经 atomic.Value 原子替换，保证 Start 与在途 Handle 并发安全。
type Rewrite struct {
	cfg   conf.Manager
	rules string       // REWRITE_RULES 配置字符串（*string 注册，easyconf 自动写入）
	rt    atomic.Value // 持有 *rewriteTable 不可变快照
}

// New 创建改写挂件并注册 REWRITE_RULES 配置项。
func New(cfgMgr conf.Manager) *Rewrite {
	r := &Rewrite{cfg: cfgMgr}
	r.rt.Store(&rewriteTable{})
	if cfgMgr != nil {
		_ = cfgMgr.Register(&r.rules, "REWRITE_RULES", "",
			"转发前改写规则（<prefix>=<spec>[;<spec>...]，逗号分隔。spec=uri|<new_prefix> 或 header=<name>:<value>）",
			"示例：/api/v1/=uri|/api/[;header=X-Proxy-Tag:rewrite")
	}
	return r
}

// Name 返回中间件名称。
func (r *Rewrite) Name() string { return "rewrite" }

// Slot 挂载位置：转发前改写，在路由分发之后、转发之前执行。
func (r *Rewrite) Slot() chain.Slot { return chain.Middle }

// Start 从配置字符串解析并重建快照，原子替换内部快照。
// 解析失败时保留旧快照并返回 error（实例继续以旧规则服务）。
func (r *Rewrite) Start(_ any) error {
	rt, err := parseRules(r.rules)
	if err != nil {
		return err
	}
	r.rt.Store(rt)
	return nil
}

// Stop 清理资源（本挂件无特别资源）。
func (r *Rewrite) Stop() error { return nil }

// Handle 转发前改写：命中前缀则改写 URI 前缀并设置请求头，然后继续转发链。
// 未命中不动，返回 true（不中断链）。
func (r *Rewrite) Handle(ctx *chain.Context) bool {
	rt := r.rt.Load().(*rewriteTable)
	path := ctx.R.URL.Path
	for _, rule := range rt.rules {
		if !strings.HasPrefix(path, rule.prefix) {
			continue
		}
		if rule.rewriteURI != "" {
			ctx.R.URL.Path = rule.rewriteURI + strings.TrimPrefix(path, rule.prefix)
		}
		for k, v := range rule.headers {
			ctx.R.Header.Set(k, v)
		}
		return true
	}
	return true
}

// parseRules 解析 REWRITE_RULES：逗号分隔，每条 <prefix>=<spec>[;<spec>...]。
// 空字符串 → 空规则表（不改写任何请求）。
func parseRules(s string) (*rewriteTable, error) {
	rt := &rewriteTable{}
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
			return nil, fmt.Errorf("REWRITE_RULES 条目格式错误（应为 <prefix>=<spec>）: %q", part)
		}
		prefix := strings.TrimSpace(part[:idx])
		spec := strings.TrimSpace(part[idx+1:])
		if !strings.HasPrefix(prefix, "/") {
			return nil, fmt.Errorf("REWRITE_RULES 前缀必须以 / 开头: %q", prefix)
		}
		rule, err := parseRule(prefix, spec)
		if err != nil {
			return nil, err
		}
		rt.rules = append(rt.rules, rule)
	}
	return rt, nil
}

// parseRule 解析单条规则：spec = <动作>[;<动作>...]。
func parseRule(prefix, spec string) (*rewriteRule, error) {
	if spec == "" {
		return nil, fmt.Errorf("REWRITE_RULES 条目 %q 缺少 spec", prefix)
	}
	rule := &rewriteRule{prefix: prefix, headers: map[string]string{}}
	for _, seg := range strings.Split(spec, ";") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		switch {
		case strings.HasPrefix(seg, "uri|"):
			rule.rewriteURI = strings.TrimSpace(strings.TrimPrefix(seg, "uri|"))
			if !strings.HasPrefix(rule.rewriteURI, "/") {
				return nil, fmt.Errorf("REWRITE_RULES 改写前缀必须以 / 开头: %q", seg)
			}
		case strings.HasPrefix(seg, "header="):
			kv := strings.TrimPrefix(seg, "header=")
			colon := strings.Index(kv, ":")
			if colon <= 0 {
				return nil, fmt.Errorf("REWRITE_RULES header 格式错误（应为 header=<name>:<value>）: %q", seg)
			}
			rule.headers[strings.TrimSpace(kv[:colon])] = strings.TrimSpace(kv[colon+1:])
		default:
			return nil, fmt.Errorf("REWRITE_RULES 不支持的改写动作 %q（仅支持 uri| 与 header=）", seg)
		}
	}
	return rule, nil
}

// 编译期断言：Rewrite 满足 hotswap.MiddlewareLifecycle。
var _ hotswap.MiddlewareLifecycle = (*Rewrite)(nil)