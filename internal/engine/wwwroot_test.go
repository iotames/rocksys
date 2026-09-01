package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"rocksys/internal/chain"
	"rocksys/internal/conf"
)

// stubConfMgr 测试用 conf.Manager 桩：只实现 Current/Watch，其余为空实现。
type stubConfMgr struct {
	cfg      *conf.Config
	watchers []func(*conf.Config)
}

func (m *stubConfMgr) Current() *conf.Config              { return m.cfg }
func (m *stubConfMgr) Watch(fn func(*conf.Config))        { m.watchers = append(m.watchers, fn) }
func (m *stubConfMgr) StartWatcher() error                { return nil }
func (m *stubConfMgr) Shutdown(ctx context.Context) error { return nil }
func (m *stubConfMgr) Register(pval any, name, defval, title string, usage ...string) error {
	return nil
}
func (m *stubConfMgr) SyncDefaultFile() error                { return nil }
func (m *stubConfMgr) Set(name, value string) error { return nil }
func (m *stubConfMgr) List() []conf.ConfigItem      { return nil }

// notify 同步触发全部 watcher（模拟配置热更广播）
func (m *stubConfMgr) notify(cfg *conf.Config) {
	m.cfg = cfg
	for _, fn := range m.watchers {
		fn(cfg)
	}
}

// newWWWRootEngine 构造带 WWWROOT 配置的引擎
func newWWWRootEngine(t *testing.T) (*Engine, string, *stubConfMgr) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello rocksys"), 0644); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(dir, "404.html")
	if err := os.WriteFile(page, []byte("<h1>custom not found</h1>"), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := &stubConfMgr{cfg: &conf.Config{
		ListenAddr:      ":0",
		DefaultUpstream: "", // 不配默认上游：未命中请求回落 wwwroot
		WWWRoot:         dir,
		NotFoundPage:    page,
	}}
	e := New(mgr, chain.New())
	return e, dir, mgr
}

func TestEngineWWWRootFallback(t *testing.T) {
	e, _, mgr := newWWWRootTestEngine(t)
	req := func(method, target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		w := httptest.NewRecorder()
		e.server.ServeHTTP(w, r)
		return w
	}

	t.Run("未命中回落wwwroot文件", func(t *testing.T) {
		w := req(http.MethodGet, "/hello.txt")
		if w.Code != http.StatusOK || w.Body.String() != "hello rocksys" {
			t.Fatalf("code=%d body=%q", w.Code, w.Body.String())
		}
	})

	t.Run("兜底未命中走自定义404页面", func(t *testing.T) {
		w := req(http.MethodGet, "/no-such")
		if w.Code != http.StatusNotFound || w.Body.String() != "<h1>custom not found</h1>" {
			t.Fatalf("code=%d body=%q", w.Code, w.Body.String())
		}
	})

	t.Run("配置热更切换兜底目录", func(t *testing.T) {
		mgr.notify(&conf.Config{ListenAddr: ":0", WWWRoot: "", NotFoundPage: ""})
		w := req(http.MethodGet, "/hello.txt")
		if w.Body.String() == "hello rocksys" {
			t.Fatal("关闭 WWWROOT 后不应再返回兜底文件")
		}
		if w.Code != http.StatusOK {
			t.Fatalf("关闭自定义404后应回退默认 JSON 404（现状语义，隐式200），got %d", w.Code)
		}
		if w.Body.String() == "" {
			t.Fatal("默认 404 响应体不应为空")
		}
	})
}

// newWWWRootTestEngine 与 newWWWRootEngine 相同（保留独立命名便于后续扩展差异用例）
func newWWWRootTestEngine(t *testing.T) (*Engine, string, *stubConfMgr) {
	return newWWWRootEngine(t)
}

// TestEngineUpstreamStillForwarded 配置了默认上游时行为不变：未命中请求照常转发，不回落 wwwroot
func TestEngineUpstreamStillForwarded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello rocksys"), 0644); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("from upstream"))
	}))
	defer upstream.Close()

	mgr := &stubConfMgr{cfg: &conf.Config{
		ListenAddr:      ":0",
		DefaultUpstream: upstream.URL,
		WWWRoot:         dir,
	}}
	e := New(mgr, chain.New())

	r := httptest.NewRequest(http.MethodGet, "/hello.txt", nil) // 与 wwwroot 文件同名
	w := httptest.NewRecorder()
	e.server.ServeHTTP(w, r)
	if w.Body.String() != "from upstream" {
		t.Fatalf("配置了默认上游时应照常转发，got %q", w.Body.String())
	}
}
