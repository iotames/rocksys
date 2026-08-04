package jwtutil

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSignAndVerify(t *testing.T) {
	secret := []byte("test-secret")
	claims := map[string]interface{}{
		"user_id": "u-1",
		"tenant_id": "t-1",
	}
	token, err := Sign(secret, claims, time.Hour)
	if err != nil {
		t.Fatalf("Sign err: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token 结构错误: %q", token)
	}

	got, err := Verify(secret, token, "")
	if err != nil {
		t.Fatalf("Verify err: %v", err)
	}
	if got["user_id"] != "u-1" || got["tenant_id"] != "t-1" {
		t.Errorf("载荷不符: %v", got)
	}
	// exp 应为未来时间（json.Number）
	if n, ok := got["exp"].(json.Number); ok {
		exp, _ := n.Int64()
		if exp <= time.Now().Unix() {
			t.Errorf("exp=%d 应在未来", exp)
		}
	} else {
		t.Errorf("exp 字段类型 = %T", got["exp"])
	}
}

func TestVerifyErrors(t *testing.T) {
	secret := []byte("secret")
	claims := map[string]interface{}{"sub": "x"}

	// 错误密钥
	token, _ := Sign(secret, claims, time.Hour)
	if _, err := Verify([]byte("wrong"), token, ""); err != ErrTokenSign {
		t.Errorf("错误密钥应 ErrTokenSign, got %v", err)
	}

	// 过期 token（全新 map，Sign 会写入过去时间的 exp）
	expired, _ := Sign(secret, map[string]interface{}{"sub": "x"}, -time.Minute)
	if _, err := Verify(secret, expired, ""); err != ErrTokenExpired {
		t.Errorf("过期应 ErrTokenExpired, got %v", err)
	}

	// 格式错误
	if _, err := Verify(secret, "not-a-jwt", ""); err != ErrTokenFormat {
		t.Errorf("格式错应 ErrTokenFormat, got %v", err)
	}

	// 篡改载荷
	tampered := token[:len(token)-3] + "abc"
	if _, err := Verify(secret, tampered, ""); err != ErrTokenSign {
		t.Errorf("篡改应 ErrTokenSign, got %v", err)
	}
}

func TestVerifyIssuer(t *testing.T) {
	secret := []byte("secret")
	claims := map[string]interface{}{"iss": "rocksys"}
	token, _ := Sign(secret, claims, time.Hour)

	if _, err := Verify(secret, token, "rocksys"); err != nil {
		t.Errorf("iss 匹配应通过: %v", err)
	}
	if _, err := Verify(secret, token, "other"); err == nil {
		t.Error("iss 不匹配应失败")
	}
}

// 兼容性：验签必须与 miniutils 生成的 token 互通（标准 HS256 base64url）。
func TestCompatMiniutils(t *testing.T) {
	secret := []byte("secret")
	claims := map[string]interface{}{
		"id":   123456789,
		"user": "harvey",
		"exp":  time.Now().Add(time.Hour).Unix(),
	}
	token, err := Sign(secret, claims, 0)
	if err != nil {
		t.Fatalf("Sign err: %v", err)
	}
	got, err := Verify(secret, token, "")
	if err != nil {
		t.Fatalf("Verify err: %v", err)
	}
	if got["id"].(json.Number).String() != "123456789" {
		t.Errorf("id 精度丢失: %v", got["id"])
	}
	if got["user"] != "harvey" {
		t.Errorf("user=%v", got["user"])
	}
}
