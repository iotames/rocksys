//go:build integration && windows

// devdb_lock_windows_test.go：Windows 无 syscall.Flock，退化为仅持有锁文件句柄。
// Why：集成测试按单包命令运行（不与其他包并行共享 devdb），句柄占位足以表达意图；
// 引入 x/sys 或 LockFileEx 不值得（依赖最小化）。与 internal/db 的 windows 实现同款。
package shield

import (
	"os"
	"testing"
)

// lockSharedDevDB 获取共享开发库互斥锁（进程间 flock，测试结束自动释放）。
func lockSharedDevDB(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile(os.TempDir()+"/rocksys-devdb-it.lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("打开互斥锁文件失败: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
}
