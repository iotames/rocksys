// 回归测试：数据层降级状态透出与账号端点前置拦截。
//
// 回归背景：远程库（PG/MySQL）连接失败时 dataDB 为 nil，userstore 降级为 nil，
// WebUI 却照常弹出「初始化管理员」注册面板，提交后才报笼统的
// 「用户存储未初始化（数据库未配置）」——误导运维去查错方向。
package adminapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAuthStatusStoreDegraded 未接用户存储时 status 应显式透出 store_ready=false 与降级原因。
func TestAuthStatusStoreDegraded(t *testing.T) {
	s := New("0.0.0.0:19527", nil, nil, nil) // edb=nil：模拟 DB 打开失败
	s.SetStoreDegraded("数据库初始化失败（driver=postgres）：密码认证失败")
	ctx := newCtx(http.MethodGet, PathAuthStatus, "")
	s.handleAuthStatus(ctx)
	out := jsonBody(t, ctx)
	if out["store_ready"] != false {
		t.Fatalf("降级时应 store_ready=false, got %v", out["store_ready"])
	}
	if out["store_error"] == "" {
		t.Fatal("降级时应透出 store_error 原因")
	}
}

// TestAuthStatusStoreReady 存储就绪时 status 应 store_ready=true 且无原因。
func TestAuthStatusStoreReady(t *testing.T) {
	s := setupAuthServer(t)
	ctx := newCtx(http.MethodGet, PathAuthStatus, "")
	s.handleAuthStatus(ctx)
	out := jsonBody(t, ctx)
	if out["store_ready"] != true {
		t.Fatalf("存储就绪应 store_ready=true, got %v", out["store_ready"])
	}
	if out["store_error"] != "" {
		t.Fatalf("存储就绪不应有 store_error, got %v", out["store_error"])
	}
}

// TestRegisterStoreDegraded503 降级时注册/登录应在入口即 503 拦截并给出引导文案，
// 而非走到保存才报「用户存储未初始化」。
func TestRegisterStoreDegraded503(t *testing.T) {
	s := New("0.0.0.0:19527", nil, nil, nil)
	s.SetStoreDegraded("数据库初始化失败（driver=postgres）：连接超时")

	ctx := newCtx(http.MethodPost, PathRegister, `{"username":"admin","password":"Admin@12345"}`)
	s.handleRegister(ctx)
	rec := ctx.Writer.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("降级注册应 503, got %d", rec.Code)
	}
	out := jsonBody(t, ctx)
	msg, _ := out["error"].(string)
	for _, want := range []string{"请检查数据库配置", "下一步"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误文案缺三要素 %q: %s", want, msg)
		}
	}

	ctx = newCtx(http.MethodPost, PathLogin, `{"username":"admin","password":"Admin@12345"}`)
	s.handleLogin(ctx)
	if ctx.Writer.(*httptest.ResponseRecorder).Code != http.StatusServiceUnavailable {
		t.Fatalf("降级登录应 503, got %d", ctx.Writer.(*httptest.ResponseRecorder).Code)
	}
}

// TestStoreDegradedOverriddenByInit 迟到的存储初始化成功应覆盖降级标记（装配顺序兜底）。
func TestStoreDegradedOverriddenByInit(t *testing.T) {
	s := setupAuthServer(t)
	s.SetStoreDegraded("过期原因") // 模拟装配层早于 SetSQLSource 注入的场景
	if !s.storeReady() {
		t.Fatal("initUsers 成功后 storeErr 未被覆盖，store_ready 应为 true")
	}
}
