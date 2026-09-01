package conf

import "time"

// Config 底座全部配置的只读载体
type Config struct {
	ListenAddr      string        // 监听地址，默认 ":8080"
	DefaultUpstream string        // 默认后端，默认 "http://127.0.0.1:9000"（占位示例，需改为实际后端；勿与监听端口相同）
	UpstreamTimeout time.Duration // 转发超时，默认 18s
	ConfigFile      string        // 配置文件路径（--config 指定，任意位置），空=极简模式（只用环境变量+命令行）
	AdminAddr       string        // 管理接口监听地址，默认 "127.0.0.1:19527"
	LogLevel        string        // 日志级别，默认 "info"
	LogToFile       bool          // 文件存档开关（E1），默认 false
	LogFile         string        // 日志文件路径，默认 "logs/rocksys.log"
	LogMaxSize      int64         // 文件大小上限（整数 MB，0=不限制；E2），默认 50
	WWWRoot         string        // 主引擎兜底静态目录：无路由命中且无默认上游时，从该目录按请求路径返回文件；空=关闭
	NotFoundPage    string        // 自定义 404 页面文件路径（HTML 等）；空=easyserver 默认 JSON 404 响应体
}

// 底座全部配置项的默认值
const (
	defaultListenAddr      = ":8080"
	defaultDefaultUpstream = "http://127.0.0.1:9000"
	defaultUpstreamTimeout = 18 // 秒
	defaultConfigFile      = ""
	defaultAdminAddr       = "127.0.0.1:19527"
	defaultLogLevel        = "info"
	defaultLogFile         = "logs/rocksys.log"
	defaultLogMaxSize      = 50 // 整数 MB，0=不限制
	defaultWWWRoot         = ""
	defaultNotFoundPage    = ""
)
