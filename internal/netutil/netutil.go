// Package netutil 网络层工具：客户端真实 IP 获取（可信代理模型）。
//
// ★ 主仓库收敛单一入口：获取客户端 IP 一律经本包 GetClientIP，
// 禁止其他包自行解析 X-Real-IP / X-Forwarded-For / RemoteAddr。
//
// 可信代理模型：直连源 IP（TCP 层）命中可信代理列表时才信任转发头，
// 否则直接返回直连源 IP，防公网直连伪造。可信代理列表经 internal/hotswap
// 外挂文件优先、内嵌兜底加载（默认 127.0.0.1），启动装配时加载一次快照。
package netutil

import (
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"

	"rocksys/internal/hotswap"
)

//go:embed trusted_proxies.txt
var trustedProxiesFS embed.FS

// trustedProxiesRoot 可信代理外挂子目录：TRUSTED_PROXIES_FILE 为相对
// HOT_SCRIPTS_DIR/trusted_proxies（默认 hotscripts/trusted_proxies）的文件路径
// （不允许绝对路径、不允许 .. 逃逸，见 safeRelPath）。
const trustedProxiesRoot = "trusted_proxies"

// trustedProxyList 可信代理快照：精确 IP + CIDR 网段。
type trustedProxyList struct {
	ips  []net.IP
	nets []*net.IPNet
}

// contains 判断 ip 是否命中可信代理列表。
func (l *trustedProxyList) contains(ip net.IP) bool {
	if l == nil || ip == nil {
		return false
	}
	for _, p := range l.ips {
		if p.Equal(ip) {
			return true
		}
	}
	for _, n := range l.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// proxyList 当前生效的可信代理快照。默认 = 内嵌文件（127.0.0.1），
// LoadTrustedProxies 成功后原子替换；GetClientIP 每次请求原子读，无锁。
var proxyList atomic.Pointer[trustedProxyList]

// init 默认快照：解析内嵌 trusted_proxies.txt，保证未装配时行为不回归
// （127.0.0.1 默认可信，与旧 loopback 特判等价且只多不少）。
func init() {
	b, err := fs.ReadFile(trustedProxiesFS, "trusted_proxies.txt")
	if err != nil {
		panic("netutil: 读取内嵌可信代理文件失败: " + err.Error())
	}
	l, err := parseTrustedProxies(string(b))
	if err != nil {
		panic("netutil: 内嵌可信代理默认值解析失败: " + err.Error())
	}
	proxyList.Store(l)
}

// LoadTrustedProxies 加载可信代理列表（启动装配时调用一次，快照替换原子生效）。
//
// filePath 为相对 HOT_SCRIPTS_DIR/trusted_proxies 外挂子目录的文件路径
// （默认 trusted_proxies.txt，不允许绝对路径/.. 逃逸，见 safeRelPath）；
// 外挂文件优先（可热修改无需重新编译），缺失/内容为空时回退内嵌 trusted_proxies.txt。
// 文件内容解析失败（非法 IP/CIDR）返回 error，装配方应 fail-fast，避免静默降级掩盖配置错误。
// ★ 统一收敛：外挂根目录统一为 HOT_SCRIPTS_DIR（默认 hotscripts，相对工作目录）。
func LoadTrustedProxies(filePath string) error {
	rel, err := safeRelPath(filePath)
	if err != nil {
		return err
	}
	sd := hotswap.NewScriptDir(trustedProxiesFS, trustedProxiesRoot)
	text, err := sd.GetScriptText(rel)
	if err != nil {
		return fmt.Errorf("netutil: 加载可信代理列表 %q 失败: %w", rel, err)
	}
	l, err := parseTrustedProxies(text)
	if err != nil {
		return err
	}
	proxyList.Store(l)
	return nil
}

// safeRelPath 校验并规范化可信代理文件相对路径：
// 拒绝绝对路径（含 Windows 盘符，跨平台判断）、空路径与 .. 上级目录逃逸。
func safeRelPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("netutil: TRUSTED_PROXIES_FILE 不能为空")
	}
	norm := strings.ReplaceAll(p, "\\", "/") // 反斜杠统一视为分隔符，防 Windows 风格路径在 Linux 上逃逸
	// 跨平台盘符判断（Windows 卷名 C:/D:，与运行平台无关，Linux 上同样拒绝）
	if hasDriveLetter(norm) || filepath.VolumeName(p) != "" || strings.HasPrefix(norm, "/") {
		return "", fmt.Errorf("netutil: TRUSTED_PROXIES_FILE 不允许绝对路径: %q", p)
	}
	clean := filepath.Clean(filepath.FromSlash(norm))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("netutil: TRUSTED_PROXIES_FILE 不允许 .. 上级目录逃逸: %q", p)
	}
	return filepath.ToSlash(clean), nil
}

