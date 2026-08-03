// 集成/装配冒烟测试：验证 cmd/rocksys 装配顺序、挂件注册、mq 条件装配与真实代理转发。
package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"rocksys/internal/hotswap"
)

// cleanupEnvFiles 清理 easyconf 在包目录自动创建的 .env / default.env（与 conf 测试一致）。
func cleanupEnvFiles(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_ = os.Remove(".env")
		_ = os.Remove("default.env")
	})
}

// namesOf 提取 mgr.List() 的 name→kind 映射，便于断言。
func namesOf(list []hotswap.Status) map[string]string {
	m := make(map[string]string, len(list))
	for _, s := range list {
		m[s.Name] = s.Kind
	}
	return m
}

func TestSlogLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"INFO", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"verbose", slog.LevelInfo},
	}
	for _, c := range cases {
		if got := slogLevel(c.in); got != c.want {
			t.Errorf("slogLevel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestBuildServer 装配全部挂件：7 个链中间件 + config/registry/object 3 个独立组件，默认不注册 mq。
func TestBuildServer(t *testing.T) {
	cleanupEnvFiles(t)
	t.Setenv("MQ_ENABLED", "false")
	t.Setenv("MQ_DSN", "")

	srv, err := buildServer([]string{
		"--upstream", "http://127.0.0.1:9000",
		"--admin", "127.0.0.1:19529",
	})
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if srv.mqDB != nil {
		t.Fatal("默认配置不应打开 mqDB")
	}

	got := namesOf(srv.mgr.List())
	wantMiddleware := map[string]bool{
		"shield": true, "trace": true, "auth": true,
		"dispatch": true, "script": true, "result": true, "obs": true,
	}
	for name := range wantMiddleware {
		if got[name] != "middleware" {
			t.Errorf("中间件 %s 未注册为 middleware，got kind=%q", name, got[name])
		}
	}
	wantComponent := map[string]bool{"config": true, "registry": true, "object": true}
	for name := range wantComponent {
		if got[name] != "component" {
			t.Errorf("组件 %s 未注册为 component，got kind=%q", name, got[name])
		}
	}
	if _, ok := got["mq"]; ok {
		t.Error("默认配置不应注册 mq 组件")
	}
	// 排空函数已注入：启用→禁用中间件时不应报"未注入排空"错误
	if err := srv.mgr.Enable("shield"); err != nil {
		t.Fatalf("Enable shield: %v", err)
	}
	if err := srv.mgr.Disable("shield"); err != nil {
		t.Fatalf("Disable shield: %v", err)
	}
}

// TestBuildServerMQEnabled MQ_ENABLED=true 且 MQ_DSN 非空时注册 mq 组件并打开 sqlite。
func TestBuildServerMQEnabled(t *testing.T) {
	cleanupEnvFiles(t)
	t.Setenv("MQ_ENABLED", "true")
	t.Setenv("MQ_DSN", ":memory:")

	srv, err := buildServer(nil)
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if srv.mqDB == nil {
		t.Fatal("MQ_ENABLED=true 时应打开 sqlite 并持有 db")
	}
	got := namesOf(srv.mgr.List())
	if got["mq"] != "component" {
		t.Errorf("mq 组件未注册，got=%v", got)
	}
	_ = srv.mqDB.Close()
}

// TestBuildServerMQDisabled MQ_ENABLED=true 但 DSN 为空（或缺省）时跳过 mq 注册，避免无 DSN 崩溃。
func TestBuildServerMQDisabled(t *testing.T) {
	cleanupEnvFiles(t)
	t.Setenv("MQ_ENABLED", "true")
	t.Setenv("MQ_DSN", "")

	srv, err := buildServer(nil)
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if srv.mqDB != nil {
		t.Fatal("DSN 为空时不应打开 sqlite")
	}
	if _, ok := namesOf(srv.mgr.List())["mq"]; ok {
		t.Error("DSN 为空时不应注册 mq 组件")
	}
}

// freePort 获取一个随机空闲端口（127.0.0.1）。
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// waitReady 轮询等待 addr 可连接（上限 5s）。
func waitReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("服务 %s 未在 5s 内就绪", addr)
}

// TestSmokeProxy 真实监听冒烟：主代理转发到上游 + admin API 可访问 + 优雅停机清理。
func TestSmokeProxy(t *testing.T) {
	cleanupEnvFiles(t)
	t.Setenv("MQ_ENABLED", "false")
	t.Setenv("MQ_DSN", "")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from upstream"))
	}))
	defer upstream.Close()

	listenAddr := freePort(t)
	adminAddr := freePort(t)
	srv, err := buildServer([]string{
		"--listen", listenAddr,
		"--admin", adminAddr,
		"--upstream", upstream.URL,
	})
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() { _ = srv.eng.ListenAndServe() }()
	go func() { _ = srv.adminSrv.ListenAndServe() }()
	waitReady(t, listenAddr)
	waitReady(t, adminAddr)

	// 主代理转发：上游响应原样返回
	resp, err := http.Get("http://" + listenAddr + "/")
	if err != nil {
		t.Fatalf("proxy get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hello from upstream" {
		t.Fatalf("proxy 响应不符: code=%d body=%q", resp.StatusCode, body)
	}

	// admin API 可访问（switch/list 返回挂件列表）
	adminResp, err := http.Get("http://" + adminAddr + "/admin/switch/list")
	if err != nil {
		t.Fatalf("admin get: %v", err)
	}
	adminBody, _ := io.ReadAll(adminResp.Body)
	_ = adminResp.Body.Close()
	if adminResp.StatusCode != http.StatusOK {
		t.Fatalf("admin 状态码=%d body=%s", adminResp.StatusCode, adminBody)
	}
	if !strings.Contains(string(adminBody), "shield") || !strings.Contains(string(adminBody), "config") {
		t.Fatalf("admin switch/list 缺少挂件: %s", adminBody)
	}

	// 优雅停机：主代理 + admin 排空
	if err := srv.eng.Shutdown(ctx); err != nil {
		t.Fatalf("engine shutdown: %v", err)
	}
	if err := srv.adminSrv.Shutdown(ctx); err != nil {
		t.Fatalf("admin shutdown: %v", err)
	}
	if err := srv.mgr.Shutdown(ctx); err != nil {
		t.Fatalf("hotswap shutdown: %v", err)
	}
	if err := srv.cfgMgr.Shutdown(ctx); err != nil {
		t.Fatalf("conf shutdown: %v", err)
	}
}
