package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rocksys/internal/hotswap"
)

// TestFileStoreSetGet FileStore Set/Get 往返（含重启恢复）
func TestFileStoreSetGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.env")
	fs := NewFileStore(nil, path)

	if got, _ := fs.Get("key1"); got != "" {
		t.Errorf("初始值=%q，want 空串（默认值）", got)
	}
	if err := fs.Set("key1", "v1"); err != nil {
		t.Fatalf("Set key1 err: %v", err)
	}
	if err := fs.Set("key2", "v2"); err != nil {
		t.Fatalf("Set key2 err: %v", err)
	}
	if got, _ := fs.Get("key1"); got != "v1" {
		t.Errorf("key1=%q，want v1", got)
	}
	if got, _ := fs.Get("key2"); got != "v2" {
		t.Errorf("key2=%q，want v2", got)
	}

	// 重建实例（模拟进程重启）→ 从文件恢复
	fs2 := NewFileStore(nil, path)
	if got, _ := fs2.Get("key1"); got != "v1" {
		t.Errorf("重启后 key1=%q，want v1", got)
	}
	if got, _ := fs2.Get("key2"); got != "v2" {
		t.Errorf("重启后 key2=%q，want v2", got)
	}
}

// TestFileStoreAtomicPersist 原子落盘：写后文件存在且内容正确、无残留临时文件
func TestFileStoreAtomicPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.env")
	fs := NewFileStore(nil, path)

	if err := fs.Set("upstream", "http://127.0.0.1:9090"); err != nil {
		t.Fatalf("Set err: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("写后文件应存在: %v", err)
	}
	if !strings.Contains(string(content), "upstream=http://127.0.0.1:9090") {
		t.Errorf("文件内容=%q，want 含 upstream=http://127.0.0.1:9090", content)
	}
	if !strings.HasSuffix(string(content), "\n") {
		t.Errorf("文件应以换行结尾: %q", content)
	}

	// 无残留临时文件（原子替换后临时文件应已被移走）
	matches, _ := filepath.Glob(filepath.Join(dir, ".rocksys-config-*.tmp"))
	if len(matches) != 0 {
		t.Errorf("残留临时文件: %v", matches)
	}
}

// TestFileStoreWatch Watch 回调触发
func TestFileStoreWatch(t *testing.T) {
	fs := NewFileStore(nil, filepath.Join(t.TempDir(), "app.env"))
	ch := make(chan ChangeEvent, 4)
	if err := fs.Watch(func(c ChangeEvent) { ch <- c }); err != nil {
		t.Fatalf("Watch err: %v", err)
	}
	if err := fs.Set("a", "1"); err != nil {
		t.Fatalf("Set err: %v", err)
	}
	select {
	case c := <-ch:
		if c.Key != "a" || c.Value != "1" {
			t.Errorf("ChangeEvent=%+v，want {Key:a Value:1}", c)
		}
	case <-time.After(time.Second):
		t.Fatal("Watch 回调未触发")
	}
}

// TestConfigComponent Config 组件生命周期（hotswap.Component）
func TestConfigComponent(t *testing.T) {
	old := defaultFile
	defaultFile = filepath.Join(t.TempDir(), "test.env")
	defer func() { defaultFile = old }()

	c := New(nil)
	if c.Name() != "config" {
		t.Errorf("Name=%q，want config", c.Name())
	}
	if c.State() != hotswap.StateDisabled {
		t.Errorf("初始 State=%v，want disabled", c.State())
	}
	// 未启动时操作应报错
	if _, err := c.Get("k"); err == nil {
		t.Error("未启动 Get 应报错")
	}

	if err := c.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	if c.State() != hotswap.StateEnabled {
		t.Errorf("Start 后 State=%v，want enabled", c.State())
	}
	// 幂等：重复 Start 不报错
	if err := c.Start(nil); err != nil {
		t.Errorf("重复 Start err: %v", err)
	}

	if err := c.Set("k", "v"); err != nil {
		t.Fatalf("Set err: %v", err)
	}
	if got, _ := c.Get("k"); got != "v" {
		t.Errorf("Get k=%q，want v", got)
	}

	if err := c.Stop(); err != nil {
		t.Fatalf("Stop err: %v", err)
	}
	if c.State() != hotswap.StateDisabled {
		t.Errorf("Stop 后 State=%v，want disabled", c.State())
	}
	if _, err := c.Get("k"); err == nil {
		t.Error("Stop 后 Get 应报错")
	}
	// 幂等：重复 Stop 不报错
	if err := c.Stop(); err != nil {
		t.Errorf("重复 Stop err: %v", err)
	}
}
