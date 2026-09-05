// IP 黑名单增强 STEP A4 单测：自动拉黑引擎。
// 覆盖：配置注册 / 分档聚合口径（攻击档命中 1 次即永久、爬虫档独立阈值、通用档
// 跨类别合计、多档取最严、次数最多类别、并列取枚举小者、排除 block_type=1 与
// 非精确 IP）/ 运行周期计算 / 四态处理（无记录新增、活跃跳过、软删恢复、过期恢复）
// / 累计入狱达 REPEAT_LIMIT 转永久与 0=关闭 / TTL=0 永久 / 白名单 CIDR 过滤 /
// 处理后快照重建。
// 设计依据：docs/IP_BLACKLIST_PLAN.md §3.4 决策 11/12 + 风险分档升级（banTierOf）。
package shield

import (
	"testing"
	"time"
)

// ── 配置注册 ─────────────────────────────────────────────────────────

// NewAutoBanEngine 应注册全部 SHIELD_AUTO_BAN_* 配置项。
func TestNewAutoBanEngineRegistersConfig(t *testing.T) {
	f := newFakeConf()
	s, _ := newTestShield(t)
	rec, _ := newTestRecorder(t)
	e := NewAutoBanEngine(f, s, rec)
	for _, n := range []string{
		"SHIELD_AUTO_BAN_ENABLED", "SHIELD_AUTO_BAN_THRESHOLD",
		"SHIELD_AUTO_BAN_CRAWLER_THRESHOLD", "SHIELD_AUTO_BAN_REPEAT_LIMIT",
		"SHIELD_AUTO_BAN_WINDOW", "SHIELD_AUTO_BAN_TTL",
	} {
		if _, ok := f.regs[n]; !ok {
			t.Errorf("应注册配置项 %s", n)
		}
	}
	_ = e
}

// ── 风险分档映射 ─────────────────────────────────────────────────────

// candRows 构造候选查询返回行：参数为 (ip, block_type, cnt) 三元组序列。
func candRows(triples ...any) []map[string]any {
	rows := make([]map[string]any, 0, len(triples)/3)
	for i := 0; i+2 < len(triples); i += 3 {
		rows = append(rows, map[string]any{
			"client_ip":  triples[i],
			"block_type": int64(triples[i+1].(int)),
			"cnt":        int64(triples[i+2].(int)),
		})
	}
	return rows
}

func TestBanTierOf(t *testing.T) {
	for _, c := range []struct {
		bt   BlockType
		want banTier
	}{
		{BlockRiskPath, banTierAttack}, {BlockPathTraversal, banTierAttack},
		{BlockSQLInjection, banTierAttack}, {BlockXSS, banTierAttack},
		{BlockCrawlerUA, banTierCrawler},
		{BlockRateLimit, banTierGeneric}, {BlockMethodNotAllowed, banTierGeneric},
		{BlockBodyTooLarge, banTierGeneric}, {BlockPathRuleDeny, banTierGeneric},
		{BlockOther, 0}, {BlockIPBlacklist, 0}, {BlockManual, 0},
	} {
		if got := banTierOf(c.bt); got != c.want {
			t.Errorf("banTierOf(%d) = %d, want %d", c.bt, got, c.want)
		}
	}
}

// ── 分档聚合口径（决策 12 分档版）──────────────────────────────────────

