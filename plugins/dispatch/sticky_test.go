// sticky_test.go：sticky cookie 表驱动测试——直路由 / 失效回落重种 / 不跨均衡器误粘 / Secure 跟随协议。
package dispatch

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// itoa64 节点 id 转字符串（Cookie 值口径）。
func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

// parseSetCookies 解析 Set-Cookie 头值列表（手工解析，标准库未导出该解析器）。
func parseSetCookies(vals []string) []*http.Cookie {
	out := make([]*http.Cookie, 0, len(vals))
	for _, v := range vals {
		c := &http.Cookie{SameSite: http.SameSiteDefaultMode}
		for i, part := range strings.Split(v, ";") {
			part = strings.TrimSpace(part)
			k, attr, _ := strings.Cut(part, "=")
			k = strings.TrimSpace(k)
			if i == 0 {
				c.Name, c.Value = k, strings.TrimSpace(attr)
				continue
			}
			switch strings.ToLower(k) {
			case "path":
				c.Path = attr
			case "httponly":
				c.HttpOnly = true
			case "secure":
				c.Secure = true
			case "samesite":
				switch strings.ToLower(attr) {
				case "lax":
					c.SameSite = http.SameSiteLaxMode
				case "strict":
					c.SameSite = http.SameSiteStrictMode
				case "none":
					c.SameSite = http.SameSiteNoneMode
				}
			case "max-age":
				if n, err := strconv.Atoi(attr); err == nil {
					c.MaxAge = n
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// stickyFixture 均衡器 + registry（101 高优健康、102 高优、103 备份，均健康）。
func stickyFixture(t *testing.T, sticky bool) (*UpstreamRT, *MemRegistry) {
	t.Helper()
	g := &GraphInput{
		Upstreams: []UpstreamRow{{ID: 7, Name: "粘性池", Algo: 1, StickyEnabled: sticky, StickyCookie: "rocksys_node", Enabled: true}},
		Nodes: []NodeRow{
			{ID: 101, URL: "http://n1:9000"},
			{ID: 102, URL: "http://n2:9000"},
			{ID: 103, URL: "http://n3:9000"},
		},
		Relations: []UpstreamNodeRow{
			{ID: 1, UpstreamID: 7, NodeID: 101, Weight: 1, Priority: 0},
			{ID: 2, UpstreamID: 7, NodeID: 102, Weight: 1, Priority: 0},
			{ID: 3, UpstreamID: 7, NodeID: 103, Weight: 1, Priority: 1},
		},
	}
	snap, err := BuildGraph(g)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	u := snap.Upstreams[7]
	reg := NewMemRegistry()
	for _, n := range u.Nodes {
		reg.SetHealth(n.ID, HealthOK)
	}
	return u, reg
}

// TestStickyDirectRoute 直路由：Cookie 所指节点在关系内且健康 → 直路由、不重种、计入在途。
func TestStickyDirectRoute(t *testing.T) {
	u, reg := stickyFixture(t, true)
	r := httptest.NewRequest("GET", "http://gw/api/x", nil)
	r.AddCookie(&http.Cookie{Name: "rocksys_node", Value: "102"})

	out := AcquireNode(u, reg, r)
	if !out.OK || !out.ViaSticky || out.NeedPlant {
		t.Fatalf("直路由标记错误: %+v", out)
	}
	if out.Node == nil || out.Node.ID != 102 {
		t.Fatalf("应直路由到 102, got %+v", out.Node)
	}
	if got := reg.Inflight(102); got != 1 {
		t.Fatalf("直路由应计入在途 +1, got %d", got)
	}
	// 重复请求稳定粘同一节点。
	out2 := AcquireNode(u, reg, r)
	if !out2.ViaSticky || out2.Node.ID != 102 {
		t.Fatalf("二次请求未粘住: %+v", out2)
	}
}

// TestStickyDirectRouteIgnoresPriority 直路由不校验 priority：备份节点健康即可粘。
func TestStickyDirectRouteIgnoresPriority(t *testing.T) {
	u, reg := stickyFixture(t, true)
	r := httptest.NewRequest("GET", "http://gw/api/x", nil)
	r.AddCookie(&http.Cookie{Name: "rocksys_node", Value: "103"}) // 103 是备份节点
	out := AcquireNode(u, reg, r)
	if !out.ViaSticky || out.Node.ID != 103 {
		t.Fatalf("粘性应优先于高优回落（备份健康可直路由）: %+v", out)
	}
}

// TestStickyFailoverReplant 失效回落：Cookie 所指节点判死 → 策略选点并标记重种。
func TestStickyFailoverReplant(t *testing.T) {
	u, reg := stickyFixture(t, true)
	reg.SetHealth(102, HealthBad) // Cookie 所指节点判死
	r := httptest.NewRequest("GET", "http://gw/api/x", nil)
	r.AddCookie(&http.Cookie{Name: "rocksys_node", Value: "102"})

	out := AcquireNode(u, reg, r)
	if !out.OK || out.ViaSticky || !out.NeedPlant {
		t.Fatalf("失效应回落策略选点并标记重种: %+v", out)
	}
	if out.Node.ID == 102 {
		t.Fatalf("不应选中失效节点 102: %+v", out.Node)
	}
	// 种值落到响应头，且值是新选中的节点 id。
	w := httptest.NewRecorder()
	PlantSticky(w, u, out.Node.ID, r)
	cookies := parseSetCookies(w.Header().Values("Set-Cookie"))
	if len(cookies) != 1 {
		t.Fatalf("应恰好种 1 个 Cookie, got %v", w.Header().Values("Set-Cookie"))
	}
	c := cookies[0]
	if c.Name != "rocksys_node" || c.Value != itoa64(out.Node.ID) {
		t.Fatalf("重种 Cookie 错误: %+v", c)
	}
	if c.Path != "/" || !c.HttpOnly || c.MaxAge != 0 {
		t.Fatalf("Cookie 属性错误（应 Path=/、HttpOnly、会话级无 Max-Age）: %+v", c)
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite 应为 Lax: %+v", c)
	}
	if c.Secure {
		t.Fatalf("http 请求不应带 Secure: %+v", c)
	}
}

// TestStickyNoCookieFallback 无 Cookie / sticky 关闭：走策略选点并标记重种（开启时）。
func TestStickyNoCookieFallback(t *testing.T) {
	u, reg := stickyFixture(t, true)
	r := httptest.NewRequest("GET", "http://gw/api/x", nil) // 不带 Cookie
	out := AcquireNode(u, reg, r)
	if !out.OK || out.ViaSticky || !out.NeedPlant {
		t.Fatalf("无 Cookie 应回落并标记重种: %+v", out)
	}
	// sticky 关闭：不直路由、不种值。
	u2, reg2 := stickyFixture(t, false)
	r2 := httptest.NewRequest("GET", "http://gw/api/x", nil)
	r2.AddCookie(&http.Cookie{Name: "rocksys_node", Value: "102"})
	out2 := AcquireNode(u2, reg2, r2)
	if out2.ViaSticky || out2.NeedPlant {
		t.Fatalf("sticky 关闭不应直路由或种值: %+v", out2)
	}
}

// TestStickyNotAcrossUpstreams 不跨均衡器误粘：Cookie 值指向别的均衡器的节点（不在当前关系内）→ 回落。
func TestStickyNotAcrossUpstreams(t *testing.T) {
	uA, regA := stickyFixture(t, true) // 节点 101/102/103
	// 另一个均衡器 B，只含节点 201。
	g := &GraphInput{
		Upstreams: []UpstreamRow{{ID: 8, Name: "另一池", Algo: 1, StickyEnabled: true, StickyCookie: "rocksys_node", Enabled: true}},
		Nodes:     []NodeRow{{ID: 201, URL: "http://x1:9000"}},
		Relations: []UpstreamNodeRow{{ID: 9, UpstreamID: 8, NodeID: 201, Weight: 1, Priority: 0}},
	}
	snapB, err := BuildGraph(g)
	if err != nil {
		t.Fatalf("构建 B 失败: %v", err)
	}
	uB := snapB.Upstreams[8]
	for _, n := range uB.Nodes {
		regA.SetHealth(n.ID, HealthOK)
	}
	// 同名 Cookie 但值 201 不在均衡器 A 的关系内 → A 应回落策略选点。
	r := httptest.NewRequest("GET", "http://gw/api/x", nil)
	r.AddCookie(&http.Cookie{Name: "rocksys_node", Value: "201"})
	out := AcquireNode(uA, regA, r)
	if out.ViaSticky || out.Node.ID == 201 {
		t.Fatalf("不应跨均衡器误粘: %+v", out)
	}
	if !out.NeedPlant {
		t.Fatalf("回落应标记重种: %+v", out)
	}
	// 反向：A 关系内的 101 对 B 同样不粘。
	r2 := httptest.NewRequest("GET", "http://gw/api/x", nil)
	r2.AddCookie(&http.Cookie{Name: "rocksys_node", Value: "101"})
	out2 := AcquireNode(uB, regA, r2)
	if out2.ViaSticky || out2.Node.ID != 201 {
		t.Fatalf("B 不应粘 A 的节点: %+v", out2)
	}
}

// TestStickySecureFollowsProto Secure 属性跟随 X-Forwarded-Proto。
func TestStickySecureFollowsProto(t *testing.T) {
	u, _ := stickyFixture(t, true)

	// https（前置代理 TLS 卸载经头透传）→ 附加 Secure。
	w := httptest.NewRecorder()
	rHTTPS := httptest.NewRequest("GET", "http://gw/api/x", nil)
	rHTTPS.Header.Set("X-Forwarded-Proto", "https")
	PlantSticky(w, u, 101, rHTTPS)
	sc := strings.Join(w.Header().Values("Set-Cookie"), ";")
	if !strings.Contains(sc, "Secure") {
		t.Fatalf("https 请求应带 Secure: %q", sc)
	}

	// http → 不带 Secure。
	w2 := httptest.NewRecorder()
	PlantSticky(w2, u, 101, httptest.NewRequest("GET", "http://gw/api/x", nil))
	sc2 := strings.Join(w2.Header().Values("Set-Cookie"), ";")
	if strings.Contains(sc2, "Secure") {
		t.Fatalf("http 请求不应带 Secure: %q", sc2)
	}
}

// TestStickyPlantUsesAdd 追加语义：已有上游 Set-Cookie 时种值不得覆盖（Add 非 Set）。
func TestStickyPlantUsesAdd(t *testing.T) {
	u, _ := stickyFixture(t, true)
	w := httptest.NewRecorder()
	w.Header().Set("Set-Cookie", "app_sid=abc; Path=/")
	PlantSticky(w, u, 101, httptest.NewRequest("GET", "http://gw/api/x", nil))
	vals := w.Header().Values("Set-Cookie")
	if len(vals) != 2 {
		t.Fatalf("应追加而非覆盖（共 2 条）: %v", vals)
	}
	if vals[0] != "app_sid=abc; Path=/" {
		t.Fatalf("上游 Set-Cookie 被覆盖: %v", vals)
	}
}
