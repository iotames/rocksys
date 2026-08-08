package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/hotswap"

	"github.com/iotames/easyserver/httpsvr"
)

// fakeMiddleware 测试用链中间件，实现 hotswap.MiddlewareLifecycle。
type fakeMiddleware struct {
	name  string
	state hotswap.State
	slot  chain.Slot
}

func (f *fakeMiddleware) Name() string                      { return f.name }
func (f *fakeMiddleware) Handle(*chain.Context) (next bool) { return true }
func (f *fakeMiddleware) Start(cfg any) error               { f.state = hotswap.StateEnabled; return nil }
func (f *fakeMiddleware) Stop() error                       { f.state = hotswap.StateDisabled; return nil }
func (f *fakeMiddleware) Slot() chain.Slot                  { return f.slot }

// setup 构造 conf.Manager 与 hotswap.Manager。
func setup(t *testing.T) (conf.Manager, *hotswap.Manager, string) {
	t.Helper()
	cfgMgr, err := conf.Load(nil)
	if err != nil {
		t.Fatalf("conf.Load: %v", err)
	}
	// 清理 easyconf 在包目录自动创建的工作目录 .env / default.env（配置中心红线：运行时文件不得残留源码树）。
	t.Cleanup(func() { _ = os.Remove(".env"); _ = os.Remove("default.env") })
	ch := chain.New()
	mgr := hotswap.NewManager(ch, nil)
	mgr.RegisterMiddleware(&fakeMiddleware{name: "shield", slot: chain.Middle})
	return cfgMgr, mgr, cfgMgr.Current().AdminAddr
}

// newCtx 构造 httpsvr.Context。
func newCtx(method, path, body string) httpsvr.Context {
	var r *http.Request
	if method == http.MethodGet || method == http.MethodPut {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.Header.Set("Content-Type", "application/json")
	return httpsvr.Context{Writer: httptest.NewRecorder(), Request: r}
}

// decode 解析 Json(map) 响应。
func decode(t *testing.T, ctx httpsvr.Context) map[string]any {
	t.Helper()
	rec := ctx.Writer.(*httptest.ResponseRecorder)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, rec.Body.String())
	}
	return out
}

func TestNew(t *testing.T) {
	a := New("127.0.0.1:19527", nil, nil, nil)
	if a == nil || a.srv == nil {
		t.Fatal("New 未创建 AdminServer")
	}
}

func TestHandleSwitchOn(t *testing.T) {
	_, mgr, _ := setup(t)
	s := New("127.0.0.1:19527", nil, mgr, nil)
	ctx := newCtx(http.MethodPost, PathSwitchOn, `{"name":"shield"}`)
	s.handleSwitchOn(ctx)
	out := decode(t, ctx)
	if out["ok"] != true {
		t.Fatalf("期望 ok:true，得到 %v", out["ok"])
	}
	if mgr.List()[0].State != hotswap.StateEnabled {
		t.Fatal("switch/on 后 shield 未启用")
	}
}

func TestHandleSwitchOff(t *testing.T) {
	_, mgr, _ := setup(t)
	s := New("127.0.0.1:19527", nil, mgr, nil)
	_ = s.hotswapMgr.Enable("shield")
	ctx := newCtx(http.MethodPost, PathSwitchOff, `{"name":"shield"}`)
	s.handleSwitchOff(ctx)
	out := decode(t, ctx)
	if out["ok"] != true {
		t.Fatalf("期望 ok:true，得到 %v", out["ok"])
	}
	if mgr.List()[0].State != hotswap.StateDisabled {
		t.Fatal("switch/off 后 shield 未禁用")
	}
}

func TestHandleSwitchNotFound(t *testing.T) {
	_, mgr, _ := setup(t)
	s := New("127.0.0.1:19527", nil, mgr, nil)
	ctx := newCtx(http.MethodPost, PathSwitchOn, `{"name":"nope"}`)
	s.handleSwitchOn(ctx)
	out := decode(t, ctx)
	if out["ok"] != false {
		t.Fatalf("期望 ok:false，得到 %v", out)
	}
}

func TestHandleSwitchList(t *testing.T) {
	_, mgr, _ := setup(t)
	_ = mgr.Enable("shield")
	s := New("127.0.0.1:19527", nil, mgr, nil)
	ctx := newCtx(http.MethodGet, PathSwitchList, "")
	s.handleSwitchList(ctx)
	rec := ctx.Writer.(*httptest.ResponseRecorder)
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("期望 1 项，得到 %d", len(list))
	}
	if list[0]["name"] != "shield" || list[0]["kind"] != "middleware" || list[0]["state"] != "enabled" {
		t.Fatalf("list 内容不符: %v", list[0])
	}
	if _, ok := list[0]["started_at"]; !ok {
		t.Fatal("缺少 started_at 字段")
	}
}

func TestHandleConfigGet(t *testing.T) {
	cfgMgr, _, _ := setup(t)
	s := New("127.0.0.1:19527", cfgMgr, nil, nil)
	ctx := newCtx(http.MethodGet, PathConfig, "")
	s.handleConfigGet(ctx)
	out := decode(t, ctx)
	if _, ok := out["listen"]; !ok {
		t.Fatalf("缺少 listen 字段: %v", out)
	}
	if _, ok := out["upstream"]; !ok {
		t.Fatalf("缺少 upstream 字段: %v", out)
	}
	if _, ok := out["config_file"]; !ok {
		t.Fatalf("缺少 config_file 字段: %v", out)
	}
}

