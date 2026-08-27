package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runWithServer 在内存中启动模拟 admin API 服务器，捕获请求并返回记录，
// 以注入 baseURL 的方式运行 CLI 分发逻辑。此测试直接调用命令处理函数，
// 避开 os.Exit（通过错误返回表达失败）。
func runWithServer(t *testing.T, handler http.HandlerFunc, args ...string) error {
	t.Helper()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	c, err := newClient(srv.URL, "")
	if err != nil {
		return err
	}
	dispatch := func(args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("缺子命令")
		}
		switch args[0] {
		case "switch":
			return runSwitch(c, args[1:])
		case "config":
			return runConfig(c, args[1:])
		case "script":
			return runScript(c, args[1:])
		default:
			return fmt.Errorf("未知子命令：%s", args[0])
		}
	}
	return dispatch(args)
}

func TestSwitchOnOff(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	h := func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		fmt.Fprint(w, `{"ok":true}`)
	}

	if err := runWithServer(t, h, "switch", "on", "shield"); err != nil {
		t.Fatalf("switch on 失败: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/admin/switch/on" {
		t.Fatalf("期望 POST /admin/switch/on，实际 %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(gotBody, `"name":"shield"`) {
		t.Fatalf("请求体不含组件名: %s", gotBody)
	}

	if err := runWithServer(t, h, "switch", "off", "shield"); err != nil {
		t.Fatalf("switch off 失败: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/admin/switch/off" {
		t.Fatalf("期望 POST /admin/switch/off，实际 %s %s", gotMethod, gotPath)
	}
}

func TestSwitchList(t *testing.T) {
	var gotMethod, gotPath string
	h := func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `[{"name":"shield","state":"disabled"}]`)
	}
	if err := runWithServer(t, h, "switch", "list"); err != nil {
		t.Fatalf("switch list 失败: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/admin/switch/list" {
		t.Fatalf("期望 GET /admin/switch/list，实际 %s %s", gotMethod, gotPath)
	}
}

func TestConfigGet(t *testing.T) {
	var gotMethod, gotPath string
	h := func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"listen":":8080","upstream":"http://127.0.0.1:9000"}`)
	}
	if err := runWithServer(t, h, "config", "get"); err != nil {
		t.Fatalf("config get 失败: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/admin/config" {
		t.Fatalf("期望 GET /admin/config，实际 %s %s", gotMethod, gotPath)
	}
}

func TestConfigSet(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	h := func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		fmt.Fprint(w, `{"ok":true}`)
	}
	if err := runWithServer(t, h, "config", "set", "ROCKSYS_UPSTREAM", "http://127.0.0.1:9001"); err != nil {
		t.Fatalf("config set 失败: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/admin/config" {
		t.Fatalf("期望 PUT /admin/config，实际 %s %s", gotMethod, gotPath)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(gotBody), &m); err != nil {
		t.Fatalf("请求体非法 JSON: %v", err)
	}
	if m["ROCKSYS_UPSTREAM"] != "http://127.0.0.1:9001" {
		t.Fatalf("请求体 key 或值错误: %v", m)
	}
}

func TestScriptPublishRollback(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	h := func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		fmt.Fprint(w, `{"ok":true,"version":1}`)
	}
	if err := runWithServer(t, h, "script", "publish", "testdata/rule.lua"); err != nil {
		t.Fatalf("script publish 失败: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/admin/script/publish" {
		t.Fatalf("期望 POST /admin/script/publish，实际 %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(gotBody, `"source":"return true\n"`) {
		t.Fatalf("publish 请求体未携带脚本 source: %s", gotBody)
	}

	if err := runWithServer(t, h, "script", "rollback"); err != nil {
		t.Fatalf("script rollback 失败: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/admin/script/rollback" {
		t.Fatalf("期望 POST /admin/script/rollback，实际 %s %s", gotMethod, gotPath)
	}
}

func TestHTTPErrorStatusCode(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}
	if err := runWithServer(t, h, "config", "get"); err == nil {
		t.Fatal("期望非 2xx 时返回错误，实际返回 nil")
	}
}

func TestScriptPublishMissingFile(t *testing.T) {
	c, _ := newClient("http://127.0.0.1:9", "")
	if err := runScript(c, []string{"publish", "不存在.lua"}); err == nil {
		t.Fatal("期望文件不存在时报错，实际返回 nil")
	}
}