func TestAggregateAutoBanCandidates(t *testing.T) {
	// 场景一：攻击档命中 1 次即候选（阈值 attackBanThreshold=1），直永久。
	got := aggregateAutoBanCandidates(candRows("10.0.0.1", 7, 1), 50, 20)
	if len(got) != 1 || got[0].IP != "10.0.0.1" || got[0].Tier != banTierAttack ||
		got[0].Total != 1 || got[0].TopType != BlockSQLInjection {
		t.Fatalf("攻击档应命中 1 次即候选，got %+v", got)
	}

	// 场景二：跨档不合并——SQL注入(7)×1 + 限流(2)×2 不等于攻击档多计；
	// 限流 2 次未达通用阈值 50 → 整体仍只按攻击档候选。
	got = aggregateAutoBanCandidates(candRows(
		"10.0.0.2", 7, 1, "10.0.0.2", 2, 2,
	), 50, 20)
	if len(got) != 1 || got[0].Tier != banTierAttack || got[0].Total != 1 || got[0].TopType != BlockSQLInjection {
		t.Fatalf("跨档不应合并计数，got %+v", got)
	}

	// 场景三：爬虫档独立阈值（20 达标、19 不达标）。
	got = aggregateAutoBanCandidates(candRows("10.0.0.3", 9, 20), 50, 20)
	if len(got) != 1 || got[0].Tier != banTierCrawler || got[0].TopType != BlockCrawlerUA {
		t.Fatalf("爬虫档 20 次应候选，got %+v", got)
	}
	got = aggregateAutoBanCandidates(candRows("10.0.0.3", 9, 19), 50, 20)
	if len(got) != 0 {
		t.Fatalf("爬虫档 19 次不应候选，got %+v", got)
	}

	// 场景四：通用档跨类别合计（限流 3 + 方法 2 = 5 达阈值）+ 档内最多类别并列取枚举小者。
	got = aggregateAutoBanCandidates(candRows(
		"10.0.0.4", 2, 3, "10.0.0.4", 3, 2,
	), 5, 20)
	if len(got) != 1 || got[0].Tier != banTierGeneric || got[0].Total != 5 || got[0].TopType != BlockRateLimit {
		t.Fatalf("通用档合计/并列取小不符，got %+v", got)
	}

	// 场景五：多档同时达标取最严（攻击 > 爬虫 > 通用）。
	got = aggregateAutoBanCandidates(candRows(
		"10.0.0.5", 8, 1, "10.0.0.5", 9, 20, "10.0.0.5", 2, 50,
	), 50, 20)
	if len(got) != 1 || got[0].Tier != banTierAttack || got[0].Total != 1 {
		t.Fatalf("多档达标应取最严档，got %+v", got)
	}

	// 场景六：排除 block_type=1（黑名单自我拦截，决策 11）与非拦截语境枚举（0/11）、
	// 非精确 IP（CIDR/空串）、cnt=0。
	got = aggregateAutoBanCandidates(candRows(
		"10.0.0.6", 1, 3, // 全为自我拦截 → 不候选
		"10.0.0.7", 1, 5, "10.0.0.7", 2, 2, // 1 不计，通用档 2 < 5 → 不候选
		"10.0.0.8", 2, 5, // 通用档 5 → 候选
		"10.0.0.0/8", 2, 9, // 非精确 IP → 排除
		"", 2, 9, // 空 IP → 排除
		"10.0.0.9", 2, 0, "10.0.0.9", 11, 9, // cnt=0 与人工收录(11) → 排除
	), 5, 20)
	if len(got) != 1 || got[0].IP != "10.0.0.8" || got[0].Total != 5 || got[0].TopType != BlockRateLimit {
		t.Fatalf("过滤口径不符，got %+v", got)
	}

	// 场景七：各档均未达阈值 → 不候选。
	got = aggregateAutoBanCandidates(candRows(
		"10.0.1.1", 2, 4, "10.0.1.1", 9, 19, "10.0.1.1", 7, 0,
	), 5, 20)
	if len(got) != 0 {
		t.Fatalf("均未达阈值不应候选，got %+v", got)
	}
}

// ── 运行周期 ─────────────────────────────────────────────────────────

