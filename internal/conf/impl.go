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

	"github.com/iotames/easyserver/log"

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

// envFile 默认始终监听的配置文件（不存在时 easyconf 自动创建）。
// ★ 相对当前工作目录解析，不写死绝对/固定前缀路径：程序在哪个目录运行，运行时文件就落在哪个目录。
//   开发规范要求工作目录为 bin/（make run 已 `cd bin`），故实际生成 bin/.env；禁止在项目根目录运行程序。
const envFile = ".env"

// defaultEnvFile 全量默认值快照文件：装配完成后同步所有已注册项默认值（代表代码真实兜底行为）。该文件为 easyconf 配置文件列表成员，参与取值（最低优先级兜底）。
// 同样相对工作目录（开发规范下为 bin/default.env）。
const defaultEnvFile = "default.env"

// DefaultEnvPath 返回 default.env 全量默认值快照路径（相对工作目录，供 make gen-env 打印）。
func DefaultEnvPath() string { return defaultEnvFile }

// watcherPollInterval 热更轮询间隔
const watcherPollInterval = 3 * time.Second

// confManager Manager 接口的默认实现
type confManager struct {
	cfg      atomic.Value       // 持有 *Config，并发安全读取
	ec       *easyconf.Conf     // 底层 easyconf 封装
	watchers []func(*Config)    // 热更订阅者
	args     []string           // ★ 保存 Load 时改写后的命令行参数（--ROCKSYS_* 注册名）
	mu       sync.Mutex         // 保护 watchers / started / cancel / done / m.args / easyconf 项读写
	started  bool               // 轮询是否已启动
	cancel   context.CancelFunc // 停止轮询
	done     chan struct{}      // 轮询 goroutine 退出信号

	// easyconf 绑定的底座指针（重建 Config 时取用）
	listenAddr      *string
	defaultUpstream *string
	timeoutSec      *int
	configFile      *string
	adminAddr       *string
	logLevel        *string
	logToFile       *bool   // 文件存档开关（E1）
	logFile         *string // 日志文件路径
	logMaxSize      *string // 文件大小上限（整数 MB，字符串存储便于校验；E2）
}

// defaultLoader Load 的默认实现
func defaultLoader(args []string) (Manager, error) {
	// 0. ★ 短参数名映射，并把结果写回 os.Args
	args = mapShortFlags(args)
	os.Args = append([]string{os.Args[0]}, args...)
	// ★ 重置全局 flag 集，避免多进程内多次 Load 重复注册 panic
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	// 配置文件为相对工作目录路径（.env / default.env），由 easyconf 自动创建。
	// ★ 开发规范：必须在 bin/ 目录运行程序（make run 已 `cd bin`），运行时文件跟随工作目录落在 bin/ 下。
	ec := easyconf.NewConf(envFile, defaultEnvFile)
	m := &confManager{
		ec:   ec,
		args: args,
	}
	m.bindBaseVars()

	// Parse(true) 启用 flag 解析 → 三级优先级：命令行 > 环境变量 > 配置文件（.env 覆盖 default.env）
	if err := ec.Parse(true); err != nil {
		return nil, err
	}

	// 1. ★ 指定 --config 时的优先级修补（命令行 > 环境变量 > ConfigFile > 工作目录 .env）
	//    装配期单线程，无并发，直接读 *m.configFile 无锁可接受。
	if *m.configFile != "" {
		if err := m.reloadFilesLocked(); err != nil {
			return nil, err
		}
		return m, nil
	}
	m.publish()
	return m, nil
}

// bindBaseVars 注册底座 9 个配置项（6 个既有 + 3 个日志文件项）
func (m *confManager) bindBaseVars() {
	m.listenAddr = new(string)
	m.defaultUpstream = new(string)
	m.timeoutSec = new(int)
	m.configFile = new(string)
	m.adminAddr = new(string)
	m.logLevel = new(string)
	m.logToFile = new(bool)
	m.logFile = new(string)
	m.logMaxSize = new(string)

	m.ec.StringVar(m.listenAddr, "ROCKSYS_LISTEN", defaultListenAddr, "监听地址")
	m.ec.StringVar(m.defaultUpstream, "ROCKSYS_UPSTREAM", defaultDefaultUpstream, "默认后端")
	m.ec.IntVar(m.timeoutSec, "ROCKSYS_TIMEOUT", defaultUpstreamTimeout, "转发超时(秒)")
	m.ec.StringVar(m.configFile, "ROCKSYS_CONFIG", defaultConfigFile, "配置文件路径")
	m.ec.StringVar(m.adminAddr, "ROCKSYS_ADMIN", defaultAdminAddr, "管理接口地址")
	m.ec.StringVar(m.logLevel, "ROCKSYS_LOG_LEVEL", defaultLogLevel, "日志级别")
	m.ec.BoolVar(m.logToFile, "ROCKSYS_LOG_TO_FILE", false, "文件存档（E1）")
	m.ec.StringVar(m.logFile, "ROCKSYS_LOG_FILE", defaultLogFile, "日志文件路径")
	m.ec.StringVar(m.logMaxSize, "ROCKSYS_LOG_MAX_SIZE", "50", "文件大小上限（整数 MB，0=不限制；E2）")
}

