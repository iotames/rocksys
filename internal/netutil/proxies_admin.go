// 可信代理文件在线编辑端点（WebUI「可信代理」页数据源；文件机制见 netutil.go）。
//
// 端点（cmd/rocksys 装配时经 adminapi.RegisterPlugin 注入）：
//   - GET  /admin/proxy/trusted       可信代理文件清单（含是否外挂覆写/行数/修改时间）
//   - GET  /admin/proxy/trusted/file  读当前生效内容 + 内嵌默认内容（?name=）
//   - POST /admin/proxy/trusted/save  保存到 HOT_SCRIPTS_DIR/trusted_proxies/<rel>（原子写；
//                                     ScriptHub 监控 ≤3s 自动重读快照热更生效）
//
// 安全约束：文件名固定为装配传入的 TRUSTED_PROXIES_FILE（safeRelPath 校验，禁止路径穿越）、
// 内容大小上限 512KB、保存前先解析校验（非法 IP/CIDR 直接拒绝，不给热更回调留失败）、
// 保存端点仅 POST（防本机恶意页面无凭证触发）。
package netutil

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rocksys/internal/hotswap"
)

// 可信代理管理端点路径常量（main.go 装配引用）。
const (
	PathProxyTrusted     = "/admin/proxy/trusted"
	PathProxyTrustedFile = "/admin/proxy/trusted/file"
	PathProxyTrustedSave = "/admin/proxy/trusted/save"
)

// proxiesSaveMaxBytes 保存内容大小上限（IP/CIDR 按行清单，512KB 足够宽裕）。
const proxiesSaveMaxBytes = 512 * 1024

// trustedProxiesEmbedded 内嵌默认文件名（仅该文件名提供"恢复默认"内嵌内容）。
const trustedProxiesEmbedded = "trusted_proxies.txt"

// ProxiesAdmin 可信代理文件在线编辑 handler。
// relPath 为装配传入的 TRUSTED_PROXIES_FILE（唯一可编辑文件，构造时校验）；
// hub 为 ScriptHub 统一内容中枢（可 nil，nil 时退化为 ScriptDir 直读）。
type ProxiesAdmin struct {
	relPath string
	hub     *hotswap.ScriptHub
}

// NewProxiesAdmin 构造 handler：relPath 经 safeRelPath 校验（非法 fail-fast）。
func NewProxiesAdmin(hub *hotswap.ScriptHub, relPath string) (*ProxiesAdmin, error) {
	rel, err := safeRelPath(relPath)
	if err != nil {
		return nil, err
	}
	return &ProxiesAdmin{relPath: rel, hub: hub}, nil
}

// currentText 读取当前生效文本（hub 缓存优先、ScriptDir 直读兜底；所见即文件原文）。
func (p *ProxiesAdmin) currentText() (string, error) {
	if p.hub != nil {
		return p.hub.GetScriptText(TrustedProxiesRoot, p.relPath)
	}
	return hotswap.NewScriptDir(trustedProxiesFS, trustedProxiesRoot).GetScriptText(p.relPath)
}

// hotPath 外挂覆写文件落点。
func (p *ProxiesAdmin) hotPath() string {
	return filepath.Join(hotswap.HotScriptsDir(), TrustedProxiesRoot, filepath.FromSlash(p.relPath))
}

// statHot 外挂覆写文件状态（不存在返回 nil）。
func (p *ProxiesAdmin) statHot() os.FileInfo {
	if info, err := os.Stat(p.hotPath()); err == nil {
		return info
	}
	return nil
}

// effectiveLines 统计当前生效内容的有效行数（去 # 注释与空行）。
func effectiveLines(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			n++
		}
	}
	return n
}

// List GET /admin/proxy/trusted → 可信代理文件清单（单文件，结构与 shield 规则清单一致）。
func (p *ProxiesAdmin) List(w http.ResponseWriter, r *http.Request) {
	text, err := p.currentText()
	if err != nil {
		http.Error(w, "读取可信代理文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	entry := map[string]any{
		"name":     p.relPath,
		"title":    "可信代理列表",
		"desc":     "直连源 IP 命中列表时才信任 X-Forwarded-For 等转发头（默认 127.0.0.1）",
		"override": false, // true = 存在外挂覆写文件（保存落点，删除即回退内嵌默认）
		"lines":    effectiveLines(text),
	}
	if info := p.statHot(); info != nil {
		entry["override"] = true
		entry["modified"] = info.ModTime().Format(time.RFC3339)
		entry["bytes"] = info.Size()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{entry}})
}

// File GET /admin/proxy/trusted/file?name= → 当前生效内容 + 内嵌默认内容。
func (p *ProxiesAdmin) File(w http.ResponseWriter, r *http.Request) {
	if name := r.URL.Query().Get("name"); name != p.relPath {
		http.Error(w, "非法文件名（仅允许装配配置的 TRUSTED_PROXIES_FILE）", http.StatusBadRequest)
		return
	}
	text, err := p.currentText()
	if err != nil {
		http.Error(w, "读取可信代理文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var embedded string
	if p.relPath == trustedProxiesEmbedded {
		if b, rerr := fs.ReadFile(trustedProxiesFS, trustedProxiesEmbedded); rerr == nil {
			embedded = string(b)
		}
	}
	modified := ""
	if info := p.statHot(); info != nil {
		modified = info.ModTime().Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":       p.relPath,
		"content":    text,
		"embedded":   embedded,
		"override":   p.statHot() != nil,
		"modified":   modified,
		"hot_path":   p.hotPath(),
		"max_tokens": proxiesSaveMaxBytes,
	})
}

// Save POST /admin/proxy/trusted/save（body: {name, content}）→ 原子写外挂覆写文件。
// 保存前先解析校验（非法 IP/CIDR 返回 400）；保存后由 ScriptHub 监控自动感知
// （≤ HOT_FILES_WATCH_INTERVAL，默认 3s）重读快照热更生效。
func (p *ProxiesAdmin) Save() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
			return
		}
		p.save(w, r)
	}
}

func (p *ProxiesAdmin) save(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, proxiesSaveMaxBytes+1024)).Decode(&body); err != nil {
		http.Error(w, "请求体非法: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Name != p.relPath {
		http.Error(w, "非法文件名（仅允许装配配置的 TRUSTED_PROXIES_FILE）", http.StatusBadRequest)
		return
	}
	if len(body.Content) > proxiesSaveMaxBytes {
		http.Error(w, fmt.Sprintf("内容超出上限（%dKB）", proxiesSaveMaxBytes/1024), http.StatusBadRequest)
		return
	}
	content := strings.ReplaceAll(body.Content, "\r\n", "\n") // 统一 CRLF → LF
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	// 保存前解析校验：非法 IP/CIDR 直接拒绝（空内容合法 = 回退内嵌默认）
	if err := ApplyTrustedProxiesText(content); err != nil {
		http.Error(w, "内容校验失败（不生效）: "+err.Error(), http.StatusBadRequest)
		return
	}
	dir := filepath.Dir(p.hotPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "创建外挂目录失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// 原子写：临时文件 + rename，避免监控读到半截内容
	tmp := p.hotPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		http.Error(w, "写入临时文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, p.hotPath()); err != nil {
		_ = os.Remove(tmp)
		http.Error(w, "替换目标文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":        true,
		"name":      p.relPath,
		"path":      p.hotPath(),
		"bytes":     len(content),
		"note":      "已保存，可信代理快照将在 ≤3s 内自动重读热更生效（HOT_FILES_WATCH_INTERVAL）",
		"timestamp": time.Now().Format(time.RFC3339),
	})
}
