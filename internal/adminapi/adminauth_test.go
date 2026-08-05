package adminapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iotames/easydb"
	"github.com/iotames/easyserver/httpsvr"

	"rocksys/internal/db"

	_ "modernc.org/sqlite"
)

// setupAuthServer 构造绑定非回环地址的管理接口服务器（触发登录鉴权），用户存储用临时 sqlite。
func setupAuthServer(t *testing.T) *AdminServer {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "admin_test.db")
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	src, err := db.EmbeddedSQLSource("sqlite")
	if err != nil {
		t.Fatalf("EmbeddedSQLSource(sqlite): %v", err)
	}
	s := New("0.0.0.0:19527", nil, nil, easydb.NewEasyDbBySqlDB(sqldb))
	s.SetSQLSource(src)
	return s
}

// jsonBody 解析 JSON 响应为 map。
func jsonBody(t *testing.T, ctx httpsvr.Context) map[string]any {
	t.Helper()
	rec := ctx.Writer.(*httptest.ResponseRecorder)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v, body=%s", err, rec.Body.String())
	}
	return out
}

func TestPasswordHash(t *testing.T) {
	hash, err := hashPassword("Admin@12345")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, passwordHashPrefix) {
		t.Fatalf("哈希缺少前缀: %s", hash)
	}
	if !checkPassword("Admin@12345", hash) {
		t.Fatal("正确密码校验失败")
	}
	if checkPassword("wrong-pass", hash) {
		t.Fatal("错误密码校验通过")
	}
	if checkPassword("Admin@12345", "not-a-hash") {
		t.Fatal("非法哈希校验通过")
	}
}

func TestAuthStatusUninitialized(t *testing.T) {
	s := setupAuthServer(t)
	ctx := newCtx(http.MethodGet, PathAuthStatus, "")
	s.handleAuthStatus(ctx)
	out := jsonBody(t, ctx)
	if out["has_user"] != false {
		t.Fatalf("未初始化应 has_user=false, got %v", out["has_user"])
	}
	if out["auth_required"] != true {
		t.Fatalf("非回环地址应 auth_required=true, got %v", out["auth_required"])
	}
	if out["setup_mode"] != false {
		t.Fatalf("全新系统不应 setup_mode, got %v", out["setup_mode"])
	}
}

func TestRegisterLoginFlow(t *testing.T) {
	s := setupAuthServer(t)

	// 首次注册
	ctx := newCtx(http.MethodPost, PathRegister, `{"username":"admin","password":"Admin@12345"}`)
	s.handleRegister(ctx)
	out := jsonBody(t, ctx)
	if out["ok"] != true {
		t.Fatalf("注册失败: %v", out)
	}

	// 重复注册 → 403
	ctx = newCtx(http.MethodPost, PathRegister, `{"username":"admin2","password":"Admin@12345"}`)
	s.handleRegister(ctx)
	if ctx.Writer.(*httptest.ResponseRecorder).Code != http.StatusForbidden {
		t.Fatalf("重复注册应 403, got %d", ctx.Writer.(*httptest.ResponseRecorder).Code)
	}

	// 错误密码 → 401
	ctx = newCtx(http.MethodPost, PathLogin, `{"username":"admin","password":"wrong-pass"}`)
	s.handleLogin(ctx)
	if ctx.Writer.(*httptest.ResponseRecorder).Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401, got %d", ctx.Writer.(*httptest.ResponseRecorder).Code)
	}

	// 正确登录 → token
	ctx = newCtx(http.MethodPost, PathLogin, `{"username":"admin","password":"Admin@12345"}`)
	s.handleLogin(ctx)
	out = jsonBody(t, ctx)
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("登录未返回 token: %v", out)
	}

	// 携带 token 访问受保护接口 → 放行
	ctx = newCtx(http.MethodGet, PathSwitchList, "")
	ctx.Request.Header.Set(authorizationHeader, bearerPrefix+token)
	if !s.auth.check(ctx) {
		t.Fatal("携带有效 JWT 应通过鉴权")
	}

	// 无 token 访问受保护接口 → 拒绝
	ctx = newCtx(http.MethodGet, PathSwitchList, "")
	if s.auth.check(ctx) {
		t.Fatal("无 token 不应通过鉴权")
	}
	if ctx.Writer.(*httptest.ResponseRecorder).Code != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401, got %d", ctx.Writer.(*httptest.ResponseRecorder).Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	s := setupAuthServer(t)
	// 先注册
	ctx := newCtx(http.MethodPost, PathRegister, `{"username":"admin","password":"Admin@12345"}`)
	s.handleRegister(ctx)

	// 连续 5 次错误密码
	for i := 0; i < loginMaxFailures; i++ {
		ctx = newCtx(http.MethodPost, PathLogin, `{"username":"admin","password":"bad-pass"}`)
		s.handleLogin(ctx)
		if ctx.Writer.(*httptest.ResponseRecorder).Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误密码应 401, got %d", i+1, ctx.Writer.(*httptest.ResponseRecorder).Code)
		}
	}
	// 第 6 次即使密码正确也 429（被限流）
	ctx = newCtx(http.MethodPost, PathLogin, `{"username":"admin","password":"Admin@12345"}`)
	s.handleLogin(ctx)
	if ctx.Writer.(*httptest.ResponseRecorder).Code != http.StatusTooManyRequests {
		t.Fatalf("超限后应 429, got %d", ctx.Writer.(*httptest.ResponseRecorder).Code)
	}
}

