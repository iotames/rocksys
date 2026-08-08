// Package registry 见 doc.go。本文件实现 RockRegistry：服务注册与发现。
// 依据 DEV_HANDBOOK.md 第 17 章实现 StaticTable/Server/Watcher + hotswap.Component。
// 与 dispatch 的联动经 conf 配置热更通道完成（写 DISPATCH_RULES），不直接依赖 dispatch。
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/iotames/easyserver/log"

	"rocksys/internal/conf"
	"rocksys/internal/hotswap"
)

const (
	// DefaultTTL 心跳超时：30s 未续约自动摘除（第 17 章验收标准）。
	DefaultTTL = 30 * time.Second
	// DefaultAddr 内置注册服务默认监听地址。
	DefaultAddr = ":9800"
	// rulesKey 联动 dispatch 的配置项注册名（与 plugins/dispatch 保持一致）。
	rulesKey = "DISPATCH_RULES"
)

// Instance 注册表中的一个服务实例。
type Instance struct {
	Name          string    `json:"name" yaml:"name"` // 服务名（如 order-svc）
	Addr          string    `json:"addr" yaml:"addr"` // 实例地址 http://host:port
	Healthy       bool      `json:"healthy"`          // 是否健康（心跳周期内；静态实例恒真）
	LastHeartbeat time.Time `json:"-" yaml:"-"`       // 最近一次心跳时间
	Static        bool      `json:"-" yaml:"-"`       // 静态实例：不参与心跳过期摘除
}

// instKey 实例唯一键：服务名 + 地址（同名服务多副本各自独立注册）。
func instKey(name, addr string) string { return name + "|" + addr }

// Watcher 实例变更通知：订阅回调 func(instances []Instance)（第 17 章）。
type Watcher struct {
	mu  sync.Mutex
	cbs []func([]Instance)
}

// NewWatcher 创建空 Watcher。
func NewWatcher() *Watcher { return &Watcher{} }

// Watch 注册实例变更回调（fn 为 nil 时忽略）。
func (w *Watcher) Watch(fn func([]Instance)) {
	if fn == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cbs = append(w.cbs, fn)
}

// notify 向所有回调广播实例列表快照（同步、按注册顺序；调用方不得持有实例表锁）。
func (w *Watcher) notify(instances []Instance) {
	w.mu.Lock()
	cbs := append([]func([]Instance){}, w.cbs...)
	w.mu.Unlock()
	if len(cbs) == 0 {
		return
	}
	snap := append([]Instance{}, instances...)
	for _, fn := range cbs {
		fn(snap)
	}
}

// StaticTable 从 YAML/JSON 文件加载的静态实例列表（默认实现，第 17 章）。
//
// 文件格式（YAML 或等价 JSON）：
//
//	instances:
//	  - name: order-svc
//	    addr: http://order-svc:9001
//
// 解析失败视为空表（不阻断组件启动），外部文件变更后可调用 Reload 刷新。
type StaticTable struct {
	path      string
	mu        sync.RWMutex
	instances []Instance
}

// NewStaticTable 从 path 加载静态实例列表；path 为空或解析失败 → 空表。
func NewStaticTable(path string) *StaticTable {
	t := &StaticTable{path: path}
	t.reload()
	return t
}

// Instances 返回实例列表快照（仅含静态文件中的 Name/Addr）。
func (t *StaticTable) Instances() []Instance {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]Instance{}, t.instances...)
}

// Reload 重新加载文件（解析失败保留旧表）。
func (t *StaticTable) Reload() { t.reload() }

func (t *StaticTable) reload() {
	list, ok := parseInstancesFile(t.path)
	if !ok {
		return // 读取/解析失败保留旧表（构造时旧表为空 → 空表）
	}
	t.mu.Lock()
	t.instances = list
	t.mu.Unlock()
}

