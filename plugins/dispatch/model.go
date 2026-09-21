// 运行时对象图：路由规则 → 负载均衡器 → 上游节点（ROUTE_DISPATCH 三层模型）。
//
// 本文件为 STEP2 旁路新建（与旧 DSL 链 dispatch.go/router.go/chash.go 完全解耦）：
//   - DB 行结构（RuleRow/UpstreamRow/NodeRow/UpstreamNodeRow）与三方言建表脚本、
//     docs/DATA_DICT.md 字段一一对应；
//   - BuildGraph 从四表行构建不可变运行时快照（RouteSnapshot）：均衡器经关系表
//     展开为节点列表（含权重/优先级），规则持均衡器引用；
//   - ValidateGraph 加载期校验（S6）：引用存在、节点 URL 合法 http(s)://、weight
//     正整数、priority ∈ {0,1}、match_order 1–999、path_value 以 / 开头、path_type/
//     algo 枚举值合法；domain 构建期统一归一转小写——放行不拒绝（外部改库存入
//     大写 domain 时静默归一，不导致快照构建失败）。
//
// 并发安全注记：快照整体不可变（含游标初始状态），热更经整体原子替换（STEP5）；
// UpstreamRT 内的轮询游标是唯一的可变状态，经互斥锁保护。
package dispatch

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// PathType 路径类型（dispatch_rule.path_type，INTEGER 存储，数值稳定只增不改；
// 数据字典见 docs/DATA_DICT.md §3.6，三方言建表脚本注释一一对应）。
type PathType int

// 路径类型枚举（数值稳定，禁止改动已有值）。
const (
	PathTypePrefix PathType = 1 // 前缀匹配（段对齐且命中自身）；path_value=/ + 前缀 = 全路径兜底
	PathTypeExact  PathType = 2 // 精确匹配（全等，不做尾斜杠归一）
	PathTypeMode   PathType = 3 // 模式匹配（:param 捕获 / * 通配，段匹配语义）
)

// Valid 判断是否为合法枚举值。
func (p PathType) Valid() bool { return p == PathTypePrefix || p == PathTypeExact || p == PathTypeMode }

// AlgoKind 均衡策略（dispatch_upstream.algo，INTEGER 存储，数值稳定只增不改；
// 数据字典见 docs/DATA_DICT.md §3.7）。
type AlgoKind int

// 均衡策略枚举（数值稳定，禁止改动已有值）。
const (
	AlgoRoundRobin AlgoKind = 1 // round_robin：平滑加权轮询（默认；权重默认 1 即纯轮询）
	AlgoLeastConn  AlgoKind = 2 // least_conn：在途最少优先（平局回落轮询游标）
)

// Valid 判断是否为合法枚举值。
func (a AlgoKind) Valid() bool { return a == AlgoRoundRobin || a == AlgoLeastConn }

// Priority 节点优先级（dispatch_upstream_node.priority，INTEGER 存储，数值稳定
// 只增不改；NGINX backup 同款语义，数据字典见 docs/DATA_DICT.md §3.8）。
type Priority int

// 优先级枚举（数值稳定，禁止改动已有值）。
const (
	PriorityPrimary Priority = 0 // 高优（默认）：参与常规负载均衡
	PriorityBackup  Priority = 1 // 备份：高优健康集全不健康时才启用
)

// Valid 判断是否为合法枚举值。
func (p Priority) Valid() bool { return p == PriorityPrimary || p == PriorityBackup }

// RuleRow 路由规则表行（dispatch_rule，字段与 DATA_DICT §2.11 一一对应）。
type RuleRow struct {
	ID         int64  // 自增主键
	MatchOrder int    // 匹配序号 1–999 升序、命中即停
	Domain     string // 域名（可选）：空=任意；非空=精确匹配（归一小写）
	PathType   int    // 路径类型枚举（PathType）
	PathValue  string // 路径值，以 / 开头
	Title      string // 规则标题（人类可读，空允许）
	UpstreamID int64  // → dispatch_upstream.id
	Enabled    bool   // 启用（构建期只拉启用行，此处保留字段语义完整）
}

// UpstreamRow 负载均衡器表行（dispatch_upstream，字段与 DATA_DICT §2.12 一一对应）。
type UpstreamRow struct {
	ID            int64  // 自增主键
	Name          string // 均衡器名称（唯一）
	Algo          int    // 均衡策略枚举（Algo）
	StickyEnabled bool   // 会话保持开关
	StickyCookie  string // Cookie 名（默认 rocksys_node；仅 StickyEnabled 时生效）
	Enabled       bool   // 启用（停用 = 引用它的规则全部 503）
}

// NodeRow 后端节点表行（dispatch_node，字段与 DATA_DICT §2.13 一一对应）。
type NodeRow struct {
	ID           int64  // 自增主键
	Name         string // 节点名称（空允许）
	URL          string // http(s)://host[:port]，唯一
	HCIntervalMS int    // 探活周期毫秒（本步不消费，STEP4 健康检查中心使用）
	HCTimeoutMS  int    // 探活超时毫秒（同上）
	HCPath       string // 探活路径；空 = 不探活视为健康（同上）
	Enabled      bool   // 启用
}

