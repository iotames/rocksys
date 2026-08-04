// Package shield WAF 检测单测（§9.6 批次10 新增）。
package shield

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rocksys/internal/chain"
)

// testWAF 从内嵌规则文件加载并构建测试用 wafSnapshot。
func testWAF(t *testing.T) *wafSnapshot {
	t.Helper()
	rl, err := newRuleLoader(defaultRulesDir)
	if err != nil {
		t.Fatalf("创建规则加载器失败: %v", err)
	}
	rs, err := rl.load()
	if err != nil {
		t.Fatalf("加载内嵌规则失败: %v", err)
	}
	return &wafSnapshot{
		sqlPatterns:  rs.SQLPatterns,
		xssPatterns:  rs.XSSPatterns,
		pathPatterns: rs.PathTraversal,
		riskPaths:    mergeRiskPaths(rs.RiskPaths, ""),
		crawlerUAs:   rs.CrawlerUA,
	}
}

// ---------------------------------------------------------------------------
// 检测方法单测（模式来自内嵌规则文件）
// ---------------------------------------------------------------------------

func TestHasSQL(t *testing.T) {
	w := testWAF(t)
	cases := []struct {
		path, query string
		want        bool
	}{
		{"/api/list", "id=1", false},
		{"/api/list", "id=1&name=foo", false},
		{"/api/list", "id=1 union select * from users", true},
		{"/api/list", "id=1%20union%20select%20*%20from%20users", true},
		{"/api/list", "id=1 or 1=1", true},
		{"/api/list", "name=x'; drop table users;--", true},
		{"/api/list", "name=sleep(5)", true},
		{"/api/list", "name=selective", false}, // 误报防护：select 出现在普通单词中
		{"/api/list", "name=select", false},    // 单关键词不拦截
	}
	for _, c := range cases {
		if got := w.hasSQL(c.path, c.query); got != c.want {
			t.Errorf("hasSQL(%q,%q)=%v, want %v", c.path, c.query, got, c.want)
		}
	}
}

func TestHasXSS(t *testing.T) {
	w := testWAF(t)
	cases := []struct {
		query string
		want  bool
	}{
		{"q=hello", false},
		{"q=<script>alert(1)</script>", true},
		{"q=%3Cscript%3Ealert(1)%3C/script%3E", true},
		{"q=javascript:alert(1)", true},
		{"q=<img src=x onerror=alert(1)>", true},
		{"q=document.cookie", true},
		{"q=select", false},
	}
	for _, c := range cases {
		if got := w.hasXSS(c.query); got != c.want {
			t.Errorf("hasXSS(%q)=%v, want %v", c.query, got, c.want)
		}
	}
}

func TestHasPathTraversal(t *testing.T) {
	w := testWAF(t)
	cases := []struct {
		escaped, path string
		want          bool
	}{
		{"/safe/path", "/safe/path", false},
		{"/../etc/passwd", "/../etc/passwd", true},
		{"/static/..%2f..%2fetc/passwd", "/static/../../etc/passwd", true},
		{"/%2e%2e/%2e%2e/etc/passwd", "/../../etc/passwd", true},
		{"/static/....//etc/passwd", "/static/....//etc/passwd", true},
		{"/static/file.txt", "/static/file.txt", false},
	}
	for _, c := range cases {
		if got := w.hasPathTraversal(c.escaped, c.path); got != c.want {
			t.Errorf("hasPathTraversal(%q,%q)=%v, want %v", c.escaped, c.path, got, c.want)
		}
	}
}

func TestMatchRiskPath(t *testing.T) {
	w := testWAF(t)
	cases := []struct {
		path string
		want bool
	}{
		{"/.env", true},
		{"/.git/config", true},
		{"/.well-known/security.txt", true}, // 前缀匹配
		{"/phpmyadmin/index.php", true},
		{"/api/ok", false},
		{"/env", false}, // 非完整段不误伤
	}
	for _, c := range cases {
		if got := w.matchRiskPath(c.path); got != c.want {
			t.Errorf("matchRiskPath(%q)=%v, want %v", c.path, got, c.want)
		}
	}
}

