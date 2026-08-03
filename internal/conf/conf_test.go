package conf

import (
	"os"
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