//go:build integration

// devdb_lock_integration_test.go：共享开发库跨包互斥锁（仅集成测试用）。
// devdb 为共享真库，go test 多包并行时本包 TestPostgresAttackArchive 操作固定表名
// attack_archive，与 internal/db 的表结构同步全清单验收测试（DROP/CREATE 7 张规范表）
// 互踩同名表；以文件锁把冲突面串行化。与 internal/db/schema_sync_acceptance_integration_test.go
// 的同名辅助保持一致的锁文件路径。
package shield

import (
	"os"
	"syscall"
	"testing"
)

// lockSharedDevDB 获取共享开发库互斥锁（进程间 flock，测试结束自动释放）。
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
