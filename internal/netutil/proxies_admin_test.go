// 可信代理文件在线编辑端点单测（proxies_admin.go）：
// 清单/读文件/保存回路、文件名白名单、非法内容拒绝、GET 保存拒绝。
package netutil

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rocksys/internal/hotswap"
)

// newTestProxiesAdmin 构造落点在临时目录的 handler（隔离外挂根目录，避免污染包目录）。
func newTestProxiesAdmin(t *testing.T) *ProxiesAdmin {
	t.Helper()
	tmp := t.TempDir()
	hotswap.SetHotScriptsDir(tmp)
	t.Cleanup(func() { hotswap.SetHotScriptsDir("hotscripts") }) // 恢复默认，防串扰其他测试
	p, err := NewProxiesAdmin(nil, "trusted_proxies.txt")
	if err != nil {
		t.Fatalf("NewProxiesAdmin: %v", err)
	}
	return p
}

func TestProxiesAdminListAndFile(t *testing.T) {
	p := newTestProxiesAdmin(t)

	rec := httptest.NewRecorder()
	p.List(rec, httptest.NewRequest(http.MethodGet, "/admin/proxy/trusted", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"files"`) {
		t.Fatalf("List 应返回 200 + files，实际 %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	p.File(rec, httptest.NewRequest(http.MethodGet, "/admin/proxy/trusted/file?name=trusted_proxies.txt", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"content"`) {
		t.Fatalf("File 应返回 200 + content，实际 %d %q", rec.Code, rec.Body.String())
	}

	// 文件名白名单：非装配文件名拒绝
	rec = httptest.NewRecorder()
	p.File(rec, httptest.NewRequest(http.MethodGet, "/admin/proxy/trusted/file?name=../../etc/passwd", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法文件名应 400，实际 %d", rec.Code)
	}
}

func TestProxiesAdminSaveRoundTrip(t *testing.T) {
	p := newTestProxiesAdmin(t)
	body := `{"name":"trusted_proxies.txt","content":"10.0.0.1\n192.168.0.0/16\n"}`
	rec := httptest.NewRecorder()
	p.Save()(rec, httptest.NewRequest(http.MethodPost, "/admin/proxy/trusted/save", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("Save 应成功，实际 %d %q", rec.Code, rec.Body.String())
	}
	// 落盘校验
	want := "10.0.0.1\n192.168.0.0/16\n"
	data, err := os.ReadFile(filepath.Join(p.hotPath()))
	if err != nil || string(data) != want {
		t.Fatalf("外挂文件未正确落盘: %v %q", err, string(data))
	}
	// 重读：当前生效文本 = 保存内容（ScriptDir 外挂优先）
	text, err := p.currentText()
	if err != nil || text != want {
		t.Fatalf("currentText 应读到保存内容: %v %q", err, text)
	}

	// 非法 CIDR 拒绝保存且不落盘
	rec = httptest.NewRecorder()
	p.Save()(rec, httptest.NewRequest(http.MethodPost, "/admin/proxy/trusted/save",
		strings.NewReader(`{"name":"trusted_proxies.txt","content":"not-an-ip"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法内容应 400，实际 %d", rec.Code)
	}

	// 文件名不一致拒绝
	rec = httptest.NewRecorder()
	p.Save()(rec, httptest.NewRequest(http.MethodPost, "/admin/proxy/trusted/save",
		strings.NewReader(`{"name":"evil.txt","content":"1.2.3.4"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法文件名应 400，实际 %d", rec.Code)
	}

	// GET 保存拒绝
	rec = httptest.NewRecorder()
	p.Save()(rec, httptest.NewRequest(http.MethodGet, "/admin/proxy/trusted/save", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET 保存应 405，实际 %d", rec.Code)
	}
}
