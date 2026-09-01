// Package script 单测：脚本发布/回滚/执行、沙箱拦截、Admin 端点。
package script

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iotames/easyserver/httpsvr"

	"rocksys/internal/chain"
	"rocksys/internal/dataflow"
	"rocksys/internal/hotswap"
)

// newChainCtx 手工构造 chain.Context（用 httptest.ResponseRecorder 充当 ResponseWriter）。
func newChainCtx(method, path string) (*chain.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, path, nil)
	df := dataflow.New(httpsvr.NewDataFlow(), req)
	rec := httptest.NewRecorder()
	return &chain.Context{W: rec, R: req, DF: df, RespW: rec}, rec
}

func TestNewImplementsInterfaces(t *testing.T) {
	var _ chain.Middleware = New(0, nil)
	var _ hotswap.MiddlewareLifecycle = New(0, nil)
}

func TestEngine_SlotName(t *testing.T) {
	e := New(0, nil)
	if e.Name() != "script" {
		t.Errorf("Name()=%q, want script", e.Name())
	}
	if e.Slot() != chain.Middle {
		t.Errorf("Slot()=%v, want Middle", e.Slot())
	}
}

func TestHandle_BlockByPath(t *testing.T) {
	e := New(0, nil)
	// 发布脚本：路径 /block 时直接 403 响应（§15 验收场景）。
	_, err := e.Publish("test", `if req.path() == "/block" then ctx.respond(403, "blocked") end`)
	if err != nil {
		t.Fatalf("Publish err: %v", err)
	}

	// 命中 /block → 返回 false（中断链）、响应 403 blocked。
	ctx, rec := newChainCtx(http.MethodGet, "/block")
	if e.Handle(ctx) {
		t.Fatal("Handle 应返回 false（脚本已响应并中断链）")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status=%d, want 403", rec.Code)
	}
	if rec.Body.String() != "blocked" {
		t.Errorf("body=%q, want blocked", rec.Body.String())
	}

	// 未命中路径 → 返回 true（继续转发），不写响应。
	ctx2, rec2 := newChainCtx(http.MethodGet, "/ok")
	if !e.Handle(ctx2) {
		t.Fatal("Handle 应返回 true（未命中继续转发）")
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("未命中不应写响应体, got %q", rec2.Body.String())
	}
}

func TestHandle_SetTarget(t *testing.T) {
	e := New(0, nil)
	_, err := e.Publish("s", `if req.path() == "/api" then ctx.set_target("http://svc:9001") end`)
	if err != nil {
		t.Fatalf("Publish err: %v", err)
	}
	ctx, _ := newChainCtx(http.MethodGet, "/api")
	if !e.Handle(ctx) {
		t.Fatal("Handle 应返回 true（仅改 target 未响应）")
	}
	if got := ctx.DF.Target(); got != "http://svc:9001" {
		t.Errorf("Target()=%q, want http://svc:9001", got)
	}
}

