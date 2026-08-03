package conf

import "context"

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
	// Set 运行期按注册名全名设值并广播
	Set(name, value string) error
}

// Load 从命令行/环境变量/.env文件加载配置
// 用法：mgr, err := conf.Load(os.Args[1:])
func Load(args []string) (Manager, error) { return defaultLoader(args) }