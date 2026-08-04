// Package dispatch chash 一致性哈希负载均衡专项测试。
package dispatch

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseRule_ChashAlgo(t *testing.T) {
	rt := mustRT(t, "/api/=http://a:1;http://b:1|alg=chash")
	rule := rt.rules[0]
	if rule.Algo != AlgoCHash {
		t.Errorf("Algo=%q, want chash", rule.Algo)
	}
	if rule.ChashKey != defaultChashKey {
		t.Errorf("ChashKey=%q, want 默认 $remote_addr", rule.ChashKey)
	}
}

func TestParseRule_ChashKey(t *testing.T) {
	rt := mustRT(t, "/api/=http://a:1;http://b:1|alg=chash|key=$http_x-user-id")
	rule := rt.rules[0]
	if rule.Algo != AlgoCHash {
		t.Errorf("Algo=%q, want chash", rule.Algo)
	}
	if rule.ChashKey != "$http_x-user-id" {
		t.Errorf("ChashKey=%q, want $http_x-user-id", rule.ChashKey)
	}
}

func TestParseRule_ChashInvalidAlgo(t *testing.T) {
	if _, err := parseRules("/api/=http://a:1|alg=least_conn"); err == nil {
		t.Fatal("不支持的算法应返回 error")
	}
}

func TestChash_StableByRemoteAddr(t *testing.T) {
	rt := mustRT(t, "/api/=http://a:1;http://b:1;http://c:1|alg=chash")
	rule := rt.rules[0]
	// 同一客户端 IP 稳定打到同一节点。
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "10.0.0.5:12345"
	first, ok := rule.Select(req)
	if !ok {
		t.Fatal("Select 应返回节点")
	}
	for i := 0; i < 10; i++ {
		up, _ := rule.Select(req)
		if up != first {
			t.Fatalf("同一 key 应稳定命中同一节点, got %q want %q", up, first)
		}
	}
}

func TestChash_StableByHeader(t *testing.T) {
	rt := mustRT(t, "/api/=http://a:1;http://b:1;http://c:1|alg=chash|key=$http_x-user-id")
	rule := rt.rules[0]
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("X-User-Id", "user-42")
	first, ok := rule.Select(req)
	if !ok {
		t.Fatal("Select 应返回节点")
	}
	// 不同用户可能不同节点，但同一用户稳定。
	for i := 0; i < 10; i++ {
		up, _ := rule.Select(req)
		if up != first {
			t.Fatalf("同一 key 应稳定, got %q want %q", up, first)
		}
	}
}

func TestChash_DistributesAcrossNodes(t *testing.T) {
	rt := mustRT(t, "/api/=http://a:1;http://b:1;http://c:1|alg=chash")
	rule := rt.rules[0]
	seen := map[string]bool{}
	for i := 0; i < 300; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.RemoteAddr = "10.0.0." + string(rune('0'+i%10)) + ":1"
		up, ok := rule.Select(req)
		if !ok {
			t.Fatal("Select 应返回节点")
		}
		seen[up] = true
	}
	// 多个不同 key 应分散到多个节点（哈希分布）。
	if len(seen) < 2 {
		t.Errorf("chash 应分散到多个节点, 仅命中 %d 个: %v", len(seen), seen)
	}
}

func TestChash_MissingKeyFallsBackToRR(t *testing.T) {
	// key 提取为空（无该 header）→ 回退平滑加权轮询，不 panic。
	rt := mustRT(t, "/api/=http://a:1;http://b:1|alg=chash|key=$http_x-missing")
	rule := rt.rules[0]
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	for i := 0; i < 10; i++ {
		if _, ok := rule.Select(req); !ok {
			t.Fatal("key 缺失时应回退轮询并返回节点")
		}
	}
}

func TestChash_NilReq(t *testing.T) {
	rt := mustRT(t, "/api/=http://a:1;http://b:1|alg=chash")
	rule := rt.rules[0]
	if _, ok := rule.Select(nil); !ok {
		t.Fatal("nil req 时应回退轮询并返回节点")
	}
}