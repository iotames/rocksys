package hotswap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
)

// fakeConfMgr 最小 conf.Manager 桩：记录 watcher，支持手动广播（异步调用，模拟 conf 行为）。
type fakeConfMgr struct {
	mu       sync.Mutex
	watchers []func(*conf.Config)
	cfg      *conf.Config
}

func (f *fakeConfMgr) Current() *conf.Config              { return f.cfg }
func (f *fakeConfMgr) StartWatcher() error                { return nil }
func (f *fakeConfMgr) Shutdown(ctx context.Context) error { return nil }
func (f *fakeConfMgr) Register(any, string, string, string, ...string) error {
	return nil
}
func (f *fakeConfMgr) Set(string, string) error { return nil }
func (f *fakeConfMgr) List() []conf.ConfigItem  { return nil }
func (f *fakeConfMgr) SyncDefaultFile() error   { return nil }

func (f *fakeConfMgr) Watch(fn func(*conf.Config)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watchers = append(f.watchers, fn)
}

// broadcast 手动触发配置热更：回调在独立 goroutine 执行（与 conf.impl publish 一致）。
func (f *fakeConfMgr) broadcast(cfg *conf.Config) {
	f.mu.Lock()
	ws := append([]func(*conf.Config){}, f.watchers...)
	f.mu.Unlock()
	f.cfg = cfg
	for _, fn := range ws {
		go fn(cfg)
	}
}

// fakeMiddleware 可计数的 MiddlewareLifecycle 桩。
type fakeMiddleware struct {
	name     string
	slot     chain.Slot
	started  atomic.Int32
	stopped  atomic.Int32
	handled  atomic.Int32
	startErr error
	stopErr  error
}

func (f *fakeMiddleware) Name() string { return f.name }
func (f *fakeMiddleware) Handle(ctx *chain.Context) bool {
	f.handled.Add(1)
	return true
}
func (f *fakeMiddleware) Start(cfg any) error {
	f.started.Add(1)
	return f.startErr
}
func (f *fakeMiddleware) Stop() error {
	f.stopped.Add(1)
	return f.stopErr
}
func (f *fakeMiddleware) Slot() chain.Slot { return f.slot }

// fakeComponent 可计数的 Component 桩：状态自行持有。
type fakeComponent struct {
	name     string
	state    atomic.Int32
	started  atomic.Int32
	stopped  atomic.Int32
	startErr error
	stopErr  error
}

func (f *fakeComponent) Name() string { return f.name }
func (f *fakeComponent) Start(cfg any) error {
	f.started.Add(1)
	if f.startErr != nil {
		return f.startErr
	}
	f.state.Store(int32(StateEnabled))
	return nil
}
func (f *fakeComponent) Stop() error {
	f.stopped.Add(1)
	f.state.Store(int32(StateDisabled))
	return f.stopErr
}
func (f *fakeComponent) State() State { return State(f.state.Load()) }

func newTestCtx() *chain.Context {
	return &chain.Context{
		W: httptest.NewRecorder(),
		R: httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil),
	}
}

