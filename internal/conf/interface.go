package conf

import "context"

// ConfigItem 配置项元数据（供 WebUI /admin/config/list 输出）。
type ConfigItem struct {
	Key     string `json:"key"`     // 注册名（即环境变量名，热改时用此名）
	Title   string `json:"title"`   // 中文说明
	Defval  string `json:"defval"`  // 默认值（字符串形态）
	Current string `json:"current"` // 当前值（字符串形态）
	Example string `json:"example"` // 使用说明/示例（可能为空）
}

// Manager 配置管理器的公开接口（默认实现见 impl.go 的 confManager）
type Manager interface {
	// Current 返回当前只读配置（原子读取，无锁）
	Current() *Config
	// Watch 订阅配置变更；回调在独立 goroutine 执行
	Watch(fn func(*Config))
	// StartWatcher 启动配置文件 mtime 轮询
	StartWatcher() error
	// Shutdown 停止热更轮询，阻塞直到后台 goroutine 退出
	Shutdown(ctx context.Context) error
	// Register 挂件配置项注册（name 即环境变量名）
	Register(pval any, name, defval, title string, usage ...string) error
	// SyncDefaultFile 将全部已注册配置项的默认值快照同步到工作目录 default.env（开发规范下即 bin/default.env，全量覆盖）。
	SyncDefaultFile() error
	// Set 运行期按注册名全名设值并广播
	Set(name, value string) error
	// List 列出全部已注册配置项元数据（含底座与各挂件），供管理接口输出。
	List() []ConfigItem
}

// Load 从命令行/环境变量/工作目录 .env 文件加载配置
// 用法：mgr, err := conf.Load(os.Args[1:])
func Load(args []string) (Manager, error) { return defaultLoader(args) }
