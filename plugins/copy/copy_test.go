// Package copy 单测：目标解析、请求快照复制、异步抄送。
package copy

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"rocksys/internal/chain"
)

func TestNewImplementsInterfaces(t *testing.T) {
	var _ chain.Middleware = New(nil)
	if _, ok := chain.Middleware(New(nil)).(interface{ Slot() chain.Slot }); !ok {
		t.Fatal("copy 未实现 Slot()")
	}
}

func TestParseTargets_Empty(t *testing.T) {
	snap, err := parseTargets("")
	if err != nil {
		t.Fatalf("parseTargets empty err: %v", err)
	}
	if len(snap.targets) != 0 {
		t.Errorf("空目标应为空, got %d", len(snap.targets))
	}
}

func TestParseTargets_Error(t *testing.T) {
	if _, err := parseTargets("shadow-a:9100"); err == nil {
		t.Fatal("非 http(s):// 目标应返回 error")
	}
}

func TestParseTargets_Multi(t *testing.T) {
	snap, err := parseTargets("http://a:1;http://b:2")
	if err != nil {
		t.Fatalf("parseTargets err: %v", err)
	}
	if len(snap.targets) != 2 || snap.targets[0] != "http://a:1" || snap.targets[1] != "http://b:2" {
		t.Errorf("目标解析错误: %v", snap.targets)
	}
}

func TestCloneRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/orders?x=1", nil)
	req.Header.Set("X-Tenant", "acme")
	req.Host = "api.example.com"
	clone := cloneRequest(req)
	if clone.Method != http.MethodPost || clone.URL.Path != "/api/orders" || clone.URL.RawQuery != "x=1" {
		t.Errorf("克隆请求错误: %+v", clone)
	}
	if clone.Header.Get("X-Tenant") != "acme" {
		t.Errorf("克隆请求头错误: %v", clone.Header)
	}
	if clone.Host != "api.example.com" {
		t.Errorf("克隆 Host 错误: %q", clone.Host)
	}
}

func TestOnResponse_CopiesToTarget(t *testing.T) {
	var got atomic.Int64
	shadow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer shadow.Close()

	c := New(nil)
	c.targets = shadow.URL
	if err := c.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/orders/123", nil)
	ctx := &chain.Context{R: req}
	if err := c.OnResponse(ctx); err != nil {
		t.Fatalf("OnResponse err: %v", err)
	}
	// 异步发送，轮询等待。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && got.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got.Load() == 0 {
		t.Fatal("shadow 后端未收到抄送请求")
	}
}

func TestOnResponse_NoTargets(t *testing.T) {
	c := New(nil)
	if err := c.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	ctx := &chain.Context{R: httptest.NewRequest(http.MethodGet, "/x", nil)}
	if err := c.OnResponse(ctx); err != nil {
		t.Fatalf("无目标时 OnResponse 不应报错: %v", err)
	}
}

func TestStart_ErrorKeepsOldSnapshot(t *testing.T) {
	c := New(nil)
	c.targets = "http://a:1"
	if err := c.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	old := c.snap.Load().(*copyTargets)
	c.targets = "bad-target"
	if err := c.Start(nil); err == nil {
		t.Fatal("Start 非法配置应返回 error")
	}
	if got := c.snap.Load().(*copyTargets); got != old {
		t.Error("Start 失败后不应替换旧快照")
	}
}