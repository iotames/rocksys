// 路由匹配引擎与 Host 归一化（三层模型：规则 → 均衡器 → 节点）。
//
// 匹配语义：
//   - Host 归一：剥端口（含 IPv6 `[::1]:80` 方括号形态）+ 转小写；空 Host 保持空；
//     裸 IPv6（多冒号无方括号）视为无端口整体保留；
//   - domain 条件：空 = 匹配任意 Host；非空 = 与归一 Host 精确相等（快照内已归一小写，
//     请求侧经 normalizeHost 归一后比对，域名匹配不敏感大小写）；
//   - 路径条件（路径匹配大小写敏感）：
//     前缀 = 段对齐且命中自身（/api 命中 /api 与 /api/x、不匹配 /apix；
//     / 命中一切路径含 / 本身）；
//     精确 = 全等，不做尾斜杠归一；
//     模式 = :param 捕获 / * 通配的段匹配，沿用旧 Radix 语义（经公共段匹配
//     函数 matchSegments 实现），命中产出 X-Route-Param-* 参数；
//   - 主流程：快照规则已按 (match_order, id) 稳定升序，依序逐条判定，
//     命中即停返回（规则 + 捕获参数）；全未命中返回 nil。
package dispatch

import "strings"

// normalizeHost 归一化请求 Host：剥端口（含 IPv6 方括号形态 [::1]:80 → [::1]）、
// 转小写；空 Host 保持空。裸 IPv6（如 ::1，多冒号且无方括号）不含端口，整体保留。
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	// IPv6 方括号形态：[::1]:80 → [::1]（仅剥端口，保留方括号原样小写）。
	if strings.HasPrefix(host, "[") {
		if i := strings.Index(host, "]"); i >= 0 {
			return strings.ToLower(host[:i+1])
		}
		return strings.ToLower(host)
	}
	// 多冒号无方括号 = 裸 IPv6，LastIndex(":") 会误剥，整体保留。
	if strings.Count(host, ":") > 1 {
		return strings.ToLower(host)
	}
	// 常规 host[:port]：剥端口。
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return strings.ToLower(host)
}

// matchPrefix 前缀匹配：段对齐且命中自身。
//   - prefix=/ 命中一切路径（含 / 本身）；
//   - prefix=/api 命中 /api（自身）与 /api/x（段对齐），不匹配 /apix（非段边界）。
func matchPrefix(prefix, path string) bool {
	if prefix == "/" {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) == len(prefix) {
		return true // 命中自身
	}
	return path[len(prefix)] == '/' // 段对齐：多出的部分必须以 / 起始
}

// matchExact 精确匹配：全等，不做尾斜杠归一（/api 只命中 /api，不命中 /api/）。
func matchExact(value, path string) bool {
	return value == path
}

// matchRoutePath 按规则路径类型匹配请求路径（大小写敏感），返回是否命中。
// pathSegs 为请求路径的预切段（同一 Match 调用内全候选复用，首个模式规则处
// 惰性切分一次；前缀/精确不消费，可为 nil）。
func matchRoutePath(rule *RuleRT, path string, pathSegs []string) (map[string]string, bool) {
	switch rule.PathType {
	case PathTypePrefix:
		return nil, matchPrefix(rule.PathValue, path)
	case PathTypeExact:
		return nil, matchExact(rule.PathValue, path)
	case PathTypeMode:
		params, ok := matchPreSegs(rule.patSegs, pathSegs)
		return params, ok
	default:
		// 构建期校验已拦截非法枚举；此处 fail-closed 不命中。
		return nil, false
	}
}

// Match 主流程：从 Host 对应的候选序中依序逐条判定（候选序 = 按 (match_order, id)
// 全局序保持相对次序的子序列，见 RouteSnapshot.byDomain / buildHostCandidates），
// domain 条件（空=任意 / 非空=与归一 Host 精确相等）× 路径条件均满足即命中返回
// （规则 + 模式捕获参数，非模式规则参数为 nil）；全未命中返回 (nil, nil)。
//
// 候选序选取：空 Host → 仅无域名规则（非空域名规则与空 Host 精确比对必不等，
// 跳过语义等价）；已收录域名 → 该域名预归并候选数组；未收录域名 → 无域名规则
// （该域名规则必不命中，只可能由无域名规则接管）。请求路径只在首个模式规则处
// 切分一次、全候选复用（切分结果只依赖 path 本身，跨候选复用不改语义）。
// 并发安全：只读快照，无副作用。
func Match(snap *RouteSnapshot, host, path string) (*RuleRT, map[string]string) {
	if snap == nil {
		return nil, nil
	}
	nh := normalizeHost(host)
	cands := snap.anyDomain
	if nh != "" {
		if list, ok := snap.byDomain[nh]; ok {
			cands = list
		}
	}
	var pathSegs []string // 惰性切段缓存：首个模式规则才切分，之后复用
	for _, rule := range cands {
		if rule.PathType == PathTypeMode && pathSegs == nil {
			pathSegs = splitSegments(path)
		}
		if params, ok := matchRoutePath(rule, path, pathSegs); ok {
			return rule, params
		}
	}
	return nil, nil
}

// matchSegments 公共单模式段匹配（自 Radix Tree 单链语义收编，供模式匹配
// matchRoutePath 与单测使用；断言语义见 match_test.go）。
//
// pattern 形如 /api/order/:id、/api/*、/；语义：
//   - 静态段：逐段相等；
//   - :name 段：匹配任意单个路径段并捕获参数；
//   - * 段：匹配其后剩余所有路径（至少消费一个段——/api/* 不命中 /api）；
//   - 前缀语义：模式段全部匹配完即命中，路径剩余任意段均算命中；
//   - pattern=/ 分段为空，命中一切路径。
//
// 命中返回捕获的参数（无参数时为 nil）；未命中返回 (nil, false)。
func matchSegments(pattern, path string) (map[string]string, bool) {
	return matchPreSegs(splitSegments(pattern), splitSegments(path))
}

// matchPreSegs 预分段版段匹配：pattern 段与请求路径段均已切好——pattern 段由
// 加载期切好（RuleRT.patSegs），路径段由调用方在单次 Match 内切分一次后跨候选
// 复用。热路径零额外分配（参数捕获 map 除外）。
// 语义与 matchSegments 完全一致（同一段匹配内核，仅输入来源不同）。
func matchPreSegs(patSegs, pathSegs []string) (map[string]string, bool) {
	var params map[string]string
	for i, seg := range patSegs {
		if seg == "*" {
			// 通配：至少消费一个段后命中，剩余任意。
			if i >= len(pathSegs) {
				return nil, false
			}
			return params, true
		}
		if i >= len(pathSegs) {
			return nil, false // 路径段不足
		}
		if strings.HasPrefix(seg, ":") {
			// 参数段：匹配任意单段并捕获。
			if params == nil {
				params = make(map[string]string)
			}
			params[seg[1:]] = pathSegs[i]
			continue
		}
		if seg != pathSegs[i] {
			return nil, false // 静态段不等
		}
	}
	return params, true
}

// splitSegments 按 "/" 分段，去掉首尾空段。
func splitSegments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}
