package hotswap

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"rocksys/internal/chain"
	"rocksys/internal/conf"

	"github.com/iotames/easyserver/log"
)

// 排空等待上限与轮询间隔（§6.3 流程 B）。
const (
	drainTimeout      = 10 * time.Second
	drainPollInterval = 100 * time.Millisecond
)

// Manager 统一管理所有可切换实体（§6.2）。
type Manager struct {
	chain            *chain.Chain                   // 用于中间件挂载/摘除
	confMgr          conf.Manager                   // 订阅配置热更（§6.4）
	components       map[string]Component           // 独立组件注册表
	middlewares      map[string]MiddlewareLifecycle // 链中间件注册表
	middlewareStates map[string]State               // 中间件状态簿记（MiddlewareLifecycle 不暴露 State()）
	drainCheck       func() int64                   // 排空判定（Adapter.ActiveCount），未注入则跳过排空
	startedAt        map[string]time.Time           // 实体最近一次 Start 成功时间
	lastSwitch       map[string]time.Time           // 实体最近一次状态切换时间
	message          map[string]string              // 实体最近一次操作消息/故障信息
	mu               sync.RWMutex
}

// NewManager 创建热运维管理器，并订阅配置热更。
// cfgMgr 为 nil 时跳过热更订阅（测试或极简场景）。
func NewManager(ch *chain.Chain, cfgMgr conf.Manager) *Manager {
	m := &Manager{
		chain:            ch,
		confMgr:          cfgMgr,
		components:       make(map[string]Component),
		middlewares:      make(map[string]MiddlewareLifecycle),
		middlewareStates: make(map[string]State),
		startedAt:        make(map[string]time.Time),
		lastSwitch:       make(map[string]time.Time),
		message:          make(map[string]string),
	}
	if cfgMgr != nil {
		// 订阅配置热更（§6.4）：收到变更仅对 State == StateEnabled 的实体走流程 C。
		// Start 的 cfg 按 §6.3 约定传 nil——实体内部自行从 conf.Manager.Current() 读取最新配置。
		cfgMgr.Watch(func(*conf.Config) {
			m.hotReload()
		})
	}
	return m
}

// SetDrainCheck 注入排空判定函数（通常为 Adapter.ActiveCount）。
// 未注入时 Disable 跳过排空等待（直接继续）——hotswap 不直接持有 Adapter，由此解耦。
func (m *Manager) SetDrainCheck(fn func() int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drainCheck = fn
}

// RegisterComponent 注册独立组件（默认 Disabled，Enable 触发 Start）。
func (m *Manager) RegisterComponent(c Component) {
	if c == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.components[c.Name()] = c
}

// RegisterMiddleware 注册链中间件（默认 Disabled，不自动挂载；
// 由 Enable 触发 Start + chain.Add 挂载，见 §6.3 流程 A）。
func (m *Manager) RegisterMiddleware(ml MiddlewareLifecycle) {
	if ml == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	name := ml.Name()
	m.middlewares[name] = ml
	if _, ok := m.middlewareStates[name]; !ok {
		m.middlewareStates[name] = StateDisabled
	}
}

// Enable 开启/挂载实体（§6.3 流程 A）。
// 查找顺序：先中间件后组件。Start 成功 → 链中间件 chain.Add 追加到槽位 + 簿记 Enabled；
// 组件簿记 StartedAt/LastSwitchAt。Start 失败 → 保持 Disabled，记录故障+告警（不中断服务）。
func (m *Manager) Enable(name string) error {
	m.mu.RLock()
	ml, isMiddleware := m.middlewares[name]
	comp, isComponent := m.components[name]
	m.mu.RUnlock()

	now := time.Now()
	switch {
	case isMiddleware:
		if m.middlewareStates[name] == StateEnabled {
			return nil // 幂等：已启用不重复挂载
		}
		if err := ml.Start(nil); err != nil {
			m.noteFail(name, "enable failed: "+err.Error())
			log.Warn("hotswap: enable failed", "name", name, "kind", "middleware", "err", err)
			return fmt.Errorf("hotswap: enable middleware %q: %w", name, err)
		}
		m.mu.Lock()
		m.chain.Add(ml.Slot(), ml) // 追加到槽位，不影响同槽位其他中间件
		m.middlewareStates[name] = StateEnabled
		m.startedAt[name] = now
		m.lastSwitch[name] = now
		m.message[name] = "enabled"
		m.mu.Unlock()
		log.Info("hotswap: enabled", "name", name, "kind", "middleware")
		return nil
	case isComponent:
		if comp.State() == StateEnabled {
			return nil // 幂等
		}
		if err := comp.Start(nil); err != nil {
			m.noteFail(name, "enable failed: "+err.Error())
			log.Warn("hotswap: enable failed", "name", name, "kind", "component", "err", err)
			return fmt.Errorf("hotswap: enable component %q: %w", name, err)
		}
		m.mu.Lock()
		m.startedAt[name] = now
		m.lastSwitch[name] = now
		m.message[name] = "enabled"
		m.mu.Unlock()
		log.Info("hotswap: enabled", "name", name, "kind", "component")
		return nil
	default:
		return errors.New("hotswap: entity not found: " + name)
	}
}

