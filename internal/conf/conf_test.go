package conf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cleanup 清理测试期间 easyconf 自动创建的 .env / default.env
func cleanup(t *testing.T) {
	t.Cleanup(func() {
		_ = os.Remove(".env")
		_ = os.Remove("default.env")
	})
}

// TestLoadDefaults 默认值正确
func TestLoadDefaults(t *testing.T) {
	cleanup(t)
	mgr, err := Load(nil)
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	cfg := mgr.Current()
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr=%q, want :8080", cfg.ListenAddr)
	}
	if cfg.DefaultUpstream != "http://127.0.0.1:8080" {
		t.Errorf("DefaultUpstream=%q", cfg.DefaultUpstream)
	}
	if cfg.AdminAddr != "127.0.0.1:19527" {
		t.Errorf("AdminAddr=%q", cfg.AdminAddr)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel=%q", cfg.LogLevel)
	}
	if cfg.ConfigFile != "" {
		t.Errorf("ConfigFile=%q, want empty", cfg.ConfigFile)
	}
	if cfg.UpstreamTimeout != 5*time.Second {
		t.Errorf("UpstreamTimeout=%v, want 5s", cfg.UpstreamTimeout)
	}
}

// TestLoadCommandLineArgs 命令行参数生效 + UpstreamTimeout 单位换算
func TestLoadCommandLineArgs(t *testing.T) {
	cleanup(t)
	mgr, err := Load([]string{
		"--listen=:9090",
		"--upstream", "http://127.0.0.1:9000",
		"--timeout", "7",
		"--admin", "127.0.0.1:19528",
		"--log-level=debug",
	})
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	cfg := mgr.Current()
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr=%q, want :9090", cfg.ListenAddr)
	}
	if cfg.DefaultUpstream != "http://127.0.0.1:9000" {
		t.Errorf("DefaultUpstream=%q", cfg.DefaultUpstream)
	}
	if cfg.AdminAddr != "127.0.0.1:19528" {
		t.Errorf("AdminAddr=%q", cfg.AdminAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel=%q", cfg.LogLevel)
	}
	// 5 → 5s 换算正确（防止 5 被当成 5ns）
	if cfg.UpstreamTimeout != 7*time.Second {
		t.Errorf("UpstreamTimeout=%v, want 7s", cfg.UpstreamTimeout)
	}
}

// TestLoadEnv 环境变量生效
func TestLoadEnvVar(t *testing.T) {
	cleanup(t)
	t.Setenv("ROCKSYS_LISTEN", ":7777")
	t.Setenv("ROCKSYS_LOG_LEVEL", "warn")
	mgr, err := Load(nil)
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	cfg := mgr.Current()
	if cfg.ListenAddr != ":7777" {
		t.Errorf("ListenAddr=%q, want :7777", cfg.ListenAddr)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel=%q, want warn", cfg.LogLevel)
	}
}

// TestLoadEnvOverridesConfigFile 优先级契约：环境变量 > --config 指定文件 > .env
func TestLoadEnvOverridesConfigFile(t *testing.T) {
	cleanup(t)
	cfgPath := filepath.Join(t.TempDir(), "app.env")
	content := "ROCKSYS_UPSTREAM = \"http://127.0.0.1:8080\"\nROCKSYS_LISTEN = \":8080\"\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCKSYS_UPSTREAM", "http://127.0.0.1:9001")
	t.Setenv("ROCKSYS_LISTEN", ":9090")

	mgr, err := Load([]string{"--config", cfgPath})
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	cfg := mgr.Current()
	if cfg.DefaultUpstream != "http://127.0.0.1:9001" {
		t.Errorf("DefaultUpstream=%q, want 9001（环境变量应覆盖 config 文件）", cfg.DefaultUpstream)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr=%q, want :9090", cfg.ListenAddr)
	}
}

// TestRegisterKeepsEnvPriority 挂件 Register 后不得破坏优先级契约：环境变量 > .env。
// 回归：Register 曾以 .env 后置覆盖环境变量，导致默认 upstream 被静默改回 .env 值。
func TestRegisterKeepsEnvPriority(t *testing.T) {
	cleanup(t)
	if err := os.WriteFile(".env", []byte("ROCKSYS_UPSTREAM = \"http://127.0.0.1:8080\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCKSYS_UPSTREAM", "http://127.0.0.1:9001")

	mgr, err := Load(nil)
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if got := mgr.Current().DefaultUpstream; got != "http://127.0.0.1:9001" {
		t.Fatalf("Load 后 upstream=%q, want 9001", got)
	}

	var rules string
	if err := mgr.Register(&rules, "REWRITE_RULES", "", "测试挂件项"); err != nil {
		t.Fatalf("Register err: %v", err)
	}
	if got := mgr.Current().DefaultUpstream; got != "http://127.0.0.1:9001" {
		t.Errorf("Register 后 upstream=%q, want 9001（环境变量应覆盖 .env）", got)
	}
}

// TestSetPersistsToEnvFile 热更即持久化（第一原则）：Set 立即生效并写回 .env，重启后保留。
func TestSetPersistsToEnvFile(t *testing.T) {
	cleanup(t)
	mgr, err := Load(nil)
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if err := mgr.Set("ROCKSYS_LOG_LEVEL", "debug"); err != nil {
		t.Fatalf("Set err: %v", err)
	}
	if got := mgr.Current().LogLevel; got != "debug" {
		t.Errorf("Current().LogLevel=%q, want debug", got)
	}
	data, err := os.ReadFile(".env")
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	if s := string(data); !strings.Contains(s, "ROCKSYS_LOG_LEVEL") || !strings.Contains(s, "debug") {
		t.Errorf(".env 未持久化 ROCKSYS_LOG_LEVEL=debug:\n%s", s)
	}
}

// TestSetPersistsToConfigFile --config 场景：Set 写回 configFile（而非 .env），重启后保留。
func TestSetPersistsToConfigFile(t *testing.T) {
	cleanup(t)
	cfgPath := filepath.Join(t.TempDir(), "app.env")
	content := "ROCKSYS_UPSTREAM = \"http://127.0.0.1:8080\"\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	mgr, err := Load([]string{"--config", cfgPath})
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if err := mgr.Set("ROCKSYS_LISTEN", ":9090"); err != nil {
		t.Fatalf("Set err: %v", err)
	}
	if got := mgr.Current().ListenAddr; got != ":9090" {
		t.Errorf("Current().ListenAddr=%q, want :9090", got)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read configFile: %v", err)
	}
	if !strings.Contains(string(data), ":9090") {
		t.Errorf("configFile 未持久化 ROCKSYS_LISTEN=:9090:\n%s", data)
	}
}