func TestAutoBanInterval(t *testing.T) {
	for _, c := range []struct {
		window time.Duration
		want   time.Duration
	}{
		{30 * time.Minute, 10 * time.Minute}, // window/3
		{10 * time.Minute, 3*time.Minute + 20*time.Second},
		{3 * time.Minute, time.Minute},  // window/3 = 1m 恰达下限
		{90 * time.Second, time.Minute}, // window/3 = 30s < 1m → 下限
		{time.Minute, time.Minute},      // window/3 = 20s → 下限
		{0, time.Minute},                // 非法窗口回落下限
		{-time.Minute, time.Minute},     // 负值回落下限
	} {
		if got := autoBanInterval(c.window); got != c.want {
			t.Errorf("autoBanInterval(%v) = %v, want %v", c.window, got, c.want)
		}
	}
	// 时长解析："0" 特判永久。
	if d, err := parseAutoBanDuration("0"); err != nil || d != 0 {
		t.Errorf(`parseAutoBanDuration("0") = %v, %v`, d, err)
	}
	if d, err := parseAutoBanDuration(" 24h "); err != nil || d != 24*time.Hour {
		t.Errorf(`parseAutoBanDuration(" 24h ") = %v, %v`, d, err)
	}
}

// ── 四态处理 + 转永久 + TTL=0 + 白名单过滤 ────────────────────────────

// newTestAutoBanEngine 构造引擎全套测试环境：临时 sqlite 库记录器 + 注入黑白名单
// store 的 Shield + 引擎（开关字段按测试需要直改，fake 配置不回填默认值）。
func newTestAutoBanEngine(t *testing.T) (*AutoBanEngine, *Shield, *IPListStore, *IPListStore, *EventRecorder) {
	t.Helper()
	s, _ := newTestShield(t)
	rec, _ := newTestRecorder(t)
	white, _ := newTestListStore(t, false)
	black, _ := newTestListStore(t, true)
	s.SetIPListStores(black, white)
	e := NewAutoBanEngine(newFakeConf(), s, rec)
	e.enabled = true
	e.threshold = 2
	e.crawlerThreshold = 2
	e.repeatLimit = 5
	e.window = "10m"
	e.ttl = "1h"
	return e, s, black, white, rec
}

// insertShieldEvent 直接插入一条拦截事件（绕过 recorder 异步通道，测试确定性）。
func insertShieldEvent(t *testing.T, rec *EventRecorder, at time.Time, bt BlockType, ip string) {
	t.Helper()
	ins, err := rec.sqlText("shield_event_insert.sql")
	if err != nil {
		t.Fatalf("读插入脚本失败: %v", err)
	}
	if _, err := rec.edb.Exec(ins,
		at.UTC(), "trace-ab", int(bt), ip, "GET", "/a", "/a?a=1",
		"UA", "example.com", 403, "test", int64(0), "{}"); err != nil {
		t.Fatalf("插入拦截事件失败: %v", err)
	}
}

