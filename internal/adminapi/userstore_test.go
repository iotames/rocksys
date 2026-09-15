// Copyright © 用户存储 nil 降级防护测试：DB 未配置时 userStore 为 nil，各方法不得 panic。
package adminapi

import "testing"

// TestNilUserStoreNoPanic 降级场景（s.users == nil）下各方法应返回错误而非空指针 panic。
// 回归背景：handleAuthStatus 曾在 DB 未配置时直接调 s.users.get() 引发 panic。
func TestNilUserStoreNoPanic(t *testing.T) {
	var s *userStore

	if _, err := s.get(); err == nil {
		t.Fatal("nil 接收者 get 应返回错误")
	}
	if _, err := s.getByUsername("admin"); err == nil {
		t.Fatal("nil 接收者 getByUsername 应返回错误")
	}
	if _, err := s.count(); err == nil {
		t.Fatal("nil 接收者 count 应返回错误")
	}
	if err := s.save("admin", "hash"); err == nil {
		t.Fatal("nil 接收者 save 应返回错误")
	}
}
