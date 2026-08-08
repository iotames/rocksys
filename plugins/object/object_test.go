package object

import (
	"os"
	"path/filepath"
	"testing"

	"rocksys/internal/hotswap"
)

// TestLocalStoreRoundTrip 正常 Put/Get/Delete 往返
func TestLocalStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStore(dir)

	if err := s.Put("a/b.txt", []byte("hello")); err != nil {
		t.Fatalf("Put err: %v", err)
	}
	got, err := s.Get("a/b.txt")
	if err != nil {
		t.Fatalf("Get err: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("Get=%q，want hello", got)
	}
	if err := s.Delete("a/b.txt"); err != nil {
		t.Fatalf("Delete err: %v", err)
	}
	if _, err := s.Get("a/b.txt"); err == nil {
		t.Error("Delete 后 Get 应报错")
	}
}

// TestLocalStoreTraversal 路径穿越防护（§19 验收核心）：../ 不得逃逸 baseDir
func TestLocalStoreTraversal(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalStore(dir)

	// 穿越目标：逃逸到 dir 的父级目录
	escapeFile := filepath.Join(filepath.Dir(dir), "object-escape-test.txt")

	// Put 穿越必须被拒绝
	if err := s.Put("../object-escape-test.txt", []byte("boom")); err == nil {
		t.Error("Put 穿越路径应被拒绝")
	}
	if err := s.Put("../../../etc/passwd", []byte("boom")); err == nil {
		t.Error("Put 深层穿越路径应被拒绝")
	}
	// 确认没有逃逸写出
	if _, err := os.Stat(escapeFile); err == nil {
		t.Error("穿越 Put 不应真正写文件")
	}

	// Get 穿越必须被拒绝
	if _, err := s.Get("../../../etc/passwd"); err == nil {
		t.Error("Get 穿越路径应被拒绝")
	}
	// Delete 穿越必须被拒绝
	if err := s.Delete("../../../etc/passwd"); err == nil {
		t.Error("Delete 穿越路径应被拒绝")
	}

	// 相对 baseDir 也应防御穿越
	rel := NewLocalStore(filepath.Join("..", "should-not-exist"))
	if err := rel.Put("../../../etc/passwd", []byte("boom")); err == nil {
		t.Error("相对路径下穿越也应被拒绝")
	}

	// 合理嵌套路径应放行
	nested := filepath.Join(dir, "sub")
	if err := s.Put("sub/deep/x.txt", []byte("ok")); err != nil {
		t.Fatalf("嵌套正常 Put err: %v", err)
	}
	if got, _ := s.Get("sub/deep/x.txt"); string(got) != "ok" {
		t.Errorf("嵌套 Get=%q，want ok", got)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Errorf("嵌套目录应被创建: %v", err)
	}
}

// TestObjectComponent Object 组件生命周期（hotswap.Component）
func TestObjectComponent(t *testing.T) {
	o := New(nil)
	o.baseDir = t.TempDir()

	if o.Name() != "object" {
		t.Errorf("Name=%q，want object", o.Name())
	}
	if o.State() != hotswap.StateDisabled {
		t.Errorf("初始 State=%v，want disabled", o.State())
	}
	// 未启动时操作应报错
	if err := o.Put("x", []byte("v")); err == nil {
		t.Error("未启动 Put 应报错")
	}

	if err := o.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	if o.State() != hotswap.StateEnabled {
		t.Errorf("Start 后 State=%v，want enabled", o.State())
	}
	// 幂等：重复 Start 不报错
	if err := o.Start(nil); err != nil {
		t.Errorf("重复 Start err: %v", err)
	}

	if err := o.Put("k", []byte("v")); err != nil {
		t.Fatalf("Put err: %v", err)
	}
	if got, _ := o.Get("k"); string(got) != "v" {
		t.Errorf("Get k=%q，want v", got)
	}
	// 组件下同样拒绝穿越
	if err := o.Put("../../../etc/passwd", []byte("boom")); err == nil {
		t.Error("组件 Put 穿越路径应被拒绝")
	}

	if err := o.Stop(); err != nil {
		t.Fatalf("Stop err: %v", err)
	}
	if o.State() != hotswap.StateDisabled {
		t.Errorf("Stop 后 State=%v，want disabled", o.State())
	}
	if err := o.Put("k", []byte("v")); err == nil {
		t.Error("Stop 后 Put 应报错")
	}
	// 幂等：重复 Stop 不报错
	if err := o.Stop(); err != nil {
		t.Errorf("重复 Stop err: %v", err)
	}
}