func TestAutoBanRunOnceFourStates(t *testing.T) {
	e, s, black, white, rec := newTestAutoBanEngine(t)
	now := time.Now()
	base := now.Add(-time.Minute) // 窗口内（窗口 10m）

	// 预置事件（四态主链路全部走通用档，攻击/爬虫档独立用例覆盖）：
	// 10.1.1.1 无记录（限流×2）→ 态一：新增
	// 10.1.1.2 活跃条目（方法白名单×2）→ 态二：跳过
	// 10.1.1.3 软删条目（体积超限×2）→ 态三：恢复续封（TTL×10=10h，warn 1→2）
	// 10.1.1.4 已过期条目（规则 deny×2）→ 态四：恢复续封
	// 10.1.1.5 软删且 warn=4（规则 deny×2）→ 恢复后 warn=5 → 转永久
	// 10.1.1.6 白名单网段内（10.9.0.0/16，爬虫UA×2）→ 过滤跳过
	// 10.1.1.7 自我拦截（block_type=1×5）→ 决策 11 排除
	for i := 0; i < 2; i++ {
		insertShieldEvent(t, rec, base, BlockRateLimit, "10.1.1.1")
		insertShieldEvent(t, rec, base, BlockMethodNotAllowed, "10.1.1.2")
		insertShieldEvent(t, rec, base, BlockBodyTooLarge, "10.1.1.3")
		insertShieldEvent(t, rec, base, BlockPathRuleDeny, "10.1.1.4")
		insertShieldEvent(t, rec, base, BlockPathRuleDeny, "10.1.1.5")
		insertShieldEvent(t, rec, base, BlockCrawlerUA, "10.9.1.6")
		insertShieldEvent(t, rec, base, BlockIPBlacklist, "10.1.1.7")
	}

	// 预置条目：活跃（10.1.1.2）、软删（10.1.1.3）、过期（10.1.1.4）、warn=4 软删（10.1.1.5）。
	if _, err := black.BanInsert("10.1.1.2", "在押", BlockMethodNotAllowed, timePtr(now.Add(time.Hour)), now); err != nil {
		t.Fatal(err)
	}
	id3, err := black.BanInsert("10.1.1.3", "软删", BlockBodyTooLarge, timePtr(now.Add(time.Hour)), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := black.SoftDelete(id3, now); err != nil {
		t.Fatal(err)
	}
	if _, err := black.BanInsert("10.1.1.4", "过期", BlockPathRuleDeny, timePtr(now.Add(-time.Hour)), now); err != nil {
		t.Fatal(err)
	}
	id5, err := black.BanInsert("10.1.1.5", "将永久", BlockPathRuleDeny, timePtr(now.Add(time.Hour)), now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // warn 1 → 4
		if _, err := black.RestoreBan("10.1.1.5", timePtr(now.Add(time.Hour)), now, banWarnTimesLimit); err != nil {
			t.Fatal(err)
		}
	}
	if err := black.SoftDelete(id5, now); err != nil {
		t.Fatal(err)
	}
	// 白名单入库 CIDR 网段并重建快照（覆盖白名单 CIDR 匹配语义）。
	if _, err := white.Insert("10.9.0.0/16", "内网段", BlockIPBlacklist, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Rebuild(); err != nil {
		t.Fatal(err)
	}

	e.runOnce()

	// 态一：新增（warn=1、解封=now+1h、类别=限流、title 定式）。
	e1, err := black.GetByIP("10.1.1.1")
	if err != nil {
		t.Fatalf("10.1.1.1 应已入库: %v", err)
	}
	if e1.WarnTimes != 1 || e1.BlockType != BlockRateLimit || e1.Deleted {
		t.Errorf("新增态不符: %+v", e1)
	}
	if want := "自动拉黑：10m内拦截≥2次"; e1.Title != want {
		t.Errorf("title = %q, want %q", e1.Title, want)
	}
	if exp, err := time.Parse(time.RFC3339, e1.ExpiresAt); err != nil ||
		exp.Sub(now) > time.Hour+time.Minute || exp.Sub(now) < time.Hour-time.Minute {
		t.Errorf("expires_at 应≈now+1h, got %q", e1.ExpiresAt)
	}

	// 态二：活跃跳过（warn 不变、解封时间不变）。
	e2, _ := black.GetByIP("10.1.1.2")
	if e2.WarnTimes != 1 {
		t.Errorf("活跃条目应跳过，warn = %d, want 1", e2.WarnTimes)
	}

	// 态三：软删恢复（warn 1→2、deleted 清除、解封=now+TTL×10=10h）。
	e3, _ := black.GetByIP("10.1.1.3")
	if e3.Deleted || e3.WarnTimes != 2 {
		t.Errorf("软删恢复态不符: %+v", e3)
	}
	if exp, err := time.Parse(time.RFC3339, e3.ExpiresAt); err != nil ||
		exp.Sub(now) > 10*time.Hour+time.Minute || exp.Sub(now) < 10*time.Hour-time.Minute {
		t.Errorf("恢复解封应≈now+10h(TTL×10), got %q", e3.ExpiresAt)
	}

	// 态四：过期恢复（deleted 本为 false、warn 1→2、解封重设）。
	e4, _ := black.GetByIP("10.1.1.4")
	if e4.WarnTimes != 2 || e4.Deleted {
		t.Errorf("过期恢复态不符: %+v", e4)
	}
	if exp, err := time.Parse(time.RFC3339, e4.ExpiresAt); err != nil || !exp.After(now.Add(9*time.Hour)) {
		t.Errorf("过期条目解封应重设为≈now+10h, got %q", e4.ExpiresAt)
	}

	// 态四延伸：warn 4→5 → 转永久（expires NULL、title 追加标记）。
	e5, _ := black.GetByIP("10.1.1.5")
	if e5.ExpiresAt != "" || e5.WarnTimes != 5 {
		t.Errorf("累计 5 次应转永久: %+v", e5)
	}
	if want := "将永久" + banPermanentTitleSuffix(5); e5.Title != want {
		t.Errorf("转永久 title = %q, want %q", e5.Title, want)
	}

	// 白名单 CIDR 过滤：网段内 IP 不入库。
	if _, err := black.GetByIP("10.9.1.6"); err == nil {
		t.Error("白名单网段内 IP 不应被自动拉黑")
	}

	// 决策 11：自我拦截事件不计入 → 不入库。
	if _, err := black.GetByIP("10.1.1.7"); err == nil {
		t.Error("block_type=1（自我拦截）不应触发自动拉黑")
	}

	// 处理完有变更 → 快照已重建：新增封禁对拦截判定即时生效。
	if !s.InBlacklist("10.1.1.1") {
		t.Error("自动拉黑后拦截快照应包含新封禁 IP")
	}
}

// TTL=0：新增即永久（expires NULL）；软删恢复后仍永久、不再触发转永久标记。
func TestAutoBanRunOnceTTLOPermanent(t *testing.T) {
	e, s, black, _, rec := newTestAutoBanEngine(t)
	e.ttl = "0"
	now := time.Now()
	base := now.Add(-time.Minute)

	for i := 0; i < 2; i++ {
		insertShieldEvent(t, rec, base, BlockSQLInjection, "10.2.1.1")
		insertShieldEvent(t, rec, base, BlockRateLimit, "10.2.1.2")
	}
	// 软删的限时条目（TTL=0 恢复语义：恢复后永久）。
	id, err := black.BanInsert("10.2.1.2", "软删", BlockRateLimit, timePtr(now.Add(time.Hour)), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := black.SoftDelete(id, now); err != nil {
		t.Fatal(err)
	}

	e.runOnce()

	e1, err := black.GetByIP("10.2.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if e1.ExpiresAt != "" {
		t.Errorf("TTL=0 新增应永久（expires NULL）, got %q", e1.ExpiresAt)
	}
	e2, _ := black.GetByIP("10.2.1.2")
	if e2.Deleted || e2.ExpiresAt != "" || e2.WarnTimes != 2 {
		t.Errorf("TTL=0 软删恢复应永久: %+v", e2)
	}
	if !s.InBlacklist("10.2.1.1") {
		t.Error("TTL=0 永久封禁应进快照")
	}
}

// 攻击档直永久：SQLi 窗口内命中 1 次即永久封禁（attackBanThreshold=1，与 TTL 无关），
// title 走攻击档定式；同样 1 次的限流（通用档）不封。
func TestAutoBanRunOnceAttackDirectPermanent(t *testing.T) {
	e, s, black, _, rec := newTestAutoBanEngine(t)
	now := time.Now()
	insertShieldEvent(t, rec, now.Add(-time.Minute), BlockSQLInjection, "10.5.1.1")
	insertShieldEvent(t, rec, now.Add(-time.Minute), BlockRateLimit, "10.5.1.2")

	e.runOnce()

	e1, err := black.GetByIP("10.5.1.1")
	if err != nil {
		t.Fatalf("SQLi 1 次应直接永久封禁: %v", err)
	}
	if e1.ExpiresAt != "" {
		t.Errorf("攻击档应永久（expires NULL）, got %q", e1.ExpiresAt)
	}
	if want := "自动拉黑：高危攻击直接永久封禁"; e1.Title != want {
		t.Errorf("攻击档 title = %q, want %q", e1.Title, want)
	}
	if !s.InBlacklist("10.5.1.1") {
		t.Error("攻击档永久封禁应进快照")
	}
	if _, err := black.GetByIP("10.5.1.2"); err == nil {
		t.Error("限流 1 次未达通用阈值不应封禁")
	}
}

// 爬虫档独立阈值：爬虫 UA 达 CRAWLER_THRESHOLD（2）限时封禁，title 用爬虫档阈值。
func TestAutoBanRunOnceCrawlerThreshold(t *testing.T) {
	e, _, black, _, rec := newTestAutoBanEngine(t)
	now := time.Now()
	for i := 0; i < 2; i++ {
		insertShieldEvent(t, rec, now.Add(-time.Minute), BlockCrawlerUA, "10.6.1.1")
	}

	e.runOnce()

	e1, err := black.GetByIP("10.6.1.1")
	if err != nil {
		t.Fatalf("爬虫 UA 2 次应封禁: %v", err)
	}
	if e1.ExpiresAt == "" {
		t.Error("爬虫档应为限时封禁（TTL=1h）")
	}
	if want := "自动拉黑：10m内拦截≥2次"; e1.Title != want {
		t.Errorf("爬虫档 title = %q, want %q", e1.Title, want)
	}
}

// REPEAT_LIMIT=0：永不自动转永久——warn 达 5 仍按时长续封。
func TestAutoBanRunOnceRepeatLimitOff(t *testing.T) {
	e, _, black, _, rec := newTestAutoBanEngine(t)
	e.repeatLimit = 0
	now := time.Now()
	base := now.Add(-time.Minute)
	id, err := black.BanInsert("10.7.1.1", "屡犯", BlockPathRuleDeny, timePtr(now.Add(time.Hour)), now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // warn 1 → 4
		if _, err := black.RestoreBan("10.7.1.1", timePtr(now.Add(time.Hour)), now, banWarnTimesLimit); err != nil {
			t.Fatal(err)
		}
	}
	if err := black.SoftDelete(id, now); err != nil {
		t.Fatal(err)
	}
	insertShieldEvent(t, rec, base, BlockPathRuleDeny, "10.7.1.1")
	insertShieldEvent(t, rec, base, BlockPathRuleDeny, "10.7.1.1") // 通用档阈值 2 达标

	e.runOnce()

	e1, _ := black.GetByIP("10.7.1.1")
	if e1.Deleted || e1.ExpiresAt == "" || e1.WarnTimes != 5 {
		t.Errorf("REPEAT_LIMIT=0 应恢复续封不转永久: %+v", e1)
	}
}

// 开关关闭：runOnce 空转，零写入。
func TestAutoBanRunOnceDisabled(t *testing.T) {
	e, _, black, _, rec := newTestAutoBanEngine(t)
	e.enabled = false
	insertShieldEvent(t, rec, time.Now().Add(-time.Minute), BlockSQLInjection, "10.3.1.1")
	insertShieldEvent(t, rec, time.Now().Add(-time.Minute), BlockSQLInjection, "10.3.1.1")
	e.runOnce()
	if _, err := black.GetByIP("10.3.1.1"); err == nil {
		t.Error("开关关闭不应入库")
	}
}

// Start/Stop 生命周期：启动后立即执行一轮，Stop 优雅退出（幂等）。
func TestAutoBanEngineStartStop(t *testing.T) {
	e, _, black, _, rec := newTestAutoBanEngine(t)
	insertShieldEvent(t, rec, time.Now().Add(-time.Minute), BlockSQLInjection, "10.4.1.1")
	insertShieldEvent(t, rec, time.Now().Add(-time.Minute), BlockSQLInjection, "10.4.1.1")
	e.Start()
	e.Start() // 幂等
	// Start 即执行一轮：轮询等待入库（goroutine 异步）。
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := black.GetByIP("10.4.1.1"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Start 后首轮未入库")
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.Stop()
	e.Stop() // 幂等
}
