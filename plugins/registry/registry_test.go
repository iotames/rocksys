// Package registry 单测：StaticTable 加载、Server 注册/心跳/超时摘除、
// 实例变更联动（Watcher 回调 + conf.Set DISPATCH_RULES）与组件生命周期（DEV_HANDBOOK.md §17）。
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rocksys/internal/conf"
	"rocksys/internal/hotswap"
)

// fakeConfMgr 记录 conf.Manager.Set 调用的测试桩（不启动 easyconf，避免 .env 副作用）。
type fakeConfMgr struct {
	mu   sync.Mutex
	vals map[string]string
}

func newFakeConfMgr() *fakeConfMgr { return &fakeConfMgr{vals: make(map[string]string)} }

func (f *fakeConfMgr) Current() *conf.Config { return &conf.Config{} }
func (f *fakeConfMgr) Watch(func(*conf.Config)) {
}
func (f *fakeConfMgr) StartWatcher() error { return nil }
func (f *fakeConfMgr) Shutdown(context.Context) error {
	return nil
}
func (f *fakeConfMgr) Register(any, string, string, string, ...string) error {
	return nil
}
func (f *fakeConfMgr) Set(name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vals[name] = value
	return nil
}
func (f *fakeConfMgr) List() []conf.ConfigItem { return nil }
func (f *fakeConfMgr) SyncDefaultFile() error  { return nil }
func (f *fakeConfMgr) value(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.vals[name]
}

// writeTemp 写入临时文件返回路径。
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写临时文件 %s: %v", name, err)
	}
	return path
}

// TestNewStaticTable_FromYAML YAML 静态实例加载
func TestNewStaticTable_FromYAML(t *testing.T) {
	path := writeTemp(t, "instances.yaml", `
# 静态实例清单
instances:
  - name: order-svc
    addr: http://order-svc:9001
  - name: user-svc
    addr: http://user-svc:9002
`)
	table := NewStaticTable(path)
	got := table.Instances()
	if len(got) != 2 {
		t.Fatalf("实例数=%d，want 2", len(got))
	}
	if got[0].Name != "order-svc" || got[0].Addr != "http://order-svc:9001" {
		t.Errorf("实例[0]=%+v，want order-svc", got[0])
	}
	if got[1].Name != "user-svc" || got[1].Addr != "http://user-svc:9002" {
		t.Errorf("实例[1]=%+v，want user-svc", got[1])
	}
}

// TestNewStaticTable_FromJSON JSON 静态实例加载
func TestNewStaticTable_FromJSON(t *testing.T) {
	path := writeTemp(t, "instances.json",
		`{"instances":[{"name":"order-svc","addr":"http://order-svc:9001"},{"name":"pay-svc","addr":"http://pay-svc:9003"}]}`)
	table := NewStaticTable(path)
	got := table.Instances()
	if len(got) != 2 {
		t.Fatalf("实例数=%d，want 2", len(got))
	}
	if got[0].Name != "order-svc" || got[0].Addr != "http://order-svc:9001" {
		t.Errorf("实例[0]=%+v，want order-svc", got[0])
	}
}

// TestNewStaticTable_BadFile_Empty 文件缺失 / 内容非法 → 空表（不 panic）
func TestNewStaticTable_BadFile_Empty(t *testing.T) {
	if got := NewStaticTable(filepath.Join(t.TempDir(), "not-exist.yaml")).Instances(); len(got) != 0 {
		t.Errorf("文件缺失应返回空表，got %+v", got)
	}
	bad := writeTemp(t, "bad.yaml", "::: 非法内容 [[[")
	if got := NewStaticTable(bad).Instances(); len(got) != 0 {
		t.Errorf("内容非法应返回空表，got %+v", got)
	}
	empty := writeTemp(t, "empty.yaml", "instances: []")
	if got := NewStaticTable(empty).Instances(); len(got) != 0 {
		t.Errorf("空列表应返回空表，got %+v", got)
	}
	// 缺少 name/addr 的条目被过滤
	skip := writeTemp(t, "skip.yaml", "instances:\n  - name: ''\n    addr: ''")
	if got := NewStaticTable(skip).Instances(); len(got) != 0 {
		t.Errorf("无效条目应被过滤，got %+v", got)
	}
}

