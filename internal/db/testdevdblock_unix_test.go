//go:build integration && unix

// Unix 实现：syscall.Flock 独占锁（Windows 无 Flock API，见 testdevdblock_windows_test.go）。
package db_test

import (
	"os"
	"syscall"
	"testing"
)

func lockSharedDevDB(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile(os.TempDir()+"/rocksys-devdb-it.lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("打开互斥锁文件失败: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("加互斥锁失败: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}