// findStatus 在 List 结果中按名称查找实体状态。
func findStatus(t *testing.T, statuses []Status, name string) Status {
	t.Helper()
	for _, s := range statuses {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("List 中未找到实体 %q", name)
	return Status{}
}

// waitUntil 轮询等待条件成立（最多 3s），用于异步热更断言。
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// 中间件生命周期：Register → Enable（挂载+Enabled）→ Disable（摘除+Disabled）。
func TestMiddlewareLifecycle(t *testing.T) {
	ch := chain.New()
	mgr := NewManager(ch, &fakeConfMgr{})
	ml := &fakeMiddleware{name: "shield", slot: chain.Middle}
	mgr.RegisterMiddleware(ml)

	if mgr.GetMiddleware("shield") != ml {
		t.Fatal("GetMiddleware 应返回已注册实例")
	}

	if err := mgr.Enable("shield"); err != nil {
		t.Fatalf("Enable err: %v", err)
	}
	if ml.started.Load() != 1 {
		t.Errorf("Start 应被调用 1 次，得到 %d", ml.started.Load())
	}
	if st := findStatus(t, mgr.List(), "shield"); st.State != StateEnabled {
		t.Errorf("Enable 后状态应 Enabled，得到 %s", st.State)
	}

	// 链上存在：Execute 会调用其 Handle
	ch.Execute(newTestCtx())
	if ml.handled.Load() != 1 {
		t.Errorf("Enable 后链上应存在该中间件，Handle 被调 1 次，得到 %d", ml.handled.Load())
	}

	// 幂等：重复 Enable 不重复挂载
	if err := mgr.Enable("shield"); err != nil {
		t.Fatalf("重复 Enable err: %v", err)
	}
	if ml.started.Load() != 1 {
		t.Errorf("重复 Enable 不应再次 Start，得到 %d", ml.started.Load())
	}
	ch.Execute(newTestCtx())
	if ml.handled.Load() != 2 {
		t.Errorf("重复 Enable 不应重复挂载，Handle 应只增 1 次，得到 %d", ml.handled.Load())
	}

	if err := mgr.Disable("shield"); err != nil {
		t.Fatalf("Disable err: %v", err)
	}
	if ml.stopped.Load() != 1 {
		t.Errorf("Stop 应被调用 1 次，得到 %d", ml.stopped.Load())
	}
	if st := findStatus(t, mgr.List(), "shield"); st.State != StateDisabled {
		t.Errorf("Disable 后状态应 Disabled，得到 %s", st.State)
	}

	// 链上已摘除：Execute 不再调用其 Handle
	ch.Execute(newTestCtx())
	if ml.handled.Load() != 2 {
		t.Errorf("Disable 后链上不应再有该中间件，Handle 应保持 2 次，得到 %d", ml.handled.Load())
	}
}

// Enable 失败（Start 返回 error）：状态保持 Disabled、chain 无该中间件、返回 error。
func TestMiddlewareEnableFailure(t *testing.T) {
	ch := chain.New()
	mgr := NewManager(ch, &fakeConfMgr{})
	ml := &fakeMiddleware{name: "shield", slot: chain.Middle, startErr: errors.New("boom")}
	mgr.RegisterMiddleware(ml)

	if err := mgr.Enable("shield"); err == nil {
		t.Fatal("Start 失败时 Enable 应返回 error")
	}
	if ml.started.Load() != 1 {
		t.Errorf("Start 应被调用 1 次，得到 %d", ml.started.Load())
	}
	if mgr.middlewareStates["shield"] != StateDisabled {
		t.Errorf("Enable 失败后状态应保持 Disabled，得到 %s", mgr.middlewareStates["shield"])
	}
	// chain 无该中间件
	ch.Execute(newTestCtx())
	if ml.handled.Load() != 0 {
		t.Errorf("Enable 失败后链上不应有该中间件，Handle 应为 0，得到 %d", ml.handled.Load())
	}
}

// List 返回全部实体状态（component/middleware、kind、名称排序）。
func TestList(t *testing.T) {
	ch := chain.New()
	mgr := NewManager(ch, &fakeConfMgr{})
	mgr.RegisterMiddleware(&fakeMiddleware{name: "shield", slot: chain.Middle})
	mgr.RegisterMiddleware(&fakeMiddleware{name: "obs", slot: chain.Tail})
	mgr.RegisterComponent(&fakeComponent{name: "config"})

	_ = mgr.Enable("obs")
	_ = mgr.Enable("config")

	statuses := mgr.List()
	if len(statuses) != 3 {
		t.Fatalf("List 应返回 3 条，得到 %d", len(statuses))
	}
	shield := findStatus(t, statuses, "shield")
	if shield.Kind != "middleware" || shield.State != StateDisabled {
		t.Errorf("shield 应为 middleware/disabled，得到 %s/%s", shield.Kind, shield.State)
	}
	obs := findStatus(t, statuses, "obs")
	if obs.Kind != "middleware" || obs.State != StateEnabled {
		t.Errorf("obs 应为 middleware/enabled，得到 %s/%s", obs.Kind, obs.State)
	}
	config := findStatus(t, statuses, "config")
	if config.Kind != "component" || config.State != StateEnabled {
		t.Errorf("config 应为 component/enabled，得到 %s/%s", config.Kind, config.State)
	}
	if config.StartedAt.IsZero() || config.LastSwitchAt.IsZero() {
		t.Errorf("启用后 StartedAt/LastSwitchAt 不应为零值")
	}
}

// 组件生命周期：Register → Enable（内部置 Enabled）→ Disable（内部置 Disabled）。
func TestComponentLifecycle(t *testing.T) {
	ch := chain.New()
	mgr := NewManager(ch, &fakeConfMgr{})
	comp := &fakeComponent{name: "config"}
	mgr.RegisterComponent(comp)

	if mgr.GetComponent("config") != comp {
		t.Fatal("GetComponent 应返回已注册实例")
	}

	if err := mgr.Enable("config"); err != nil {
		t.Fatalf("Enable err: %v", err)
	}
	if comp.started.Load() != 1 || comp.State() != StateEnabled {
		t.Errorf("Enable 后 started=1, state=enabled，得到 %d/%s", comp.started.Load(), comp.State())
	}

	if err := mgr.Disable("config"); err != nil {
		t.Fatalf("Disable err: %v", err)
	}
	if comp.stopped.Load() != 1 || comp.State() != StateDisabled {
		t.Errorf("Disable 后 stopped=1, state=disabled，得到 %d/%s", comp.stopped.Load(), comp.State())
	}
}

// 组件 Enable 失败：状态保持 Disabled、返回 error。
func TestComponentEnableFailure(t *testing.T) {
	ch := chain.New()
	mgr := NewManager(ch, &fakeConfMgr{})
	comp := &fakeComponent{name: "config", startErr: errors.New("boom")}
	mgr.RegisterComponent(comp)

	if err := mgr.Enable("config"); err == nil {
		t.Fatal("Start 失败时 Enable 应返回 error")
	}
	if comp.State() != StateDisabled {
		t.Errorf("Enable 失败后组件状态应保持 Disabled，得到 %s", comp.State())
	}
}

// 热更：Watcher 触发后仅 Enabled 实体被调用 Start（Disabled 实体不响应）。
func TestHotReload(t *testing.T) {
	cfgMgr := &fakeConfMgr{}
	ch := chain.New()
	mgr := NewManager(ch, cfgMgr)

	ml := &fakeMiddleware{name: "shield", slot: chain.Middle}
	mgr.RegisterMiddleware(ml)
	comp := &fakeComponent{name: "config"}
	mgr.RegisterComponent(comp)
	off := &fakeMiddleware{name: "off", slot: chain.Tail} // 始终不启用
	mgr.RegisterMiddleware(off)

	_ = mgr.Enable("shield")
	_ = mgr.Enable("config")

	cfgMgr.broadcast(&conf.Config{})
	waitUntil(t, func() bool { return ml.started.Load() == 2 && comp.started.Load() == 2 })

	if off.started.Load() != 0 {
		t.Errorf("Disabled 实体不应响应热更，Start 应为 0，得到 %d", off.started.Load())
	}
	if st := findStatus(t, mgr.List(), "shield"); st.State != StateEnabled {
		t.Errorf("热更后中间件应保持 Enabled，得到 %s", st.State)
	}

	// 热更失败：保留旧快照、实例仍挂载、状态不变
	ml.startErr = errors.New("reload boom")
	cfgMgr.broadcast(&conf.Config{})
	waitUntil(t, func() bool { return ml.started.Load() == 3 })
	if st := findStatus(t, mgr.List(), "shield"); st.State != StateEnabled {
		t.Errorf("热更失败后中间件应保持 Enabled（旧快照继续服务），得到 %s", st.State)
	}
}

// 排空：注入 drainCheck 后，Disable 等待活跃计数归零才 Stop。
func TestDrainWaiting(t *testing.T) {
	ch := chain.New()
	mgr := NewManager(ch, &fakeConfMgr{})
	ml := &fakeMiddleware{name: "shield", slot: chain.Middle}
	mgr.RegisterMiddleware(ml)

	var active atomic.Int64
	active.Store(1)
	mgr.SetDrainCheck(func() int64 { return active.Load() })
	_ = mgr.Enable("shield")

	done := make(chan error, 1)
	go func() { done <- mgr.Disable("shield") }()

	// 活跃请求未归零前不应 Stop
	time.Sleep(150 * time.Millisecond)
	if ml.stopped.Load() != 0 {
		t.Fatalf("排空未完成前不应调用 Stop")
	}
	active.Store(0)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Disable err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("活跃计数归零后 Disable 应返回")
	}
	if ml.stopped.Load() != 1 {
		t.Errorf("排空完成后 Stop 应被调用 1 次，得到 %d", ml.stopped.Load())
	}
	if mgr.middlewareStates["shield"] != StateDisabled {
		t.Errorf("Disable 后状态应 Disabled，得到 %s", mgr.middlewareStates["shield"])
	}
}

// 未注入 drainCheck 时 Disable 跳过排空等待，直接 Stop。
func TestDrainCheckNotInjected(t *testing.T) {
	ch := chain.New()
	mgr := NewManager(ch, &fakeConfMgr{})
	ml := &fakeMiddleware{name: "shield", slot: chain.Middle}
	mgr.RegisterMiddleware(ml)
	_ = mgr.Enable("shield")

	start := time.Now()
	if err := mgr.Disable("shield"); err != nil {
		t.Fatalf("Disable err: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("未注入 drainCheck 时 Disable 不应阻塞等待排空")
	}
	if ml.stopped.Load() != 1 {
		t.Errorf("Stop 应被调用 1 次，得到 %d", ml.stopped.Load())
	}
}

// 实体不存在时 Enable/Disable 返回 error。
func TestEntityNotFound(t *testing.T) {
	ch := chain.New()
	mgr := NewManager(ch, &fakeConfMgr{})
	if err := mgr.Enable("nope"); err == nil {
		t.Error("Enable 不存在的实体应返回 error")
	}
	if err := mgr.Disable("nope"); err == nil {
		t.Error("Disable 不存在的实体应返回 error")
	}
}
