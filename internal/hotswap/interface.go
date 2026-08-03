package hotswap

import (
	"time"

	"rocksys/internal/chain"
)

// State 热切换实体运行状态（§6.2）。
type State int

const (
	StateDisabled State = iota // 未启用：不在链上，不响应配置热更
	StateEnabled               // 已启用：挂载在链上（中间件）或已启动（组件），响应配置热更
	StateDraining              // 排空中：已摘除/停止接受新流量，等待存量请求完成
)

// String State 的可读表示（供日志/管理接口输出）。
func (s State) String() string {
	switch s {
	case StateEnabled:
		return "enabled"
	case StateDraining:
		return "draining"
	default:
		return "disabled"
	}
}

// Component 独立组件接口（config/registry/mq/object 实现此接口）。
// 组件自管理生命周期、不挂 chain；状态由组件自身持有，Manager 直接读取。
type Component interface {
	Name() string
	Start(cfg any) error
	Stop() error
	State() State
}

// MiddlewareLifecycle 链中间件接口（shield/dispatch/result/trace/auth/script/obs 实现此接口）。
// 中间件实现 chain.Middleware + 此接口即可被 hotswap 管理生命周期。
// ★ 注意：本接口不包含 State()——中间件的 Enabled/Disabled 状态由 Manager 内部
// 通过 map[name]State 统一簿记（与 Component 自行持有状态不同），中间件无需也不应暴露 State()。
type MiddlewareLifecycle interface {
	chain.Middleware
	// Start 用新配置重新初始化【本实例】（挂件实例可复用，不存在"新/旧实例"两套）。
	// 内部必须用不可变快照承载运行状态（如 RouteTable 整体重建后原子替换），
	// 保证 Start 与在途请求的 Handle 并发安全（§6.3）。
	Start(cfg any) error
	// Stop 清理资源（如关闭文件、清空连接池）。
	Stop() error
	// Slot 返回中间件挂载位置（chain.Head / chain.Middle / chain.Tail）。
	Slot() chain.Slot
}

// Status 实体状态（Manager.List 返回）。
type Status struct {
	Name         string    // 实体名
	Kind         string    // "component" | "middleware"
	State        State     // 当前状态
	StartedAt    time.Time // 最近一次 Start 成功时间（零值表示从未启用）
	LastSwitchAt time.Time // 最近一次状态切换时间
	Message      string    // 最近一次操作结果/故障信息
}
