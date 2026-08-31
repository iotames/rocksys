// WAF 规则文件管理端点（WebUI「文件编辑」页签数据源；文件机制见 rules.go）。
//
// 端点（cmd/rocksys 装配时经 adminapi.RegisterPlugin 注入）：
//   - GET  /admin/shield/rules        规则文件清单（含是否外挂覆写/行数/修改时间）
//   - GET  /admin/shield/rules/file   读单个文件当前生效内容 + 内嵌默认内容（?name=）
//   - POST /admin/shield/rules/save   保存到 HOT_SCRIPTS_DIR/rules/<name>（原子写；
//     ScriptHub 监控 ≤3s 自动重建规则快照热更生效）
//
// 安全约束：文件名走白名单（ruleFileMetas，禁止路径穿越）、内容大小上限 512KB、
// 副作用端点 postOnly（防本机恶意页面无凭证触发）。
package shield

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

// 规则文件管理端点路径常量（main.go 装配引用）。
const (
	PathShieldRules     = "/admin/shield/rules"
	PathShieldRulesFile = "/admin/shield/rules/file"
	PathShieldRulesSave = "/admin/shield/rules/save"
)

// rulesSaveMaxBytes 保存内容大小上限（规则文件均为按行特征清单，512KB 足够宽裕）。
const rulesSaveMaxBytes = 512 * 1024

// ruleFileMetas 可编辑规则文件白名单（与 rules/ 目录一致；title/desc 供 WebUI 展示）。
var ruleFileMetas = []struct {
	name  string
	title string
	desc  string
}{
	{ruleFileRiskPaths, "风险路径", "SHIELD_WAF_RISK_PATH 开启后命中的敏感/管理路径特征"},
	{ruleFileSQLPatterns, "SQL 注入特征", "SHIELD_WAF_SQL_INJECTION 开启后匹配 URL 路径/查询串的组合特征"},
	{ruleFileXSSPatterns, "XSS 特征", "SHIELD_WAF_XSS 开启后匹配 URL 查询串的注入特征"},
	{ruleFilePathTraversal, "路径遍历特征", "SHIELD_WAF_PATH_TRAVERSAL 开启后匹配的目录穿越特征"},
	{ruleFileCrawlerUA, "UA黑名单", "SHIELD_WAF_CRAWLER_UA（即 UA 黑名单开关）开启后匹配 User-Agent 的爬虫/扫描器特征；命中 ua_whitelist.txt 白名单的 UA 优先放行（「黑白名单」页签行级管理，本页整文编辑）"},
	{ruleFileUAWhitelist, "UA白名单", "无开关，有数据即生效；UA 黑名单开关（SHIELD_WAF_CRAWLER_UA）开启后命中即在黑名单判定前放行，仅豁免该步，其余检测照常（「黑白名单」页签行级管理，本页整文编辑）"},
	{ruleFileIPBlacklist, "静态 IP 黑名单", "外挂/.env 来源的静态 IP/CIDR 黑名单（与动态 DB 黑名单合并生效）"},
}

// ruleFileName 校验 name 在白名单内，返回元信息。
func ruleFileName(name string) (string, bool) {
	for _, m := range ruleFileMetas {
		if m.name == name {
			return m.name, true
		}
	}
	return "", false
}

// ruleLoaderRawText 读取规则文件当前生效文本（hub 缓存优先、ScriptDir 直读兜底）
// 与是否外挂覆写。与 loadLines 同数据源，但不做小写化/去注释（编辑所见即文件原文）。
func ruleLoaderRawText(rl *ruleLoader, name string) (text string, override bool, err error) {
	if rl.hub != nil {
		text, err = rl.hub.GetScriptText(ruleSubDir, name)
	} else {
		text, err = rl.sd.GetScriptText(name)
	}
	if err != nil {
		return "", false, err
	}
	hotPath := filepath.Join(hotswap.HotScriptsDir(), ruleSubDir, name)
	if _, statErr := os.Stat(hotPath); statErr == nil {
		override = true
	}
	return text, override, nil
}

// ruleEmbeddedText 读取编译期嵌入的规则文件原始文本（"恢复默认"数据源）。
func ruleEmbeddedText(name string) string {
	b, err := fs.ReadFile(shieldRulesFS, "rules/"+name)
	if err != nil {
		return ""
	}
	return string(b)
}

