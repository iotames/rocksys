package hotswap

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
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
	hub              *ScriptHub                     // 外挂文件统一内容中枢（可选；装配方注入，Shutdown 时随管理器一并停止监控循环）
	autoMap          map[string]string              // 挂件自动开关映射：中间件名 → XXX_ENABLED 配置键（启动/热更按配置值联动挂载）
	regOrder         []string                       // 中间件注册顺序（自动挂载时按注册顺序挂链，保证链顺序确定）
	lifecycleMu      sync.Mutex                     // 串行化 Enable/Disable 的"检查→Start→挂链/摘链→置位"，消除并发双调用竞态（switch 显式调用与热更联动并发时）
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
		autoMap:          make(map[string]string),
	}
	if cfgMgr != nil {
		// 订阅配置热更（§6.4）：先按 XXX_ENABLED 联动挂载/摘除，再对 Enabled 实体走流程 C。
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

// SetScriptHub 注入外挂文件统一内容中枢（ScriptHub，实现见 hub.go）。
// 仅保存引用、不做启动：监控循环的 Start 由装配方在全部子目录注册完成后调用
// （buildServer 尾部 scriptHub.Start()，幂等）；本管理器 Shutdown 时统一停止，
// 保证监控 goroutine 随组件生命周期启停、不泄漏。
func (m *Manager) SetScriptHub(hub *ScriptHub) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hub = hub
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
	// 记录注册顺序：自动挂载（ApplyAutoEnable）时按此顺序挂链，保证链顺序确定。
	for _, n := range m.regOrder {
		if n == name {
			return // 已记录，幂等
		}
	}
	m.regOrder = append(m.regOrder, name)
}

// SetAutoEnableMap 注入挂件自动开关映射：中间件名 → XXX_ENABLED 配置键。
// 装配完成后须调用 ApplyAutoEnable 做初始同步；此后配置热更（conf.Watch）
// 会自动按配置值联动挂载/摘除（hotReload 内调用 applyAutoEnable），
// 保证"配置中心是挂载状态的唯一真源"、两态永不分裂。
func (m *Manager) SetAutoEnableMap(autoMap map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoMap = make(map[string]string, len(autoMap))
	for k, v := range autoMap {
		m.autoMap[k] = v
	}
}

// ApplyAutoEnable 启动时按当前配置值同步挂载状态（幂等，可重复调用）。
// 对 autoMap 中每个中间件：配置值为 true 且未挂载 → Enable；false 且已挂载 → Disable。
// Enable/Disable 失败仅记录告警，不阻断启动。
func (m *Manager) ApplyAutoEnable() {
	m.applyAutoEnable()
}

// applyAutoEnable 按配置当前值联动挂载/摘除（启动初始同步与配置热更共用）。
// 读取配置：conf.Manager.List() 的 Current（当前生效值，含 .env/环境变量覆盖）。
// 按 regOrder（注册顺序）处理，保证链上中间件顺序确定。
// 返回值：本次热更中由本函数新挂载的中间件名集合（供 hotReload 跳过重复 Start）。
func (m *Manager) applyAutoEnable() map[string]bool {
	m.mu.RLock()
	confMgr := m.confMgr
	autoMap := m.autoMap
	order := append([]string(nil), m.regOrder...)
	m.mu.RUnlock()
	if confMgr == nil || len(autoMap) == 0 {
		return nil
	}

	// 从配置中心读取全部 XXX_ENABLED 当前值（启动/热更低频，全量遍历可接受）。
	want := make(map[string]bool, len(autoMap))
	for _, it := range confMgr.List() {
		for name, key := range autoMap {
			if it.Key == key {
				if v, err := strconv.ParseBool(it.Current); err == nil {
					want[name] = v
				}
			}
		}
	}

	newly := make(map[string]bool)
	for _, name := range order {
		w, ok := want[name]
		if !ok {
			continue // 配置未注册或不可解析，跳过
		}
		m.mu.RLock()
		st := m.middlewareStates[name]
		m.mu.RUnlock()
		if w && st != StateEnabled {
			if err := m.Enable(name); err != nil {
				log.Warn("hotswap: auto enable failed", "name", name, "err", err.Error())
			} else {
				newly[name] = true
			}
		} else if !w && st == StateEnabled {
			if err := m.Disable(name); err != nil {
				log.Warn("hotswap: auto disable failed", "name", name, "err", err.Error())
			}
		}
	}
	return newly
}

// Enable 开启/挂载实体（§6.3 流程 A）。
// 查找顺序：先中间件后组件。Start 成功 → 链中间件 chain.Add 追加到槽位 + 簿记 Enabled；
// 组件簿记 StartedAt/LastSwitchAt。Start 失败 → 保持 Disabled，记录故障+告警（不中断服务）。
// ★ 挂载变更整体持 lifecycleMu 串行化：switch 显式调用与配置热更联动（applyAutoEnable）
// 可能并发触发同一实体的 Enable/Disable，串行后第二个进入时幂等检查（锁内）即命中，
// 避免 Start 双调/重复挂链。
func (m *Manager) Enable(name string) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	m.mu.RLock()
	ml, isMiddleware := m.middlewares[name]
	comp, isComponent := m.components[name]
	var st State
	if isMiddleware {
		st = m.middlewareStates[name] // 幂等检查与簿记写同受 m.mu 保护（Shutdown 不经 lifecycleMu，锁外直读会竞争）
	}
	m.mu.RUnlock()

	now := time.Now()
	switch {
	case isMiddleware:
		if st == StateEnabled {
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
// ★ 与 Enable 同持 lifecycleMu 串行化（见 Enable 注释），排空最长 10s 会阻塞其他挂载变更（可接受）。
func (m *Manager) Disable(name string) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	m.mu.RLock()
	ml, isMiddleware := m.middlewares[name]
	comp, isComponent := m.components[name]
	var st State
	if isMiddleware {
		st = m.middlewareStates[name] // 幂等检查与簿记写同受 m.mu 保护（Shutdown 不经 lifecycleMu）
	}
	m.mu.RUnlock()

	now := time.Now()
	switch {
	case isMiddleware:
		if st == StateDisabled {
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

	// 3. 停止外挂文件统一内容中枢的监控循环（随 Manager 生命周期启停）。
	m.mu.RLock()
	hub := m.hub
	m.mu.RUnlock()
	if hub != nil {
		hub.Shutdown()
	}

	return errors.Join(errs...)
}

// hotReload 配置热更（§6.3 流程 C / §6.4）。
// ① 先按 XXX_ENABLED 配置值联动挂载/摘除（applyAutoEnable：配置中心是挂载状态唯一真源）；
// ② 再对 State == StateEnabled 的中间件/组件执行：调用 Start(nil) 重建内部快照并原子替换。
// Start 失败 → 保留旧快照（实例继续以旧配置服务），记录故障+告警。
// StateDisabled 的实体不响应配置热更事件（其配置变更将在下次 Enable 时经 Start 首次生效）。
func (m *Manager) hotReload() {
	// ① 先按 XXX_ENABLED 配置值联动挂载/摘除（applyAutoEnable：配置中心是挂载状态唯一真源）。
	// 新挂载的实体已用最新配置 Start（跳过下方流程②的重复 Start）。
	newly := m.applyAutoEnable()

	m.mu.RLock()
	mws := make([]MiddlewareLifecycle, 0, len(m.middlewares))
	for name, ml := range m.middlewares {
		if m.middlewareStates[name] == StateEnabled && !newly[name] {
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
