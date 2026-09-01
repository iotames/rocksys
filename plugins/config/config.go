// Package config 见 doc.go。本文件实现 RockConfig：KV 配置服务。
// KVStore SPI 用于替换后端，FileStore 为基于
// easyconf 的本地文件默认实现，Config 为 hotswap 独立组件（不挂 chain）。
package config

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/iotames/easyconf"

	"rocksys/internal/conf"
	"rocksys/internal/hotswap"
)

// ChangeEvent KV 变更事件：Key 变更的键，Value 变更后的值。
type ChangeEvent struct {
	Key   string
	Value string
}

// KVStore KV 配置后端 SPI 接口（§13）。
// 默认实现为 FileStore（本地 .env 文件），可替换为远程/内存后端。
type KVStore interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Watch(fn func(change ChangeEvent)) error
}

// 编译期断言：FileStore 实现 KVStore。
var _ KVStore = (*FileStore)(nil)

// defaultFile 默认 KV 配置文件（与 easyconf/.env 共用同一文件）。
var defaultFile = ".env"

// FileStore KVStore 的本地文件默认实现（基于 easyconf）。
//
// 读：解析 .env 文件进内存快照；写：更新快照 + 原子落盘（临时文件 + os.Rename）。
// 热更：订阅 conf.Manager.Watch，外部改文件后重载并广播变更（§13 不重启）。
type FileStore struct {
	mu     sync.RWMutex
	path   string                  // 配置文件路径（.env 格式）
	kv     map[string]string       // 内存快照
	cbs    []func(ChangeEvent)     // Watch 回调
	cfgMgr conf.Manager            // 可空；非空时用于热更广播与 Set 联动
	once   sync.Once               // 仅订阅一次 cfgMgr.Watch
	unsub  chan struct{}           // 停止热更订阅信号
}

// NewFileStore 创建基于 path 的 FileStore。
// cfgMgr 可为 nil（纯文件存储，无 conf 联动）；文件不存在时以默认空值起步。
func NewFileStore(cfgMgr conf.Manager, path string) *FileStore {
	fs := &FileStore{
		path:   path,
		kv:     make(map[string]string),
		cfgMgr: cfgMgr,
	}
	fs.reload()
	return fs
}

// subscribe 订阅 conf.Manager 热更：外部文件变更经 cfgMgr.Watch 广播 → 重载文件。
// 幂等：同一实例只订阅一次；cfgMgr 为 nil 时跳过。
func (fs *FileStore) subscribe() {
	if fs.cfgMgr == nil {
		return
	}
	fs.once.Do(func() {
		fs.unsub = make(chan struct{})
		fs.cfgMgr.Watch(func(*conf.Config) {
			select {
			case <-fs.unsub:
				return
			default:
				fs.reload()
			}
		})
	})
}

// Get 读取键值；键不存在返回空串。
func (fs *FileStore) Get(key string) (string, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return fs.kv[key], nil
}

// Set 更新键值并原子落盘（临时文件 + os.Rename，§13 边界），随后通知 Watch 回调。
// cfgMgr 非空时同时通过 conf.Manager 广播（其他 conf 订阅者热更）。
func (fs *FileStore) Set(key, value string) error {
	fs.mu.Lock()
	fs.kv[key] = value
	if err := fs.persistLocked(); err != nil {
		fs.mu.Unlock()
		return err
	}
	cbs := append([]func(ChangeEvent){}, fs.cbs...)
	fs.mu.Unlock()

	for _, fn := range cbs {
		fn(ChangeEvent{Key: key, Value: value})
	}
	if fs.cfgMgr != nil {
		_ = fs.cfgMgr.Set(key, value) // 广播到 conf 订阅者；未注册键静默跳过
	}
	return nil
}

// Watch 注册 KV 变更回调；返回错误恒为 nil（保持 SPI 接口语义）。
func (fs *FileStore) Watch(fn func(change ChangeEvent)) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.cbs = append(fs.cbs, fn)
	return nil
}

// Close 停止热更订阅（可选清理）。
func (fs *FileStore) Close() {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.unsub != nil {
		close(fs.unsub)
		fs.unsub = nil
	}
	fs.cbs = nil
}

