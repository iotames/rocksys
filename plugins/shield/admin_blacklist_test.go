package shield

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
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
