// IP 黑名单增强 STEP A2 单测：warn_times 读写 / BanInsert / RestoreBan 三分支 /
// 排序白名单与非法回退 / 小黑屋 jail 条件与升序 / block_type 0-11 校验边界。
// 设计依据：docs/IP_BLACKLIST_PLAN.md §3.4/§3.7/§3.8。
package shield

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ── warn_times 读写 ─────────────────────────────────────────────────

func TestIPListStore_WarnTimes(t *testing.T) {
	s, raw := newTestListStore(t, true)
	now := testNow()

	// 普通录入：warn_times=0
	if _, err := s.Insert("10.1.0.1", "普通", BlockManual, nil, now); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// 封禁入库：warn_times=1 起算
	if _, err := s.BanInsert("10.1.0.2", "自动拉黑", BlockRateLimit, timePtr(now.Add(24*time.Hour)), now); err != nil {
		t.Fatalf("BanInsert: %v", err)
	}
	// 导入：warn_times=0
	if _, _, err := s.Import([]string{"10.1.0.3"}, "导入", BlockOther, now); err != nil {
		t.Fatalf("Import: %v", err)
	}

	for _, c := range []struct {
		ip   string
		want int64
	}{{"10.1.0.1", 0}, {"10.1.0.2", 1}, {"10.1.0.3", 0}} {
		got, err := s.GetByIP(c.ip)
		if err != nil {
			t.Fatalf("GetByIP(%s): %v", c.ip, err)
		}
		if got.WarnTimes != c.want {
			t.Errorf("GetByIP(%s).WarnTimes = %d, want %d", c.ip, got.WarnTimes, c.want)
		}
	}
	// 管理面更新不改 warn_times（update 脚本显式保留原值）
	if _, err := s.BanInsert("10.1.0.4", "封", BlockRateLimit, nil, now); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetByIP("10.1.0.4")
	if err := s.Update(got.ID, "改备注", BlockPathTraversal, nil, now); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetByIP("10.1.0.4"); got.WarnTimes != 1 {
		t.Fatalf("Update 后 warn=%d, want 1（管理面编辑不改封禁次数）", got.WarnTimes)
	}
	_ = raw
}

// ── RestoreBan 三分支 ───────────────────────────────────────────────

