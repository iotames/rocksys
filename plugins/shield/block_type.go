// block_type 拦截类别枚举（WAF 监控统计；数据字典见 docs/DATA_DICT.md）。
//
// 数值稳定（只增不改），SMALLINT 存储：新增检测项追加常量即可，DDL 不动。
// 枚举与 shield.go 拦截点一一对应：Handle 中 3 个（IP 黑名单/路径规则 deny/限流）
// + runWAF 中 7 个（方法/体积/风险路径/路径遍历/SQL 注入/XSS/爬虫 UA）。
//
// ★ 维护约定：本枚举与三方言建表脚本（sql/<sqlite|mysql|postgres>/shield_event_create_table.sql）
// 的表头/列注释一一对应，新增或改动枚举值时必须同步更新 SQL 注释（DBA 依赖注释理解数据）。
package shield

// BlockType 拦截类别（SMALLINT 存储）。
//
// ★ 语境分离口径（详见 docs/IP_BLACKLIST_PLAN.md §5.2）：
//   - shield_event 拦截事件永远只写 1-10（真实拦截动作）；
//   - 0（其他）与 11（人工收录）仅出现在 ip_blacklist 表语境（拉黑条目来源标记）；
//   - 查询参数 0=全部 属语境分离的另一含义（过滤语义），与本枚举值语义区分；
//   - 拦截明细过滤校验保持 0-10 不变。
type BlockType int

// 拦截类别常量（数值稳定，禁止改动已有值）。
const (
	BlockOther            BlockType = 0  // 其他（仅 ip_blacklist 语境：非拦截识别的兜底来源，如批量导入未标注）
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
	BlockManual           BlockType = 11 // 人工收录（仅 ip_blacklist 语境：管理员人工录入的拉黑条目）
)

// blockTypeCount 枚举总数（滑动窗口计数器数组长度按此分配）。
const blockTypeCount = 10

// blockTypeNames 类别名注册表（WebUI 展示与 admin API 输出共用）。
// 索引 = BlockType 值 - 1；新增类别须同步追加。
// 注意：0 与 11 不在数组内（语境特殊，见 BlockType 注释），由 String() 特判。
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

// String 返回类别中文名（0="其他"、11="人工收录"仅黑名单语境；越界值返回"未知"）。
func (b BlockType) String() string {
	switch b {
	case BlockOther:
		return "其他"
	case BlockManual:
		return "人工收录"
	}
	if b >= 1 && int(b) <= len(blockTypeNames) {
		return blockTypeNames[b-1]
	}
	return "未知"
}

// Valid 判断是否为合法枚举值。
func (b BlockType) Valid() bool { return b >= 1 && int(b) <= blockTypeCount }

// banTier 自动拉黑风险分档（类别→处置策略映射，见 auto_ban.go）。
// 分档定死在代码不占配置：档位是安全策略判断而非运维参数，给配置口子只会调坏
// （能替用户减负、按需取舍攻击面的开关才保留）。
type banTier int

const (
	banTierAttack  banTier = iota + 1 // 攻击档：风险路径/遍历/SQL注入/XSS（真实攻击，命中 1 次直接永久封禁）
	banTierCrawler                    // 爬虫档：爬虫/扫描器 UA（君子协议伪造成本低但按流量计费烧钱，独立低阈值限时封禁）
	banTierGeneric                    // 通用档：限流（异常高并发判断）/方法白名单/体积超限/规则 deny（通用阈值限时封禁）
)

// banTierOf 返回拦截类别所属分档；非真实拦截类别（0/1/11 语境值）返回 0（不参与分档）。
func banTierOf(bt BlockType) banTier {
	switch bt {
	case BlockRiskPath, BlockPathTraversal, BlockSQLInjection, BlockXSS:
		return banTierAttack
	case BlockCrawlerUA:
		return banTierCrawler
	case BlockRateLimit, BlockMethodNotAllowed, BlockBodyTooLarge, BlockPathRuleDeny:
		return banTierGeneric
	}
	return 0
}