func TestHandle_ReqRead(t *testing.T) {
	e := New(0, nil)
	_, err := e.Publish("t", `
        if req.method() == "DELETE" then ctx.respond(405, "no delete") end
    `)
	if err != nil {
		t.Fatalf("Publish err: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/x", nil)
	df := dataflow.New(httpsvr.NewDataFlow(), req)
	rec := httptest.NewRecorder()
	ctx := &chain.Context{W: rec, R: req, DF: df, RespW: rec}
	if e.Handle(ctx) {
		t.Fatal("DELETE 应被拦截返回 false")
	}
	if rec.Code != http.StatusMethodNotAllowed || rec.Body.String() != "no delete" {
		t.Errorf("响应=%d %q, want 405 no delete", rec.Code, rec.Body.String())
	}
}

func TestPublish_RejectSandbox(t *testing.T) {
	e := New(0, nil)
	malicious := []string{
		`os.execute("rm -rf /")`,
		`require("os")`,
		`require 'io'`,
		`file.open("/etc/passwd")`,
		`net.http.get(...)`,
		`local f = ffi.parse`,
	}
	for _, src := range malicious {
		if _, err := e.Publish("bad", src); err == nil {
			t.Errorf("恶意脚本未被拦截: %q", src)
		}
	}
	// "profile."/"planet.net" 等英文单词不应误伤（防误判词边界）。
	for _, src := range []string{
		`local x = "profile."`,
		`local a = 'planet.net'`,
	} {
		if _, err := e.Publish("ok", src); err != nil {
			t.Errorf("合法脚本被误杀: %q → %v", src, err)
		}
	}
}

func TestSandbox_RejectRequireDangerous(t *testing.T) {
	e := New(0, nil)
	_, err := e.Publish("x", `local os = require("os")`)
	if err == nil {
		t.Fatal("require(\"os\") 应被沙箱拦截")
	}
}

func TestPublish_VersionMonotonic(t *testing.T) {
	e := New(0, nil)
	v1, err := e.Publish("r", `ctx.respond(403, "v1")`)
	if err != nil {
		t.Fatalf("Publish v1 err: %v", err)
	}
	v2, err := e.Publish("r", `ctx.respond(403, "v2")`)
	if err != nil {
		t.Fatalf("Publish v2 err: %v", err)
	}
	if v1 != 1 || v2 != 2 {
		t.Errorf("版本号单调递增失败: v1=%d v2=%d, want 1 2", v1, v2)
	}
}

func TestRollback_RemoveAndRestore(t *testing.T) {
	e := New(0, nil)
	// 发布 /block 阻断 v1。
	_, err := e.Publish("r", `if req.path() == "/block" then ctx.respond(403, "b1") end`)
	if err != nil {
		t.Fatalf("Publish v1 err: %v", err)
	}
	// 再发布 v2（同样阻断但响应体不同）→ 当前生效 v2。
	if _, err = e.Publish("r", `if req.path() == "/block" then ctx.respond(403, "b2") end`); err != nil {
		t.Fatalf("Publish v2 err: %v", err)
	}
	ctx, rec := newChainCtx(http.MethodGet, "/block")
	if e.Handle(ctx) {
		t.Fatal("v2 应阻断（Handle 应返回 false）")
	}
	if rec.Body.String() != "b2" {
		t.Errorf("v2 生效时 body=%q, want b2", rec.Body.String())
	}

	// 回滚到版本 1 → 重新生效 v1。
	if err = e.Rollback("r", 1); err != nil {
		t.Fatalf("Rollback to v1 err: %v", err)
	}
	ctx2, rec2 := newChainCtx(http.MethodGet, "/block")
	if e.Handle(ctx2) {
		t.Fatal("回滚 v1 后应阻断（Handle 应返回 false）")
	}
	if rec2.Body.String() != "b1" {
		t.Errorf("回滚 v1 后 body=%q, want b1", rec2.Body.String())
	}

	// 回滚到 version 0 → 移除脚本 → 不再阻断。
	if err = e.Rollback("r", 0); err != nil {
		t.Fatalf("Rollback remove err: %v", err)
	}
	ctx3, _ := newChainCtx(http.MethodGet, "/block")
	if !e.Handle(ctx3) {
		t.Fatal("移除后不应再阻断")
	}
}

func TestRollback_UnknownError(t *testing.T) {
	e := New(0, nil)
	if _, err := e.Publish("r", `print("hi")`); err != nil {
		t.Fatalf("Publish err: %v", err)
	}
	if err := e.Rollback("r", 99); err == nil {
		t.Fatal("回滚到不存在版本应返回 error")
	}
	if err := e.Rollback("nope", 0); err == nil {
		t.Fatal("回滚不存在的脚本应返回 error")
	}
}

func TestHandle_TimeoutInterruptsLoop(t *testing.T) {
	e := New(5*time.Millisecond, nil)
	if _, err := e.Publish("loop", `while true do end`); err != nil {
		t.Fatalf("Publish err: %v", err)
	}
	ctx, _ := newChainCtx(http.MethodGet, "/x")
	start := time.Now()
	// 死循环应被超时中断（返回 true 继续，不 hang）。
	if !e.Handle(ctx) {
		t.Fatal("超时中断后应继续转发返回 true")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("死循环未被超时中断，耗时 %v", elapsed)
	}
}

// —— Admin Handler 单测 ——

// newMgr 构造内置 script 引擎的 hotswap.Manager（便于 AdminHandler 通过 GetMiddleware 取回）。
func newMgr(t *testing.T) *hotswap.Manager {
	t.Helper()
	ch := chain.New()
	mgr := hotswap.NewManager(ch, nil)
	mgr.RegisterMiddleware(New(0, nil))
	return mgr
}

func doAdmin(h func(http.ResponseWriter, *http.Request), body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/script/publish", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func decodeResp(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应非 JSON: %v, body=%q", err, rec.Body.String())
	}
	return m
}

func TestAdmin_PublishAndRollback(t *testing.T) {
	admin := NewAdminHandler(newMgr(t))

	// 发布。
	rec := doAdmin(admin.Publish, `{"name":"rule1","source":"if req.path() == \"/block\" then ctx.respond(403, \"blocked\") end"}`)
	m := decodeResp(t, rec)
	if rec.Code != http.StatusOK || m["ok"] != true {
		t.Fatalf("Publish 失败: code=%d resp=%v", rec.Code, m)
	}
	if ver, ok := m["version"].(float64); !ok || ver != 1 {
		t.Errorf("version=%v, want 1", m["version"])
	}

	// 引擎生效：/block 被拦截。
	eng := admin.engine()
	ctx, _ := newChainCtx(http.MethodGet, "/block")
	if eng.Handle(ctx) {
		t.Fatal("发布后 /block 应被拦截")
	}

	// 回滚移除。
	rec = doAdmin(admin.Rollback, `{"name":"rule1","version":0}`)
	m = decodeResp(t, rec)
	if rec.Code != http.StatusOK || m["ok"] != true {
		t.Fatalf("Rollback 失败: code=%d resp=%v", rec.Code, m)
	}
	ctx2, _ := newChainCtx(http.MethodGet, "/block")
	if !eng.Handle(ctx2) {
		t.Fatal("回滚移除后 /block 不应再被拦截")
	}
}