// hasDriveLetter 判断字符串是否以盘符（如 C:、d:）开头。
func hasDriveLetter(s string) bool {
	if len(s) < 2 || s[1] != ':' {
		return false
	}
	c := s[0]
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// parseTrustedProxies 解析可信代理文件：按行、去空白、忽略 # 注释与空行；
// 含 / 按 CIDR 网段解析，否则按精确 IP 解析。
func parseTrustedProxies(text string) (*trustedProxyList, error) {
	l := &trustedProxyList{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "/") {
			_, ipnet, err := net.ParseCIDR(line)
			if err != nil {
				return nil, fmt.Errorf("netutil: 可信代理 CIDR 解析失败 %q: %w", line, err)
			}
			l.nets = append(l.nets, ipnet)
			continue
		}
		ip := net.ParseIP(line)
		if ip == nil {
			return nil, fmt.Errorf("netutil: 可信代理 IP 解析失败 %q", line)
		}
		l.ips = append(l.ips, ip)
	}
	return l, nil
}

// GetOriginalDirectIP 获取直连源 IP（TCP 层真实来源，不读取任何 HTTP 头）。
// 仅用于内部可信判断；主仓库获取客户端 IP 一律用 GetClientIP。
func GetOriginalDirectIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// GetClientIP 获取客户端真实 IP（主仓库统一入口）。
//
// 逻辑：
//  1. 取直连源 IP（TCP 层）；未命中可信代理列表 → 直接返回（不信任可伪造的转发头）。
//  2. 命中可信代理 → 依次尝试：
//     a. X-Real-IP：可信代理覆写语义，net.ParseIP 校验合法即返回；
//     b. X-Forwarded-For：从右往左跳过可信代理，取第一个合法且不可信的 IP
//     （业界 real_ip 语义；非法值跳过继续向左）；
//     c. 兜底返回直连源 IP（如 XFF 全为可信代理/非法值）。
//
// 返回值为 net.IP.String() 规范化形式（如 IPv4 输出点分十进制）。
func GetClientIP(r *http.Request) string {
	direct := GetOriginalDirectIP(r)
	ip := net.ParseIP(direct)
	if ip == nil || !proxyList.Load().contains(ip) {
		return direct
	}
	// X-Real-IP：覆写语义，校验合法性
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		if v := net.ParseIP(realIP); v != nil {
			return v.String()
		}
	}
	// X-Forwarded-For：从右往左跳过可信代理
	if v := parseForwardedFor(r.Header.Get("X-Forwarded-For")); v != "" {
		return v
	}
	return direct
}

// parseForwardedFor 解析 X-Forwarded-For：从右往左（最近代理方向）跳过可信代理，
// 返回第一个合法且不在可信列表的 IP；非法值跳过继续向左；全为可信/非法时返回空串。
func parseForwardedFor(xff string) string {
	if xff == "" {
		return ""
	}
	list := proxyList.Load()
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			continue
		}
		if list.contains(ip) {
			continue
		}
		return ip.String()
	}
	return ""
}
