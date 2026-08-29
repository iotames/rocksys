package shield

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"rocksys/internal/hotswap"
)

// newRulesAdminTest 构造规则文件管理 AdminHandler（外挂根目录隔离到 TempDir）。
func newRulesAdminTest(t *testing.T) (*AdminHandler, string) {
	t.Helper()
	orig := hotswap.HotScriptsDir()
	t.Cleanup(func() { hotswap.SetHotScriptsDir(orig) })
	root := t.TempDir()
	hotswap.SetHotScriptsDir(root)
	s, _ := newTestShield(t)
	t.Cleanup(func() { s.Stop() })
	return &AdminHandler{shield: s}, root
}

// 清单：返回全部白名单文件，默认均无外挂覆写。
func TestAdmin_RulesList(t *testing.T) {
	h, _ := newRulesAdminTest(t)
	w := doReq(t, h.Rules, http.MethodGet, "/admin/shield/rules", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", w.Code, w.Body.String())
	}
	m := decodeResp(t, w)
	files, ok := m["files"].([]any)
	if !ok {
		t.Fatalf("响应缺少 files 数组: %v", m)
	}
	if len(files) != len(ruleFileMetas) {
		t.Fatalf("文件数 = %d, 期望 %d", len(files), len(ruleFileMetas))
	}
	first, _ := files[0].(map[string]any)
	if first["override"] != false {
		t.Errorf("默认应无外挂覆写, got %v", first["override"])
	}
	if first["lines"] == nil {
		t.Errorf("缺少 lines 字段: %v", first)
	}
}

// 读文件：返回当前生效内容（内嵌兜底）与内嵌默认；非法文件名 400。
func TestAdmin_RuleFileRead(t *testing.T) {
	h, _ := newRulesAdminTest(t)

	w := doReq(t, h.RuleFile, http.MethodGet, "/admin/shield/rules/file?name=risk_paths.txt", "")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", w.Code, w.Body.String())
	}
	m := decodeResp(t, w)
	if m["override"] != false {
		t.Errorf("无外挂时应为内嵌兜底, got override=%v", m["override"])
	}
	if m["content"] == "" || m["embedded"] == "" {
		t.Errorf("content/embedded 不应为空: %v", m)
	}

	w = doReq(t, h.RuleFile, http.MethodGet, "/admin/shield/rules/file?name=../../etc/passwd", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("路径穿越应 400, got %d", w.Code)
	}
}

// 保存：落点外挂覆写文件、CRLF 归一、结尾补换行；再读 override=true 且内容一致。
func TestAdmin_RuleSave(t *testing.T) {
	h, root := newRulesAdminTest(t)

	body := `{"name":"crawler_ua.txt","content":"curl/\r\npython-requests\r\n# comment"}`
	w := doReq(t, h.RuleSave(), http.MethodPost, "/admin/shield/rules/save", body)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", w.Code, w.Body.String())
	}

	raw, err := os.ReadFile(filepath.Join(root, "rules", "crawler_ua.txt"))
	if err != nil {
		t.Fatalf("外挂覆写文件未落盘: %v", err)
	}
	if got, want := string(raw), "curl/\npython-requests\n# comment\n"; got != want {
		t.Errorf("落盘内容 = %q, 期望 %q", got, want)
	}

	w = doReq(t, h.RuleFile, http.MethodGet, "/admin/shield/rules/file?name=crawler_ua.txt", "")
	if w.Code != http.StatusOK {
		t.Fatalf("再读 code = %d", w.Code)
	}
	m := decodeResp(t, w)
	if m["override"] != true {
		t.Errorf("保存后 override 应为 true, got %v", m["override"])
	}

	// 白名单外文件名拒绝；GET 触发副作用端点拒绝
	w = doReq(t, h.RuleSave(), http.MethodPost, "/admin/shield/rules/save", `{"name":"evil.sh","content":"x"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("白名单外文件名应 400, got %d", w.Code)
	}
	w = doReq(t, h.RuleSave(), http.MethodGet, "/admin/shield/rules/save", "")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET 应 405, got %d", w.Code)
	}
}

// 大小上限：超 512KB 拒绝。
func TestAdmin_RuleSaveSizeLimit(t *testing.T) {
	h, _ := newRulesAdminTest(t)
	huge := make([]byte, rulesSaveMaxBytes+1)
	for i := range huge {
		huge[i] = 'a'
	}
	body := fmt.Sprintf(`{"name":"risk_paths.txt","content":%q}`, string(huge))
	w := doReq(t, h.RuleSave(), http.MethodPost, "/admin/shield/rules/save", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("超限应 400, got %d", w.Code)
	}
}
