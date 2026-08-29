// WAF 安全检测：SQL 注入 / XSS / 路径遍历 / 风险路径 / 方法白名单 / 请求体大小限制 / 爬虫 UA。
//
// 设计要点：
//   - 全部检测默认关闭（配置项默认 false/空），开启后才参与处理，符合"演进 = 开关切换"红线。
//   - 检测模式（SQL/XSS/路径遍历/风险路径/爬虫 UA）全部外置到 rules/ 目录文件，
//     运行时经 ruleLoader 加载（外置目录优先、嵌入兜底），改规则无需重新编译（见 rules.go）。
//   - 检测集合在 Start 时编译进不可变快照（wafSnapshot），随 atomic.Value 整体原子替换，
//     与在途请求的 Handle 并发安全（§6.3）。
//   - 仅检测 URL 路径、查询串与 User-Agent（无需读取请求体，避免 Body 重放问题）；
//     请求体大小仅按 Content-Length 预检，chunked（ContentLength == -1）无法预知大小，暂不拦截（见 §9.6 边界）。
//   - 注入检测采用"组合特征子串"而非单关键词，降低误报（URL 中出现普通单词不会误拦截）。
package shield

import (
	"net/url"
	"strings"
)

// wafSnapshot WAF 检测编译态（构建于 Start，随不可变快照整体替换）。
type wafSnapshot struct {
	sqlEnabled      bool
	xssEnabled      bool
	pathTravEnabled bool
	riskPathEnabled bool
	crawlerEnabled  bool
	allowMethods    map[string]struct{} // 方法白名单（大写，nil/空 = 不限）
	maxBodySize     int64               // 请求体上限字节（0 = 不限）

	// 检测模式（来自规则文件，小写/原样）。
	sqlPatterns  []string
	xssPatterns  []string
	pathPatterns []string
	crawlerUAs   []string
	riskPaths    map[string]struct{} // 文件风险路径 + 配置追加（小写）
}

// ---------------------------------------------------------------------------
// 检测方法（模式来自 wafSnapshot，即规则文件编译态）
// ---------------------------------------------------------------------------

// hasSQL 检测 URL 路径与查询串中的 SQL 注入特征。
// path 为已解码路径，rawQuery 为原始查询串；两者均做 URL 解码（+ → 空格）后小写匹配。
func (w *wafSnapshot) hasSQL(path, rawQuery string) bool {
	combined := decodePath(path) + " " + decodeQuery(rawQuery)
	for _, p := range w.sqlPatterns {
		if strings.Contains(combined, p) {
			return true
		}
	}
	// SQL 行注释 `--`：必须后随空白或位于结尾，避免误伤普通内容。
	trimmed := strings.TrimSpace(combined)
	if strings.Contains(combined, "-- ") || strings.HasSuffix(trimmed, "--") {
		return true
	}
	return false
}

// hasXSS 检测查询串中的 XSS 特征（URL 解码 + 小写）。
func (w *wafSnapshot) hasXSS(rawQuery string) bool {
	decoded := decodeQuery(rawQuery)
	for _, p := range w.xssPatterns {
		if strings.Contains(decoded, p) {
			return true
		}
	}
	return false
}

// hasPathTraversal 检测路径遍历（原始转义路径 + 解码后路径双路）。
func (w *wafSnapshot) hasPathTraversal(escapedPath, path string) bool {
	raw := strings.ToLower(escapedPath)
	for _, p := range w.pathPatterns {
		if strings.Contains(raw, p) {
			return true
		}
	}
	decoded := decodePath(path)
	if strings.Contains(decoded, "../") || strings.Contains(decoded, "..\\") {
		return true
	}
	return false
}

// hasCrawlerUA 检测 User-Agent 是否命中爬虫/扫描器特征（小写子串）。
// ★ 空 UA 直接命中：正常浏览器必然携带 User-Agent，空 UA 是批量扫描器/脚本的典型特征
// （本方法仅在 SHIELD_WAF_CRAWLER_UA=true 时被调用，故空 UA 拦截与该开关联动）。
func (w *wafSnapshot) hasCrawlerUA(ua string) bool {
	if ua == "" {
		return true // 空 UA 拦截
	}
	if len(w.crawlerUAs) == 0 {
		return false
	}
	lower := strings.ToLower(ua)
	for _, p := range w.crawlerUAs {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// matchRiskPath 风险路径匹配：精确匹配；目录型规则（/.well-known 等）前缀匹配其下全部子路径。
func (w *wafSnapshot) matchRiskPath(path string) bool {
	p := strings.ToLower(path)
	if _, ok := w.riskPaths[p]; ok {
		return true
	}
	for rp := range w.riskPaths {
		if strings.HasSuffix(rp, "/") && strings.HasPrefix(p, rp) {
			return true
		}
		if strings.HasPrefix(p, rp+"/") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 构建辅助
// ---------------------------------------------------------------------------

// mergeRiskPaths 构建风险路径集合：规则文件内置集 + 配置项 SHIELD_WAF_RISK_PATHS 追加。
func mergeRiskPaths(filePaths []string, extra string) map[string]struct{} {
	m := make(map[string]struct{}, len(filePaths)+8)
	for _, p := range filePaths {
		m[strings.ToLower(p)] = struct{}{}
	}
	for _, p := range splitList(extra) {
		p = strings.ToLower(p)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		m[p] = struct{}{}
	}
	return m
}

// newMethodSet 方法白名单：逗号分隔 → 大写集合；空串返回 nil（不限）。
func newMethodSet(allow string) map[string]struct{} {
	if strings.TrimSpace(allow) == "" {
		return nil
	}
	m := make(map[string]struct{})
	for _, s := range splitList(allow) {
		m[strings.ToUpper(s)] = struct{}{}
	}
	return m
}

// decodeQuery URL 查询串解码（+ → 空格）并小写；解码失败回退原串。
func decodeQuery(s string) string {
	if d, err := url.QueryUnescape(s); err == nil {
		return strings.ToLower(d)
	}
	return strings.ToLower(s)
}

// decodePath URL 路径解码并小写；解码失败回退原串。
func decodePath(s string) string {
	if d, err := url.PathUnescape(s); err == nil {
		return strings.ToLower(d)
	}
	return strings.ToLower(s)
}