// Disable 关闭/摘除实体（§6.3 流程 B）——统一语义：从链上摘除，绝不"保持挂载但放行"。
// 链中间件：chain.Remove 仅移除目标（在途请求持旧快照继续）→ 排空 → Stop → 置 Disabled。
// 独立组件：自身业务排空由 Stop 内部完成，此处直接调用 Stop。
func (m *Manager) Disable(name string) error {
	m.mu.RLock()
	ml, isMiddleware := m.middlewares[name]
	comp, isComponent := m.components[name]
	m.mu.RUnlock()

	now := time.Now()
	switch {
	case isMiddleware:
		if m.middlewareStates[name] == StateDisabled {
			return nil // 幂等：已摘除
		}
		// 1. 从链上摘除（仅移除目标，同槽位其他中间件不受影响）
		if err := m.chain.Remove(name); err != nil {
			return err
		}
		// 2. 置排空中
		m.mu.Lock()
		m.middlewareStates[name] = StateDraining
		m.lastSwitch[name] = now
		m.mu.Unlock()
		// 3. 排空：轮询活跃请求数归零（上限 10s；未注入 drainCheck 则跳过等待）
		if err := m.drain(context.Background()); err != nil {
			m.noteFail(name, "drain timeout, forced")
			log.Warn("hotswap: drain timeout", "name", name, "err", err)
		}
		// 4. 清理资源
		if err := ml.Stop(); err != nil {
			m.noteFail(name, "stop failed: "+err.Error())
			m.mu.Lock()
			m.middlewareStates[name] = StateDisabled
			m.lastSwitch[name] = time.Now()
			m.mu.Unlock()
			log.Warn("hotswap: stop failed", "name", name, "err", err)
			return fmt.Errorf("hotswap: stop middleware %q: %w", name, err)
		}
		// 5. 置 Disabled + 审计
		m.mu.Lock()
		m.middlewareStates[name] = StateDisabled
		m.lastSwitch[name] = time.Now()
		m.message[name] = "disabled"
		m.mu.Unlock()
		log.Info("hotswap: disabled", "name", name, "kind", "middleware")
		return nil
	case isComponent:
		if comp.State() == StateDisabled {
			return nil // 幂等
		}
		// 独立组件：排空由组件自身业务完成（Stop 内部处理），hotswap 直接调用 Stop。
		if err := comp.Stop(); err != nil {
			m.noteFail(name, "stop failed: "+err.Error())
			log.Warn("hotswap: stop failed", "name", name, "kind", "component", "err", err)
			return fmt.Errorf("hotswap: stop component %q: %w", name, err)
		}
		m.mu.Lock()
		m.lastSwitch[name] = now
		m.message[name] = "disabled"
		m.mu.Unlock()
		log.Info("hotswap: disabled", "name", name, "kind", "component")
		return nil
	default:
		return errors.New("hotswap: entity not found: " + name)
	}
}

// GetMiddleware 按名称获取已注册中间件实例（未注册返回 nil）。
func (m *Manager) GetMiddleware(name string) MiddlewareLifecycle {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.middlewares[name]
}

// GetComponent 按名称获取已注册组件实例（未注册返回 nil）。
func (m *Manager) GetComponent(name string) Component {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.components[name]
}

// List 列出所有实体状态（组件在前、中间件在后，各自按名称排序，保证输出确定性）。
func (m *Manager) List() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Status, 0, len(m.components)+len(m.middlewares))
	for _, name := range sortedKeys(m.components) {
		c := m.components[name]
		out = append(out, Status{
			Name:         name,
			Kind:         "component",
			State:        c.State(),
			StartedAt:    m.startedAt[name],
			LastSwitchAt: m.lastSwitch[name],
			Message:      m.message[name],
		})
	}
	for _, name := range sortedKeys(m.middlewares) {
		out = append(out, Status{
			Name:         name,
			Kind:         "middleware",
			State:        m.middlewareStates[name],
			StartedAt:    m.startedAt[name],
			LastSwitchAt: m.lastSwitch[name],
			Message:      m.message[name],
		})
	}
	return out
}

