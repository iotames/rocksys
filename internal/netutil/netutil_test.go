package netutil

import (
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"rocksys/internal/hotswap"
)

// 测试外挂文件名（唯一，避免与内嵌同名文件混淆；t.Cleanup 清理）。
const zzProxyFile = "zz_proxy_test.txt"

// resetToEmbedded 将快照恢复为内嵌默认（127.0.0.1）。
func resetToEmbedded(t *testing.T) {
	t.Helper()
	b, err := fs.ReadFile(trustedProxiesFS, "trusted_proxies.txt")
	if err != nil {
		t.Fatalf("读取内嵌文件失败: %v", err)
	}
	l, err := parseTrustedProxies(string(b))
	if err != nil {
		t.Fatalf("解析内嵌文件失败: %v", err)
	}
	proxyList.Store(l)
}

// loadForTest 写外挂文件并加载为当前快照。
// 外挂根目录统一为 HOT_SCRIPTS_DIR（默认 hotscripts，相对工作目录），
// 可信代理外挂子目录固定为 trusted_proxies/。
func loadForTest(t *testing.T, text string) {
	t.Helper()
	// 注入隔离外挂根（t.TempDir），避免写默认 hotscripts/ 污染源码树，且不受 CWD 下真实 hotscripts/ 干扰。
	orig := hotswap.HotScriptsDir()
	t.Cleanup(func() { hotswap.SetHotScriptsDir(orig) })
	hotswap.SetHotScriptsDir(t.TempDir())
	dir := filepath.Join(hotswap.HotScriptsDir(), trustedProxiesRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建外挂目录失败: %v", err)
	}
	f := filepath.Join(dir, zzProxyFile)
	if err := os.WriteFile(f, []byte(text), 0o644); err != nil {
		t.Fatalf("写外挂文件失败: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(f)
		_ = os.Remove(dir) // 空目录一并清理（忽略错误）
	})
	if err := LoadTrustedProxies(zzProxyFile); err != nil {
		t.Fatalf("LoadTrustedProxies: %v", err)
	}
}

