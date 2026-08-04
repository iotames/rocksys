// Radix Tree 路由引擎：dispatch 内部子组件（不改主架构，仅增强匹配能力）。
//
// 支持三种 pattern（DISPATCH_RULES 的 <Prefix> 部分）：
//   - 纯前缀  /api/order/     → 匹配以该前缀开头的任意路径（向后兼容旧格式）
//   - 参数    /api/order/:id  → 匹配单个路径段并捕获参数（:name）
//   - 通配    /api/*          → 匹配剩余所有路径
//   - 兜底    /               → 匹配所有路径（等同旧版 "/" 兜底路由）
//
// 匹配语义：全部规则均为"前缀匹配"（与旧版 dispatch 一致）——规则命中后，
// 路径剩余任意段均算命中；仅捕获已匹配段中的参数。匹配采用深度优先，
// 天然满足"最长匹配优先"（更具体的规则优先于更宽泛的规则）。未命中返回 nil，
// 由 Handle 回退默认 upstream（语义不变）。
package dispatch

import "strings"

// radixNode 前缀树节点。
type radixNode struct {
	segment    string // 静态段名（isWild/paramName 为空时有效）
	paramName  string // 参数名（:name，非空表示参数节点）
	isWild     bool   // 通配节点（*）
	children   []*radixNode
	prefixRule *Rule // 绑定规则：匹配到此节点后剩余任意段均命中
}

// Router 前缀树路由引擎。
type Router struct {
	root *radixNode
}

// newRouter 创建空路由引擎。
func newRouter() *Router {
	return &Router{root: &radixNode{}}
}

// insert 插入规则。pattern 形如 /api/order/、/api/order/:id、/api/*、/。
// 同一 pattern 重复插入时后者覆盖前者（规则以最后注册为准）。
func (r *Router) insert(pattern string, rule *Rule) {
	if pattern == "/" {
		// 根兜底：匹配所有路径。
		r.root.prefixRule = rule
		return
	}
	p := strings.TrimSuffix(pattern, "/")
	node := r.root
	for _, seg := range splitSegments(p) {
		switch {
		case seg == "*":
			node = node.wildChild()
		case strings.HasPrefix(seg, ":"):
			node = node.paramChild(seg[1:])
		default:
			node = node.staticChild(seg)
		}
	}
	node.prefixRule = rule
}

// staticChild 获取/创建静态段子节点。
func (n *radixNode) staticChild(seg string) *radixNode {
	for _, c := range n.children {
		if !c.isWild && c.paramName == "" && c.segment == seg {
			return c
		}
	}
	c := &radixNode{segment: seg}
	n.children = append(n.children, c)
	return c
}

// paramChild 获取/创建参数子节点。
func (n *radixNode) paramChild(name string) *radixNode {
	for _, c := range n.children {
		if c.paramName == name {
			return c
		}
	}
	c := &radixNode{paramName: name}
	n.children = append(n.children, c)
	return c
}

// wildChild 获取/创建通配子节点。
func (n *radixNode) wildChild() *radixNode {
	for _, c := range n.children {
		if c.isWild {
			return c
		}
	}
	c := &radixNode{isWild: true}
	n.children = append(n.children, c)
	return c
}

// match 匹配路径，返回命中的规则与捕获的参数。未命中返回 (nil, nil)。
func (r *Router) match(path string) (*Rule, map[string]string) {
	segs := splitSegments(path)
	var best *Rule
	var bestParams map[string]string
	bestDepth := -1
	params := make(map[string]string)
	r.root.match(segs, 0, params, &best, &bestParams, &bestDepth)
	return best, bestParams
}

// match 深度优先匹配。优先深入更具体的分支，回溯时更新为最深命中。
// 命中规则即记录（前缀语义：剩余任意段均命中），最终取最深者。
func (n *radixNode) match(segs []string, idx int, params map[string]string, best **Rule, bestParams *map[string]string, bestDepth *int) {
	// 本节点绑定规则：前缀命中（匹配到此即满足，剩余任意）。
	if n.prefixRule != nil {
		if idx > *bestDepth {
			*best, *bestDepth, *bestParams = n.prefixRule, idx, cloneParams(params)
		}
	}
	if idx >= len(segs) {
		return
	}
	seg := segs[idx]
	// 静态子节点：精确匹配当前段。
	for _, c := range n.children {
		if c.isWild || c.paramName != "" {
			continue
		}
		if c.segment == seg {
			c.match(segs, idx+1, params, best, bestParams, bestDepth)
		}
	}
	// 参数子节点：匹配任意单段并捕获。
	for _, c := range n.children {
		if c.paramName != "" {
			params[c.paramName] = seg
			c.match(segs, idx+1, params, best, bestParams, bestDepth)
			delete(params, c.paramName)
		}
	}
	// 通配子节点：匹配剩余所有路径。
	for _, c := range n.children {
		if c.isWild && c.prefixRule != nil {
			if idx+1 > *bestDepth {
				*best, *bestDepth, *bestParams = c.prefixRule, idx+1, cloneParams(params)
			}
		}
	}
}

// cloneParams 复制参数映射（匹配回溯时避免共享可变 map）。
func cloneParams(params map[string]string) map[string]string {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]string, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}

// splitSegments 按 "/" 分段，去掉首尾空段。
func splitSegments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}