// TestNewStaticTable_Reload 外部文件变更后 Reload 刷新
func TestNewStaticTable_Reload(t *testing.T) {
	path := writeTemp(t, "instances.yaml", "instances:\n  - name: order-svc\n    addr: http://order-svc:9001\n")
	table := NewStaticTable(path)
	if got := len(table.Instances()); got != 1 {
		t.Fatalf("初始实例数=%d，want 1", got)
	}
	_ = os.WriteFile(path, []byte("instances:\n  - name: user-svc\n    addr: http://user-svc:9002\n"), 0o644)
	table.Reload()
	got := table.Instances()
	if len(got) != 1 || got[0].Name != "user-svc" {
		t.Errorf("Reload 后=%+v，want user-svc", got)
	}
	// 解析失败保留旧表
	_ = os.WriteFile(path, []byte(": not yaml :"), 0o644)
	table.Reload()
	if got := table.Instances(); len(got) != 1 || got[0].Name != "user-svc" {
		t.Errorf("解析失败应保留旧表，got %+v", got)
	}
}

// TestBuildRules 实例列表 → DISPATCH_RULES 格式字符串
func TestBuildRules(t *testing.T) {
	now := time.Now()
	insts := []Instance{
		{Name: "order-svc", Addr: "http://order-svc:9001", Healthy: true, LastHeartbeat: now},
		{Name: "user-svc", Addr: "http://user-svc:9002", Healthy: true, LastHeartbeat: now},
	}
	got := buildRules(insts)
	want := "/api/order-svc/=http://order-svc:9001,/api/user-svc/=http://user-svc:9002"
	if got != want {
		t.Errorf("buildRules=%q，want %q", got, want)
	}
	// 不健康实例不进路由
	insts[1].Healthy = false
	if got := buildRules(insts); got != "/api/order-svc/=http://order-svc:9001" {
		t.Errorf("不健康实例应排除，got %q", got)
	}
	// 同名多副本 → 保留心跳最新的健康实例
	insts = []Instance{
		{Name: "order-svc", Addr: "http://order-svc:9001", Healthy: true, LastHeartbeat: now},
		{Name: "order-svc", Addr: "http://order-svc:9002", Healthy: true, LastHeartbeat: now.Add(time.Minute)},
	}
	if got := buildRules(insts); got != "/api/order-svc/=http://order-svc:9002" {
		t.Errorf("同名多副本应取心跳最新，got %q", got)
	}
}

