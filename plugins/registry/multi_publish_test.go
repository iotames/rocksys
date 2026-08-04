package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"rocksys/internal/conf"
)

// recordConfMgr 记录 conf.Set 调用的假管理器。
type recordConfMgr struct {
	values map[string]string
}

func (r *recordConfMgr) Current() *conf.Config          { return nil }
func (r *recordConfMgr) Watch(func(*conf.Config))       {}
func (r *recordConfMgr) StartWatcher() error            { return nil }
func (r *recordConfMgr) Shutdown(context.Context) error { return nil }
func (r *recordConfMgr) Register(pval any, name, defval, title string, usage ...string) error {
	return nil
}
func (r *recordConfMgr) Set(name, value string) error {
	if r.values == nil {
		r.values = make(map[string]string)
	}
	r.values[name] = value
	return nil
}
func (r *recordConfMgr) List() []conf.ConfigItem { return nil }

// TestMultiInstancePublish 多实例注册联动：注册两个实例后 DISPATCH_RULES 应同时含两者。
// 回归：曾在 E2E 中发现注册第二个实例后路由偶发失效，验证 publish 逻辑正确。
func TestMultiInstancePublish(t *testing.T) {
	cfg := &recordConfMgr{}
	s := NewServer("127.0.0.1:0")
	defer func() { _ = s.Stop() }()
	s.SetConfMgr(cfg)
	s.SetTTL(30 * time.Second)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	base := "http://" + s.Addr()
	// 注册第一个实例
	resp := postDiag(base+"/register", map[string]string{"name": "order-svc", "addr": "http://127.0.0.1:9002"})
	if resp != http.StatusOK {
		t.Fatalf("register order-svc: %d", resp)
	}
	got1 := cfg.values["DISPATCH_RULES"]
	t.Logf("注册 order-svc 后 DISPATCH_RULES=%q", got1)

	// 注册第二个实例
	resp = postDiag(base+"/register", map[string]string{"name": "user-svc", "addr": "http://127.0.0.1:9004"})
	if resp != http.StatusOK {
		t.Fatalf("register user-svc: %d", resp)
	}
	got2 := cfg.values["DISPATCH_RULES"]
	t.Logf("注册 user-svc 后 DISPATCH_RULES=%q", got2)

	// 关键断言：两个实例的规则都在
	if got2 != "/api/order-svc/=http://127.0.0.1:9002,/api/user-svc/=http://127.0.0.1:9004" {
		t.Errorf("多实例联动 DISPATCH_RULES=%q，want 同时含两者", got2)
	}
}

func postDiag(url string, body any) int {
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
