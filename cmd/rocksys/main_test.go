// 集成/装配冒烟测试：验证 cmd/rocksys 装配顺序、挂件注册、mq 条件装配与真实代理转发。
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"rocksys/internal/hotswap"
)

// cleanupEnvFiles 清理 easyconf 在包目录自动创建的工作目录 .env / default.env，以及
// TestBuildServerMQEnabled 默认 DB_DSN 建库残留的 rocksys.db*（配置中心红线：运行时文件不得残留源码树）。
func cleanupEnvFiles(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_ = os.Remove(".env")
		_ = os.Remove("default.env")
		_ = os.Remove("rocksys.db")
		_ = os.Remove("rocksys.db-shm")
		_ = os.Remove("rocksys.db-wal")
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

// TestPrintVersion 验证 --version 输出：Version/BuildTime 为注入值，GoVersion 为编译时版本。
func TestPrintVersion(t *testing.T) {
	oldV, oldB, oldG := Version, BuildTime, GoVersion
	Version, BuildTime = "v9.9.9", "2026-01-02T03:04:05+08:00"
	GoVersion = "go1.26.5"
	defer func() { Version, BuildTime, GoVersion = oldV, oldB, oldG }()

	// 捕获 stdout
	oldOut := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	printVersion()
	_ = w.Close()
	os.Stdout = oldOut
	out, _ := io.ReadAll(r)
	_ = r.Close()

	want := "Version: v9.9.9\nBuildTime: 2026-01-02T03:04:05+08:00\nGoVersion: go1.26.5\n"
	if string(out) != want {
		t.Fatalf("printVersion 输出不匹配:\n got %q\nwant %q", out, want)
	}
}

// TestWaitForQuitOrFail 验证停机决策（fail-fast 红线）：
// 监听失败（如 EADDRINUSE）→ 返回 error，调用方将 log.Error + os.Exit(1)；
// 收到停机信号 → 返回 nil，进入优雅停机。
func TestWaitForQuitOrFail(t *testing.T) {
	// 分支一：监听失败 → 必须返回 error（fail-fast）。
	quit := make(chan os.Signal, 1)
	errCh := make(chan error, 1)
	errCh <- errors.New("bind: address already in use")
	if err := waitForQuitOrFail(quit, errCh); err == nil {
		t.Error("监听失败时应返回 error（fail fast）")
	}

	// 分支二：收到停机信号 → 返回 nil，进入优雅停机。
	quit2 := make(chan os.Signal, 1)
	errCh2 := make(chan error, 1)
	quit2 <- syscall.SIGTERM
	if err := waitForQuitOrFail(quit2, errCh2); err != nil {
		t.Errorf("收到停机信号应返回 nil，got %v", err)
	}
}

// TestBuildServer 装配全部挂件：7 个链中间件 + config/registry/object 3 个独立组件，默认不注册 mq。
func TestBuildServer(t *testing.T) {
	cleanupEnvFiles(t)
	t.Setenv("MQ_ENABLED", "false")

	srv, err := buildServer([]string{
		"--upstream", "http://127.0.0.1:9000",
		"--admin", "127.0.0.1:19529",
	})
	if err != nil {
		t.Fatalf("buildServer: %v", err)
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

	// 配置中心红线：default.env 为全量默认值快照，须包含 DB_DSN 与本次收敛的全部新增注册项。
	def, err := os.ReadFile("default.env")
	if err != nil {
		t.Fatalf("read default.env: %v", err)
	}
	s := string(def)
	for _, key := range []string{"DB_DSN", "SCRIPT_TIMEOUT", "REGISTRY_ADDR", "REGISTRY_TTL", "OBJECT_BASE_DIR", "MQ_ENABLED", "MQ_POLL_INTERVAL", "MQ_MAX_RETRIES", "MQ_BASE_BACKOFF", "MQ_CONSUMER_BASE_URL"} {
		if !strings.Contains(s, key) {
			t.Errorf("default.env 应包含全量默认值 %s:\n%s", key, s)
		}
	}
	if srv.dataDB != nil {
		_ = srv.dataDB.Close() // 关闭 sqlite 连接，释放 rocksys.db 句柄，供 cleanupEnvFiles 删除
	}
}

// TestBuildServerMQEnabled MQ_ENABLED=true 时注册 mq 组件，复用 dataDB 业务库连接：
// outbox 表应建在 dataDB（sqlite）中，脚本源与连接同方言。
func TestBuildServerMQEnabled(t *testing.T) {
	cleanupEnvFiles(t)
	t.Setenv("MQ_ENABLED", "true")

	srv, err := buildServer(nil)
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if srv.dataDB == nil {
		t.Fatal("MQ 复用 dataDB，dataDB 应已就绪")
	}
	got := namesOf(srv.mgr.List())
	if got["mq"] != "component" {
		t.Errorf("mq 组件未注册，got=%v", got)
	}
	// 启用 mq：outbox 表应建在 dataDB 业务库（同一 *sql.DB 连接）。
	if err := srv.mgr.Enable("mq"); err != nil {
		t.Fatalf("Enable mq: %v", err)
	}
	var n int
	if err := srv.dataDB.EasyDB().GetSqlDB().
		QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='outbox'").Scan(&n); err != nil {
		t.Fatalf("查询 outbox 表失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("outbox 表应建在 dataDB 中，got count=%d", n)
	}
	if err := srv.mgr.Disable("mq"); err != nil {
		t.Fatalf("Disable mq: %v", err)
	}
	if srv.dataDB != nil {
		_ = srv.dataDB.Close() // 关闭 sqlite 连接，释放 rocksys.db 句柄，供 cleanupEnvFiles 删除
	}
}

// TestBuildServerMQDataDBMissing MQ_ENABLED=true 但数据访问层未就绪（DB_DSN 无效）时，
// 跳过 mq 注册（组件降级），底座照常启动。
func TestBuildServerMQDataDBMissing(t *testing.T) {
	cleanupEnvFiles(t)
	t.Setenv("MQ_ENABLED", "true")
	t.Setenv("DB_DSN", "/nonexistent-dir-mqtest/rocksys.db") // 使 db.Open 失败 → dataDB=nil

	srv, err := buildServer(nil)
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if srv.dataDB != nil {
		t.Fatal("DB_DSN 无效时 dataDB 应为 nil")
	}
	if _, ok := namesOf(srv.mgr.List())["mq"]; ok {
		t.Error("数据访问层未就绪时不应注册 mq 组件")
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
	if srv.dataDB != nil {
		_ = srv.dataDB.Close() // 关闭 sqlite 连接，释放 rocksys.db 句柄，供 cleanupEnvFiles 删除
	}
}