// watchFiles 返回热更监听/重载顺序的文件列表（优先级从低到高，configFile 覆盖工作目录 .env）
func (m *confManager) watchFiles() []string {
	files := []string{envFile}
	if cf := *m.configFile; cf != "" && cf != envFile {
		files = append(files, cf)
	}
	return files
}

// reloadFilesLocked 重载配置文件列表（从低到高）→ 重放环境变量 → 重放命令行 → 重建并广播。
// ★ 本函数整体持 m.mu（调用方不持锁）。args 与 files 均在锁内读取（M4）：
//
//	若先快照 args 再释放锁、进入本函数再加锁，conf.Set（更新 args + 写盘）插入两次持锁之间时，
//	重载会用旧 args 覆盖热更值——内存回退、磁盘已更新、热更静默失败不自愈。
//
// ★ 内部 publishLocked 已含 rebuildConfig + cfg.Store + 快照 + go fn(cfg)（M3），
//
//	本函数不重复 rebuild/Store。
func (m *confManager) reloadFilesLocked() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	files := m.watchFiles() // 锁内读 *m.configFile（中-2：锁外读与 Set 写竞争）
	args := m.args          // 锁内读（M4：与 syncArgsLocked 同锁串行）
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
	m.publishLocked()
	return nil
}

// parseLogMaxSizeMB 解析 ROCKSYS_LOG_MAX_SIZE（整数 MB，字符串存储便于校验）为 int64。
// 空串/解析失败/负数均视为非法，返回 ok=false；合法值 ok=true（0 表示不限制）。
func parseLogMaxSizeMB(raw string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// rebuildConfig 从 easyconf 绑定变量重建 Config
// （UpstreamTimeout 秒 → Duration 换算；ROCKSYS_LOG_MAX_SIZE 解析为整数 MB）。
func (m *confManager) rebuildConfig() *Config {
	logMaxSize, ok := parseLogMaxSizeMB(*m.logMaxSize)
	if !ok {
		// 防御性兜底：publishLocked 已在重建前把 easyconf 项修正为默认 "50"，正常不会走到
		logMaxSize = defaultLogMaxSize
	}
	return &Config{
		ListenAddr:      *m.listenAddr,
		DefaultUpstream: *m.defaultUpstream,
		UpstreamTimeout: time.Duration(*m.timeoutSec) * time.Second,
		ConfigFile:      *m.configFile,
		AdminAddr:       *m.adminAddr,
		LogLevel:        *m.logLevel,
		LogToFile:       *m.logToFile,
		LogFile:         *m.logFile,
		LogMaxSize:      logMaxSize,
	}
}

// publishLocked 无锁内部版（调用方须已持 m.mu）：
//  1. M5 修正：ROCKSYS_LOG_MAX_SIZE 为非法值（解析失败/负数）时，先将 easyconf 项修正为
//     默认 "50"（否则后续 conf.Set 的 UpdateFile 会把非法原始串写回工作目录 .env）。【修正点：publishLocked 内】
//  2. cfg := m.rebuildConfig()   // 移入锁内（M1，防与 Set 并发 race）
//  3. m.cfg.Store(cfg)           // 必须（M3）：否则 Set 热更后 Current() 读到旧配置
//  4. 快照 watchers（依赖调用方持锁，写 watchers 的 Watch() 同样持 m.mu，读写互斥成立）
//  5. 逐个 go fn(cfg)            // 回调在独立 goroutine（锁外执行），不二次加锁
//
// ⚠️ 本函数不得加任何锁（不持 m.mu，也不二次加锁）。
func (m *confManager) publishLocked() {
	if _, ok := parseLogMaxSizeMB(*m.logMaxSize); !ok {
		log.Warn(fmt.Sprintf("ROCKSYS_LOG_MAX_SIZE(%q) 非法，回退默认 %d", *m.logMaxSize, defaultLogMaxSize))
		// M5 修正：同步把 easyconf 项修正为默认 "50"（SetItemValue 会更新绑定指针 *m.logMaxSize）
		_ = m.ec.SetItemValue("ROCKSYS_LOG_MAX_SIZE", strconv.Itoa(defaultLogMaxSize))
	}
	cfg := m.rebuildConfig()
	m.cfg.Store(cfg)
	watchers := append([]func(*Config){}, m.watchers...)
	for _, fn := range watchers {
		go fn(cfg)
	}
}

// publish 加锁公开版：装配期 defaultLoader/Register 调用（L1：装配期无并发，保持加锁版亦可）。
// 内部加 m.mu.Lock 后调 publishLocked。
func (m *confManager) publish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publishLocked()
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
// 默认始终监听工作目录 .env（开发规范下即 bin/.env）；当 ConfigFile 非空时额外监听该文件
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
				// 直接调无锁内部版；args/files 均在 reloadFilesLocked 内持锁读取（消除 TOCTOU）
				_ = m.reloadFilesLocked()
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
// 注册后触发一次"重载 + 广播"，保证挂件项能从环境变量/工作目录 .env/命令行读入
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
	// 注册后触发"重载 + 广播"：工作目录 .env 文件 → 环境变量 → 命令行重放
	// ★ 优先级必须与 defaultLoader/reloadFilesLocked 一致（命令行 > 环境变量 > .env）：
	// 先读文件（低优先）、再环境变量（覆盖）、最后命令行（最高）。
	// ⚠️ 装配期例外（中-6/L1）：Register 仅在 StartWatcher 前的装配期调用，实际无并发，
	//    故保持无锁直调 SetValuesByEnvFile/SetValuesByEnv/重放 args（与既有行为一致）。
	//    若未来运行期再注册，须改为持 m.mu（可复用 reloadFilesLocked 的语义）。
	if err := m.ec.SetValuesByEnvFile(envFile); err != nil {
		return err
	}
	if err := m.ec.SetValuesByEnv(); err != nil {
		return err
	}
	for k, v := range parseArgsToMap(m.args) {
		_ = m.ec.SetItemValue(k, v)
	}
	m.publish() // 装配期无并发，加锁公开版可接受（L1）
	return nil
}