// reload 从文件重载快照并广播变更（文件不存在视为空，即默认值）。
func (fs *FileStore) reload() {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	newMap := loadMap(fs.path)
	changes := make([]ChangeEvent, 0, len(newMap))
	for k, v := range newMap {
		if fs.kv[k] != v {
			fs.kv[k] = v
			changes = append(changes, ChangeEvent{Key: k, Value: v})
		}
	}
	for k := range fs.kv {
		if _, ok := newMap[k]; !ok {
			delete(fs.kv, k)
			changes = append(changes, ChangeEvent{Key: k})
		}
	}
	if len(changes) == 0 {
		return
	}
	cbs := append([]func(ChangeEvent){}, fs.cbs...)
	for _, fn := range cbs {
		for _, c := range changes {
			fn(c)
		}
	}
}

// persistLocked 原子落盘：写临时文件 → fsync → os.Rename（需持有 fs.mu）。
func (fs *FileStore) persistLocked() error {
	tmp, err := os.CreateTemp(filepath.Dir(fs.path), ".rocksys-config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 失败时清理临时文件

	keys := make([]string, 0, len(fs.kv))
	for k := range fs.kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(fs.kv[k])
		sb.WriteString("\n")
	}
	if _, err := tmp.WriteString(sb.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, fs.path)
}

// loadMap 解析 .env 格式文件为 map；文件不存在返回空 map（默认值）。
func loadMap(path string) map[string]string {
	m := make(map[string]string)
	content, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(content), "\n") {
		k, v := easyconf.GetConfStrByLine(line)
		if k != "" {
			m[k] = v
		}
	}
	return m
}

// Config RockConfig 组件（hotswap 独立组件，§13）。
// 状态由组件自身持有（atomic），Get/Set/Watch 委托 FileStore。
type Config struct {
	cfgMgr conf.Manager
	mu     sync.Mutex
	store  *FileStore
	state  atomic.Value // 持有 hotswap.State
}

// 编译期断言：Config 实现 hotswap.Component。
var _ hotswap.Component = (*Config)(nil)

// New 构造 Config 组件（默认 StateDisabled，由 hotswap.Enable 触发 Start）。
func New(cfgMgr conf.Manager) *Config {
	c := &Config{cfgMgr: cfgMgr}
	c.state.Store(hotswap.StateDisabled)
	return c
}

// Name 返回组件名：config。
func (c *Config) Name() string { return "config" }

// Start 初始化 FileStore（指向默认 .env）并置 Enabled。
// cfg 按 §6.3 约定忽略（实体内部自行从 conf.Manager 读取配置）；幂等。
func (c *Config) Start(_ any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store != nil {
		return nil // 幂等：已启动
	}
	store := NewFileStore(c.cfgMgr, defaultFile)
	store.subscribe()
	c.store = store
	c.state.Store(hotswap.StateEnabled)
	return nil
}

// Stop 停止热更订阅、清理 FileStore 并置 Disabled。
func (c *Config) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return nil // 幂等
	}
	c.store.Close()
	c.store = nil
	c.state.Store(hotswap.StateDisabled)
	return nil
}

// State 返回组件自身状态。
func (c *Config) State() hotswap.State {
	return c.state.Load().(hotswap.State)
}

// Get 委托 FileStore 读取键值。
func (c *Config) Get(key string) (string, error) {
	store, err := c.getStore()
	if err != nil {
		return "", err
	}
	return store.Get(key)
}

// Set 委托 FileStore 更新键值（含原子落盘与广播）。
func (c *Config) Set(key, value string) error {
	store, err := c.getStore()
	if err != nil {
		return err
	}
	return store.Set(key, value)
}

// Watch 委托 FileStore 注册 KV 变更回调。
func (c *Config) Watch(fn func(change ChangeEvent)) error {
	store, err := c.getStore()
	if err != nil {
		return err
	}
	return store.Watch(fn)
}

// getStore 返回当前 FileStore；组件未启动时返回错误。
func (c *Config) getStore() (*FileStore, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return nil, errors.New("config: 组件未启动")
	}
	return c.store, nil
}
