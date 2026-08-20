// obs 管理端点单元测试（admin.go）：
// Prune 方法校验（GET 拒绝）+ 500 错误不回显底层细节。
package obs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Prune 方法校验：GET 一律 405（RegisterPlugin 同 path 注册 GET+POST，
// 防止本机恶意页面无凭证触发清理）；POST 在 obs 未注册时正常走 503 降级。
func TestAdminObsPruneMethodCheck(t *testing.T) {
	// obs 未注册：GET → 405（方法校验先于实例检查），POST → 503。
	h := &AdminHandler{}
	rec := httptest.NewRecorder()
	h.Prune(rec, httptest.NewRequest(http.MethodGet, "/admin/logs/prune", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("obs 未注册时 GET 应返回 405，实际 %d（body=%q）", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.Prune(rec, httptest.NewRequest(http.MethodPost, "/admin/logs/prune", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("obs 未注册时 POST 应返回 503，实际 %d（body=%q）", rec.Code, rec.Body.String())
	}

	// obs 已注册：GET 仍被拒绝，POST 进入清理逻辑（dataDB nil → 500）。
	o, _ := newTestObs(t)
	h = &AdminHandler{obs: o}
	rec = httptest.NewRecorder()
	h.Prune(rec, httptest.NewRequest(http.MethodGet, "/admin/logs/prune", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET 应返回 405，实际 %d（body=%q）", rec.Code, rec.Body.String())
	}
}

// 500 错误不应回显底层错误细节：dataDB 未就绪时 PruneLog 返回内部错误，
// 响应只给固定文案，细节仅写服务端日志。
func TestAdminObsPruneErrorNoDetail(t *testing.T) {
	o, _ := newTestObs(t) // dataDB=nil，PruneLog 必然失败
	h := &AdminHandler{obs: o}

	rec := httptest.NewRecorder()
	h.Prune(rec, httptest.NewRequest(http.MethodPost, "/admin/logs/prune", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("dataDB 未就绪应返回 500，实际 %d（body=%q）", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "prune 失败") {
		t.Errorf("响应应包含固定文案，body=%q", body)
	}
	if strings.Contains(body, "未就绪") || strings.Contains(body, "数据访问层") || strings.Contains(body, "Error") {
		t.Errorf("500 响应不应回显底层错误细节，body=%q", body)
	}
}