// rulesLoader 取规则加载器（复用 shield 实例的 hub；未注入时 ScriptDir 直读）。
func (h *AdminHandler) rulesLoader() (*ruleLoader, error) {
	if h.shield == nil {
		return nil, fmt.Errorf("shield 未注册")
	}
	return newRuleLoader(h.shield.hub)
}

// Rules GET /admin/shield/rules → 规则文件清单（含外挂覆写状态/生效行数/修改时间）。
func (h *AdminHandler) Rules(w http.ResponseWriter, r *http.Request) {
	rl, err := h.rulesLoader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	files := make([]map[string]any, 0, len(ruleFileMetas))
	for _, m := range ruleFileMetas {
		lines, _ := rl.loadLines(m.name) // 行数展示用途：失败按 0 行（不阻断清单）
		override := false
		var modTime time.Time
		var size int64
		hotPath := filepath.Join(hotswap.HotScriptsDir(), ruleSubDir, m.name)
		if info, statErr := os.Stat(hotPath); statErr == nil {
			override = true
			modTime = info.ModTime()
			size = info.Size()
		}
		entry := map[string]any{
			"name":     m.name,
			"title":    m.title,
			"desc":     m.desc,
			"override": override, // true = 存在外挂覆写文件（保存落点，删除即回退内嵌默认）
			"lines":    len(lines),
		}
		if override {
			entry["modified"] = modTime.Format(time.RFC3339)
			entry["bytes"] = size
		}
		files = append(files, entry)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
}

// RuleFile GET /admin/shield/rules/file?name= → 当前生效内容 + 内嵌默认内容。
func (h *AdminHandler) RuleFile(w http.ResponseWriter, r *http.Request) {
	rl, err := h.rulesLoader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	name, ok := ruleFileName(r.URL.Query().Get("name"))
	if !ok {
		http.Error(w, "非法文件名（不在规则文件白名单内）", http.StatusBadRequest)
		return
	}
	text, override, err := ruleLoaderRawText(rl, name)
	if err != nil {
		http.Error(w, "读取规则文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var modTime string
	hotPath := filepath.Join(hotswap.HotScriptsDir(), ruleSubDir, name)
	if info, statErr := os.Stat(hotPath); statErr == nil {
		modTime = info.ModTime().Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":       name,
		"content":    text,
		"embedded":   ruleEmbeddedText(name),
		"override":   override,
		"modified":   modTime,
		"hot_path":   hotPath,
		"max_tokens": rulesSaveMaxBytes,
	})
}

// RuleSave POST /admin/shield/rules/save（body: {name, content}）→ 原子写外挂覆写文件。
// 保存后由 ScriptHub 监控自动感知（≤ HOT_FILES_WATCH_INTERVAL，默认 3s）重建规则快照热更生效。
// 与黑白名单端点不同：规则文件不依赖 DB，仅做 POST 门控（防本机恶意页面无凭证触发）。
func (h *AdminHandler) RuleSave() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
			return
		}
		h.ruleSave(w, r)
	}
}

func (h *AdminHandler) ruleSave(w http.ResponseWriter, r *http.Request) {
	if h.shield == nil {
		http.Error(w, "shield 未注册", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, rulesSaveMaxBytes+1024)).Decode(&body); err != nil {
		http.Error(w, "请求体非法: "+err.Error(), http.StatusBadRequest)
		return
	}
	name, ok := ruleFileName(body.Name)
	if !ok {
		http.Error(w, "非法文件名（不在规则文件白名单内）", http.StatusBadRequest)
		return
	}
	if len(body.Content) > rulesSaveMaxBytes {
		http.Error(w, fmt.Sprintf("内容超出上限（%dKB）", rulesSaveMaxBytes/1024), http.StatusBadRequest)
		return
	}
	// 统一 CRLF → LF，结尾补一个换行（按行解析语义友好）
	content := strings.ReplaceAll(body.Content, "\r\n", "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	dir := filepath.Join(hotswap.HotScriptsDir(), ruleSubDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "创建外挂目录失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// 原子写：临时文件 + rename，避免监控读到半截内容
	target := filepath.Join(dir, name)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		http.Error(w, "写入临时文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		http.Error(w, "替换目标文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":        true,
		"name":      name,
		"path":      target,
		"bytes":     len(content),
		"note":      "已保存，规则快照将在 ≤3s 内自动重建热更生效（HOT_FILES_WATCH_INTERVAL）",
		"timestamp": time.Now().Format(time.RFC3339),
	})
}