// UpstreamNodeRow 节点与均衡器关系表行（dispatch_upstream_node，字段与
// DATA_DICT §2.14 一一对应）。
type UpstreamNodeRow struct {
	ID         int64 // 自增主键
	UpstreamID int64 // → dispatch_upstream.id
	NodeID     int64 // → dispatch_node.id
	Weight     int   // 权重正整数（默认 1，round_robin 平滑加权用）
	Priority   int   // 优先级枚举（Priority）
	Enabled    bool  // 关系启用（预留：停用即该节点退出此均衡器）
}

// NodeRT 运行时上游节点（关系展开后的节点视图，不可变）。
type NodeRT struct {
	ID       int64    // 节点 id（跨热更稳定，sticky Cookie 值与 registry 键均用此 id）
	URL      string   // http(s)://host[:port]
	Weight   int      // 权重正整数
	Priority Priority // 0=高优 / 1=备份
}

// UpstreamRT 运行时负载均衡器（关系展开为节点列表，除游标外不可变）。
type UpstreamRT struct {
	ID            int64     // 均衡器 id
	Name          string    // 均衡器名称
	Algo          AlgoKind  // 均衡策略
	StickyEnabled bool      // 会话保持开关
	StickyCookie  string    // Cookie 名（空时取 DefaultStickyCookie）
	Enabled       bool      // 启用（fail-closed：停用不剔除引用它的规则，命中后由 Handle 503）
	Nodes         []*NodeRT // 关系展开的节点列表（构建期已校验非空）

	rr rrCursor // 平滑加权轮询游标（与 Nodes 等长；互斥锁保护，快照内唯一可变状态）
}

// rrCursor 平滑加权轮询游标状态（复用 balancer.go rrState 思路，作用于新对象图）。
// 并发安全：互斥锁保护 current；快照替换（STEP5）后新快照携带全新零值游标，
// 旧游标随旧快照被在途请求安全使用至请求结束。
type rrCursor struct {
	mu      sync.Mutex
	current []int
}

// RuleRT 运行时路由规则（持均衡器引用，不可变）。
type RuleRT struct {
	ID         int64       // 规则 id
	MatchOrder int         // 匹配序号 1–999
	Domain     string      // 归一小写后的域名（空=任意）
	PathType   PathType    // 路径类型
	PathValue  string      // 路径值
	Title      string      // 规则标题
	Upstream   *UpstreamRT // 命中后的转发目标均衡器（构建期已校验存在）
}

// RouteSnapshot 运行时路由快照（整体不可变，热更经原子替换——STEP5）。
type RouteSnapshot struct {
	Rules     []*RuleRT             // 规则列表（按 match_order,id 升序，仅启用行）
	Upstreams map[int64]*UpstreamRT // 均衡器 id → 运行时对象
}

// GraphInput 构建对象图的四表行输入（调用方负责只传参与构建的行——启用且未软删）。
type GraphInput struct {
	Rules     []RuleRow
	Upstreams []UpstreamRow
	Nodes     []NodeRow
	Relations []UpstreamNodeRow
}

// BuildGraph 校验并构建运行时快照：任一校验失败返回 error（调用方保留旧快照）。
// domain 在此统一归一转小写（放行不拒绝）。
func BuildGraph(in *GraphInput) (*RouteSnapshot, error) {
	if err := ValidateGraph(in); err != nil {
		return nil, err
	}
	ups := make(map[int64]*UpstreamRT, len(in.Upstreams))
	for i := range in.Upstreams {
		u := &in.Upstreams[i]
		ups[u.ID] = &UpstreamRT{
			ID:            u.ID,
			Name:          u.Name,
			Algo:          AlgoKind(u.Algo),
			StickyEnabled: u.StickyEnabled,
			StickyCookie:  u.StickyCookie,
			Enabled:       u.Enabled,
		}
	}
	// 关系展开：均衡器聚合其节点（保持输入顺序，游标与列表按下标对应）。
	relByUp := make(map[int64][]*NodeRT, len(in.Upstreams))
	nodeByID := make(map[int64]*NodeRow, len(in.Nodes))
	for i := range in.Nodes {
		nodeByID[in.Nodes[i].ID] = &in.Nodes[i]
	}
	for i := range in.Relations {
		rel := &in.Relations[i]
		n := nodeByID[rel.NodeID]
		relByUp[rel.UpstreamID] = append(relByUp[rel.UpstreamID], &NodeRT{
			ID:       n.ID,
			URL:      n.URL,
			Weight:   rel.Weight,
			Priority: Priority(rel.Priority),
		})
	}
	for id, u := range ups {
		nodes := relByUp[id]
		u.Nodes = nodes
		u.rr.current = make([]int, len(nodes))
	}
	rules := make([]*RuleRT, 0, len(in.Rules))
	for i := range in.Rules {
		r := &in.Rules[i]
		rules = append(rules, &RuleRT{
			ID:         r.ID,
			MatchOrder: r.MatchOrder,
			Domain:     strings.ToLower(r.Domain), // 构建期归一：转小写放行不拒绝（S6）
			PathType:   PathType(r.PathType),
			PathValue:  r.PathValue,
			Title:      r.Title,
			Upstream:   ups[r.UpstreamID],
		})
	}
	sortRules(rules)
	return &RouteSnapshot{Rules: rules, Upstreams: ups}, nil
}