// parseInstancesFile 读取并解析静态实例文件；返回加载的实例列表与是否成功。
// YAML 是 JSON 的超集，优先用 YAML 解析，失败再尝试 JSON。
func parseInstancesFile(path string) ([]Instance, bool) {
	if path == "" {
		return nil, false
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var doc struct {
		Instances []Instance `yaml:"instances" json:"instances"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		if err2 := json.Unmarshal(content, &doc); err2 != nil {
			return nil, false
		}
	}
	list := make([]Instance, 0, len(doc.Instances))
	for _, inst := range doc.Instances {
		if strings.TrimSpace(inst.Name) == "" || strings.TrimSpace(inst.Addr) == "" {
			continue
		}
		list = append(list, inst)
	}
	return list, true
}

// buildRules 将实例列表转为 DISPATCH_RULES 格式字符串：<Prefix>=<Upstream>，逗号分隔。
// 每个服务名保留心跳最新的健康实例；Prefix 约定 /api/<name>/（第 17 章）。
func buildRules(instances []Instance) string {
	best := make(map[string]*Instance)
	for i := range instances {
		inst := &instances[i]
		if !inst.Healthy {
			continue
		}
		if cur := best[inst.Name]; cur == nil || inst.LastHeartbeat.After(cur.LastHeartbeat) {
			best[inst.Name] = inst
		}
	}
	names := make([]string, 0, len(best))
	for n := range best {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, "/api/"+n+"/="+best[n].Addr)
	}
	return strings.Join(parts, ",")
}

// Server 内置轻量注册服务（标准库 http，第 17 章）。
// 提供 POST /register 注册实例、PUT /heartbeat 心跳续约；
// 心跳超时（默认 30s）未续约自动摘除（后台 goroutine 扫描）。
type Server struct {
	addr      string
	ttl       time.Duration
	rulesKey  string
	cfgMgr    conf.Manager // 可空；实例变更时经 conf.Set 联动 dispatch
	watcher   *Watcher
	mu        sync.RWMutex
	instances map[string]*Instance
	httpSrv   *http.Server
	ln        net.Listener
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	started   bool
}

// NewServer 创建注册服务。addr 为监听地址（如 ":9800"，测试可用 "127.0.0.1:0"）。
func NewServer(addr string) *Server {
	return &Server{
		addr:      addr,
		ttl:       DefaultTTL,
		rulesKey:  rulesKey,
		watcher:   NewWatcher(),
		instances: make(map[string]*Instance),
	}
}

// SetTTL 设置心跳超时（0 或负数恢复默认 30s）。
func (s *Server) SetTTL(d time.Duration) {
	if d <= 0 {
		d = DefaultTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ttl = d
}

// SetConfMgr 绑定 conf.Manager：实例变更时经 conf.Set(rulesKey) 联动 dispatch（第 17 章）。
// 可为 nil（纯注册，不联动）。
func (s *Server) SetConfMgr(m conf.Manager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfgMgr = m
}

// SetRulesKey 设置联动配置项名（默认 DISPATCH_RULES）。
func (s *Server) SetRulesKey(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != "" {
		s.rulesKey = key
	}
}

// Watch 注册实例变更回调。
func (s *Server) Watch(fn func([]Instance)) { s.watcher.Watch(fn) }

// Instances 返回当前实例列表快照（按名称/地址排序，输出确定性）。
func (s *Server) Instances() []Instance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]Instance, 0, len(s.instances))
	for _, inst := range s.instances {
		list = append(list, *inst)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Name != list[j].Name {
			return list[i].Name < list[j].Name
		}
		return list[i].Addr < list[j].Addr
	})
	return list
}

// Addr 返回实际监听地址（Start 成功后可用，含随机分配的端口）。
func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.addr
}

// Handler 返回 HTTP 处理路由（便于 httptest 或自定义装配）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /register", s.handleRegister)
	mux.HandleFunc("PUT /heartbeat", s.handleHeartbeat)
	return mux
}

// Start 启动 HTTP 监听与心跳过期扫描（非阻塞）。返回后可用 Addr() 取实际地址。
func (s *Server) Start() error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("registry: server already started")
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("registry: listen %s: %w", s.addr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.ln = ln
	s.cancel = cancel
	s.started = true
	srv := &http.Server{Handler: s.Handler()}
	s.httpSrv = srv
	s.mu.Unlock()

	s.wg.Add(2)
	go s.serve(ln, srv)
	go s.scanLoop(ctx)
	return nil
}

// serve 阻塞托管 HTTP 服务；Stop 的 Shutdown 使其返回（正常关闭返回 http.ErrServerClosed 不告警）。
func (s *Server) serve(ln net.Listener, srv *http.Server) {
	defer s.wg.Done()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("registry: 注册服务退出", "err", err)
	}
}

// scanLoop 心跳过期扫描：定时调用 scanOnce，直至 ctx 取消。
func (s *Server) scanLoop(ctx context.Context) {
	defer s.wg.Done()
	s.mu.RLock()
	ttl := s.ttl
	s.mu.RUnlock()
	interval := ttl / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	if interval > time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanOnce()
		}
	}
}

// scanOnce 扫描一次：摘除心跳超时（>ttl 未续约）的动态实例，变更则发布。
func (s *Server) scanOnce() {
	now := time.Now()
	var removed int
	s.mu.Lock()
	for key, inst := range s.instances {
		if inst.Static {
			continue
		}
		if now.Sub(inst.LastHeartbeat) > s.ttl {
			delete(s.instances, key)
			removed++
		}
	}
	s.mu.Unlock()
	if removed > 0 {
		s.publish()
	}
}

// Stop 停止 HTTP 监听与扫描 goroutine（阻塞直到退出；幂等）。
func (s *Server) Stop() error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	cancel := s.cancel
	srv := s.httpSrv
	s.started = false
	s.cancel = nil
	s.httpSrv = nil
	s.ln = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	ctx, closeFn := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeFn()
	_ = srv.Shutdown(ctx)
	s.wg.Wait()
	return nil
}

// handleRegister POST /register：注册（或续约）实例，请求体
// {"name":"order-svc","addr":"http://order-svc:9001"}。
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	name, addr, err := decodeNameAddr(r)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	s.Register(Instance{Name: name, Addr: addr, Healthy: true, LastHeartbeat: time.Now()})
	writeAPI(w, http.StatusOK, "ok", map[string]any{"name": name, "addr": addr})
}

// handleHeartbeat PUT /heartbeat：心跳续约；实例不存在则自动注册（幂等 upsert）。
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	name, addr, err := decodeNameAddr(r)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	s.Heartbeat(name, addr)
	writeAPI(w, http.StatusOK, "ok", map[string]any{"name": name, "addr": addr})
}

// Register 注册（或续约）实例；返回是否新增。新增触发实例变更发布。
func (s *Server) Register(inst Instance) bool {
	return s.register(inst, true)
}

// register 内部注册；notify 为 true 且实例表结构变更时发布变更。
func (s *Server) register(inst Instance, notify bool) bool {
	if inst.Static {
		inst.Healthy = true
	}
	if inst.LastHeartbeat.IsZero() {
		inst.LastHeartbeat = time.Now()
	}
	s.mu.Lock()
	key := instKey(inst.Name, inst.Addr)
	cur, exists := s.instances[key]
	if exists {
		cur.Healthy = inst.Healthy
		cur.LastHeartbeat = inst.LastHeartbeat
	} else {
		cp := inst
		s.instances[key] = &cp
	}
	added := !exists
	s.mu.Unlock()
	if notify && added {
		s.publish()
	}
	return added
}

// Heartbeat 心跳续约；实例不存在则自动注册。返回是否新增。
func (s *Server) Heartbeat(name, addr string) bool {
	return s.Register(Instance{Name: name, Addr: addr, Healthy: true, LastHeartbeat: time.Now()})
}

// Remove 手动摘除实例；返回是否摘除成功。摘除触发实例变更发布。
func (s *Server) Remove(name, addr string) bool {
	s.mu.Lock()
	key := instKey(name, addr)
	_, exists := s.instances[key]
	if exists {
		delete(s.instances, key)
	}
	s.mu.Unlock()
	if exists {
		s.publish()
	}
	return exists
}

// publish 发布实例变更：通知 Watcher 回调 + 联动 conf.Manager（写 DISPATCH_RULES）。
// 调用方不得持有实例表锁。
func (s *Server) publish() {
	list := s.Instances()
	s.watcher.notify(list)
	s.mu.RLock()
	mgr := s.cfgMgr
	key := s.rulesKey
	s.mu.RUnlock()
	if mgr != nil {
		_ = mgr.Set(key, buildRules(list))
	}
}

// decodeNameAddr 解析注册/心跳请求体，返回 name/addr；缺失或非法返回 error。
func decodeNameAddr(r *http.Request) (string, string, error) {
	var req struct {
		Name string `json:"name"`
		Addr string `json:"addr"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err := json.Unmarshal(body, &req); err != nil {
		return "", "", errors.New("请求体不是合法 JSON: " + err.Error())
	}
	if strings.TrimSpace(req.Name) == "" {
		return "", "", errors.New("name 不能为空")
	}
	if strings.TrimSpace(req.Addr) == "" {
		return "", "", errors.New("addr 不能为空")
	}
	return req.Name, req.Addr, nil
}

// apiResp 统一响应格式 {code, msg, data}（契约第 20 章）。
type apiResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

// writeAPI 写出统一格式响应。
func writeAPI(w http.ResponseWriter, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(apiResp{Code: code, Msg: msg, Data: data})
}

// Registry hotswap 独立组件（第 17 章）。启动内置注册服务，并把最新实例列表
// 持续写入 conf.DISPATCH_RULES 联动 dispatch。不挂 chain。
type Registry struct {
	cfgMgr     conf.Manager
	addr       string
	ttl        time.Duration
	staticPath string
	mu         sync.Mutex
	server     *Server
	state      atomic.Value // 持有 hotswap.State
}

// 编译期断言：Registry 实现 hotswap.Component。
var _ hotswap.Component = (*Registry)(nil)

// New 创建 registry 组件（默认 StateDisabled，由 hotswap.Enable 触发 Start）。
// 装配期注册 REGISTRY_ADDR/REGISTRY_TTL/REGISTRY_STATIC_FILE 三项配置（cfgMgr 非 nil 时），
// 注册后从配置读取当前值；SetAddr/SetTTL/SetStaticPath 仍可在 Start 前覆盖。
func New(cfgMgr conf.Manager) *Registry {
	r := &Registry{
		cfgMgr: cfgMgr,
		addr:   DefaultAddr,
		ttl:    DefaultTTL,
	}
	if cfgMgr != nil {
		var ttlSec int
		if err := cfgMgr.Register(&r.addr, "REGISTRY_ADDR", DefaultAddr, "注册服务监听地址", "装配期生效，热更后需重启"); err != nil {
			log.Warn("registry: 注册配置项失败", "name", "REGISTRY_ADDR", "err", err)
		}
		if err := cfgMgr.Register(&ttlSec, "REGISTRY_TTL", "30", "心跳超时(秒)", "装配期生效，热更后需重启"); err != nil {
			log.Warn("registry: 注册配置项失败", "name", "REGISTRY_TTL", "err", err)
		} else if ttlSec > 0 {
			r.ttl = time.Duration(ttlSec) * time.Second
		}
		if err := cfgMgr.Register(&r.staticPath, "REGISTRY_STATIC_FILE", "", "静态实例文件路径（YAML/JSON，空=无静态实例）", "装配期生效，热更后需重启"); err != nil {
			log.Warn("registry: 注册配置项失败", "name", "REGISTRY_STATIC_FILE", "err", err)
		}
	}
	r.state.Store(hotswap.StateDisabled)
	return r
}

// SetAddr 设置内置注册服务监听地址（Start 前调用）。
func (r *Registry) SetAddr(addr string) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if addr != "" {
		r.addr = addr
	}
	return r
}

// SetTTL 设置心跳超时（Start 前调用）。
func (r *Registry) SetTTL(d time.Duration) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d > 0 {
		r.ttl = d
	}
	return r
}