// Shutdown 排空并停止所有已启用实体（§6.2）。
// 调用顺序：先停中间件（Tail → Middle → Head 槽位逆序），再停独立组件。
func (m *Manager) Shutdown(ctx context.Context) error {
	// 1. 全局排空一次（活跃请求归零）；未注入 drainCheck 则跳过
	if err := m.drain(ctx); err != nil {
		log.Warn("hotswap: shutdown drain timeout", "err", err)
	}

	// 2. 收集未禁用实体（快照，避免持锁调用 Stop）
	m.mu.RLock()
	var mws []MiddlewareLifecycle
	for name, ml := range m.middlewares {
		if m.middlewareStates[name] != StateDisabled {
			mws = append(mws, ml)
		}
	}
	var comps []Component
	for _, comp := range m.components {
		if comp.State() != StateDisabled {
			comps = append(comps, comp)
		}
	}
	m.mu.RUnlock()

	// 中间件按槽位逆序：Tail(2) > Middle(1) > Head(0)
	sort.Slice(mws, func(i, j int) bool { return mws[i].Slot() > mws[j].Slot() })

	var errs []error
	for _, ml := range mws {
		if err := ml.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("hotswap: stop middleware %q: %w", ml.Name(), err))
		}
		m.mu.Lock()
		m.middlewareStates[ml.Name()] = StateDisabled
		m.lastSwitch[ml.Name()] = time.Now()
		m.mu.Unlock()
	}
	for _, comp := range comps {
		if err := comp.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("hotswap: stop component %q: %w", comp.Name(), err))
		}
		m.mu.Lock()
		m.lastSwitch[comp.Name()] = time.Now()
		m.mu.Unlock()
	}
	return errors.Join(errs...)
}

// hotReload 配置热更（§6.3 流程 C / §6.4）。
// 仅对 State == StateEnabled 的中间件/组件执行：调用 Start(nil) 重建内部快照并原子替换。
// Start 失败 → 保留旧快照（实例继续以旧配置服务），记录故障+告警。
// StateDisabled 的实体不响应配置热更事件（其配置变更将在下次 Enable 时经 Start 首次生效）。
func (m *Manager) hotReload() {
	m.mu.RLock()
	mws := make([]MiddlewareLifecycle, 0, len(m.middlewares))
	for name, ml := range m.middlewares {
		if m.middlewareStates[name] == StateEnabled {
			mws = append(mws, ml)
		}
	}
	comps := make([]Component, 0, len(m.components))
	for _, comp := range m.components {
		if comp.State() == StateEnabled {
			comps = append(comps, comp)
		}
	}
	m.mu.RUnlock()

	for _, ml := range mws {
		name := ml.Name()
		if err := ml.Start(nil); err != nil {
			m.noteFail(name, "hot reload failed: "+err.Error())
			log.Warn("hotswap: hot reload failed", "name", name, "kind", "middleware", "err", err)
			continue
		}
		m.noteStart(name, "hot reload ok")
		log.Info("hotswap: hot reloaded", "name", name, "kind", "middleware")
	}
	for _, comp := range comps {
		name := comp.Name()
		if err := comp.Start(nil); err != nil {
			m.noteFail(name, "hot reload failed: "+err.Error())
			log.Warn("hotswap: hot reload failed", "name", name, "kind", "component", "err", err)
			continue
		}
		m.noteStart(name, "hot reload ok")
		log.Info("hotswap: hot reloaded", "name", name, "kind", "component")
	}
}

// drain 轮询活跃请求数归零（上限 drainTimeout，超时返回 error 由调用方记录告警后强制推进）。
// 未注入 drainCheck（无 Adapter 活跃计数）时跳过排空等待，直接返回。
func (m *Manager) drain(ctx context.Context) error {
	m.mu.RLock()
	fn := m.drainCheck
	m.mu.RUnlock()
	if fn == nil {
		return nil
	}
	deadline := time.Now().Add(drainTimeout)
	for fn() > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if time.Now().After(deadline) {
			return errors.New("hotswap: drain timeout, active requests still in flight")
		}
		time.Sleep(drainPollInterval)
	}
	return nil
}

// noteStart 记录实体最近一次 Start 成功（startedAt + lastSwitch + message），需独立持锁。
func (m *Manager) noteStart(name, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.startedAt[name] = now
	m.lastSwitch[name] = now
	m.message[name] = msg
}

// noteFail 记录实体最近一次故障/操作失败（lastSwitch + message，不动 startedAt）。
func (m *Manager) noteFail(name, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSwitch[name] = time.Now()
	m.message[name] = msg
}

// sortedKeys 返回 map 键的有序切片，保证 List 输出确定性。
func sortedKeys[V any](mp map[string]V) []string {
	keys := make([]string, 0, len(mp))
	for k := range mp {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
