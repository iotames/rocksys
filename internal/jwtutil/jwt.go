// Package jwtutil 自研 JWT（HS256）签发与校验工具。
//
// 铁律：非必要不引入第三方库。本包基于 github.com/iotames/miniutils/jwt.go
// 复制改造为独立包，仅依赖 Go 标准库，替换原 golang-jwt/jwt/v5 依赖。
//
// 支持：
//   - HS256 签名签发（Base64Url + HMAC-SHA256）
//   - 解码与验签（校验签名、exp 过期、可选 iss）
//   - 载荷字段用 json.Number 保留整数精度
package jwtutil

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 常见错误（语义与原 miniutils 一致）。
var (
	ErrTokenFormat  = errors.New("token is not a JWT")
	ErrTokenExpired = errors.New("token is expired")
	ErrTokenSign    = errors.New("token sign error")
	ErrTokenExp     = errors.New("token lost field: exp")
)

// Sign 以 keyBytes 为密钥、claims 为载荷签发 HS256 JWT。
// claims 中若已有 exp 则不覆盖；否则以 expiresIn 计算并写入。
func Sign(keyBytes []byte, claims map[string]interface{}, expiresIn time.Duration) (string, error) {
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(expiresIn).Unix()
	}
	sstr, err := toJwtString(claims)
	if err != nil {
		return "", err
	}
	sig := base64.RawURLEncoding.EncodeToString(hmacSHA256(sstr, keyBytes))
	return strings.Join([]string{sstr, sig}, "."), nil
}

// Verify 验签并校验有效期：解码 JWT → 校验签名 → 校验 exp。
// issuer 非空时额外校验 iss 一致。返回载荷 map（exp 等为 json.Number）。
func Verify(keyBytes []byte, token, issuer string) (map[string]interface{}, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrTokenFormat
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("jwtutil: payload 解码失败: %w", err)
	}
	var claims map[string]interface{}
	if err := jsonDecodeUseNumber(payloadBytes, &claims); err != nil {
		return nil, fmt.Errorf("jwtutil: payload 解析失败: %w", err)
	}

	// 校验 exp（与 miniutils 语义一致：必须有 exp，过期即失败）
	expVal, ok := claims["exp"]
	if !ok {
		return nil, ErrTokenExp
	}
	var expiredAt int64
	switch n := expVal.(type) {
	case json.Number:
		expiredAt, err = n.Int64()
		if err != nil {
			return nil, fmt.Errorf("jwtutil: exp 字段非整数: %w", err)
		}
	case float64:
		expiredAt = int64(n)
	default:
		return nil, errors.New("jwtutil: exp 字段类型错误")
	}
	if expiredAt < time.Now().Unix() {
		return nil, ErrTokenExpired
	}

	// 校验签名：用原始载荷重签比对。
	headerSign := strings.Join(parts[:2], ".")
	expected := base64.RawURLEncoding.EncodeToString(hmacSHA256(headerSign, keyBytes))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return nil, ErrTokenSign
	}

	// 可选校验 iss。
	if issuer != "" {
		if iss, _ := claims["iss"].(string); iss != issuer {
			return nil, errors.New("jwtutil: token iss 不匹配")
		}
	}
	return claims, nil
}

// toJwtString 构造 JWT 的 header.payload 签名输入串。
func toJwtString(claims map[string]interface{}) (string, error) {
	header := map[string]interface{}{"typ": "JWT", "alg": "HS256"}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("jwtutil: header 序列化失败: %w", err)
	}
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("jwtutil: payload 序列化失败: %w", err)
	}
	return strings.Join([]string{
		base64.RawURLEncoding.EncodeToString(headerBytes),
		base64.RawURLEncoding.EncodeToString(payloadBytes),
	}, "."), nil
}

// hmacSHA256 计算 HMAC-SHA256。
func hmacSHA256(message string, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	return mac.Sum(nil)
}

// jsonDecodeUseNumber 以 UseNumber 解析 JSON，避免大整数被转成 float64 丢精度。
func jsonDecodeUseNumber(data []byte, v interface{}) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}