// startTestServer 启动随机端口 Server，返回 baseURL 与清理函数。
func startTestServer(t *testing.T, s *Server) string {
	t.Helper()
	if err := s.Start(); err != nil {
		t.Fatalf("Server.Start err: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return "http://" + s.Addr()
}

// postJSON 发送 JSON 请求，返回状态码与响应体。
func postJSON(t *testing.T, method, url string, body any) (int, map[string]any) {
	t.Helper()
	buf, _ := json.Marshal(body)
	req, err := http.NewRequest(method, url, bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("NewRequest err: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s err: %v", method, url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestServer_RegisterAndHeartbeat Server 注册/心跳流程（真实 HTTP）
func TestServer_RegisterAndHeartbeat(t *testing.T) {
	s := NewServer("127.0.0.1:0")
	base := startTestServer(t, s)

	code, out := postJSON(t, http.MethodPost, base+"/register",
		map[string]string{"name": "order-svc", "addr": "http://order-svc:9001"})
	if code != http.StatusOK || out["msg"] != "ok" {
		t.Fatalf("register code=%d out=%+v，want 200 ok", code, out)
	}
	got := s.Instances()
	if len(got) != 1 || got[0].Name != "order-svc" || !got[0].Healthy {
		t.Fatalf("注册后=%+v，want order-svc 健康实例", got)
	}

	// 心跳续约：更新 LastHeartbeat
	before := got[0].LastHeartbeat
	time.Sleep(50 * time.Millisecond)
	code, out = postJSON(t, http.MethodPut, base+"/heartbeat",
		map[string]string{"name": "order-svc", "addr": "http://order-svc:9001"})
	if code != http.StatusOK {
		t.Fatalf("heartbeat code=%d，want 200", code)
	}
	if after := s.Instances()[0].LastHeartbeat; !after.After(before) {
		t.Error("心跳后 LastHeartbeat 应更新")
	}

	// 非法请求体 → 400
	if code, _ := postJSON(t, http.MethodPost, base+"/register", "not-json"); code != http.StatusBadRequest {
		t.Errorf("非法 JSON 应 400，got %d", code)
	}
	// 缺少 addr → 400
	if code, _ := postJSON(t, http.MethodPost, base+"/register",
		map[string]string{"name": "x"}); code != http.StatusBadRequest {
		t.Errorf("缺少 addr 应 400，got %d", code)
	}
}

// TestServer_Register_TriggersConfSet 实例变更联动 conf.Set(DISPATCH_RULES)
func TestServer_Register_TriggersConfSet(t *testing.T) {
	cfg := newFakeConfMgr()
	s := NewServer("127.0.0.1:0")
	s.SetConfMgr(cfg)
	base := startTestServer(t, s)

	postJSON(t, http.MethodPost, base+"/register",
		map[string]string{"name": "order-svc", "addr": "http://order-svc:9001"})
	postJSON(t, http.MethodPost, base+"/register",
		map[string]string{"name": "user-svc", "addr": "http://user-svc:9002"})

	want := "/api/order-svc/=http://order-svc:9001,/api/user-svc/=http://user-svc:9002"
	if got := cfg.value("DISPATCH_RULES"); got != want {
		t.Errorf("DISPATCH_RULES=%q，want %q", got, want)
	}

	// 摘除后联动更新（不再包含 user-svc）
	s.Remove("user-svc", "http://user-svc:9002")
	want = "/api/order-svc/=http://order-svc:9001"
	if got := cfg.value("DISPATCH_RULES"); got != want {
		t.Errorf("摘除后 DISPATCH_RULES=%q，want %q", got, want)
	}
}

// TestServer_Watch_Callback 实例变更触发 Watcher 回调
func TestServer_Watch_Callback(t *testing.T) {
	s := NewServer("127.0.0.1:0")
	ch := make(chan []Instance, 4)
	s.Watch(func(instances []Instance) { ch <- instances })
	base := startTestServer(t, s)

	postJSON(t, http.MethodPost, base+"/register",
		map[string]string{"name": "order-svc", "addr": "http://order-svc:9001"})

	select {
	case got := <-ch:
		if len(got) != 1 || got[0].Name != "order-svc" {
			t.Errorf("回调实例=%+v，want order-svc", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watcher 回调未触发")
	}
}

// TestServer_HeartbeatTimeout_Eviction 心跳超时自动摘除（scanOnce 确定性路径）
func TestServer_HeartbeatTimeout_Eviction(t *testing.T) {
	s := NewServer("127.0.0.1:0")
	s.SetTTL(200 * time.Millisecond)
	s.Register(Instance{Name: "order-svc", Addr: "http://order-svc:9001", Healthy: true, LastHeartbeat: time.Now()})
	time.Sleep(250 * time.Millisecond) // 超过 TTL，未续约
	s.scanOnce()
	if got := s.Instances(); len(got) != 0 {
		t.Errorf("心跳超时后应摘除，got %+v", got)
	}
}

// TestServer_Heartbeat_NoEviction_AfterRenew 续约后不摘除（e2e 扫描循环）
func TestServer_Heartbeat_NoEviction_AfterRenew(t *testing.T) {
	s := NewServer("127.0.0.1:0")
	s.SetTTL(300 * time.Millisecond)
	base := startTestServer(t, s)

	postJSON(t, http.MethodPost, base+"/register",
		map[string]string{"name": "order-svc", "addr": "http://order-svc:9001"})
	time.Sleep(200 * time.Millisecond)
	postJSON(t, http.MethodPut, base+"/heartbeat",
		map[string]string{"name": "order-svc", "addr": "http://order-svc:9001"})
	time.Sleep(200 * time.Millisecond) // 续约后 200ms < TTL，仍在

	if got := s.Instances(); len(got) != 1 {
		t.Fatalf("续约后不应被摘除，got %+v", got)
	}

	// 停止续约 → 扫描循环在 TTL 后自动摘除
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.Instances()) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("停止续约后实例应在 TTL 内被自动摘除")
}

// TestRegistryComponent hotswap.Component 生命周期
func TestRegistryComponent(t *testing.T) {
	cfg := newFakeConfMgr()
	r := New(cfg)
	if r.Name() != "registry" {
		t.Errorf("Name=%q，want registry", r.Name())
	}
	if r.State() != hotswap.StateDisabled {
		t.Errorf("初始 State=%v，want disabled", r.State())
	}
	if r.Server() != nil {
		t.Error("未启动时 Server() 应为 nil")
	}

	r.SetAddr("127.0.0.1:0")
	r.SetTTL(500 * time.Millisecond)
	if err := r.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	if r.State() != hotswap.StateEnabled {
		t.Errorf("Start 后 State=%v，want enabled", r.State())
	}
	if r.Server() == nil {
		t.Fatal("Start 后 Server() 不应为 nil")
	}
	// 幂等：重复 Start 不报错
	if err := r.Start(nil); err != nil {
		t.Errorf("重复 Start err: %v", err)
	}

	// 经组件 Server 注册实例 → conf 联动
	base := "http://" + r.Server().Addr()
	postJSON(t, http.MethodPost, base+"/register",
		map[string]string{"name": "order-svc", "addr": "http://order-svc:9001"})
	if got := cfg.value("DISPATCH_RULES"); !strings.Contains(got, "/api/order-svc/=http://order-svc:9001") {
		t.Errorf("组件联动 DISPATCH_RULES=%q，want 含 order-svc 规则", got)
	}

	if err := r.Stop(); err != nil {
		t.Fatalf("Stop err: %v", err)
	}
	if r.State() != hotswap.StateDisabled {
		t.Errorf("Stop 后 State=%v，want disabled", r.State())
	}
	if r.Server() != nil {
		t.Error("Stop 后 Server() 应为 nil")
	}
	// 幂等：重复 Stop 不报错
	if err := r.Stop(); err != nil {
		t.Errorf("重复 Stop err: %v", err)
	}
}

// TestRegistryComponent_StaticPath 静态实例随组件启动发布并联动 dispatch
func TestRegistryComponent_StaticPath(t *testing.T) {
	cfg := newFakeConfMgr()
	path := writeTemp(t, "instances.yaml", "instances:\n  - name: pay-svc\n    addr: http://pay-svc:9003\n")
	r := New(cfg)
	r.SetAddr("127.0.0.1:0")
	r.SetStaticPath(path)

	if err := r.Start(nil); err != nil {
		t.Fatalf("Start err: %v", err)
	}
	defer func() { _ = r.Stop() }()

	got := r.Server().Instances()
	if len(got) != 1 || got[0].Name != "pay-svc" || !got[0].Static {
		t.Fatalf("静态实例未加载，got %+v", got)
	}
	if got := cfg.value("DISPATCH_RULES"); !strings.Contains(got, "/api/pay-svc/=http://pay-svc:9003") {
		t.Errorf("静态实例未联动 DISPATCH_RULES，got %q", got)
	}

	// 静态实例不参与心跳过期摘除
	r.Server().SetTTL(100 * time.Millisecond)
	time.Sleep(250 * time.Millisecond)
	if got := r.Server().Instances(); len(got) != 1 {
		t.Errorf("静态实例不应被摘除，got %+v", got)
	}
}
