package shield

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newAdminListTest 构造带 DB 黑白名单的 AdminHandler（黑/白按 isBlack 注入）。
func newAdminListTest(t *testing.T, isBlack bool) (*AdminHandler, *Shield) {
	t.Helper()
	store, _ := newFileListStore(t, isBlack)
	s, _ := newTestShield(t)
	s.enabled = true
	if isBlack {
		s.SetIPListStores(store, nil)
	} else {
		s.SetIPListStores(nil, store)
	}
	t.Cleanup(func() { s.Stop() })
	return &AdminHandler{shield: s}, s
}

func doReq(t *testing.T, h http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func decodeResp(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应非 JSON: %v（body=%s）", err, w.Body.String())
	}
	return m
}

// ── 黑名单 CRUD + 软删恢复 + 列表过滤 ──────────────────────────────

func TestAdmin_BlacklistCRUD(t *testing.T) {
	h, _ := newAdminListTest(t, true)

	// 新增（POST 到列表 path）
	w := doReq(t, h.Blacklist(), http.MethodPost, "/admin/shield/blacklist",
		`{"ip":"10.0.0.5","title":"扫描器","block_type":7,"expires_at":"2026-09-01T00:00:00Z"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("新增 code = %d, body=%s", w.Code, w.Body.String())
	}
	id := int64(decodeResp(t, w)["id"].(float64))
	if id <= 0 {
		t.Fatalf("id = %d, want > 0", id)
	}
	// 重复 ip → 400
	w = doReq(t, h.Blacklist(), http.MethodPost, "/admin/shield/blacklist", `{"ip":"10.0.0.5"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("重复新增 code = %d, want 400", w.Code)
	}
	// 非法 ip → 400
	w = doReq(t, h.Blacklist(), http.MethodPost, "/admin/shield/blacklist", `{"ip":"not-an-ip"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 ip code = %d, want 400", w.Code)
	}

	// 列表（GET）
	w = doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?limit=10", "")
	if w.Code != http.StatusOK {
		t.Fatalf("列表 code = %d", w.Code)
	}
	m := decodeResp(t, w)
	if m["total"].(float64) != 1 {
		t.Fatalf("total = %v, want 1", m["total"])
	}
	// 过滤：ip 模糊
	w = doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?ip=10.0", "")
	if decodeResp(t, w)["total"].(float64) != 1 {
		t.Fatal("ip 模糊过滤应命中 1 条")
	}
	// 过滤：block_type 不匹配
	w = doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?block_type=1", "")
	if decodeResp(t, w)["total"].(float64) != 0 {
		t.Fatal("block_type 过滤应 0 条")
	}
	// 参数校验：block_type 越界
	w = doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?block_type=99", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("block_type=99 code = %d, want 400", w.Code)
	}

	// 更新
	w = doReq(t, h.BlacklistUpdate(), http.MethodPost, "/admin/shield/blacklist/update",
		`{"id":`+itoa(id)+`,"title":"改后","block_type":6}`)
	if w.Code != http.StatusOK {
		t.Fatalf("更新 code = %d, body=%s", w.Code, w.Body.String())
	}
	// 软删 → 仅有效过滤 0 条
	w = doReq(t, h.BlacklistDelete(), http.MethodPost, "/admin/shield/blacklist/delete", `{"id":`+itoa(id)+`}`)
	if w.Code != http.StatusOK {
		t.Fatalf("软删 code = %d", w.Code)
	}
	w = doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?valid_only=1", "")
	if decodeResp(t, w)["total"].(float64) != 0 {
		t.Fatal("软删后仅有效过滤应 0 条")
	}
	// 恢复 → 仅有效 1 条
	w = doReq(t, h.BlacklistRestore(), http.MethodPost, "/admin/shield/blacklist/restore", `{"id":`+itoa(id)+`}`)
	if w.Code != http.StatusOK {
		t.Fatalf("恢复 code = %d", w.Code)
	}
	w = doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?valid_only=1", "")
	if decodeResp(t, w)["total"].(float64) != 1 {
		t.Fatal("恢复后仅有效过滤应 1 条")
	}

	// 方法校验：GET 到 update 端点 → 405
	w = doReq(t, h.BlacklistUpdate(), http.MethodGet, "/admin/shield/blacklist/update", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET update code = %d, want 405", w.Code)
	}
}

// ── 批量导入 ────────────────────────────────────────────────────────

func TestAdmin_BlacklistImport(t *testing.T) {
	h, _ := newAdminListTest(t, true)
	// 含注释/空行/重复
	w := doReq(t, h.BlacklistImport(), http.MethodPost, "/admin/shield/blacklist/import?title=%E6%89%B9%E9%87%8F&block_type=7",
		"# 注释\n10.0.0.1\n\n10.0.0.2\n10.0.0.1\n10.0.0.0/8\n")
	if w.Code != http.StatusOK {
		t.Fatalf("import code = %d, body=%s", w.Code, w.Body.String())
	}
	m := decodeResp(t, w)
	if m["imported"].(float64) != 3 || m["skipped"].(float64) != 1 {
		t.Fatalf("imported = %v skipped = %v, want 3/1", m["imported"], m["skipped"])
	}
	// 空 text → 400
	w = doReq(t, h.BlacklistImport(), http.MethodPost, "/admin/shield/blacklist/import", "  \n\n  ")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空 text code = %d, want 400", w.Code)
	}
	// 前端 api.post 场景：JSON 字符串编码的 body 亦兼容
	w = doReq(t, h.BlacklistImport(), http.MethodPost, "/admin/shield/blacklist/import", `"10.0.0.9"`)
	if w.Code != http.StatusOK {
		t.Fatalf("JSON 字符串导入 code = %d, body=%s", w.Code, w.Body.String())
	}
	// 导入后列表 total 4（3 条纯文本 + 1 条 JSON 字符串）
	w = doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?limit=10", "")
	if decodeResp(t, w)["total"].(float64) != 4 {
		t.Fatalf("导入后 total = %v, want 4", decodeResp(t, w)["total"])
	}
}

// ── 白名单同构（无 block_type/expires_at）──────────────────────────

func TestAdmin_WhitelistCRUD(t *testing.T) {
	h, _ := newAdminListTest(t, false)
	w := doReq(t, h.Whitelist(), http.MethodPost, "/admin/shield/whitelist", `{"ip":"10.0.0.5","title":"办公"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("新增 code = %d, body=%s", w.Code, w.Body.String())
	}
	id := int64(decodeResp(t, w)["id"].(float64))
	w = doReq(t, h.Whitelist(), http.MethodGet, "/admin/shield/whitelist?limit=10", "")
	if decodeResp(t, w)["total"].(float64) != 1 {
		t.Fatal("白名单 total 应为 1")
	}
	// 白名单忽略 expires_at/block_type：传了也不报错
	w = doReq(t, h.WhitelistUpdate(), http.MethodPost, "/admin/shield/whitelist/update",
		`{"id":`+itoa(id)+`,"title":"改","block_type":7,"expires_at":"2026-01-01T00:00:00Z"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("白名单更新 code = %d, body=%s", w.Code, w.Body.String())
	}
	// 软删 + 恢复
	w = doReq(t, h.WhitelistDelete(), http.MethodPost, "/admin/shield/whitelist/delete", `{"id":`+itoa(id)+`}`)
	if w.Code != http.StatusOK {
		t.Fatal("白名单软删失败")
	}
	w = doReq(t, h.WhitelistRestore(), http.MethodPost, "/admin/shield/whitelist/restore", `{"id":`+itoa(id)+`}`)
	if w.Code != http.StatusOK {
		t.Fatal("白名单恢复失败")
	}
	// 导入
	w = doReq(t, h.WhitelistImport(), http.MethodPost, "/admin/shield/whitelist/import", "192.168.1.1\n")
	if w.Code != http.StatusOK {
		t.Fatalf("白名单导入 code = %d", w.Code)
	}
}

// ── DB 未配置 → 503 ─────────────────────────────────────────────────

func TestAdmin_IPListDisabled(t *testing.T) {
	s, _ := newTestShield(t)
	h := &AdminHandler{shield: s} // 未注入 store
	w := doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置 DB code = %d, want 503", w.Code)
	}
	// shield 未注册 → 503
	h2 := &AdminHandler{}
	w = doReq(t, h2.Blacklist(), http.MethodGet, "/admin/shield/blacklist", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("未注册 shield code = %d, want 503", w.Code)
	}
}

// itoa 测试辅助。
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// InBlacklist 与拦截判定同源：未加入不命中，AddIPList 落库并重建快照后命中。
func TestShield_InBlacklist(t *testing.T) {
	h, s := newAdminListTest(t, true)
	if s.InBlacklist("1.2.3.4") {
		t.Fatal("未加入前不应命中黑名单")
	}
	if _, err := s.AddIPList(true, "1.2.3.4", "测试", BlockIPBlacklist, nil); err != nil {
		t.Fatalf("AddIPList: %v", err)
	}
	if !s.InBlacklist("1.2.3.4") {
		t.Fatal("加入后应命中黑名单")
	}
	if s.InBlacklist("5.6.7.8") {
		t.Fatal("其他 IP 不应命中黑名单")
	}
	_ = h
}

// ── A3：sync_file / ban / jail / sort 端点（IP_BLACKLIST_PLAN §3.3/§3.5/§3.7/§3.8）──

// TestAdmin_BlacklistSyncFile 从外挂规则文件同步：正常导入、非法行计入 skipped、
// 重复同步幂等 skipped 递增、文件为空 400。
func TestAdmin_BlacklistSyncFile(t *testing.T) {
	h, _ := newAdminListTest(t, true)

	// 正常：2 个有效 IP + 1 非法行 + 1 文件内重复（Import 幂等跳过）
	writeExternalRule(t, "ip_blacklist.txt", "# 注释\n\n10.1.1.1\n10.1.1.2\nbogus-line\n10.1.1.2\n")
	w := doReq(t, h.BlacklistSyncFile(), http.MethodPost, "/admin/shield/blacklist/sync_file", "")
	if w.Code != http.StatusOK {
		t.Fatalf("sync_file code = %d, body=%s", w.Code, w.Body.String())
	}
	m := decodeResp(t, w)
	if m["imported"].(float64) != 2 || m["skipped"].(float64) != 2 {
		t.Fatalf("imported = %v skipped = %v, want 2/2", m["imported"], m["skipped"])
	}
	// 重复同步幂等：imported=0，skipped 递增（2 非法 + 2 已存在）
	w = doReq(t, h.BlacklistSyncFile(), http.MethodPost, "/admin/shield/blacklist/sync_file", "")
	if w.Code != http.StatusOK {
		t.Fatalf("重复同步 code = %d", w.Code)
	}
	m = decodeResp(t, w)
	if m["imported"].(float64) != 0 || m["skipped"].(float64) != 4 {
		t.Fatalf("重复同步 imported = %v skipped = %v, want 0/4", m["imported"], m["skipped"])
	}
	// 文件为空（仅注释/空行）→ 400 三要素文案
	writeExternalRule(t, "ip_blacklist.txt", "# 只有注释\n\n")
	w = doReq(t, h.BlacklistSyncFile(), http.MethodPost, "/admin/shield/blacklist/sync_file", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空文件 code = %d, want 400", w.Code)
	}
	// 方法校验：GET → 405
	w = doReq(t, h.BlacklistSyncFile(), http.MethodGet, "/admin/shield/blacklist/sync_file", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET sync_file code = %d, want 405", w.Code)
	}
}

// TestAdmin_BlacklistBan 封禁三态：无记录入库 / 活跃 400 / 过期恢复续封（warn+1）、
// 累计满 5 转永久响应注明、参数校验。
func TestAdmin_BlacklistBan(t *testing.T) {
	h, s := newAdminListTest(t, true)

	// ① 无记录 → 入库（warn_times=1 起算）
	w := doReq(t, h.BlacklistBan(), http.MethodPost, "/admin/shield/blacklist/ban",
		`{"ip":"10.2.0.1","title":"人工封禁：SQL注入","block_type":7,"duration":"24h"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("首次封禁 code = %d, body=%s", w.Code, w.Body.String())
	}
	if decodeResp(t, w)["to_permanent"].(bool) {
		t.Fatal("首次封禁不应转永久")
	}
	// ② 活跃条目 → 400 含"已在黑名单"+ 去向指引
	w = doReq(t, h.BlacklistBan(), http.MethodPost, "/admin/shield/blacklist/ban",
		`{"ip":"10.2.0.1","duration":"permanent"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "已在黑名单") {
		t.Fatalf("重复封禁 code = %d body = %s, want 400 含'已在黑名单'", w.Code, w.Body.String())
	}
	// 列表核对 warn_times=1、block_type=7
	w = doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?ip=10.2.0.1", "")
	rows := decodeResp(t, w)["rows"].([]any)
	row := rows[0].(map[string]any)
	if row["warn_times"].(float64) != 1 || row["block_type"].(float64) != 7 {
		t.Fatalf("warn_times = %v block_type = %v, want 1/7", row["warn_times"], row["block_type"])
	}
	id := int64(row["id"].(float64))

	// ③ 已过期条目 → 恢复续封（warn+1，按所选时长 permanent 重设）
	past := time.Now().Add(-time.Hour)
	if err := s.UpdateIPList(true, id, "人工封禁：SQL注入", BlockType(7), &past); err != nil {
		t.Fatalf("置过期: %v", err)
	}
	w = doReq(t, h.BlacklistBan(), http.MethodPost, "/admin/shield/blacklist/ban",
		`{"ip":"10.2.0.1","duration":"permanent"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("过期恢复封禁 code = %d, body=%s", w.Code, w.Body.String())
	}
	w = doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?ip=10.2.0.1&valid_only=1", "")
	row = decodeResp(t, w)["rows"].([]any)[0].(map[string]any)
	// expires_at 为 NULL 时行内可能省略该键（easydb 扫描 NULL 列不落 map），缺键等同永久
	if row["warn_times"].(float64) != 2 || !banExpiresEmpty(row["expires_at"]) {
		t.Fatalf("恢复后 warn_times = %v expires_at = %v, want 2/空(永久)", row["warn_times"], row["expires_at"])
	}

	// ④ 累计满 5 转永久：另起 IP，循环置过期再封 24h，第 4 次恢复 warn 达 5
	banBody := `{"ip":"10.2.0.2","duration":"24h"}`
	w = doReq(t, h.BlacklistBan(), http.MethodPost, "/admin/shield/blacklist/ban", banBody)
	if w.Code != http.StatusOK {
		t.Fatalf("第二次封禁 code = %d", w.Code)
	}
	w = doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?ip=10.2.0.2", "")
	id2 := int64(decodeResp(t, w)["rows"].([]any)[0].(map[string]any)["id"].(float64))
	perm := false
	for i := 0; i < 4; i++ {
		if err := s.UpdateIPList(true, id2, "", BlockManual, &past); err != nil {
			t.Fatalf("置过期: %v", err)
		}
		w = doReq(t, h.BlacklistBan(), http.MethodPost, "/admin/shield/blacklist/ban", banBody)
		if w.Code != http.StatusOK {
			t.Fatalf("续封第 %d 次 code = %d, body=%s", i+1, w.Code, w.Body.String())
		}
		perm = decodeResp(t, w)["to_permanent"].(bool)
	}
	if !perm {
		t.Fatal("累计封禁达 5 次的限时封禁应转永久（to_permanent=true）")
	}
	w = doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist?ip=10.2.0.2&valid_only=1", "")
	row = decodeResp(t, w)["rows"].([]any)[0].(map[string]any)
	if row["warn_times"].(float64) != 5 || !banExpiresEmpty(row["expires_at"]) || !strings.Contains(row["title"].(string), "转永久") {
		t.Fatalf("转永久后行 = %v, want warn=5/expires_at空/title含'转永久'", row)
	}

	// ⑤ 参数校验：非法 duration / block_type 越界 / 空 ip → 400
	w = doReq(t, h.BlacklistBan(), http.MethodPost, "/admin/shield/blacklist/ban",
		`{"ip":"10.2.0.3","duration":"1h"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 duration code = %d, want 400", w.Code)
	}
	w = doReq(t, h.BlacklistBan(), http.MethodPost, "/admin/shield/blacklist/ban",
		`{"ip":"10.2.0.3","block_type":12}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("block_type=12 code = %d, want 400", w.Code)
	}
	w = doReq(t, h.BlacklistBan(), http.MethodPost, "/admin/shield/blacklist/ban", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空 ip code = %d, want 400", w.Code)
	}
}

// TestAdmin_Jail 小黑屋：在押条目查询 + limit 边界（0/负数/非法/超上限回默认 20）。
func TestAdmin_Jail(t *testing.T) {
	h, _ := newAdminListTest(t, true)
	for _, ip := range []string{"10.3.0.1", "10.3.0.2", "10.3.0.3"} {
		w := doReq(t, h.BlacklistBan(), http.MethodPost, "/admin/shield/blacklist/ban",
			`{"ip":"`+ip+`","duration":"24h"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("封禁 %s code = %d, body=%s", ip, w.Code, w.Body.String())
		}
	}
	// 默认（缺省 query）→ total 3，rows 3
	w := doReq(t, h.Jail, http.MethodGet, "/admin/shield/jail", "")
	if w.Code != http.StatusOK {
		t.Fatalf("jail code = %d, body=%s", w.Code, w.Body.String())
	}
	m := decodeResp(t, w)
	if m["total"].(float64) != 3 {
		t.Fatalf("total = %v, want 3", m["total"])
	}
	rows := m["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("rows len = %d, want 3", len(rows))
	}
	// rows 字段齐全（IP_BLACKLIST_PLAN §3.7）
	row := rows[0].(map[string]any)
	for _, k := range []string{"ip", "block_type", "hit_count", "warn_times", "created_at", "expires_at"} {
		if _, ok := row[k]; !ok {
			t.Fatalf("jail 行缺字段 %s", k)
		}
	}
	// limit=1 → 仅 1 行，total 仍 3
	w = doReq(t, h.Jail, http.MethodGet, "/admin/shield/jail?limit=1", "")
	m = decodeResp(t, w)
	if m["total"].(float64) != 3 || len(m["rows"].([]any)) != 1 {
		t.Fatalf("limit=1 total = %v rows = %d, want 3/1", m["total"], len(m["rows"].([]any)))
	}
	// 非法/负数/超上限 → 回默认（3 行，不报错）
	for _, q := range []string{"limit=0", "limit=-5", "limit=abc", "limit=500"} {
		w = doReq(t, h.Jail, http.MethodGet, "/admin/shield/jail?"+q, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s code = %d, want 200", q, w.Code)
		}
		if n := len(decodeResp(t, w)["rows"].([]any)); n != 3 {
			t.Fatalf("%s rows = %d, want 3（回默认）", q, n)
		}
	}
}

// banExpiresEmpty 判列表行 expires_at 为空（NULL/缺键/空串均视为永久）。
func banExpiresEmpty(v any) bool {
	s, _ := v.(string)
	return v == nil || s == ""
}

// TestAdmin_BlacklistSort 列表 sort 参数接入白名单映射：hit_count 生效、非法回默认、白名单不受影响。
func TestAdmin_BlacklistSort(t *testing.T) {
	blackStore, _ := newFileListStore(t, true)
	whiteStore, _ := newFileListStore(t, false)
	s, _ := newTestShield(t)
	s.enabled = true
	s.SetIPListStores(blackStore, whiteStore)
	t.Cleanup(func() { s.Stop() })
	h := &AdminHandler{shield: s}
	store := blackStore

	// 依序插入 3 条（id 1/2/3）
	for _, ip := range []string{"10.4.0.1", "10.4.0.2", "10.4.0.3"} {
		if _, err := s.AddIPList(true, ip, "排序测试", BlockManual, nil); err != nil {
			t.Fatalf("AddIPList %s: %v", ip, err)
		}
	}
	// hit_count：id1=5、id2=1（经攒批同款 AddHitCount 直写）
	if err := store.AddHitCount(1, 5); err != nil {
		t.Fatalf("AddHitCount: %v", err)
	}
	if err := store.AddHitCount(2, 1); err != nil {
		t.Fatalf("AddHitCount: %v", err)
	}
	firstIP := func(query string) string {
		w := doReq(t, h.Blacklist(), http.MethodGet, "/admin/shield/blacklist"+query, "")
		if w.Code != http.StatusOK {
			t.Fatalf("列表 %s code = %d, body=%s", query, w.Code, w.Body.String())
		}
		rows := decodeResp(t, w)["rows"].([]any)
		return rows[0].(map[string]any)["ip"].(string)
	}
	// sort=hit_count → 命中最高的 10.4.0.1 在前
	if got := firstIP("?sort=hit_count"); got != "10.4.0.1" {
		t.Fatalf("sort=hit_count 首行 = %s, want 10.4.0.1", got)
	}
	// 非法 sort → 回默认 id DESC（最后插入的 10.4.0.3 在前）
	if got := firstIP("?sort=bogus;--"); got != "10.4.0.3" {
		t.Fatalf("非法 sort 首行 = %s, want 10.4.0.3（默认 id DESC）", got)
	}
	// 缺省 → 同默认
	if got := firstIP(""); got != "10.4.0.3" {
		t.Fatalf("缺省 sort 首行 = %s, want 10.4.0.3", got)
	}
	// 白名单不受影响（无 hit_count 语义，sort 忽略不报错）
	if _, err := s.AddIPList(false, "192.168.9.9", "白名单排序测试", 0, nil); err != nil {
		t.Fatalf("白名单新增: %v", err)
	}
	w := doReq(t, h.Whitelist(), http.MethodGet, "/admin/shield/whitelist?sort=hit_count", "")
	if w.Code != http.StatusOK || decodeResp(t, w)["total"].(float64) != 1 {
		t.Fatalf("白名单 sort 透传 code = %d body = %s", w.Code, w.Body.String())
	}
}

// ── 拦截明细 events 行内 in_blacklist 标记（STEP A6）────────────────

// Events 接口对当前页 IP 遍历内存快照附 in_blacklist 标记（与 stats TOP 同源、零 DB）：
// 快照命中的行 true、未命中 false；NDJSON 流式逐行输出均携带该字段。
func TestAdmin_EventsInBlacklistFlag(t *testing.T) {
	h, s := newAdminListTest(t, true)
	r, _ := newTestRecorder(t)
	s.SetEventRecorder(r)

	// 落两条明细：10.5.0.1 / 10.5.0.2
	for _, ip := range []string{"10.5.0.1", "10.5.0.2"} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = ip + ":1234"
		r.Record(newEventCtx(req), BlockSQLInjection, "sql_pattern")
	}
	r.Stop() // 排空通道落库

	// 10.5.0.1 入黑名单（落库并重建快照）
	if _, err := s.AddIPList(true, "10.5.0.1", "测试封禁", 7, nil); err != nil {
		t.Fatalf("AddIPList: %v", err)
	}

	w := doReq(t, h.Events, http.MethodGet, "/admin/shield/events?limit=10", "")
	if w.Code != http.StatusOK {
		t.Fatalf("events code = %d, body=%s", w.Code, w.Body.String())
	}
	flagByIP := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("NDJSON 行解析失败: %v（%s）", err, line)
		}
		flag, ok := row["in_blacklist"].(bool)
		if !ok {
			t.Fatalf("行缺 in_blacklist 布尔字段: %s", line)
		}
		flagByIP[row["client_ip"].(string)] = flag
	}
	if len(flagByIP) != 2 {
		t.Fatalf("应有 2 行明细，实际 %d", len(flagByIP))
	}
	if !flagByIP["10.5.0.1"] {
		t.Errorf("10.5.0.1 已在黑名单快照，in_blacklist 应为 true")
	}
	if flagByIP["10.5.0.2"] {
		t.Errorf("10.5.0.2 不在黑名单快照，in_blacklist 应为 false")
	}
}
