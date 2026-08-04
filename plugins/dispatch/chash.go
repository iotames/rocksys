// 一致性哈希（chash）：按请求 key 稳定选点，用于会话保持 / 缓存亲和。
// dispatch 内部子组件。
//
// key 提取方式（Rule.ChashKey）：
//   - $remote_addr   客户端 IP（默认）
//   - $http_<name>   请求头（如 $http_x-user-id）
//   - $cookie_<name> Cookie 值（如 $cookie_session_id）
//
// 说明：当前实现为"按 key 稳定取模"（简化一致性哈希），同一 key 在节点集合
// 不变时固定打到同一节点；节点增减/健康翻转时会重新映射。后续可升级为
// hash ring 以最小化节点变化时的重映射。
package dispatch

import (
	"hash/fnv"
	"net/http"
	"strings"
)

// defaultChashKey 默认 chash key 提取方式：客户端 IP。
const defaultChashKey = "$remote_addr"

// extractHashKey 按 ChashKey 提取哈希 key；提取不到返回空串。
func extractHashKey(req *http.Request, keyBy string) string {
	switch {
	case keyBy == "" || keyBy == defaultChashKey:
		// 取 RemoteAddr 的 IP 部分（去端口），保证同一客户端稳定。
		addr := req.RemoteAddr
		if i := strings.LastIndex(addr, ":"); i >= 0 {
			return addr[:i]
		}
		return addr
	case strings.HasPrefix(keyBy, "$http_"):
		return req.Header.Get(strings.TrimPrefix(keyBy, "$http_"))
	case strings.HasPrefix(keyBy, "$cookie_"):
		c, err := req.Cookie(strings.TrimPrefix(keyBy, "$cookie_"))
		if err != nil {
			return ""
		}
		return c.Value
	default:
		// 字面 key（所有请求同一 key，恒打同一节点）。
		return keyBy
	}
}

// hashKey 计算 key 的 FNV-1a 32 位哈希。
func hashKey(key string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return h.Sum32()
}
