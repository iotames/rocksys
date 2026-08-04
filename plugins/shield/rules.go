// WAF 规则文件加载器：外置目录优先、编译期嵌入兜底（复用 internal/hotswap.ScriptDir，
// 与 internal/db 加载 sql/<dbtype>/ 同机制，实现"改规则不重新编译"）。
//
// 规则文件按行组织：# 开头为注释、空行忽略、其余每行一个模式。
// 外置目录（SHIELD_RULES_DIR，默认 "rules"）存在同名文件时优先使用；
// 找不到/内容为空时回退到本包编译期嵌入的 rules/。
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

// 规则文件名清单（与 rules/ 目录一致）。
const (
	ruleFileRiskPaths    = "risk_paths.txt"
	ruleFileSQLPatterns  = "sql_patterns.txt"
	ruleFileXSSPatterns  = "xss_patterns.txt"
	ruleFilePathTraversal = "path_traversal.txt"
	ruleFileCrawlerUA    = "crawler_ua.txt"
)

// RuleSet 一次加载的全部规则（Start 时读取，编译进不可变快照）。
type RuleSet struct {
	RiskPaths    []string // 风险路径（小写）
	SQLPatterns  []string // SQL 注入特征
	XSSPatterns  []string // XSS 特征
	PathTraversal []string // 路径遍历特征
	CrawlerUA    []string // 爬虫 UA 特征（小写）
}

// ruleLoader 规则加载器：外置目录优先、嵌入兜底。
type ruleLoader struct {
	sd *hotswap.ScriptDir
}

// newRuleLoader 创建加载器。rulesDir 为外置目录（相对工作目录），
// 目录不存在时自动回退嵌入文件（ScriptDir 语义）。
func newRuleLoader(rulesDir string) (*ruleLoader, error) {
	sub, err := fs.Sub(shieldRulesFS, "rules")
	if err != nil {
		return nil, fmt.Errorf("shield: 读取内嵌 rules/ 目录失败: %w", err)
	}
	return &ruleLoader{sd: hotswap.NewScriptDir(sub, rulesDir)}, nil
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
	return rs, nil
}

// loadLines 读取单个规则文件并解析为行列表（小写化）。
func (rl *ruleLoader) loadLines(name string) ([]string, error) {
	text, err := rl.sd.GetScriptText(name)
	if err != nil {
		return nil, fmt.Errorf("shield: 加载 WAF 规则文件 %q 失败: %w", name, err)
	}
	return parseRuleLines(text), nil
}

// parseRuleLines 解析规则文件：按行、去空白、忽略 # 注释与空行。
func parseRuleLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}