// SyncDefaultFile 将全部已注册配置项的默认值快照（DefaultString，含标题/默认值说明/用法注释）写入 default.env。
// default.env 代表代码中的真实兜底行为，参与取值（最低优先级兜底，优先级由 easyconf 决定）。
// 装配完成后调用（buildServer 尾部 / --gen-env）；持锁串行，避免与 Set/reload 并发 race。
func (m *confManager) SyncDefaultFile() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	content := m.ec.DefaultString()
	if content == "" {
		return nil
	}
	return os.WriteFile(defaultEnvFile, []byte(content+"\n"), 0o644)
}

// Set 运行期按注册名全名设值并广播。
// ★ 工程化第一原则「热更即持久化」：热更立即生效后，同步写回配置源文件
// （--config 指定时写 configFile，否则写工作目录 .env），保证重启后状态保留。
// 持久化失败返回 error（此时热更已生效，调用方需知晓持久化未落盘）。
// ★ 最终时序（§2.3）：整体持 m.mu；currentValue 值比较防循环 → SetItemValue →
//
//	publishLocked → syncArgsLocked → watchFiles 取最后文件 UpdateFile 持久化。
//
// ⚠️ 防死锁（H1）：已持 m.mu，内部只能调用 publishLocked/syncArgsLocked，
//
//	不得调用加锁版 publish()/syncArgs()（同一 goroutine 二次 Lock 永久阻塞）。
func (m *confManager) Set(name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 防循环：值相同则跳过（不重写配置源、不重复广播）
	cur, ok := m.currentValue(name)
	if !ok {
		// M7 行为变更：未注册 key 直接 return nil（不写盘不广播）
		return nil
	}
	// ⚠️ EqualFold 仅对级别类枚举键（m.isCaseInsensitiveKey）；路径类值（ROCKSYS_LOG_FILE）
	//    Linux 大小写敏感，不得 EqualFold，否则大小写差异被误判相同而跳过写盘。
	if (m.isCaseInsensitiveKey(name) && strings.EqualFold(cur, value)) || cur == value {
		return nil
	}
	if err := m.ec.SetItemValue(name, value); err != nil {
		return err
	}
	m.publishLocked()             // ★ 无锁内部版（不得调用加锁的 publish()）
	m.syncArgsLocked(name, value) // ★ 无锁内部版（不得调用加锁的 syncArgs()）
	files := m.watchFiles()
	if err := m.ec.UpdateFile(files[len(files)-1]); err != nil {
		return fmt.Errorf("set %s: 热更已生效，但持久化到配置文件失败: %w", name, err)
	}
	return nil
}

