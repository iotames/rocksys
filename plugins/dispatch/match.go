// 路由匹配引擎与 Host 归一化（ROUTE_DISPATCH 三层模型）。
//
// 匹配语义（唯一权威：docs/plan/ROUTE_DISPATCH_DESIGN_PLAN.md S1 策略与 M 表）：
//   - Host 归一：剥端口（含 IPv6 `[::1]:80` 方括号形态）+ 转小写；空 Host 保持空；
//     裸 IPv6（多冒号无方括号）视为无端口整体保留；
//   - domain 条件：空 = 匹配任意 Host；非空 = 与归一 Host 精确相等（快照内已归一小写，
//     请求侧经 normalizeHost 归一后比对，域名匹配不敏感大小写）；
//   - 路径条件（路径匹配大小写敏感）：
//     前缀 = 段对齐且命中自身（/api 命中 /api 与 /api/x、不匹配 /apix；
//     / 命中一切路径含 / 本身，D13）；
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

// matchPrefix 前缀匹配：段对齐且命中自身（D13）。
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
func matchRoutePath(rule *RuleRT, path string) (map[string]string, bool) {
	switch rule.PathType {
	case PathTypePrefix:
		return nil, matchPrefix(rule.PathValue, path)
	case PathTypeExact:
		return nil, matchExact(rule.PathValue, path)
	case PathTypeMode:
		params, ok := matchSegments(rule.PathValue, path)
		return params, ok
	default:
		// 构建期校验已拦截非法枚举；此处 fail-closed 不命中。
		return nil, false
	}
}

// Match 主流程：依序逐条判定快照规则（已按 (match_order, id) 稳定升序），
// domain 条件（空=任意 / 非空=与归一 Host 精确相等）× 路径条件均满足即命中返回
// （规则 + 模式捕获参数，非模式规则参数为 nil）；全未命中返回 (nil, nil)。
// 并发安全：只读快照，无副作用。
func Match(snap *RouteSnapshot, host, path string) (*RuleRT, map[string]string) {
	if snap == nil {
		return nil, nil
	}
	nh := normalizeHost(host)
	for _, rule := range snap.Rules {
		if rule.Domain != "" && rule.Domain != nh {
			continue
		}
		if params, ok := matchRoutePath(rule, path); ok {
			return rule, params
		}
	}
	return nil, nil
}

// matchSegments 公共单模式段匹配（STEP5 自 Radix Tree 单链语义收编，供模式匹配
// matchRoutePath 使用；断言语义见 match_test.go）。
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
	patSegs := splitSegments(pattern)
	pathSegs := splitSegments(path)
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
