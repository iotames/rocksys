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
	"rocksys/internal/catalog"
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

// TestHandleVersion 验证版本信息端点：SetVersionInfo 注入值与 --version 同源，
// 返回 version/build_time/go_version 三字段。
func TestHandleVersion(t *testing.T) {
	s := New("127.0.0.1:19527", nil, nil, nil)
	s.SetVersionInfo("v0.0.1-dev", "2026-08-19T16:11:46+08:00", "go1.25.3")
	ctx := newCtx(http.MethodGet, PathVersion, "")
	s.handleVersion(ctx)
	out := decode(t, ctx)
	if out["version"] != "v0.0.1-dev" {
		t.Errorf("version 不符: %v", out["version"])
	}
	if out["build_time"] != "2026-08-19T16:11:46+08:00" {
		t.Errorf("build_time 不符: %v", out["build_time"])
	}
	if out["go_version"] != "go1.25.3" {
		t.Errorf("go_version 不符: %v", out["go_version"])
	}
}

// TestHandleMeta 验证组件/服务元数据端点：SetCatalog 注入后经 /admin/meta 返回，
// 未注入时返回空数组（不阻塞 WebUI）。
func TestHandleMeta(t *testing.T) {
	s := New("127.0.0.1:19527", nil, nil, nil)
	ctx := newCtx(http.MethodGet, PathMeta, "")
	s.handleMeta(ctx)
	out := decode(t, ctx)
	comps, compsOK := out["components"].([]any)
	svcs, svcsOK := out["services"].([]any)
	if !compsOK || !svcsOK {
		t.Fatalf("meta 缺少 components/services 数组: %+v", out)
	}
	if len(comps) != 0 || len(svcs) != 0 {
		t.Errorf("未注入 catalog 时应返回空数组，got %d/%d", len(comps), len(svcs))
	}

	s.SetCatalog(catalog.DefaultComponents(), catalog.DefaultServices())
	ctx2 := newCtx(http.MethodGet, PathMeta, "")
	s.handleMeta(ctx2)
	out2 := decode(t, ctx2)
	comps2 := out2["components"].([]any)
	svcs2 := out2["services"].([]any)
	if len(comps2) != 9 {
		t.Errorf("components 应含 9 个链中间件，got %d", len(comps2))
	}
	if len(svcs2) != 4 {
		t.Errorf("services 应含 4 个独立服务，got %d", len(svcs2))
	}
	first := comps2[0].(map[string]any)
	if first["name"] != "shield" || first["enabled_key"] != "SHIELD_ENABLED" {
		t.Errorf("首个组件元数据不符: %+v", first)
	}
}

// TestRegisterWebUI 验证静态资源托管注册（index.html 根路径 + assets 静态文件）。
// ★ 每请求实时读取：注册后修改文件内容，下一次请求应返回新内容（开发模式热重载语义）。
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

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		s.srv.ServeHTTP(rec, req)
		return rec
	}

	// 根路径返回 index.html
	rec := get("/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / 状态码 = %d, 期望 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, 期望 text/html", ct)
	}
	if got := rec.Body.String(); got != "<!doctype html><html></html>" {
		t.Errorf("GET / body = %q, 期望 index.html 内容", got)
	}

	// 静态文件路由
	rec = get("/assets/js/main.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /assets/js/main.js 状态码 = %d, 期望 200", rec.Code)
	}
	if got := rec.Body.String(); got != "console.log('ok')" {
		t.Errorf("GET /assets/js/main.js body = %q, 期望原内容", got)
	}

	// ★ 实时读取：修改文件后，下一次请求返回新内容（无需重启/重编译）
	fsys["index.html"] = &fstest.MapFile{Data: []byte("<!doctype html><html><body>updated</body></html>")}
	rec = get("/")
	if got := rec.Body.String(); got != "<!doctype html><html><body>updated</body></html>" {
		t.Errorf("修改文件后 GET / body = %q, 期望新内容（每请求实时读取）", got)
	}

	// 空资源注册应成功
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

	// 设置 token（非回环地址）：无 Authorization → 401
	token := "secret"
	s2 := New("0.0.0.0:19527", nil, nil, nil)
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

// switch on/off 应持久化 XXX_ENABLED 到配置中心（写回 .env，重启后按配置恢复）。
func TestHandleSwitchPersist(t *testing.T) {
	cfgMgr, mgr, _ := setup(t)
	// 模拟插件注册 SHIELD_ENABLED（默认 false）。
	var shieldEnabled bool
	if err := cfgMgr.Register(&shieldEnabled, "SHIELD_ENABLED", "false", "测试"); err != nil {
		t.Fatalf("Register SHIELD_ENABLED: %v", err)
	}
	s := New("127.0.0.1:19527", cfgMgr, mgr, nil)
	s.SetAutoEnableMap(map[string]string{"shield": "SHIELD_ENABLED"})

	// switch on → 挂载 + 持久化 SHIELD_ENABLED=true。
	ctx := newCtx(http.MethodPost, PathSwitchOn, `{"name":"shield"}`)
	s.handleSwitchOn(ctx)
	if out := decode(t, ctx); out["ok"] != true {
		t.Fatalf("switch on 期望 ok:true，得到 %v", out)
	}
	if !shieldEnabled {
		t.Fatal("switch on 后 SHIELD_ENABLED 应持久化为 true")
	}
	if cur := findConfigCurrent(t, cfgMgr, "SHIELD_ENABLED"); cur != "true" {
		t.Fatalf("配置中心 SHIELD_ENABLED 应=true，得到 %q", cur)
	}

	// switch off → 摘除 + 持久化 SHIELD_ENABLED=false。
	ctx = newCtx(http.MethodPost, PathSwitchOff, `{"name":"shield"}`)
	s.handleSwitchOff(ctx)
	if out := decode(t, ctx); out["ok"] != true {
		t.Fatalf("switch off 期望 ok:true，得到 %v", out)
	}
	if shieldEnabled {
		t.Fatal("switch off 后 SHIELD_ENABLED 应持久化为 false")
	}
	if cur := findConfigCurrent(t, cfgMgr, "SHIELD_ENABLED"); cur != "false" {
		t.Fatalf("配置中心 SHIELD_ENABLED 应=false，得到 %q", cur)
	}
}

// 不在 autoMap 中的实体（独立组件等）switch on/off 不持久化、不报错。
func TestHandleSwitchNoPersistForNonMapped(t *testing.T) {
	cfgMgr, mgr, _ := setup(t)
	s := New("127.0.0.1:19527", cfgMgr, mgr, nil)
	// 未注入 autoMap（或实体不在映射内）→ persistSwitch 静默跳过。
	s.SetAutoEnableMap(nil)
	ctx := newCtx(http.MethodPost, PathSwitchOn, `{"name":"shield"}`)
	s.handleSwitchOn(ctx)
	if out := decode(t, ctx); out["ok"] != true {
		t.Fatalf("期望 ok:true，得到 %v", out)
	}
	if mgr.List()[0].State != hotswap.StateEnabled {
		t.Fatal("switch on 后 shield 未启用")
	}
}

// findConfigCurrent 从配置中心 List 中取指定键的当前值。
func findConfigCurrent(t *testing.T, cfgMgr conf.Manager, key string) string {
	t.Helper()
	for _, it := range cfgMgr.List() {
		if it.Key == key {
			return it.Current
		}
	}
	t.Fatalf("配置项 %s 未注册", key)
	return ""
}