func TestHasCrawlerUA(t *testing.T) {
	w := testWAF(t)
	cases := []struct {
		ua   string
		want bool
	}{
		{"", false},
		{"Mozilla/5.0 (X11; Linux x86_64)", false},
		{"Googlebot/2.1 (+http://www.google.com/bot.html)", true},
		{"Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/search/spider.html)", true},
		{"curl/7.88.1", true},
		{"python-requests/2.31.0", true},
		{"sqlmap/1.7", true},
		{"okhttp/4.12.0", false}, // 常见移动 App 客户端，不应误杀
	}
	for _, c := range cases {
		if got := w.hasCrawlerUA(c.ua); got != c.want {
			t.Errorf("hasCrawlerUA(%q)=%v, want %v", c.ua, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 集成单测：经 Shield.Handle 走完整 WAF 流程
// ---------------------------------------------------------------------------

// WAF 全部默认关闭 → 即使路径含注入特征也放行（不破坏现有行为）。
func TestShield_WAF_DefaultOff(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	ctx, _ := newCtx("/api/list?id=1%20union%20select%20*%20from%20users", "1.2.3.4:80", "")
	if next := s.Handle(ctx); !next {
		t.Fatal("WAF 默认关闭应放行")
	}
}

func TestShield_WAF_SQLInjection(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.wafSQLEnabled = true
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	ctx, w := newCtx("/api/list?id=1%20union%20select%20*%20from%20users", "1.2.3.4:80", "")
	if next := s.Handle(ctx); next {
		t.Fatal("SQL 注入应被拦截")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("应 403, got %d", w.Code)
	}
	// 正常请求放行
	okCtx, _ := newCtx("/api/list?id=1", "1.2.3.4:80", "")
	if next := s.Handle(okCtx); !next {
		t.Fatal("正常请求应放行")
	}
}

func TestShield_WAF_XSS(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.wafXSSEnabled = true
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	ctx, w := newCtx("/search?q=%3Cscript%3Ealert(1)%3C/script%3E", "1.2.3.4:80", "")
	if next := s.Handle(ctx); next {
		t.Fatal("XSS 应被拦截")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("应 403, got %d", w.Code)
	}
}

func TestShield_WAF_PathTraversal(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.wafPathEnabled = true
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	ctx, w := newCtx("/static/..%2f..%2fetc/passwd", "1.2.3.4:80", "")
	if next := s.Handle(ctx); next {
		t.Fatal("路径遍历应被拦截")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("应 403, got %d", w.Code)
	}
}

func TestShield_WAF_RiskPath(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.wafRiskPathOn = true
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	// 内置风险路径：/.env（需开启 SHIELD_WAF_RISK_PATH 开关）
	ctx, w := newCtx("/.env", "1.2.3.4:80", "")
	if next := s.Handle(ctx); next {
		t.Fatal("内置风险路径应被拦截")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("应 403, got %d", w.Code)
	}
}

func TestShield_WAF_RiskPath_DefaultOff(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	// 开关未开 → 内置风险路径放行（演进=开关切换）
	ctx, _ := newCtx("/.env", "1.2.3.4:80", "")
	if next := s.Handle(ctx); !next {
		t.Fatal("SHIELD_WAF_RISK_PATH 未开应放行")
	}
}

func TestShield_WAF_CustomRiskPath(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.wafRiskPathOn = true
	s.wafRiskPaths = "/admin/secret"
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	ctx, w := newCtx("/admin/secret", "1.2.3.4:80", "")
	if next := s.Handle(ctx); next {
		t.Fatal("自定义风险路径应被拦截")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("应 403, got %d", w.Code)
	}
}

func TestShield_WAF_CrawlerUA(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.wafCrawlerOn = true
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	ctx, w := newCtx("/api/ok", "1.2.3.4:80", "sqlmap/1.7")
	if next := s.Handle(ctx); next {
		t.Fatal("爬虫/扫描器 UA 应被拦截")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("应 403, got %d", w.Code)
	}
	// 普通 UA 放行
	okCtx, _ := newCtx("/api/ok", "1.2.3.4:80", "Mozilla/5.0 (X11; Linux x86_64)")
	if next := s.Handle(okCtx); !next {
		t.Fatal("普通 UA 应放行")
	}
}

// 外置规则目录优先：外部 crawler_ua.txt 覆盖内嵌文件，改规则无需重新编译。
func TestShield_WAF_RulesDirOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "crawler_ua.txt"),
		[]byte("# 自定义爬虫 UA\nmy-custom-bot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestShield(t)
	s.enabled = true
	s.wafCrawlerOn = true
	s.rulesDir = dir
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	// 自定义 UA 命中 → 拦截
	ctx, w := newCtx("/api/ok", "1.2.3.4:80", "my-custom-bot/1.0")
	if next := s.Handle(ctx); next {
		t.Fatal("外置规则新增 UA 应被拦截")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("应 403, got %d", w.Code)
	}
	// 内嵌默认的 sqlmap 不再命中（文件被整体替换，非合并）
	okCtx, _ := newCtx("/api/ok", "1.2.3.4:80", "sqlmap/1.7")
	if next := s.Handle(okCtx); !next {
		t.Fatal("外置文件整体替换后，内嵌默认 UA 应放行")
	}
}

func TestShield_WAF_AllowMethods(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.allowMethods = "GET,POST"
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodDelete, "/api/x", nil)
	r.RemoteAddr = "1.2.3.4:80"
	w := httptest.NewRecorder()
	ctx := &chain.Context{W: w, R: r}
	if next := s.Handle(ctx); next {
		t.Fatal("DELETE 不应放行")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("应 403, got %d", w.Code)
	}
	// GET 放行
	r2 := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	r2.RemoteAddr = "1.2.3.4:80"
	ctx2 := &chain.Context{W: httptest.NewRecorder(), R: r2}
	if next := s.Handle(ctx2); !next {
		t.Fatal("GET 应放行")
	}
}

func TestShield_WAF_MaxBodySize(t *testing.T) {
	s, _ := newTestShield(t)
	s.enabled = true
	s.maxBodySize = 10
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("12345678901"))
	r.RemoteAddr = "1.2.3.4:80"
	w := httptest.NewRecorder()
	ctx := &chain.Context{W: w, R: r}
	if next := s.Handle(ctx); next {
		t.Fatal("超限 body 应被拦截")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("应 413, got %d", w.Code)
	}
	// 未超限放行
	r2 := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("short"))
	r2.RemoteAddr = "1.2.3.4:80"
	ctx2 := &chain.Context{W: httptest.NewRecorder(), R: r2}
	if next := s.Handle(ctx2); !next {
		t.Fatal("未超限 body 应放行")
	}
}
