package conf

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if cfg.UpstreamTimeout != 18*time.Second {
		t.Errorf("UpstreamTimeout=%v, want 18s", cfg.UpstreamTimeout)
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

// TestLogConfigParsing 新配置项解析（P2 §2.1）：
// 默认值 / 环境变量生效 / 非法 MAX_SIZE 回退 50 / 负数回退 50 / 0=不限制。
func TestLogConfigParsing(t *testing.T) {
	cleanup(t)

	t.Run("默认值", func(t *testing.T) {
		mgr, err := Load(nil)
		if err != nil {
			t.Fatalf("Load err: %v", err)
		}
		cfg := mgr.Current()
		if cfg.LogToFile {
			t.Errorf("LogToFile=%v, want false", cfg.LogToFile)
		}
		if cfg.LogFile != "logs/rocksys.log" {
			t.Errorf("LogFile=%q, want logs/rocksys.log", cfg.LogFile)
		}
		if cfg.LogMaxSize != 50 {
			t.Errorf("LogMaxSize=%d, want 50", cfg.LogMaxSize)
		}
	})

	t.Run("环境变量生效", func(t *testing.T) {
		t.Setenv("ROCKSYS_LOG_TO_FILE", "true")
		t.Setenv("ROCKSYS_LOG_FILE", "/tmp/rocksys-test.log")
		t.Setenv("ROCKSYS_LOG_MAX_SIZE", "128")
		mgr, err := Load(nil)
		if err != nil {
			t.Fatalf("Load err: %v", err)
		}
		cfg := mgr.Current()
		if !cfg.LogToFile {
			t.Errorf("LogToFile=false, want true")
		}
		if cfg.LogFile != "/tmp/rocksys-test.log" {
			t.Errorf("LogFile=%q, want /tmp/rocksys-test.log", cfg.LogFile)
		}
		if cfg.LogMaxSize != 128 {
			t.Errorf("LogMaxSize=%d, want 128", cfg.LogMaxSize)
		}
	})

	t.Run("MAX_SIZE 非法回退 50", func(t *testing.T) {
		t.Setenv("ROCKSYS_LOG_MAX_SIZE", "abc")
		mgr, err := Load(nil)
		if err != nil {
			t.Fatalf("Load err: %v", err)
		}
		if got := mgr.Current().LogMaxSize; got != 50 {
			t.Errorf("LogMaxSize=%d, want 50（非法值回退默认）", got)
		}
	})

	t.Run("MAX_SIZE 负数回退 50", func(t *testing.T) {
		t.Setenv("ROCKSYS_LOG_MAX_SIZE", "-10")
		mgr, err := Load(nil)
		if err != nil {
			t.Fatalf("Load err: %v", err)
		}
		if got := mgr.Current().LogMaxSize; got != 50 {
			t.Errorf("LogMaxSize=%d, want 50（负数视为非法回退默认）", got)
		}
	})

	t.Run("MAX_SIZE 为 0 不限制", func(t *testing.T) {
		t.Setenv("ROCKSYS_LOG_MAX_SIZE", "0")
		mgr, err := Load(nil)
		if err != nil {
			t.Fatalf("Load err: %v", err)
		}
		if got := mgr.Current().LogMaxSize; got != 0 {
			t.Errorf("LogMaxSize=%d, want 0（0=不限制）", got)
		}
	})
}

// TestSetSameValueSkipsWrite 值比较防循环（P2 §2.2/§2.3）：
// 相同值（含 EqualFold 大小写不同）Set 不写盘，.env mtime 不变。
func TestSetSameValueSkipsWrite(t *testing.T) {
	cleanup(t)
	mgr, err := Load(nil)
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	// 先热更一次写盘，确保后续能观测到"跳过写盘"
	if err := mgr.Set("ROCKSYS_LOG_LEVEL", "debug"); err != nil {
		t.Fatalf("Set(debug) err: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	fi, err := os.Stat(".env")
	if err != nil {
		t.Fatalf("stat .env: %v", err)
	}
	before := fi.ModTime().UnixNano()

	time.Sleep(20 * time.Millisecond)
	// 完全相同值与 EqualFold 相同值都不得触发写盘
	for _, v := range []string{"debug", "DEBUG"} {
		if err := mgr.Set("ROCKSYS_LOG_LEVEL", v); err != nil {
			t.Fatalf("Set(%q) err: %v", v, err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	fi, err = os.Stat(".env")
	if err != nil {
		t.Fatalf("stat .env: %v", err)
	}
	if after := fi.ModTime().UnixNano(); before != after {
		t.Errorf("相同值 Set 仍触发写盘：mtime %d → %d", before, after)
	}
}

// TestSetUnregisteredKeyReturnsNil 未注册 key 直接 return nil（M7 行为变更）：不写盘不广播。
func TestSetUnregisteredKeyReturnsNil(t *testing.T) {
	cleanup(t)
	mgr, err := Load(nil)
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	fi, err := os.Stat(".env")
	if err != nil {
		t.Fatalf("stat .env: %v", err)
	}
	before := fi.ModTime().UnixNano()

	time.Sleep(20 * time.Millisecond)
	if err := mgr.Set("ROCKSYS_NOT_EXIST", "x"); err != nil {
		t.Fatalf("Set(未注册 key) err: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	fi, err = os.Stat(".env")
	if err != nil {
		t.Fatalf("stat .env: %v", err)
	}
	if after := fi.ModTime().UnixNano(); before != after {
		t.Errorf("未注册 key Set 仍触发写盘：mtime %d → %d", before, after)
	}
	data, err := os.ReadFile(".env")
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	if strings.Contains(string(data), "ROCKSYS_NOT_EXIST") {
		t.Errorf(".env 不应出现未注册 key:\n%s", data)
	}
}

// TestSyncArgsUpdatesArgs syncArgs 同步命令行参数两种形态：
// `--name=value` 与 `--name value`；两种形态都不存在的 key 不追加。
func TestSyncArgsUpdatesArgs(t *testing.T) {
	cleanup(t)
	mgr, err := Load([]string{
		"--log-level=debug",
		"--admin", "127.0.0.1:19528",
	})
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	mm, ok := mgr.(*confManager)
	if !ok {
		t.Fatalf("mgr 类型 %T，want *confManager", mgr)
	}

	// 形态1：--ROCKSYS_LOG_LEVEL=debug → 替换为 =warn
	mm.syncArgs("ROCKSYS_LOG_LEVEL", "warn")
	found := false
	for _, a := range mm.args {
		if a == "--ROCKSYS_LOG_LEVEL=warn" {
			found = true
		}
		if a == "--ROCKSYS_LOG_LEVEL=debug" {
			t.Errorf("args 中旧值 --ROCKSYS_LOG_LEVEL=debug 未被替换: %v", mm.args)
		}
	}
	if !found {
		t.Errorf("args 中未找到 --ROCKSYS_LOG_LEVEL=warn: %v", mm.args)
	}

	// 形态2：--ROCKSYS_ADMIN value → 替换后续元素
	mm.syncArgs("ROCKSYS_ADMIN", "0.0.0.0:9999")
	found = false
	for i, a := range mm.args {
		if a == "--ROCKSYS_ADMIN" {
			if i+1 < len(mm.args) && mm.args[i+1] == "0.0.0.0:9999" {
				found = true
			}
		}
		if a == "127.0.0.1:19528" {
			t.Errorf("args 中旧值 127.0.0.1:19528 未被替换: %v", mm.args)
		}
	}
	if !found {
		t.Errorf("args 中未找到 --ROCKSYS_ADMIN 0.0.0.0:9999: %v", mm.args)
	}

	// 两种形态都不存在：跳过不追加
	before := len(mm.args)
	mm.syncArgs("ROCKSYS_UPSTREAM", "http://127.0.0.1:8080")
	if len(mm.args) != before {
		t.Errorf("未在 args 中的 key 不应追加：len=%d, want %d", len(mm.args), before)
	}
}

// TestConcurrentSetReloadList 并发 Set（POST 热更）+ reloadFilesLocked（watcher 轮询）
// + List（GET /admin/config/list 常轮询）无数据竞争（P2 §2.6 验收，M2 并发触发用例）。
func TestConcurrentSetReloadList(t *testing.T) {
	cleanup(t)
	mgr, err := Load(nil)
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	mm, ok := mgr.(*confManager)
	if !ok {
		t.Fatalf("mgr 类型 %T，want *confManager", mgr)
	}

	var wg sync.WaitGroup
	// 并发 Set：不同值触发 SetItemValue + publishLocked + syncArgsLocked + UpdateFile
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = mgr.Set("ROCKSYS_LOG_LEVEL", fmt.Sprintf("level-%d", i))
		}(i)
	}
	// 并发 reloadFiles：watcher 轮询重载
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mm.reloadFilesLocked()
		}()
	}
	// 并发 List：管理接口常轮询
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.List()
		}()
	}
	wg.Wait()
}

// TestSyncDefaultFileWritesAllDefaults SyncDefaultFile 将全部已注册配置项（含挂件项）的默认值快照
// 写入工作目录 default.env：文件存在、含挂件项 key、含其默认值、含默认值说明注释，且为默认值形态。
func TestSyncDefaultFileWritesAllDefaults(t *testing.T) {
	cleanup(t)
	mgr, err := Load(nil)
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	var rules string
	if err := mgr.Register(&rules, "REWRITE_RULES", "r1,r2", "测试挂件项"); err != nil {
		t.Fatalf("Register err: %v", err)
	}
	if err := mgr.SyncDefaultFile(); err != nil {
		t.Fatalf("SyncDefaultFile err: %v", err)
	}
	data, err := os.ReadFile("default.env")
	if err != nil {
		t.Fatalf("read default.env: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "REWRITE_RULES") {
		t.Errorf("default.env 缺少挂件项 REWRITE_RULES:\n%s", s)
	}
	if !strings.Contains(s, "r1,r2") {
		t.Errorf("default.env 缺少 REWRITE_RULES 默认值 r1,r2:\n%s", s)
	}
	if !strings.Contains(s, "# The default value is:") {
		t.Errorf("default.env 缺少默认值说明注释:\n%s", s)
	}
	// 默认值快照形态：KEY = 默认值（而非当前值）
	if !strings.Contains(s, "REWRITE_RULES = \"r1,r2\"") {
		t.Errorf("default.env 未按默认值快照形态输出 REWRITE_RULES:\n%s", s)
	}
}
