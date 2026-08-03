package conf

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iotames/easyconf"
)

// 命令行短参数名 → 注册名映射表
// ★ 必须在 Parse 前完成：flag 包遇到未注册参数会直接 os.Exit(2) 崩溃
var shortFlagMap = map[string]string{
	"--listen":    "--ROCKSYS_LISTEN",
	"--upstream":  "--ROCKSYS_UPSTREAM",
	"--timeout":   "--ROCKSYS_TIMEOUT",
	"--config":    "--ROCKSYS_CONFIG",
	"--admin":     "--ROCKSYS_ADMIN",
	"--log-level": "--ROCKSYS_LOG_LEVEL",
}

// envFile 默认始终监听的配置文件（不存在时 easyconf 自动创建）
const envFile = ".env"

// watcherPollInterval 热更轮询间隔
const watcherPollInterval = 3 * time.Second

// confManager Manager 接口的默认实现
type confManager struct {
	cfg      atomic.Value        // 持有 *Config，并发安全读取
	ec       *easyconf.Conf      // 底层 easyconf 封装
	watchers []func(*Config)     // 热更订阅者
	args     []string            // ★ 保存 Load 时改写后的命令行参数（--ROCKSYS_* 注册名）
	mu       sync.Mutex          // 保护 watchers / started / cancel / done
	started  bool                // 轮询是否已启动
	cancel   context.CancelFunc  // 停止轮询
	done     chan struct{}       // 轮询 goroutine 退出信号

	// easyconf 绑定的底座指针（重建 Config 时取用）
	listenAddr      *string
	defaultUpstream *string
	timeoutSec      *int
	configFile      *string
	adminAddr       *string
	logLevel        *string
}

// defaultLoader Load 的默认实现
func defaultLoader(args []string) (Manager, error) {
	// 0. ★ 短参数名映射，并把结果写回 os.Args
	args = mapShortFlags(args)
	os.Args = append([]string{os.Args[0]}, args...)
	// ★ 重置全局 flag 集，避免多进程内多次 Load 重复注册 panic
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	ec := easyconf.NewConf(envFile, "default.env")
	m := &confManager{
		ec:   ec,
		args: args,
	}
	m.bindBaseVars()

	// Parse(true) 启用 flag 解析 → 三级优先级：命令行 > 环境变量 > .env文件
	if err := ec.Parse(true); err != nil {
		return nil, err
	}

	// 1. ★ 指定 --config 时的优先级修补（命令行 > 环境变量 > ConfigFile > .env）
	if *m.configFile != "" {
		if err := m.reloadFiles(m.watchFiles(), args); err != nil {
			return nil, err
		}
		return m, nil
	}
	m.publish()
	return m, nil
}

// bindBaseVars 注册底座 6 个配置项
func (m *confManager) bindBaseVars() {
	m.listenAddr = new(string)
	m.defaultUpstream = new(string)
	m.timeoutSec = new(int)
	m.configFile = new(string)
	m.adminAddr = new(string)
	m.logLevel = new(string)

	m.ec.StringVar(m.listenAddr, "ROCKSYS_LISTEN", defaultListenAddr, "监听地址")
	m.ec.StringVar(m.defaultUpstream, "ROCKSYS_UPSTREAM", defaultDefaultUpstream, "默认后端")
	m.ec.IntVar(m.timeoutSec, "ROCKSYS_TIMEOUT", defaultUpstreamTimeout, "转发超时(秒)")
	m.ec.StringVar(m.configFile, "ROCKSYS_CONFIG", defaultConfigFile, "配置文件路径")
	m.ec.StringVar(m.adminAddr, "ROCKSYS_ADMIN", defaultAdminAddr, "管理接口地址")
	m.ec.StringVar(m.logLevel, "ROCKSYS_LOG_LEVEL", defaultLogLevel, "日志级别")
}

// watchFiles 返回热更监听/重载顺序的文件列表（优先级从低到高，configFile 覆盖 .env）
func (m *confManager) watchFiles() []string {
	files := []string{envFile}
	if cf := *m.configFile; cf != "" && cf != envFile {
		files = append(files, cf)
	}
	return files
}

// reloadFiles 重载配置文件列表（从低到高）→ 重放环境变量 → 重放命令行 → 重建并广播
func (m *confManager) reloadFiles(files []string, args []string) error {
	for _, f := range files {
		if err := m.ec.SetValuesByEnvFile(f); err != nil {
			return err
		}
	}
	if err := m.ec.SetValuesByEnv(); err != nil {
		return err
	}
	for k, v := range parseArgsToMap(args) {
		_ = m.ec.SetItemValue(k, v)
	}
	m.publish()
	return nil
}

// rebuildConfig 从 easyconf 绑定变量重建 Config（UpstreamTimeout 秒 → Duration 换算）
func (m *confManager) rebuildConfig() *Config {
	return &Config{
		ListenAddr:      *m.listenAddr,
		DefaultUpstream: *m.defaultUpstream,
		UpstreamTimeout: time.Duration(*m.timeoutSec) * time.Second,
		ConfigFile:      *m.configFile,
		AdminAddr:       *m.adminAddr,
		LogLevel:        *m.logLevel,
	}
}