// req 构造请求（Header 用 Set 模拟真实解析后的 canonical key）。
func req(remoteAddr, realIP, xff string) *http.Request {
	r := &http.Request{RemoteAddr: remoteAddr, Header: http.Header{}}
	if realIP != "" {
		r.Header.Set("X-Real-IP", realIP)
	}
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

// 默认快照（内嵌 127.0.0.1）下的行为。
func TestGetClientIP_DefaultEmbedded(t *testing.T) {
	resetToEmbedded(t)
	cases := []struct {
		name       string
		remoteAddr string
		realIP     string
		xff        string
		want       string
	}{
		{"同机Nginx+X-Real-IP(局域网客户端)", "127.0.0.1:54321", "192.168.1.50", "", "192.168.1.50"},
		{"同机Nginx+仅XFF(右往左跳过可信)", "127.0.0.1:54321", "", "203.0.113.7, 127.0.0.1", "203.0.113.7"},
		{"XFF右往左首个不可信即客户端", "127.0.0.1:54321", "", "203.0.113.7, 10.0.0.1", "10.0.0.1"},
		{"同机Nginx无转发头→退直连", "127.0.0.1:54321", "", "", "127.0.0.1"},
		{"XFF全可信→退直连", "127.0.0.1:54321", "", "127.0.0.1", "127.0.0.1"},
		{"X-Real-IP非法→走XFF", "127.0.0.1:54321", "not-an-ip", "203.0.113.7, 127.0.0.1", "203.0.113.7"},
		{"X-Real-IP带端口→非法→兜底", "127.0.0.1:54321", "1.2.3.4:8080", "", "127.0.0.1"},
		{"XFF非法值跳过", "127.0.0.1:54321", "", "bogus, 203.0.113.7, 127.0.0.1", "203.0.113.7"},
		{"XFF全非法→兜底直连", "127.0.0.1:54321", "", "bogus, also-bad", "127.0.0.1"},
		{"XFF含IPv6(右往左跳过可信)", "127.0.0.1:54321", "", "2001:db8::1, 127.0.0.1", "2001:db8::1"},
		{"非loopback直连+伪造X-Real-IP→不信头", "192.168.1.50:5555", "9.9.9.9", "", "192.168.1.50"},
		{"非loopback直连+伪造XFF→不信头", "192.168.1.50:5555", "", "9.9.9.9", "192.168.1.50"},
		{"IPv4-mapped loopback命中默认列表", "[::ffff:127.0.0.1]:54321", "192.168.1.50", "", "192.168.1.50"},
	}
	for _, c := range cases {
		if got := GetClientIP(req(c.remoteAddr, c.realIP, c.xff)); got != c.want {
			t.Errorf("%s: GetClientIP = %q, want %q", c.name, got, c.want)
		}
	}
}

// 默认列表不含 ::1：IPv6 loopback 不被信任（需用户显式加入外挂文件）。
func TestGetClientIP_IPv6LoopbackNotTrustedByDefault(t *testing.T) {
	resetToEmbedded(t)
	got := GetClientIP(req("[::1]:54321", "192.168.1.50", ""))
	if got != "::1" {
		t.Errorf("默认列表应不含 ::1，GetClientIP = %q, want ::1（不可信→直连）", got)
	}
}

// 外挂文件覆盖：含 CIDR 网段与 ::1 后，命中即信任转发头。
func TestGetClientIP_ExternalOverride(t *testing.T) {
	loadForTest(t, `
# 注释行
127.0.0.1
10.0.0.0/8
::1
`)
	defer resetToEmbedded(t)
	cases := []struct {
		name       string
		remoteAddr string
		realIP     string
		xff        string
		want       string
	}{
		{"CIDR网段命中+X-Real-IP", "10.0.0.5:1234", "172.16.0.9", "", "172.16.0.9"},
		{"CIDR网段命中+XFF", "10.1.2.3:1234", "", "8.8.8.8", "8.8.8.8"},
		{"XFF从右往左跳过可信代理", "127.0.0.1:54321", "", "1.1.1.1, 10.0.0.1, 127.0.0.1", "1.1.1.1"},
		{"XFF全部可信→退直连", "127.0.0.1:54321", "", "10.0.0.1, 127.0.0.1", "127.0.0.1"},
		{"::1命中+X-Real-IP", "[::1]:54321", "192.168.1.50", "", "192.168.1.50"},
		{"外挂未覆盖网段→不信头", "172.16.0.9:1234", "6.6.6.6", "", "172.16.0.9"},
	}
	for _, c := range cases {
		if got := GetClientIP(req(c.remoteAddr, c.realIP, c.xff)); got != c.want {
			t.Errorf("%s: GetClientIP = %q, want %q", c.name, got, c.want)
		}
	}
}

// 外挂文件不存在 → 回退内嵌（trusted_proxies.txt 同名文件）。
func TestLoadTrustedProxies_FallbackToEmbedded(t *testing.T) {
	// 注入隔离外挂根（t.TempDir），避免读 CWD 下真实 hotscripts/trusted_proxies/ 干扰。
	orig := hotswap.HotScriptsDir()
	t.Cleanup(func() { hotswap.SetHotScriptsDir(orig) })
	hotswap.SetHotScriptsDir(t.TempDir())
	defer resetToEmbedded(t)
	if err := LoadTrustedProxies("trusted_proxies.txt"); err != nil {
		t.Fatalf("LoadTrustedProxies(同名文件,外挂缺失) 应回退内嵌: %v", err)
	}
	got := GetClientIP(req("127.0.0.1:54321", "192.168.1.50", ""))
	if got != "192.168.1.50" {
		t.Errorf("回退内嵌后 127.0.0.1 应仍可信，GetClientIP = %q", got)
	}
}

// 路径校验：拒绝绝对路径、盘符（跨平台）、.. 逃逸、空值。
func TestSafeRelPath_Rejects(t *testing.T) {
	bad := []string{"", "/etc/trusted_proxies.txt", `C:\proxies.txt`, "C:/proxies.txt", "d:/x",
		"../proxies.txt", "a/../../proxies.txt", `..\proxies.txt`, `a\..\..\x`,
		"..", "   " /* 纯空白经 TrimSpace 视为空 */}
	for _, p := range bad {
		if _, err := safeRelPath(p); err == nil {
			t.Errorf("safeRelPath(%q) 应拒绝", p)
		}
	}
}

// 合法相对路径：./x 规范化 x、a//b 规范化 a/b、a/ 规范化 a；均不逃逸。
func TestSafeRelPath_Accepts(t *testing.T) {
	cases := map[string]string{
		"./x":                     "x",
		"a//b":                    "a/b",
		"a/":                      "a",
		"sub/trusted_proxies.txt": "sub/trusted_proxies.txt",
	}
	for in, want := range cases {
		got, err := safeRelPath(in)
		if err != nil {
			t.Errorf("safeRelPath(%q) 不应报错: %v", in, err)
		}
		if got != want {
			t.Errorf("safeRelPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// LoadTrustedProxies 对非法路径返回 error（内部复用 safeRelPath）。
func TestLoadTrustedProxies_PathValidation(t *testing.T) {
	// 注入隔离外挂根（t.TempDir），避免读 CWD 下真实 hotscripts/trusted_proxies/ 干扰。
	orig := hotswap.HotScriptsDir()
	t.Cleanup(func() { hotswap.SetHotScriptsDir(orig) })
	hotswap.SetHotScriptsDir(t.TempDir())
	defer resetToEmbedded(t)
	bad := []string{"", "/etc/trusted_proxies.txt", "C:\\proxies.txt", "C:/proxies.txt",
		"../proxies.txt", "a/../../proxies.txt"}
	for _, p := range bad {
		if err := LoadTrustedProxies(p); err == nil {
			t.Errorf("LoadTrustedProxies(%q) 应拒绝", p)
		}
	}
	// 合法相对路径（含子目录）应通过路径校验（内嵌无此文件时由加载报错）
	if err := LoadTrustedProxies("sub/trusted_proxies.txt"); err == nil {
		t.Errorf("LoadTrustedProxies(sub/trusted_proxies.txt) 内嵌无此文件应报错")
	}
}

// 文件内容非法（坏 IP/CIDR）→ fail-fast 报错，不改动当前快照。
func TestLoadTrustedProxies_InvalidContent(t *testing.T) {
	defer resetToEmbedded(t)
	loadForTest(t, "127.0.0.1\n")
	bad := []string{"10.0.0.0/33\n", "999.1.1.1\n", "127.0.0.1\nnot-an-ip\n"}
	for _, text := range bad {
		if err := loadRawForTest(t, text); err == nil {
			t.Errorf("非法内容应报错: %q", text)
		}
	}
	// 快照未被破坏：127.0.0.1 仍可信
	got := GetClientIP(req("127.0.0.1:54321", "192.168.1.50", ""))
	if got != "192.168.1.50" {
		t.Errorf("非法内容加载失败后快照应保持，GetClientIP = %q", got)
	}
}

// 外挂文件仅注释/空行（内容非空字节）：解析为空列表 → 无可信代理 → 任何直连都不信头
// （安全退化：宁取直连 IP 也不信任转发头；用户配置了空列表需自行留意）。
func TestLoadTrustedProxies_CommentsOnly(t *testing.T) {
	defer resetToEmbedded(t)
	loadForTest(t, "# 只有注释\n\n")
	got := GetClientIP(req("127.0.0.1:54321", "192.168.1.50", ""))
	if got != "127.0.0.1" {
		t.Errorf("外挂仅注释（空列表）后 127.0.0.1 不应再可信，GetClientIP = %q", got)
	}
}

// 外挂文件 0 字节（空文件）→ 回退内嵌默认（127.0.0.1 仍可信）。
// 注：外挂名须与内嵌同名（trusted_proxies.txt），0 字节回退才读得到内嵌默认；
// loadForTest 的 zzProxyFile 为测试专用名（内嵌无此文件），无法用于本回退场景。
func TestLoadTrustedProxies_EmptyFile(t *testing.T) {
	defer resetToEmbedded(t)
	// 注入隔离外挂根（t.TempDir），与 loadForTest 模式一致。
	orig := hotswap.HotScriptsDir()
	t.Cleanup(func() { hotswap.SetHotScriptsDir(orig) })
	hotswap.SetHotScriptsDir(t.TempDir())
	dir := filepath.Join(hotswap.HotScriptsDir(), trustedProxiesRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建外挂目录失败: %v", err)
	}
	f := filepath.Join(dir, "trusted_proxies.txt")
	if err := os.WriteFile(f, nil, 0o644); err != nil { // 0 字节文件
		t.Fatalf("写 0 字节外挂文件失败: %v", err)
	}
	if err := LoadTrustedProxies("trusted_proxies.txt"); err != nil {
		t.Fatalf("LoadTrustedProxies: %v", err)
	}
	got := GetClientIP(req("127.0.0.1:54321", "192.168.1.50", ""))
	if got != "192.168.1.50" {
		t.Errorf("0 字节空外挂文件应回退内嵌（127.0.0.1 可信），GetClientIP = %q", got)
	}
}

// 并发读安全：多 goroutine 并发 GetClientIP + 快照替换（-race 下验证无数据竞争）。
func TestGetClientIP_Concurrent(t *testing.T) {
	loadForTest(t, "127.0.0.1\n10.0.0.0/8\n")
	defer resetToEmbedded(t)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = GetClientIP(req("127.0.0.1:54321", "192.168.1.50", ""))
				_ = GetClientIP(req("192.168.1.50:5555", "9.9.9.9", ""))
			}
		}()
	}
	wg.Wait()
}

// loadRawForTest 写外挂文件并加载，返回 error（用于非法内容断言）。
func loadRawForTest(t *testing.T, text string) error {
	t.Helper()
	// 注入隔离外挂根（t.TempDir），避免写默认 hotscripts/ 污染源码树，且不受 CWD 下真实 hotscripts/ 干扰。
	orig := hotswap.HotScriptsDir()
	t.Cleanup(func() { hotswap.SetHotScriptsDir(orig) })
	hotswap.SetHotScriptsDir(t.TempDir())
	dir := filepath.Join(hotswap.HotScriptsDir(), trustedProxiesRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建外挂目录失败: %v", err)
	}
	f := filepath.Join(dir, zzProxyFile)
	if err := os.WriteFile(f, []byte(text), 0o644); err != nil {
		t.Fatalf("写外挂文件失败: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(f)
		_ = os.Remove(dir) // 空目录一并清理（忽略错误）
	})
	return LoadTrustedProxies(zzProxyFile)
}

// 解析辅助：解析结果应含预期数量的 IP/CIDR 条目。
func TestParseTrustedProxies(t *testing.T) {
	l, err := parseTrustedProxies("127.0.0.1\n10.0.0.0/8\n# 注释\n\n::1\n")
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	if len(l.ips) != 2 || len(l.nets) != 1 {
		t.Errorf("解析条目数不符: ips=%d nets=%d", len(l.ips), len(l.nets))
	}
	if !l.contains(net.ParseIP("10.0.0.5")) {
		t.Error("10.0.0.5 应命中 10.0.0.0/8")
	}
	if l.contains(net.ParseIP("11.0.0.1")) {
		t.Error("11.0.0.1 不应命中 10.0.0.0/8")
	}
}