// SetStaticPath 设置静态实例文件路径（Start 前调用；空 = 无静态实例）。
func (r *Registry) SetStaticPath(path string) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.staticPath = path
	return r
}

// Name 返回组件名：registry。
func (r *Registry) Name() string { return "registry" }

// Start 启动内置注册服务与心跳扫描，加载静态实例并发布初始实例列表（幂等）。
func (r *Registry) Start(_ any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Load() == hotswap.StateEnabled {
		return nil // 幂等：已启动
	}
	table := NewStaticTable(r.staticPath)
	srv := NewServer(r.addr)
	srv.SetTTL(r.ttl)
	srv.SetConfMgr(r.cfgMgr)
	for _, inst := range table.Instances() {
		srv.register(Instance{
			Name: inst.Name, Addr: inst.Addr,
			Healthy: true, LastHeartbeat: time.Now(), Static: true,
		}, false)
	}
	if err := srv.Start(); err != nil {
		return err
	}
	srv.publish() // 发布初始实例列表（静态实例 + 空动态表）
	r.server = srv
	r.state.Store(hotswap.StateEnabled)
	return nil
}

// Stop 停止注册服务与扫描 goroutine，置 Disabled（幂等）。
func (r *Registry) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Load() == hotswap.StateDisabled {
		return nil // 幂等：已停止
	}
	var err error
	if r.server != nil {
		err = r.server.Stop()
		r.server = nil
	}
	r.state.Store(hotswap.StateDisabled)
	return err
}

// State 返回组件自身状态。
func (r *Registry) State() hotswap.State {
	return r.state.Load().(hotswap.State)
}

// Server 返回当前注册服务实例（未启动返回 nil）。
func (r *Registry) Server() *Server {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.server
}
