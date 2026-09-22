// sticky cookie 会话保持：直路由判定 / 种值 / 失效回落标记。
//
// 语义（与任意策略正交组合）：
//  1. 请求携带本均衡器的 sticky Cookie 且所指节点在当前关系内且健康 → 直路由
//     （优先于策略选点；不校验 priority——粘性优先于高优回落）；
//  2. 无 Cookie 或失效 → 回落策略选点并重种（StickyOutcome.NeedPlant 标记）；
//  3. Cookie 值 = 节点 id 明文；会话级（不设 Max-Age）；种值必须
//     Header().Add 追加、禁止 Set（防抹掉上游应用自身的 Set-Cookie）；
//     https 请求（经 X-Forwarded-Proto 判断）附加 Secure 属性。
package dispatch

import (
	"net/http"
	"strconv"
)

// DefaultStickyCookie 默认 sticky Cookie 名（与 dispatch_upstream.sticky_cookie 默认值一致）。
const DefaultStickyCookie = "rocksys_node"

// StickyOutcome 一次请求级选点的 sticky 结果标记。
type StickyOutcome struct {
	Node      *NodeRT // 选中节点（含直路由）
	ViaSticky bool    // 是否 Cookie 直路由命中
	NeedPlant bool    // 是否需要在响应头种 Cookie（策略选点产生即需种；直路由已带 Cookie 不重种）
	OK        bool    // 是否选中（false = 全部不可用，调用方写 503，不种值）
}

// AcquireNode sticky 感知的选点入口：先试直路由，失效/缺失回落策略选点并标记重种。
// 两种路径的选中均真实计入在途 +1（见 SelectNode / SelectStickyNode）。
func AcquireNode(u *UpstreamRT, reg NodeRegistry, r *http.Request) StickyOutcome {
	if u.StickyEnabled && r != nil {
		if id, err := stickyNodeID(u, r); err == nil {
			if n, ok := SelectStickyNode(u, reg, id); ok {
				return StickyOutcome{Node: n, ViaSticky: true, OK: true}
			}
		}
	}
	n, ok := SelectNode(u, reg)
	if !ok {
		return StickyOutcome{}
	}
	return StickyOutcome{Node: n, NeedPlant: u.StickyEnabled, OK: true}
}

// stickyNodeID 从请求中提取本均衡器 sticky Cookie 的值（节点 id）。
// 未携带 / Cookie 名不匹配 / 值非合法节点 id 均返回错误（统一走回落）。
func stickyNodeID(u *UpstreamRT, r *http.Request) (int64, error) {
	c, err := r.Cookie(stickyCookieName(u))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(c.Value, 10, 64)
}

// stickyCookieName 取均衡器生效的 Cookie 名（未配置时用默认值）。
func stickyCookieName(u *UpstreamRT) string {
	if u.StickyCookie == "" {
		return DefaultStickyCookie
	}
	return u.StickyCookie
}

// PlantSticky 在响应头种 sticky Cookie（Add 追加，禁止 Set——Middle 槽位设头合法，
// 转发写回经 copyHeader Add 合并自然携带，与上游同名头共存）。
// 属性：HttpOnly、SameSite=Lax、Path=/；会话级（无 Max-Age）；
// https 请求（X-Forwarded-Proto=https，信任前置代理的协议头）附加 Secure。
func PlantSticky(w http.ResponseWriter, u *UpstreamRT, nodeID int64, r *http.Request) {
	value := "Path=/; HttpOnly; SameSite=Lax"
	if isHTTPS(r) {
		value += "; Secure"
	}
	// 必须追加：Set 会整键覆盖，抹掉上游应用自身的 Set-Cookie（红线）。
	w.Header().Add("Set-Cookie", stickyCookieName(u)+"="+strconv.FormatInt(nodeID, 10)+"; "+value)
}

// isHTTPS 判断原始请求是否 https：经 X-Forwarded-Proto 判断（网关 behind 前置
// 代理 TLS 卸载的部署形态）；头缺失视为 http。
func isHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}