func TestHandleConfigPut(t *testing.T) {
	cfgMgr, _, _ := setup(t)
	before := cfgMgr.Current().DefaultUpstream
	s := New("127.0.0.1:19527", cfgMgr, nil, nil)
	ctx := newCtx(http.MethodPut, PathConfig, `{"ROCKSYS_UPSTREAM":"http://127.0.0.1:9999"}`)
	s.handleConfigPut(ctx)
	out := decode(t, ctx)
	if out["ok"] != true {
		t.Fatalf("期望 ok:true，得到 %v", out)
	}
	after := cfgMgr.Current().DefaultUpstream
	if after != "http://127.0.0.1:9999" {
		t.Fatalf("配置未生效: before=%s after=%s", before, after)
	}
}

func TestRegisterPlugin(t *testing.T) {
	s := New("127.0.0.1:19527", nil, nil, nil)
	if err := s.RegisterPlugin("/admin/custom", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}); err != nil {
		t.Fatalf("RegisterPlugin: %v", err)
	}
	if err := s.RegisterPlugin("", nil); err == nil {
		t.Fatal("空参数未报错")
	}
}

// TestHandleConfigList 验证全量配置清单端点（WebUI 配置页数据源）。
func TestHandleConfigList(t *testing.T) {
	cfgMgr, _, _ := setup(t)
	s := New("127.0.0.1:19527", cfgMgr, nil, nil)
	ctx := newCtx(http.MethodGet, PathConfigList, "")
	s.handleConfigList(ctx)
	rec := ctx.Writer.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码=%d, want 200", rec.Code)
	}
	var list []conf.ConfigItem
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析失败: %v, body=%s", err, rec.Body.String())
	}
	if len(list) < 6 {
		t.Fatalf("至少应含底座 6 项配置, got %d", len(list))
	}
	seen := false
	for _, it := range list {
		if it.Key == "ROCKSYS_UPSTREAM" {
			seen = true
			if it.Current == "" || it.Defval == "" {
				t.Errorf("配置项字段缺失: %+v", it)
			}
		}
	}
	if !seen {
		t.Fatal("清单中缺少 ROCKSYS_UPSTREAM")
	}
}

// TestRegisterWebUI 验证内嵌静态资源托管注册（index.html 根路径 + assets 静态文件）。
func TestRegisterWebUI(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":        {Data: []byte("<!doctype html><html></html>")},
		"assets/style.css":  {Data: []byte("body{}")},
		"assets/js/main.js": {Data: []byte("console.log('ok')")},
	}
	s := New("127.0.0.1:19527", nil, nil, nil)
	if err := s.RegisterWebUI(fsys); err != nil {
		t.Fatalf("RegisterWebUI: %v", err)
	}
	// 每个文件注册为精确 GET 路由，无报错即成功。真实 HTTP 链路由集成验证覆盖。
	if err := s.RegisterWebUI(fstest.MapFS{}); err != nil {
		t.Fatalf("RegisterWebUI 空资源应成功: %v", err)
	}
}

func TestContentTypeByExt(t *testing.T) {
	cases := map[string]string{
		"index.html":        "text/html",
		"assets/style.css":  "text/css",
		"assets/js/main.js": "application/javascript",
		"data.json":         "application/json",
		"pic.svg":           "image/svg+xml",
		"pic.png":           "image/png",
		"file.txt":          "text/plain",
	}
	for path, want := range cases {
		got := contentTypeByExt(path)
		if !strings.HasPrefix(got, want) {
			t.Errorf("contentTypeByExt(%q)=%q, want 前缀 %q", path, got, want)
		}
	}
}

// TestRequireAuth 验证鉴权包装器行为（§8.3）。
func TestRequireAuth(t *testing.T) {
	// 无 token：任何请求放行（回环信任）
	s := New("127.0.0.1:19527", nil, nil, nil)
	require := s.requireAuth()
	called := false
	h := require(func(httpsvr.Context) { called = true })
	ctx := newCtx(http.MethodGet, PathSwitchList, "")
	h(ctx)
	if !called {
		t.Fatal("未设置 token 时不应拦截请求")
	}

	// 设置 token：无 Authorization → 401
	token := "secret"
	s2 := New("127.0.0.1:19527", nil, nil, nil)
	s2.auth.token = &token
	require2 := s2.requireAuth()
	called = false
	h2 := require2(func(httpsvr.Context) { called = true })
	ctx2 := newCtx(http.MethodGet, PathSwitchList, "")
	h2(ctx2)
	if called {
		t.Fatal("缺少 token 时不应放行")
	}
	if ctx2.Writer.(*httptest.ResponseRecorder).Code != http.StatusUnauthorized {
		t.Fatalf("期望 401，得到 %d", ctx2.Writer.(*httptest.ResponseRecorder).Code)
	}

	// 正确 token → 放行
	ctx3 := newCtx(http.MethodGet, PathSwitchList, "")
	ctx3.Request.Header.Set(authorizationHeader, bearerPrefix+"secret")
	called = false
	h2(ctx3)
	if !called {
		t.Fatal("携带正确 token 应放行")
	}
}