func TestIPListStore_RestoreBanBranches(t *testing.T) {
	s, _ := newTestListStore(t, true)
	now := testNow()

	// 分支一：普通恢复（软删条目，warn=1 → 2，仍限时，不转永久）
	id1, err := s.BanInsert("10.2.0.1", "首次", BlockRateLimit, timePtr(now.Add(time.Hour)), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SoftDelete(id1, now); err != nil {
		t.Fatal(err)
	}
	perm, err := s.RestoreBan("10.2.0.1", timePtr(now.Add(2*time.Hour)), now)
	if err != nil || perm {
		t.Fatalf("普通恢复 perm=%v err=%v, want false/nil", perm, err)
	}
	got, err := s.GetByIP("10.2.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if got.WarnTimes != 2 || got.Deleted {
		t.Fatalf("普通恢复后 warn=%d deleted=%v, want 2/false", got.WarnTimes, got.Deleted)
	}
	if got.ExpiresAt == "" {
		t.Fatal("普通恢复后仍应为限时（expires_at 非空）")
	}

	// 分支二：续封未满限（warn=2 → 3）
	perm, err = s.RestoreBan("10.2.0.1", timePtr(now.Add(3*time.Hour)), now)
	if err != nil || perm {
		t.Fatalf("续封 perm=%v err=%v, want false/nil", perm, err)
	}
	if got, _ := s.GetByIP("10.2.0.1"); got.WarnTimes != 3 {
		t.Fatalf("续封后 warn=%d, want 3", got.WarnTimes)
	}

	// 分支三：满 5 次转永久（warn=4 → 5 且本次限时 → expires_at 置 NULL + title 追加标记）
	id3, err := s.BanInsert("10.2.0.3", "反复", BlockSQLInjection, timePtr(now.Add(time.Hour)), now)
	if err != nil {
		t.Fatal(err)
	}
	// 直接改库模拟历史累计 3 次续封（BanInsert=1 + 3 次 = 4）
	if _, err := s.edb.Exec("UPDATE ip_blacklist SET warn_times = 4 WHERE id = ?", id3); err != nil {
		t.Fatal(err)
	}
	perm, err = s.RestoreBan("10.2.0.3", timePtr(now.Add(10*time.Hour)), now)
	if err != nil || !perm {
		t.Fatalf("满限恢复 perm=%v err=%v, want true/nil", perm, err)
	}
	got3, _ := s.GetByIP("10.2.0.3")
	if got3.WarnTimes != 5 || got3.ExpiresAt != "" {
		t.Fatalf("转永久 warn=%d expires=%q, want 5/空", got3.WarnTimes, got3.ExpiresAt)
	}
	if got3.Title != "反复（累计封禁达 5 次转永久）" {
		t.Fatalf("转永久 title=%q", got3.Title)
	}

	// 转永久后再续封（expiresAt=nil 永久续封）：warn 继续累加、不重复追加标记
	perm, err = s.RestoreBan("10.2.0.3", nil, now)
	if err != nil || perm {
		t.Fatalf("永久续封 perm=%v err=%v, want false/nil", perm, err)
	}
	if got, _ := s.GetByIP("10.2.0.3"); got.WarnTimes != 6 {
		t.Fatalf("永久续封后 warn=%d, want 6", got.WarnTimes)
	}
	if got, _ := s.GetByIP("10.2.0.3"); got.Title != "反复（累计封禁达 5 次转永久）" {
		t.Fatalf("永久续封 title 不应重复追加: %q", got.Title)
	}

	// 无记录 → ErrIPNotExists；白名单 → 报错
	if _, err := s.RestoreBan("10.2.9.9", nil, now); !errors.Is(err, ErrIPNotExists) {
		t.Fatalf("无记录 RestoreBan err=%v, want ErrIPNotExists", err)
	}
	ws, _ := newTestListStore(t, false)
	if _, err := ws.RestoreBan("10.2.0.1", nil, now); err == nil {
		t.Fatal("白名单 RestoreBan 应报错")
	}
}

// ── 排序白名单与非法回退 ────────────────────────────────────────────

func TestIPListStore_SortWhitelist(t *testing.T) {
	// 映射表：合法键固定 X DESC；非法/缺省回 id DESC；字符串字段不提供
	cases := []struct {
		sort string
		want string
	}{
		{"", "id DESC"},
		{"id", "id DESC"},
		{"hit_count", "hit_count DESC"},
		{"warn_times", "warn_times DESC"},
		{"created_at", "created_at DESC"},
		{"expires_at", "expires_at DESC"},
		{"updated_at", "updated_at DESC"},
		{"block_type", "block_type DESC"},
		{"ip", "id DESC"},                  // 字符串字段不提供 → 回默认
		{"title", "id DESC"},               // 同上
		{"id; DROP TABLE ip_blacklist", "id DESC"}, // 非法 → 回默认（注入面收敛）
	}
	for _, c := range cases {
		if got := blacklistSortOrder(c.sort); got != c.want {
			t.Errorf("blacklistSortOrder(%q) = %q, want %q", c.sort, got, c.want)
		}
	}

	// 行为验证：按 hit_count DESC 排序
	s, _ := newTestListStore(t, true)
	now := testNow()
	for i, ip := range []string{"10.3.0.1", "10.3.0.2", "10.3.0.3"} {
		id, err := s.Insert(ip, "t", BlockManual, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AddHitCount(id, i+1); err != nil { // 1/2/3
			t.Fatal(err)
		}
	}
	rows, _, err := s.List(ListFilter{Limit: 10, Sort: "hit_count"}, now)
	if err != nil {
		t.Fatalf("List(hit_count): %v", err)
	}
	for i, want := range []int64{3, 2, 1} {
		if got := rows[i]["hit_count"].(int64); got != want {
			t.Fatalf("hit_count 排序第 %d 行 = %d, want %d", i, got, want)
		}
	}
	// warn_times 也应出现在归一化行中
	if _, ok := rows[0]["warn_times"].(int64); !ok {
		t.Fatalf("warn_times 未归一化: %T", rows[0]["warn_times"])
	}
	// 非法 sort 回默认 id DESC（最后插入的在前）
	rows, _, err = s.List(ListFilter{Limit: 10, Sort: "evil"}, now)
	if err != nil {
		t.Fatalf("List(evil): %v", err)
	}
	if rows[0]["ip"] != "10.3.0.3" {
		t.Fatalf("非法 sort 回退 id DESC 首行 ip=%v, want 10.3.0.3", rows[0]["ip"])
	}
}

// ── 小黑屋 jail：条件与升序 ─────────────────────────────────────────

func TestIPListStore_Jail(t *testing.T) {
	s, _ := newTestListStore(t, true)
	now := testNow()

	// 在押：两条限时未过期（解封时间 2h/1h）+ 软删的限时 + 已过期 + 永久
	if _, err := s.BanInsert("10.4.0.1", "晚解封", BlockRateLimit, timePtr(now.Add(2*time.Hour)), now); err != nil {
		t.Fatal(err)
	}
	id2, err := s.BanInsert("10.4.0.2", "早解封", BlockRateLimit, timePtr(now.Add(time.Hour)), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BanInsert("10.4.0.3", "软删", BlockRateLimit, timePtr(now.Add(3*time.Hour)), now); err != nil {
		t.Fatal(err)
	}
	if err := s.SoftDelete(id2+1, now); err != nil { // 第三条（id 连续）
		t.Fatal(err)
	}
	if _, err := s.BanInsert("10.4.0.4", "已过期", BlockRateLimit, timePtr(now.Add(-time.Hour)), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BanInsert("10.4.0.5", "永久", BlockRateLimit, nil, now); err != nil {
		t.Fatal(err)
	}

	rows, total, err := s.Jail(now, 20)
	if err != nil {
		t.Fatalf("Jail: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("Jail total=%d rows=%d, want 2/2（仅限时未过期且未软删）", total, len(rows))
	}
	// 升序：临近解封在前
	if rows[0]["ip"] != "10.4.0.2" || rows[1]["ip"] != "10.4.0.1" {
		t.Fatalf("Jail 排序 = %v, %v; want 10.4.0.2 在前", rows[0]["ip"], rows[1]["ip"])
	}
	if _, ok := rows[0]["warn_times"].(int64); !ok {
		t.Fatalf("jail 行 warn_times 未归一化: %T", rows[0]["warn_times"])
	}

	// limit 生效：limit=1 只回 1 行但 total 仍为 2
	rows, total, err = s.Jail(now, 1)
	if err != nil || len(rows) != 1 || total != 2 {
		t.Fatalf("Jail(limit=1) rows=%d total=%d err=%v, want 1/2/nil", len(rows), total, err)
	}

	// 白名单无小黑屋语义
	ws, _ := newTestListStore(t, false)
	if _, _, err := ws.Jail(now, 20); err == nil {
		t.Fatal("白名单 Jail 应报错")
	}
}

// ── BanIP 三态（Shield 层：无记录入库 / 活跃跳过 / 软删过期续封）─────

func TestShield_BanIPTriState(t *testing.T) {
	black, _ := newFileListStore(t, true)
	s := dbShield(t, &countingStore{IPListStore: black}, nil)
	now := time.Now()

	// 三态一：无记录 → 封禁入库（warn=1）
	written, perm, err := s.BanIP("10.5.0.1", "自动拉黑", BlockRateLimit, timePtr(now.Add(time.Hour)))
	if err != nil || !written || perm {
		t.Fatalf("无记录 BanIP written=%v perm=%v err=%v, want true/false/nil", written, perm, err)
	}
	e, err := black.GetByIP("10.5.0.1")
	if err != nil || e.WarnTimes != 1 {
		t.Fatalf("入库后 warn=%d err=%v, want 1/nil", e.WarnTimes, err)
	}

	// 三态二：活跃条目 → 跳过
	written, perm, err = s.BanIP("10.5.0.1", "再次", BlockRateLimit, timePtr(now.Add(time.Hour)))
	if err != nil || written || perm {
		t.Fatalf("活跃跳过 written=%v perm=%v err=%v, want false/false/nil", written, perm, err)
	}

	// 三态三：软删条目 → 恢复续封（warn=2）
	if err := black.SoftDelete(e.ID, now); err != nil {
		t.Fatal(err)
	}
	written, perm, err = s.BanIP("10.5.0.1", "续封", BlockRateLimit, timePtr(now.Add(2*time.Hour)))
	if err != nil || !written || perm {
		t.Fatalf("续封 written=%v perm=%v err=%v, want true/false/nil", written, perm, err)
	}
	if e, _ = black.GetByIP("10.5.0.1"); e.WarnTimes != 2 || e.Deleted {
		t.Fatalf("续封后 warn=%d deleted=%v, want 2/false", e.WarnTimes, e.Deleted)
	}

	// 非法 ip → ErrInvalidIP
	if _, _, err := s.BanIP("not-an-ip", "t", BlockRateLimit, nil); !errors.Is(err, ErrInvalidIP) {
		t.Fatalf("非法 ip err=%v, want ErrInvalidIP", err)
	}
}

// ── block_type 0-11 校验边界（AddIPList/ImportIPList/管理过滤）──────

func TestIPList_BlockTypeBoundaries(t *testing.T) {
	black, _ := newFileListStore(t, true)
	white, _ := newFileListStore(t, false)
	s := dbShield(t, &countingStore{IPListStore: black}, &countingStore{IPListStore: white})

	// 边界内：0（其他）与 11（人工收录）均可入库
	if _, err := s.AddIPList(true, "10.6.0.0", "其他", BlockOther, nil); err != nil {
		t.Fatalf("AddIPList block_type=0: %v", err)
	}
	if _, err := s.AddIPList(true, "10.6.0.11", "人工", BlockManual, nil); err != nil {
		t.Fatalf("AddIPList block_type=11: %v", err)
	}
	// 越界：12 拒绝
	if _, err := s.AddIPList(true, "10.6.0.99", "越界", BlockType(12), nil); err == nil {
		t.Fatal("block_type=12 应拒绝")
	}
	// 导入越界同样拒绝
	if _, _, err := s.ImportIPList(true, []string{"10.6.1.1"}, "t", BlockType(12)); err == nil {
		t.Fatal("导入 block_type=12 应拒绝")
	}
	// 白名单不受影响（非法 bt 被忽略归一）
	if _, err := s.AddIPList(false, "10.6.2.1", "白", BlockType(12), nil); err != nil {
		t.Fatalf("白名单 block_type 应被忽略: %v", err)
	}

	// 管理过滤参数：0-11 合法、12 拒绝（经 listIPList 端点）
	h := &AdminHandler{shield: s}
	w := doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?block_type=11", "")
	if w.Code != http.StatusOK {
		t.Fatalf("block_type=11 code=%d body=%s", w.Code, w.Body.String())
	}
	w = doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?block_type=12", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("block_type=12 code=%d, want 400", w.Code)
	}
}

// TestTruncateTitle title 超长截断（VARCHAR(64) 列宽；MySQL 严格模式下超长写入直接报错）。
func TestTruncateTitle(t *testing.T) {
	long := strings.Repeat("测", 100)
	if got := truncateTitle(long, 0); len([]rune(got)) != 64 {
		t.Errorf("超长 title 应截断到 64 字符, got %d", len([]rune(got)))
	}
	// 预留转永久后缀空间：截断 + 后缀合计仍 ≤ 64
	base := truncateTitle(long, len([]rune(banPermanentTitleSuffix))) + banPermanentTitleSuffix
	if len([]rune(base)) > 64 {
		t.Errorf("截断 + 转永久后缀应 ≤ 64 字符, got %d", len([]rune(base)))
	}
	if got := truncateTitle("短标题", 0); got != "短标题" {
		t.Errorf("未超长不应改动: %q", got)
	}
}

// TestRestoreBanTitleOverlong 超长 title 的条目续封转永久不报错且 title 收敛到列宽内。
func TestRestoreBanTitleOverlong(t *testing.T) {
	black, _ := newTestListStore(t, true)
	now := time.Now()
	id, err := black.BanInsert("10.9.9.9", strings.Repeat("超长标题", 40), BlockRateLimit, timePtr(now.Add(time.Hour)), now)
	if err != nil {
		t.Fatal(err)
	}
	// warn_times 直改到 4，续封一次即达 5 → 转永久（走 title 追加后缀路径）。
	if _, err := black.edb.Exec("UPDATE ip_blacklist SET warn_times = 4, deleted_at = ? WHERE id = ?", now.UTC(), id); err != nil {
		t.Fatal(err)
	}
	perm, err := black.RestoreBan("10.9.9.9", timePtr(now.Add(10*time.Hour)), now)
	if err != nil {
		t.Fatalf("超长 title 续封不应报错: %v", err)
	}
	if !perm {
		t.Error("warn=5 应转永久")
	}
	e, err := black.GetByIP("10.9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(e.Title)) > 64 {
		t.Errorf("title 应收敛到 64 字符内, got %d", len([]rune(e.Title)))
	}
	if !strings.Contains(e.Title, banPermanentTitleSuffix) {
		t.Errorf("转永久标记应保留: %q", e.Title)
	}
}

// TestImportIPListInvalidLinesSkipped 导入逐行校验：非法 IP 不落库、计入 skipped
// （管理面请求体是自由文本，后端不校验会把任意字符串写进 ip 列——浏览器验收发现）。
func TestImportIPListInvalidLinesSkipped(t *testing.T) {
	s, _ := newTestShield(t)
	white, _ := newTestListStore(t, false)
	black, _ := newTestListStore(t, true)
	s.SetIPListStores(black, white)
	imported, skipped, err := s.ImportIPList(true,
		[]string{"10.7.1.1", "not-an-ip", "10.7.1.2", "#注释", "  ", "10.7.1.1"}, "t", BlockManual)
	if err != nil {
		t.Fatal(err)
	}
	if imported != 2 || skipped != 2 {
		t.Errorf("imported=%d skipped=%d, want 2/2（非法行+重复各计 1 跳过，注释/空行不计）", imported, skipped)
	}
	if _, err := black.GetByIP("not-an-ip"); err == nil {
		t.Error("非法文本不应入库")
	}
}