// publish 重建 Config → atomic.Value.Store → 逐个回调 watchers（独立 goroutine）
func (m *confManager) publish() {
	cfg := m.rebuildConfig()
	m.cfg.Store(cfg)
	m.mu.Lock()
	watchers := append([]func(*Config){}, m.watchers...)
	m.mu.Unlock()
	for _, fn := range watchers {
		go fn(cfg)
	}
}

// Current 返回当前只读配置（原子读取，无锁）
func (m *confManager) Current() *Config {
	v := m.cfg.Load()
	if v == nil {
		return nil
	}
	return v.(*Config)
}

// Watch 订阅配置变更；回调在独立 goroutine 执行
func (m *confManager) Watch(fn func(*Config)) {
	if fn == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.watchers = append(m.watchers, fn)
}

// StartWatcher 启动配置文件 mtime 轮询
// 默认始终监听 .env；当 ConfigFile 非空时额外监听该文件
func (m *confManager) StartWatcher() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("conf: watcher already started")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	m.started = true
	go m.watchLoop(ctx, m.watchFiles())
	return nil
}

// watchLoop 每 3s 轮询一次配置文件的 ModTime，变更则重载并广播
func (m *confManager) watchLoop(ctx context.Context, files []string) {
	defer close(m.done)
	last := make(map[string]int64, len(files))
	for _, f := range files {
		last[f] = modTimeNano(f)
	}
	ticker := time.NewTicker(watcherPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			changed := false
			for _, f := range files {
				t := modTimeNano(f)
				if t != last[f] {
					last[f] = t
					changed = true
				}
			}
			if changed {
				_ = m.reloadFiles(files, m.args)
			}
		}
	}
}

// Shutdown 停止热更轮询，阻塞直到后台 goroutine 退出
func (m *confManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Register 挂件配置项注册（name 即环境变量名）
// 注册后触发一次"重载 + 广播"，保证挂件项能从环境变量/.env/命令行读入
func (m *confManager) Register(pval any, name, defval, title string, usage ...string) error {
	switch p := pval.(type) {
	case *string:
		m.ec.StringVar(p, name, defval, title, usage...)
	case *int:
		iv, err := strconv.Atoi(defval)
		if err != nil {
			return fmt.Errorf("conf: Register(%s) defval(%q) 不是合法整数: %w", name, defval, err)
		}
		m.ec.IntVar(p, name, iv, title, usage...)
	case *bool:
		bv, err := strconv.ParseBool(defval)
		if err != nil {
			return fmt.Errorf("conf: Register(%s) defval(%q) 不是合法布尔: %w", name, defval, err)
		}
		m.ec.BoolVar(p, name, bv, title, usage...)
	default:
		return fmt.Errorf("conf: Register(%s) 不支持类型 %T", name, pval)
	}
	// 注册后触发"重载 + 广播"：环境变量 → .env → 命令行重放
	if err := m.ec.SetValuesByEnv(); err != nil {
		return err
	}
	if err := m.ec.SetValuesByEnvFile(envFile); err != nil {
		return err
	}
	for k, v := range parseArgsToMap(m.args) {
		_ = m.ec.SetItemValue(k, v)
	}
	m.publish()
	return nil
}

// Set 运行期按注册名全名设值并广播
func (m *confManager) Set(name, value string) error {
	if err := m.ec.SetItemValue(name, value); err != nil {
		return err
	}
	m.publish()
	return nil
}

// mapShortFlags 将 --listen 等短名改写为 --ROCKSYS_* 注册名
// 支持 "--listen=:9090" 与 "--listen :9090" 两种形态
func mapShortFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		for short, full := range shortFlagMap {
			if a == short {
				a = full
				break
			}
			if strings.HasPrefix(a, short+"=") {
				a = full + strings.TrimPrefix(a, short)
				break
			}
		}
		out = append(out, a)
	}
	return out
}

// parseArgsToMap 解析 "--KEY value" / "--KEY=value" 为 map（键为注册名，不含 "--" 前缀）
func parseArgsToMap(args []string) map[string]string {
	mp := make(map[string]string)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			continue
		}
		body := strings.TrimPrefix(a, "--")
		if idx := strings.Index(body, "="); idx >= 0 {
			mp[body[:idx]] = body[idx+1:]
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			mp[body] = args[i+1]
			i++
			continue
		}
		// 无值的开关型参数（布尔项）按空值处理
		mp[body] = ""
	}
	return mp
}

// modTimeNano 返回文件的 ModTime（纳秒），文件不存在返回 0
func modTimeNano(f string) int64 {
	fi, err := os.Stat(f)
	if err != nil {
		return 0
	}
	return fi.ModTime().UnixNano()
}