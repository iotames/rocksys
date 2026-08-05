package conf

import "time"

// Config 底座全部配置的只读载体
type Config struct {
	ListenAddr      string        // 监听地址，默认 ":8080"
	DefaultUpstream string        // 默认后端，默认 "http://127.0.0.1:8080"
	UpstreamTimeout time.Duration // 转发超时，默认 18s
	ConfigFile      string        // .env 配置文件路径，空=极简模式（只用环境变量+命令行）
	AdminAddr       string        // 管理接口监听地址，默认 "127.0.0.1:19527"
	LogLevel        string        // 日志级别，默认 "info"
}

// 底座全部配置项的默认值
const (
	defaultListenAddr      = ":8080"
	defaultDefaultUpstream = "http://127.0.0.1:8080"
	defaultUpstreamTimeout = 18 // 秒
	defaultConfigFile      = ""
	defaultAdminAddr       = "127.0.0.1:19527"
	defaultLogLevel        = "info"
)