// WAF 规则文件加载器：外置目录优先、编译期嵌入兜底（复用 internal/hotswap.ScriptDir，
// 与 internal/db 加载 sql/<dbtype>/ 同机制，实现"改规则不重新编译"）。
//
// 规则文件按行组织：# 开头为注释、空行忽略、其余每行一个模式。
// 外置覆写目录统一为 HOT_SCRIPTS_DIR/rules（默认 hotscripts/rules，相对工作目录），
// 存在同名文件时优先使用；找不到/内容为空时回退到本包编译期嵌入的 rules/。
package shield

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"

	"rocksys/internal/hotswap"
)

//go:embed rules
var shieldRulesFS embed.FS

// ruleSubDir WAF 规则在 HOT_SCRIPTS_DIR 统一外挂根下的固定子目录：
// 外挂覆写目录 = HOT_SCRIPTS_DIR/rules（默认 hotscripts/rules，相对工作目录）。
const ruleSubDir = "rules"

// 规则文件名清单（与 rules/ 目录一致）。
const (
	ruleFileRiskPaths     = "risk_paths.txt"
	ruleFileSQLPatterns   = "sql_patterns.txt"
	ruleFileXSSPatterns   = "xss_patterns.txt"
	ruleFilePathTraversal = "path_traversal.txt"
	ruleFileCrawlerUA     = "crawler_ua.txt" // UA黑名单（开关 SHIELD_WAF_CRAWLER_UA）
	ruleFileUAWhitelist   = "ua_whitelist.txt"
	ruleFileIPBlacklist   = "ip_blacklist.txt"
)

// RuleSet 一次加载的全部规则（Start 时读取，编译进不可变快照）。
type RuleSet struct {
	RiskPaths     []string // 风险路径（小写）
	SQLPatterns   []string // SQL 注入特征
	XSSPatterns   []string // XSS 特征
	PathTraversal []string // 路径遍历特征
	CrawlerUA     []string // 爬虫 UA 特征（小写，UA黑名单）
	UAWhitelist   []string // UA 白名单（小写；优先于黑名单，仅豁免爬虫 UA 拦截步，无开关、有数据即生效）
	IPBlacklist   []string // IP 黑名单（精确 IP / CIDR）
}

// ruleLoader 规则加载器：外置目录优先、嵌入兜底。
type ruleLoader struct {
	sd  *hotswap.ScriptDir // 底层 ScriptDir（注册进 ScriptHub 与无 hub 场景兜底）
	hub *hotswap.ScriptHub // 统一内容中枢（nil 时回落 sd 直读）
}

// newRuleLoader 创建加载器。外挂覆写目录统一为 HOT_SCRIPTS_DIR/rules
// （默认 hotscripts/rules，相对工作目录），缺失时自动回退嵌入文件（ScriptDir 语义）。
// ★ 统一收敛：不再提供独立 SHIELD_RULES_DIR 配置，子目录固定为 "rules"。
// hub 为可选参数（装配方注入 ScriptHub 后，读取统一走中枢缓存，≤3s 热更）。
func newRuleLoader(hubs ...*hotswap.ScriptHub) (*ruleLoader, error) {
	sub, err := fs.Sub(shieldRulesFS, "rules")
	if err != nil {
		return nil, fmt.Errorf("shield: 读取内嵌 rules/ 目录失败: %w", err)
	}
	rl := &ruleLoader{sd: hotswap.NewScriptDir(sub, ruleSubDir)}
	if len(hubs) > 0 {
		rl.hub = hubs[0]
	}
	return rl, nil
}

// load 读取全部规则文件。
func (rl *ruleLoader) load() (*RuleSet, error) {
	rs := &RuleSet{}
	var err error
	if rs.RiskPaths, err = rl.loadLines(ruleFileRiskPaths); err != nil {
		return nil, err
	}
	if rs.SQLPatterns, err = rl.loadLines(ruleFileSQLPatterns); err != nil {
		return nil, err
	}
	if rs.XSSPatterns, err = rl.loadLines(ruleFileXSSPatterns); err != nil {
		return nil, err
	}
	if rs.PathTraversal, err = rl.loadLines(ruleFilePathTraversal); err != nil {
		return nil, err
	}
	if rs.CrawlerUA, err = rl.loadLines(ruleFileCrawlerUA); err != nil {
		return nil, err
	}
	if rs.UAWhitelist, err = rl.loadLines(ruleFileUAWhitelist); err != nil {
		return nil, err
	}
	if rs.IPBlacklist, err = rl.loadLines(ruleFileIPBlacklist); err != nil {
		return nil, err
	}
	return rs, nil
}

// loadLines 读取单个规则文件并解析为行列表（小写化）。
// 注入 hub 时经统一缓存读取（内容由中枢监控保证最新）；否则回落 ScriptDir 直读。
func (rl *ruleLoader) loadLines(name string) ([]string, error) {
	var text string
	var err error
	if rl.hub != nil {
		text, err = rl.hub.GetScriptText(ruleSubDir, name)
	} else {
		text, err = rl.sd.GetScriptText(name)
	}
	if err != nil {
		return nil, fmt.Errorf("shield: 加载 WAF 规则文件 %q 失败: %w", name, err)
	}
	return parseRuleLines(text), nil
}

// parseRuleLines 解析规则文件：按行、去空白、统一小写化、忽略 # 注释与空行。
// （规则匹配侧一律小写子串匹配，故加载时统一小写化，外挂文件大小写不敏感）
func parseRuleLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}
