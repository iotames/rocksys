// Package object 见 doc.go。本文件实现 RockObject：本地对象存储。
// LocalStore 为基于本地文件系统的对象存储，
// 提供路径穿越防护；Object 为 hotswap 独立组件（不挂 chain）。
package object

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/iotames/easyserver/log"

	"rocksys/internal/conf"
	"rocksys/internal/hotswap"
)

// defaultBaseDir 默认对象存储根目录（可被 Object.baseDir 覆盖）。
var defaultBaseDir = "./data/object"

// LocalStore 本地对象存储：以 baseDir 为根，所有对象键必须安全地落于其下。
// 路径安全校验（§19 验收核心）：join+Clean 后结果必须仍以 baseDir+分隔符 开头，
// 否则拒绝 —— 防止 ".." 穿越写出 root/baseDir 之外（如 ../../../etc/passwd）。
type LocalStore struct {
	baseDir string
}

// NewLocalStore 创建以 baseDir 为根目录的 LocalStore。
// baseDir 可用相对或绝对路径；真实路径在 resolve 时按文件系统规则规整。
func NewLocalStore(baseDir string) *LocalStore {
	return &LocalStore{baseDir: filepath.Clean(baseDir)}
}

// errTraversal 路径穿越被拒绝。
var errTraversal = errors.New("object: 非法路径，超出存储根目录")

// resolve 将对象键 path 安全解析为 baseDir 内的绝对落盘路径。
// 具体步骤：baseDir 规整 → Join → Clean → 校验结果以 baseDir+sep 开头。
// 不满足前缀则返回 errTraversal，杜绝路径穿越。
func (s *LocalStore) resolve(path string) (string, error) {
	base := filepath.Clean(s.baseDir)
	full := filepath.Clean(filepath.Join(base, path))
	prefix := base + string(filepath.Separator)
	if full != base && !strings.HasPrefix(full, prefix) {
		return "", errTraversal
	}
	if full == base {
		return "", errTraversal
	}
	return full, nil
}

// Put 将 data 写入 path 指向的对象：先校验路径安全，再创建所在目录、写文件。
// 大文件由客户端直传、不经过代理转发，本方法为底层落盘能力。
func (s *LocalStore) Put(path string, data []byte) error {
	full, err := s.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

// Get 读取 path 指向的对象内容；路径穿越时返回错误。
func (s *LocalStore) Get(path string) ([]byte, error) {
	full, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}

// Delete 删除 path 指向的对象；路径穿越时返回错误。
func (s *LocalStore) Delete(path string) error {
	full, err := s.resolve(path)
	if err != nil {
		return err
	}
	return os.Remove(full)
}

// Object RockObject 组件（hotswap 独立组件，§19）。
// 状态由组件自身持有（atomic），Put/Get/Delete 委托 LocalStore。
type Object struct {
	mu      sync.Mutex
	store   *LocalStore
	baseDir string
	state   atomic.Value // 持有 hotswap.State
}

// 编译期断言：Object 实现 hotswap.Component。
var _ hotswap.Component = (*Object)(nil)

// New 构造 Object 组件（默认 StateDisabled，由 hotswap.Enable 触发 Start）。
// cfgMgr 非 nil 时注册 OBJECT_BASE_DIR 配置项（默认 ./data/object），并读取当前值；
// 测试/自定义装配可在 Start 前覆盖 Object.baseDir。
func New(cfgMgr conf.Manager) *Object {
	o := &Object{baseDir: defaultBaseDir}
	if cfgMgr != nil {
		if err := cfgMgr.Register(&o.baseDir, "OBJECT_BASE_DIR", defaultBaseDir, "对象存储根目录", "修改后需重启服务生效；留空回退默认目录"); err != nil {
			log.Warn("object: 注册配置项失败", "name", "OBJECT_BASE_DIR", "err", err)
		}
	}
	o.state.Store(hotswap.StateDisabled)
	return o
}

// Name 返回组件名：object。
func (o *Object) Name() string { return "object" }

// Start 创建 baseDir 目录并初始化 LocalStore，随后置 Enabled。
// cfg 按 §6.3 约定忽略（baseDir 由组件自身持有）；幂等。
func (o *Object) Start(_ any) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.store != nil {
		return nil // 幂等：已启动
	}
	base := filepath.Clean(o.baseDir)
	if base == "." {
		// 空值防御：Clean("")="." 会使路径校验形同虚设，回退默认目录并告警。
		log.Warn("object: OBJECT_BASE_DIR 为空，回退默认目录", "dir", defaultBaseDir)
		base = defaultBaseDir
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	o.store = NewLocalStore(base)
	o.state.Store(hotswap.StateEnabled)
	return nil
}

// Stop 清理 LocalStore 并置 Disabled；幂等。
func (o *Object) Stop() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.store = nil
	o.state.Store(hotswap.StateDisabled)
	return nil
}

// State 返回组件自身状态。
func (o *Object) State() hotswap.State {
	return o.state.Load().(hotswap.State)
}

// getStore 返回当前 LocalStore；组件未启动时返回错误。
func (o *Object) getStore() (*LocalStore, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.store == nil {
		return nil, errors.New("object: 组件未启动")
	}
	return o.store, nil
}

// Put 委托 LocalStore 写对象。
func (o *Object) Put(path string, data []byte) error {
	store, err := o.getStore()
	if err != nil {
		return err
	}
	return store.Put(path, data)
}

// Get 委托 LocalStore 读对象。
func (o *Object) Get(path string) ([]byte, error) {
	store, err := o.getStore()
	if err != nil {
		return nil, err
	}
	return store.Get(path)
}

// Delete 委托 LocalStore 删对象。
func (o *Object) Delete(path string) error {
	store, err := o.getStore()
	if err != nil {
		return err
	}
	return store.Delete(path)
}
