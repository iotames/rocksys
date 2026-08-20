// block_type 拦截类别枚举（WAF 监控统计，见 docs/WAF_MONITOR_STATS.md）。
//
// 数值稳定（只增不改），SMALLINT 存储：新增检测项追加常量即可，DDL 不动。
// 枚举与 shield.go 拦截点一一对应：Handle 中 3 个（IP 黑名单/路径规则 deny/限流）
// + runWAF 中 7 个（方法/体积/风险路径/路径遍历/SQL 注入/XSS/爬虫 UA）。
package shield

// BlockType 拦截类别（SMALLINT 存储，0 保留表示"未设置/全部"）。
type BlockType int

// 拦截类别常量（数值稳定，禁止改动已有值）。
const (
	BlockIPBlacklist      BlockType = 1  // IP 黑名单（403）
	BlockRateLimit        BlockType = 2  // 令牌桶限流（429）
	BlockMethodNotAllowed BlockType = 3  // 方法白名单（403）
	BlockBodyTooLarge     BlockType = 4  // 请求体超限（413）
	BlockRiskPath         BlockType = 5  // 风险路径（403）
	BlockPathTraversal    BlockType = 6  // 路径遍历（403）
	BlockSQLInjection     BlockType = 7  // SQL 注入（403）
	BlockXSS              BlockType = 8  // XSS（403）
	BlockCrawlerUA        BlockType = 9  // 爬虫/扫描器 UA（403）
	BlockPathRuleDeny     BlockType = 10 // 路径/UA 规则 deny（403）
)

// blockTypeCount 枚举总数（滑动窗口计数器数组长度按此分配）。
const blockTypeCount = 10

// blockTypeNames 类别名注册表（WebUI 展示与 admin API 输出共用）。
// 索引 = BlockType 值 - 1；新增类别须同步追加。
var blockTypeNames = [blockTypeCount]string{
	"IP黑名单",
	"限流",
	"方法不允许",
	"请求体超限",
	"风险路径",
	"路径遍历",
	"SQL注入",
	"XSS",
	"爬虫UA",
	"路径规则",
}

// String 返回类别中文名（越界值返回"未知"）。
func (b BlockType) String() string {
	if b >= 1 && int(b) <= len(blockTypeNames) {
		return blockTypeNames[b-1]
	}
	return "未知"
}

// Valid 判断是否为合法枚举值。
func (b BlockType) Valid() bool { return b >= 1 && int(b) <= blockTypeCount }