// sortRules 按 (match_order, id) 稳定升序排列（S1 命中即停的扫描顺序）。
func sortRules(rules []*RuleRT) {
	for i := 1; i < len(rules); i++ {
		for j := i; j > 0; j-- {
			a, b := rules[j-1], rules[j]
			if a.MatchOrder < b.MatchOrder || (a.MatchOrder == b.MatchOrder && a.ID <= b.ID) {
				break
			}
			rules[j-1], rules[j] = rules[j], rules[j-1]
		}
	}
}

// ValidateGraph 加载期校验（S6）：逐行校验并聚合全部问题返回（便于一次看全）。
// 校验项：引用存在（规则→均衡器、关系→均衡器/节点）、节点 URL 合法 http(s)://、
// weight 正整数、priority ∈ {0,1}、match_order 1–999、path_value 以 / 开头、
// path_type/algo 枚举值合法。domain 不做拒绝项（构建期归一放行）。
func ValidateGraph(in *GraphInput) error {
	var probs []string
	add := func(format string, args ...any) {
		probs = append(probs, fmt.Sprintf(format, args...))
	}

	upIDs := make(map[int64]bool, len(in.Upstreams))
	for i := range in.Upstreams {
		u := &in.Upstreams[i]
		if !AlgoKind(u.Algo).Valid() {
			add("均衡器 %d(%s) algo 非法: %d（仅支持 1=round_robin/2=least_conn）", u.ID, u.Name, u.Algo)
		}
		upIDs[u.ID] = true
	}
	nodeIDs := make(map[int64]bool, len(in.Nodes))
	for i := range in.Nodes {
		n := &in.Nodes[i]
		if !strings.HasPrefix(n.URL, "http://") && !strings.HasPrefix(n.URL, "https://") {
			add("节点 %d(%s) URL 必须以 http(s):// 开头: %q", n.ID, n.Name, n.URL)
		} else if _, err := url.Parse(n.URL); err != nil {
			add("节点 %d(%s) URL 无法解析: %q: %v", n.ID, n.Name, n.URL, err)
		}
		nodeIDs[n.ID] = true
	}
	for i := range in.Relations {
		rel := &in.Relations[i]
		if !upIDs[rel.UpstreamID] {
			add("关系 %d 引用的均衡器不存在: upstream_id=%d", rel.ID, rel.UpstreamID)
		}
		if !nodeIDs[rel.NodeID] {
			add("关系 %d 引用的节点不存在: node_id=%d", rel.ID, rel.NodeID)
		}
		if rel.Weight <= 0 {
			add("关系 %d (upstream=%d node=%d) weight 必须为正整数: %d", rel.ID, rel.UpstreamID, rel.NodeID, rel.Weight)
		}
		if !Priority(rel.Priority).Valid() {
			add("关系 %d (upstream=%d node=%d) priority 非法: %d（仅支持 0=高优/1=备份）", rel.ID, rel.UpstreamID, rel.NodeID, rel.Priority)
		}
	}
	// 均衡器节点数校验须在关系遍历后统计。
	nodeCount := make(map[int64]int, len(in.Upstreams))
	for i := range in.Relations {
		nodeCount[in.Relations[i].UpstreamID]++
	}
	for i := range in.Upstreams {
		if nodeCount[in.Upstreams[i].ID] == 0 {
			add("均衡器 %d(%s) 无任何节点关系", in.Upstreams[i].ID, in.Upstreams[i].Name)
		}
	}
	for i := range in.Rules {
		r := &in.Rules[i]
		if r.MatchOrder < 1 || r.MatchOrder > 999 {
			add("规则 %d match_order 超出 1–999: %d", r.ID, r.MatchOrder)
		}
		if !PathType(r.PathType).Valid() {
			add("规则 %d path_type 非法: %d（仅支持 1=前缀/2=精确/3=模式）", r.ID, r.PathType)
		}
		if !strings.HasPrefix(r.PathValue, "/") {
			add("规则 %d path_value 必须以 / 开头: %q", r.ID, r.PathValue)
		}
		if !upIDs[r.UpstreamID] {
			add("规则 %d 引用的均衡器不存在: upstream_id=%d", r.ID, r.UpstreamID)
		}
	}
	if len(probs) == 0 {
		return nil
	}
	return fmt.Errorf("路由对象图校验失败（%d 项）：%s", len(probs), strings.Join(probs, "；"))
}