func TestAdmin_PublishRejectSandbox(t *testing.T) {
	admin := NewAdminHandler(newMgr(t))
	rec := doAdmin(admin.Publish, `{"name":"bad","source":"os.execute(\"rm\")"}`)
	m := decodeResp(t, rec)
	if rec.Code != http.StatusBadRequest || m["ok"] != false {
		t.Fatalf("恶意脚本发布应失败: code=%d resp=%v", rec.Code, m)
	}
	if m["error"] == "" {
		t.Error("应返回 error 说明沙箱拦截原因")
	}
}

func TestAdmin_InvalidBody(t *testing.T) {
	admin := NewAdminHandler(newMgr(t))
	rec := doAdmin(admin.Publish, `not-json`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法 body 应 400, got %d", rec.Code)
	}
}

func TestAdmin_EngineNotRegistered(t *testing.T) {
	mgr := hotswap.NewManager(chain.New(), nil)
	admin := NewAdminHandler(mgr) // 未注册 script 引擎。
	rec := doAdmin(admin.Publish, `{"name":"x","source":"print(1)"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("未注册引擎应 500, got %d", rec.Code)
	}
	if admin.engine() != nil {
		t.Error("未注册时 engine() 应返回 nil")
	}
}

func TestListScripts(t *testing.T) {
	e := New(0, nil)
	if got := e.ListScripts(); len(got) != 0 {
		t.Fatalf("未发布时 ListScripts 应为空, got %v", got)
	}

	v1, err := e.Publish("rule1", `print("v1")`)
	if err != nil {
		t.Fatalf("Publish v1 err: %v", err)
	}
	v2, err := e.Publish("rule1", `print("v2")`)
	if err != nil {
		t.Fatalf("Publish v2 err: %v", err)
	}
	if _, err = e.Publish("rule2", `print("x")`); err != nil {
		t.Fatalf("Publish rule2 err: %v", err)
	}

	infos := e.ListScripts()
	if len(infos) != 2 {
		t.Fatalf("ListScripts 数量=%d, want 2", len(infos))
	}
	var r1, r2 *ScriptInfo
	for i := range infos {
		switch infos[i].Name {
		case "rule1":
			r1 = &infos[i]
		case "rule2":
			r2 = &infos[i]
		}
	}
	if r1 == nil || r2 == nil {
		t.Fatalf("缺少脚本: %+v", infos)
	}
	if r1.CurrentVersion != v2 || r2.CurrentVersion != 3 {
		t.Errorf("CurrentVersion 不符: rule1=%d(want %d) rule2=%d(want 3, 全局单调版本号)", r1.CurrentVersion, v2, r2.CurrentVersion)
	}
	if len(r1.Versions) != 2 {
		t.Fatalf("rule1 版本历史=%d, want 2", len(r1.Versions))
	}
	if r1.Versions[0].Version != v1 || r1.Versions[1].Version != v2 {
		t.Errorf("rule1 版本序不符: %+v", r1.Versions)
	}
	if r1.Versions[0].PublishedAt == "" {
		t.Error("published_at 不应为空")
	}
}

func TestAdmin_List(t *testing.T) {
	admin := NewAdminHandler(newMgr(t))
	// 未发布：空列表。
	req := httptest.NewRequest(http.MethodGet, "/admin/script/list", nil)
	rec := httptest.NewRecorder()
	admin.List(rec, req)
	m := decodeResp(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("List 未返回 200: %d", rec.Code)
	}
	scripts, ok := m["scripts"].([]any)
	if !ok || len(scripts) != 0 {
		t.Fatalf("空列表不符: %v", m)
	}

	// 发布两条后：列表含 1 个脚本、2 个版本。
	pubReq := func(src string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/admin/script/publish", strings.NewReader(`{"name":"r","source":"`+src+`"}`))
	}
	admin.Publish(recWriter(), pubReq("print(1)"))
	admin.Publish(recWriter(), pubReq("print(2)"))
	req = httptest.NewRequest(http.MethodGet, "/admin/script/list", nil)
	rec = httptest.NewRecorder()
	admin.List(rec, req)
	m = decodeResp(t, rec)
	scripts = m["scripts"].([]any)
	if len(scripts) != 1 {
		t.Fatalf("脚本数量=%d, want 1", len(scripts))
	}
	first := scripts[0].(map[string]any)
	if first["name"] != "r" || first["current_version"].(float64) != 2 {
		t.Errorf("列表内容不符: %v", first)
	}
	if vers := first["versions"].([]any); len(vers) != 2 {
		t.Errorf("版本数=%d, want 2", len(vers))
	}
}

// recWriter 返回用于 Publish 的 ResponseRecorder（忽略响应内容）。
func recWriter() *httptest.ResponseRecorder { return httptest.NewRecorder() }