func TestResetPassword(t *testing.T) {
	s := setupAuthServer(t)
	// 注册
	ctx := newCtx(http.MethodPost, PathRegister, `{"username":"admin","password":"Admin@12345"}`)
	s.handleRegister(ctx)

	// 未处于重置模式（confMgr nil 时 setupMode=hasUser=true）→ 允许重置
	// 重置：换用户名+密码
	ctx = newCtx(http.MethodPost, PathReset, `{"username":"admin2","password":"NewPass@67890"}`)
	s.handleReset(ctx)
	out := jsonBody(t, ctx)
	if out["ok"] != true {
		t.Fatalf("重置失败: %v", out)
	}

	// 旧密码登录 → 失败
	ctx = newCtx(http.MethodPost, PathLogin, `{"username":"admin2","password":"Admin@12345"}`)
	s.handleLogin(ctx)
	if ctx.Writer.(*httptest.ResponseRecorder).Code != http.StatusUnauthorized {
		t.Fatalf("旧密码应 401, got %d", ctx.Writer.(*httptest.ResponseRecorder).Code)
	}
	// 新密码登录 → 成功
	ctx = newCtx(http.MethodPost, PathLogin, `{"username":"admin2","password":"NewPass@67890"}`)
	s.handleLogin(ctx)
	if ctx.Writer.(*httptest.ResponseRecorder).Code != http.StatusOK {
		t.Fatalf("新密码应登录成功, got %d", ctx.Writer.(*httptest.ResponseRecorder).Code)
	}
}

func TestStaticTokenDualTrack(t *testing.T) {
	defer os.Unsetenv(envAdminToken)
	os.Setenv(envAdminToken, "static-secret")
	s := setupAuthServer(t)

	// 静态 token 正确 → 放行（无需用户/JWT）
	ctx := newCtx(http.MethodGet, PathSwitchList, "")
	ctx.Request.Header.Set(authorizationHeader, bearerPrefix+"static-secret")
	if !s.auth.check(ctx) {
		t.Fatal("正确静态 token 应通过鉴权")
	}

	// 静态 token 错误 → 拒绝
	ctx = newCtx(http.MethodGet, PathSwitchList, "")
	ctx.Request.Header.Set(authorizationHeader, bearerPrefix+"wrong")
	if s.auth.check(ctx) {
		t.Fatal("错误静态 token 不应通过鉴权")
	}
}

func TestLoopbackTrust(t *testing.T) {
	// 回环地址 + 无静态 token → 免登录放行
	dsn := filepath.Join(t.TempDir(), "admin_test.db")
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sqldb.Close()
	s := New("127.0.0.1:19527", nil, nil, easydb.NewEasyDbBySqlDB(sqldb))
	ctx := newCtx(http.MethodGet, PathSwitchList, "")
	if !s.auth.check(ctx) {
		t.Fatal("回环地址免登录应放行")
	}
}