// List 列出全部已注册配置项元数据（含底座与各挂件）。
// 跳过注释项（Name 为空）与 Value 为 nil 的异常项，避免序列化 panic。
// ★ 收口到 m.mu：遍历 GetItems/GetValue 与 Set/reloadFilesLocked 的 easyconf 项读写
//
//	互斥，避免与热更/ watcher 轮询并发时构成数据竞争（M2）。
func (m *confManager) List() []ConfigItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.ec.GetItems()
	out := make([]ConfigItem, 0, len(items))
	for _, it := range items {
		if it == nil || it.Name == "" || it.Value == nil {
			continue
		}
		item := ConfigItem{
			Key:     it.Name,
			Title:   it.Title,
			Defval:  it.GetDefaultValue(),
			Current: it.GetValue(),
			Example: strings.Join(it.Usage, " "),
		}
		out = append(out, item)
	}
	return out
}

// currentValue 读当前注册值。ok=false 表示未注册。
// ★ easyconf.Conf 无 GetItem(name) 方法，只有 GetItems()；用 GetItems() 遍历匹配 name。
//
//	bool/int 项的 GetValue() 返回字符串形态（"true"/"false"），与 Set 入参同为字符串，可直接比较。
//
// ★ 调用方须已持 m.mu（避免与 Set/reloadFilesLocked 的 easyconf 项读写并发 race）。
func (m *confManager) currentValue(name string) (string, bool) {
	for _, it := range m.ec.GetItems() {
		if it != nil && it.Name == name && it.Value != nil {
			return it.GetValue(), true
		}
	}
	return "", false
}

// isCaseInsensitiveKey 哪些 key 的值比较忽略大小写（级别类枚举）；其余（路径等）精确比较。
// ⚠️ 仅 ROCKSYS_LOG_LEVEL 用 EqualFold；路径类值（ROCKSYS_LOG_FILE）Linux 上大小写敏感，不得 EqualFold。
func (m *confManager) isCaseInsensitiveKey(name string) bool {
	return name == "ROCKSYS_LOG_LEVEL"
}

// syncArgsLocked 热更写回后同步更新内存命令行参数，避免 watcher 重放旧值覆盖。
// ★ 仅当 key 原本已存在于 m.args 时才更新其值；否则**不追加**——追加会使后续
//
//	用户直接改工作目录 .env 的同一 key 永久失效（命令行优先级覆盖，直到重启）。
//
// ★ 需同时处理两种形态：`--name=value` 与 `--name value`（空格分隔，见 parseArgsToMap）。
//
//	遍历 m.args：命中任一形态则替换为新值；两种形态都不存在则跳过（不追加）。
//
// ★ 仅处理两种 value 形态；无值开关型（--name 后直接下一个参数）不在本方案覆盖——
//
//	遇到 `--name` 后跟 `--` 开头的参数时跳过。
//
// ★ 本函数为「无锁内部版」，调用方须已持 m.mu（由 Set 持有）；禁止自行加锁。
func (m *confManager) syncArgsLocked(name, value string) {
	full := "--" + name
	for i := 0; i < len(m.args); i++ {
		a := m.args[i]
		// 形态1：--name=value（含 `--name=` 与任意旧值）
		if strings.HasPrefix(a, full+"=") {
			m.args[i] = full + "=" + value
			continue
		}
		// 形态2：--name value（value 不得以 "--" 开头；`--name` 后跟 `--` 开头参数时跳过）
		if a == full && i+1 < len(m.args) && !strings.HasPrefix(m.args[i+1], "--") {
			m.args[i+1] = value
			i++
			continue
		}
	}
}

// syncArgs 公开入口：加锁后调内部版（供其他路径复用）。
func (m *confManager) syncArgs(name, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncArgsLocked(name, value)
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
