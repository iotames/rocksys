// 底座唯一入口：装配全部 internal + plugins 并启动。
// 除装配外无任何业务代码。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"rocksys/internal/adminapi"
	"rocksys/internal/catalog"
	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/db"
	"rocksys/internal/engine"
	"rocksys/internal/geoip"
	"rocksys/internal/hotswap"
	"rocksys/internal/netutil"

	"github.com/iotames/easydb"

	"rocksys/plugins/auth"
	"rocksys/plugins/config"
	"rocksys/plugins/copy"
	"rocksys/plugins/dispatch"
	"rocksys/plugins/mq"
	"rocksys/plugins/object"
	"rocksys/plugins/obs"
	"rocksys/plugins/registry"
	"rocksys/plugins/result"
	"rocksys/plugins/rewrite"
	"rocksys/plugins/script"
	"rocksys/plugins/shield"
	"rocksys/plugins/trace"

	"rocksys/webui"

	"github.com/iotames/easyserver/log"

	_ "github.com/go-sql-driver/mysql" // 注册 mysql 驱动（DB_DRIVER=mysql）
	_ "github.com/lib/pq"              // 注册 postgres 驱动（DB_DRIVER=postgres）
	_ "modernc.org/sqlite"
)

// shutdownTimeout 优雅停机总时限（§7.1 步骤 8）。
const shutdownTimeout = 30 * time.Second

// Lua 脚本执行超时经配置项 SCRIPT_TIMEOUT 注册（默认 100ms，见 buildServer 装配）。

// 构建时经 -ldflags 注入：Version 为当前项目 git 最新 tag（无 tag 时回退 dev），
// BuildTime 为构建时间；GoVersion 取编译时 runtime。由 --version/-version 命令展示。
var (
	Version   = "dev"
	BuildTime = "unknown"
	GoVersion = runtime.Version()
)

// Server 已装配的运行单元（engine + admin + mgr），供 main 与测试复用。
type Server struct {
	cfgMgr   conf.Manager
	chain    *chain.Chain
	eng      *engine.Engine
	mgr      *hotswap.Manager
	adminSrv *adminapi.AdminServer
	dataDB   *db.DB                // 统一数据访问层（DB_DRIVER/DB_DSN），mq 等插件复用；nil 表示未启用
	recorder *shield.EventRecorder // WAF 拦截事件记录器（dataDB 就绪时创建，setter 注入 shield；nil 表示未启用）
	autoBan  *shield.AutoBanEngine // 自动拉黑引擎（dataDB 就绪时创建，按配置启动；nil 表示未启用）

	geoSyncStop chan struct{} // GeoIP 自动同步定时器停止通道（nil=未启动：dataDB 未就绪或 mmdb 未加载）
}

func main() {
	// --version / -version：打印版本信息后退出；--gen-env：生成全量默认值快照后退出，均不启动服务。
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-version":
			printVersion()
			return
		case "--gen-env":
			if err := genDefaultEnv(os.Args[2:]); err != nil {
				log.Error("gen-env failed", "err", err)
				os.Exit(1)
			}
			return
		}
	}

	srv, err := buildServer(os.Args[1:])
	if err != nil {
		log.Error("rocksys assemble failed", "err", err)
		os.Exit(1)
	}

	// 6. 启动配置热更监听（★ 始终启动：默认监听工作目录 .env；--config 指定时额外监听该文件，见 §2.4）
	// 弱依赖：热更失效仅影响运行期配置变更，不阻断启动，故仅记录不 fail。
	if err := srv.cfgMgr.StartWatcher(); err != nil {
		log.Error("start config watcher", "err", err)
	}

	// 5a/7. 启动 admin API 与主引擎监听。
	// ★ fail-fast 红线（启动期）：端口绑定失败（如 EADDRINUSE）必须立刻退出，不得静默存活——
	// 否则进程"看似在线"实则服务端口未监听，运维无感知。监听错误经 errCh 上报；
	// 正常优雅停机（Shutdown）返回 http.ErrServerClosed，不视为错误。
	errCh := make(chan error, 2)
	go func() {
		if err := srv.adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("admin server: %w", err)
		}
	}()
	go func() {
		if err := srv.eng.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("server: %w", err)
		}
	}()

	// 8. 等待信号或监听失败，二者其一即进入停机流程
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	if err := waitForQuitOrFail(quit, errCh); err != nil {
		log.Error("rocksys 监听失败，fail fast", "err", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// 8a. 停止接收新请求（主代理 + admin listener）
	_ = srv.eng.Shutdown(ctx)
	_ = srv.adminSrv.Shutdown(ctx)

	// 8b. 关闭挂件（逆序：先停 obs flush 日志，再停 hotswap 排空组件，最后停配置热更）
	_ = srv.mgr.Shutdown(ctx)
	_ = srv.cfgMgr.Shutdown(ctx)
	// WAF 拦截事件记录器：停机前 flush 缓冲通道内剩余事件（防丢），须先于 dataDB 关闭。
	// 自动拉黑引擎：随 shield 停机停止（处理同步写库，无缓冲需 flush）。
	if srv.autoBan != nil {
		srv.autoBan.Stop()
	}
	if srv.recorder != nil {
		srv.recorder.Stop()
	}
	if srv.geoSyncStop != nil {
		close(srv.geoSyncStop) // 停止 GeoIP 自动同步定时器
	}
	if srv.dataDB != nil {
		_ = srv.dataDB.Close() // mq/obs/admin 均复用 dataDB 连接，一并关闭
	}
}

// waitForQuitOrFail 等待停机信号或监听失败（fail-fast 红线：监听失败必须立刻退出，不得静默存活）。
// 返回 nil 表示收到停机信号，进入优雅停机；返回 error 表示监听失败，调用方应 log.Error + os.Exit(1)。
func waitForQuitOrFail(quit <-chan os.Signal, errCh <-chan error) error {
	select {
	case <-quit:
		log.Info("rocksys shutting down...")
		return nil
	case err := <-errCh:
		return err
	}
}

// buildServer 装配全部组件（§7.1 步骤 1-5）：加载配置 → 建链 → 建引擎 → 建 hotswap → 注册全部挂件。
func buildServer(args []string) (*Server, error) {
	// 1. 加载配置
	cfgMgr, err := conf.Load(args)
	if err != nil {
		return nil, err
	}
	// ── 统一外挂脚本根目录装配（★ 必须在任何 NewScriptDir 调用前完成）────────────────
	// 全项目"内嵌兜底、外挂覆写"的加载（SQL/WAF 规则/可信代理）统一经
	// internal/hotswap 收敛入口构造，外挂覆写根目录统一为 HOT_SCRIPTS_DIR
	// （默认 hotscripts，相对工作目录；各业务固定子目录 sql/rules/trusted_proxies）。
	// ★ 时序：conf.Load 后立即注册（此时尚无 watcher，Register 广播不会触发日志初始化），
	// 再注入 hotswap。
	var hotScriptsDir string
	if err := cfgMgr.Register(&hotScriptsDir, "HOT_SCRIPTS_DIR", "hotscripts",
		"外挂脚本统一根目录（相对工作目录；sql/rules/trusted_proxies 等业务外挂子目录均位于其下，内嵌兜底）",
		"修改后需重启服务生效"); err != nil {
		return nil, fmt.Errorf("register HOT_SCRIPTS_DIR: %w", err)
	}
	hotswap.SetHotScriptsDir(hotScriptsDir)

	// ── 外挂文件统一内容中枢装配（ScriptHub：缓存 + 监控 + 推送统一热更）────
	// 三类外挂文件（sql/、rules/、trusted_proxies/）统一经中枢缓存 + 监控 + 推送：
	// 文件变更 ≤ HOT_FILES_WATCH_INTERVAL（默认 3s）内自动生效，免重启、免借配置热更。
	// 消费端只认识 GetScriptText / Subscribe 两个接口，不感知内容如何生产。
	// ★ 时序：hub 构造须早于各消费端（shield.New / db.Open / netutil.SubscribeHub）注册，
	// ★ 监控循环 Start 在所有子目录注册完成后调用（见 buildServer 尾部 scriptHub.Start()）。
	var watchIntervalSec int
	if err := cfgMgr.Register(&watchIntervalSec, "HOT_FILES_WATCH_INTERVAL", "3",
		"外挂文件统一监控轮询间隔(秒，≥1；≤0 回落默认 3)",
		"热更生效（修改 hotscripts 下 sql/rules/trusted_proxies 外挂文件后 ≤3s 自动生效，无需重启）"); err != nil {
		return nil, fmt.Errorf("register HOT_FILES_WATCH_INTERVAL: %w", err)
	}
	scriptHub := hotswap.NewScriptHub(time.Duration(watchIntervalSec) * time.Second)

	// ── 日志系统装配（时序硬约束）──────────────────────────────────────
	// 日志输出模板为 easyserver/log 包内置常量 defaultLogTpl
	// （time={{.time}} level={{.level}} msg={{.msg}}），不再走外挂机制。
	// 时序硬约束：须在首次日志调用（下方 log.Info("rocksys starting")）之前、且早于所有
	// conf.Register（其 publish 会触发 watcher 回调 → log.GetInfo() → 日志初始化）。

	// 1. 文件存档（E1/E2）。
	if cfgMgr.Current().LogToFile {
		log.SetLogWriterByFile(cfgMgr.Current().LogFile)
		log.SetMaxSize(cfgMgr.Current().LogMaxSize)
	}

	// 2. 级别钩子 → 持久化（必须在启动级别之前注册，启动级别变更才会写盘保留）。
	log.SetOnLevelChange(func(level string) {
		_ = cfgMgr.Set("ROCKSYS_LOG_LEVEL", level)
	})

	// 3. 启动级别（★ 替换原 log.SetLevel(slogLevel(...))，不保留两处）。
	log.SetLevel(slogLevel(cfgMgr.Current().LogLevel))

	// 4. 订阅配置热更（§2.5）：PUT /admin/config 改级别/文件的唯一生效通道（异步秒级内生效）。
	cfgMgr.Watch(func(cfg *conf.Config) {
		log.SetLevel(slogLevel(cfg.LogLevel))
		if cfg.LogToFile && !log.GetInfo().FileOn {
			log.SetLogWriterByFile(cfg.LogFile)
			log.SetMaxSize(cfg.LogMaxSize)
		} else if cfg.LogToFile && log.GetInfo().FileOn {
			// 文件通道已开：同步 E2 上限热更（否则改 ROCKSYS_LOG_MAX_SIZE 静默无效）。
			log.SetMaxSize(cfg.LogMaxSize)
		} else if !cfg.LogToFile && log.GetInfo().FileOn {
			log.SetFileWriter(false)
		}
	})

	log.Info("rocksys starting",
		"upstream", cfgMgr.Current().DefaultUpstream,
		"listen", cfgMgr.Current().ListenAddr,
		"admin", cfgMgr.Current().AdminAddr)

	// ── 可信代理列表装配（netutil 依赖，须在首次请求处理前完成）────────────────
	// hotswap 外挂优先/内嵌兜底（默认 127.0.0.1）。经 ScriptHub 统一内容中枢管理：
	// 注册子目录 + 订阅热更 + 初始加载（全部走统一缓存），改外挂文件 ≤3s 自动原子替换（免重启）。
	// TRUSTED_PROXIES_FILE 为相对外置根目录 trusted_proxies 的文件路径，不允许绝对路径。
	var trustedProxiesFile string
	if err := cfgMgr.Register(&trustedProxiesFile, "TRUSTED_PROXIES_FILE", "trusted_proxies.txt",
		"可信代理列表文件（相对 trusted_proxies 外置目录的相对路径，不允许绝对路径；外挂优先，缺失回退内嵌 127.0.0.1）",
		"≤3s 自动热更（修改 hotscripts/trusted_proxies 外挂文件后自动生效，无需重启）"); err != nil {
		return nil, fmt.Errorf("register TRUSTED_PROXIES_FILE: %w", err)
	}
	if err := netutil.SubscribeHub(scriptHub, trustedProxiesFile); err != nil {
		return nil, fmt.Errorf("netutil.SubscribeHub: %w", err)
	}

	// GeoIP 历史回填（数据库页入口）执行模型见 geoip_sync.go 头注（后台任务，无时间预算）：
	// 回填是大表批量读写，到点即在扫描块/更新批边界收工返回已完成进度（客户端断开同样立即停），
	// 避免「前端早已超时、服务端仍在长时间读写占锁」。属实现细节，不做配置项。

	// 2. 创建转发链（初始为空）
	ch := chain.New()

	// 3. 创建引擎（内部调用 easyserver.NewServer + AddMiddleHead 注册 chain 适配器）
	eng := engine.New(cfgMgr, ch)

	// 4. 创建 hotswap 管理器（订阅配置热更）
	mgr := hotswap.NewManager(ch, cfgMgr)
	mgr.SetScriptHub(scriptHub) // 外挂文件监控循环随管理器生命周期启停（Shutdown 统一停止）

	// 统一数据访问层（§? 数据访问层）：为可插拔组件（obs/mq/dispatch 等）提供 easydb 数据操作 + SQL 脚本逐级加载。
	// 配置：DB_DRIVER（默认 sqlite，零配置）/ DB_DSN（默认 rocksys.db）。
	// SQL 脚本外挂覆写目录统一为 HOT_SCRIPTS_DIR/sql（默认 hotscripts/sql，内嵌 sql/ 兜底；不再有独立 SQL_DIR 配置）。
	// 打开失败不阻断底座启动（底座仅反向代理），仅记录警告；obs 的默认 db 存储与 mq/dispatch 等依赖方因此不可用。
	// ★ 必须先于 obs 创建：obs 复用本数据访问层写 access_log 表；未就绪时 obs 降级丢弃日志并告警。
	// ★ 必须先于 dispatch 主件装配：dispatch 注入本数据访问层拉路由四表（未就绪时 dispatch 走降级重试）。
	var dataDB *db.DB
	var dbDriver, dbDSN string
	if err := cfgMgr.Register(&dbDriver, "DB_DRIVER", "sqlite", "数据库驱动名（sqlite/mysql/postgres）"); err != nil {
		return nil, fmt.Errorf("register DB_DRIVER: %w", err)
	}
	if err := cfgMgr.Register(&dbDSN, "DB_DSN", "rocksys.db?_busy_timeout=5000&_journal_mode=WAL",
		"数据库连接串（不同驱动取值不同；sqlite 默认已含 busy_timeout=5000 与 WAL，可显式覆盖）",
		"  sqlite（默认）: rocksys.db 或 rocksys.db?_busy_timeout=5000&_journal_mode=WAL",
		"  mysql:    user:pass@tcp(127.0.0.1:3306)/rocksys?charset=utf8mb4&parseTime=true",
		"  postgres（两种写法等价，任选其一）:",
		"    URI 形式:  postgres://postgres:password@127.0.0.1:5432/rocksys?sslmode=disable",
		"    键值形式:  host=127.0.0.1 port=5432 user=postgres password=yourpassword dbname=rocksys sslmode=disable",
	); err != nil {
		return nil, fmt.Errorf("register DB_DSN: %w", err)
	}
	var dbOpenErr error
	if d, err := db.Open(dbDriver, dbDSN, scriptHub); err != nil {
		dbOpenErr = err
		log.Warn("db: 数据访问层初始化失败（不阻断底座）", "driver", dbDriver, "err", err.Error())
	} else {
		dataDB = d
		log.Info("db: 数据访问层已就绪", "driver", dataDB.Driver())
	}

	// 5. 注册挂件（链中间件用 RegisterMiddleware，独立组件用 RegisterComponent；见 §6.1）
	// 链中间件执行顺序：Head/Middle 槽位按注册顺序；Tail 槽位逆序（§4.4）——
	// 故 Tail 上 obs 先注册、result 后注册 → result 先改写响应、obs 后记录最终状态。
	shieldMw, err := shield.New(cfgMgr, scriptHub) // hub 注入：rules/ 子目录注册 + 订阅热更（≤3s 自动重建 WAF 快照）
	if err != nil {
		return nil, fmt.Errorf("shield.New: %w", err)
	}
	mgr.RegisterMiddleware(shieldMw)           // L1 防护 → chain.Head
	mgr.RegisterMiddleware(trace.New(&cfgMgr)) // trace 透传 → chain.Head
	mgr.RegisterMiddleware(auth.New(&cfgMgr))  // JWT 认证 → chain.Head

	// L2 路由分发：双中间件共用同一 registry 运行态——
	// 主件（Middle：匹配 + 选点 + 在途 +1 + sticky 种值 + 写 Target）与收尾件
	// （Tail：转发完成后在途 -1，Start/Stop 为 no-op，生命周期由主件统一驱动）；
	// DISPATCH_ENABLED 经 autoEnableMap 两名同键联动启停（装配注册顺序固定保证链序确定）。
	dispatchReg := dispatch.NewRegistry()
	dispatchMain := dispatch.New(cfgMgr, dispatch.NewDBSource(dataDB), dispatchReg)
	mgr.RegisterMiddleware(dispatchMain)                     // 主件 → chain.Middle
	mgr.RegisterMiddleware(dispatch.NewTailFin(dispatchReg)) // 收尾件 → chain.Tail

	mgr.RegisterMiddleware(rewrite.New(cfgMgr)) // L2 转发前改写 → chain.Middle

	// Lua 策略执行超时经配置中心注册（默认 100ms，可经 SCRIPT_TIMEOUT 覆盖）。
	// 装配期生效：script.New 拷贝超时值，热更改值需重启进程才生效。
	var scriptTimeoutMS int
	if err := cfgMgr.Register(&scriptTimeoutMS, "SCRIPT_TIMEOUT", "100", "Lua 脚本执行超时(毫秒)", "修改后需重启服务生效"); err != nil {
		return nil, fmt.Errorf("register SCRIPT_TIMEOUT: %w", err)
	}
	mgr.RegisterMiddleware(script.New(time.Duration(scriptTimeoutMS)*time.Millisecond, cfgMgr)) // Lua 策略 → chain.Middle

	// ── GeoIP 功能总开关（默认开启；关闭后解析/自动同步/统计全部停用）────────
	// 开关语义内聚在 geoip.Resolver 单点裁决（Enabled/Ready/Lookup），消费方不各自读配置：
	// 关闭后——读侧实时解析停用（明细/Top IP 地区列显示「服务未开启」，历史已入库地区照常）、
	// 自动同步定时器停摆、日程登记降级、概览页地理卡降级为未开启引导；
	// 手动同步例外（特殊异步 DB 维护任务，只要求 mmdb 已加载，见 SyncReady）。
	// 支持热更（下方 Watch 注入，秒级生效）。
	var geoipEnabled bool
	if err := cfgMgr.Register(&geoipEnabled, "GEOIP_ENABLED", "true",
		"是否启用 GeoIP 地理位置功能（总开关，默认开启；关闭后地区实时解析与自动同步全部停用）",
		"关闭后：明细与统计的地区列显示「服务未开启」，概览页地理位置卡降级为功能未开启引导，自动同步定时器停摆；数据库页手动同步不受本开关限制（特殊异步 DB 维护任务，只要求 mmdb 已加载）；修改后热更秒级生效（启动时禁用的自动同步定时器需重启拉起）",
	); err != nil {
		return nil, fmt.Errorf("register GEOIP_ENABLED: %w", err)
	}

	// ── GeoIP 解析器（写时解析：obs/shield 共享一个实例）────────────────
	// 惰性加载：无 mmdb 时仅告警降级（geo 列空串），不阻断转发；文件补放后重启生效。
	var geoipDir string
	if err := cfgMgr.Register(&geoipDir, "GEOIP_MMDB_DIR", "geoip",
		"GeoIP 数据目录（含 GeoLite2-City.mmdb / GeoLite2-Country.mmdb，逐文件独立按 本目录→工作目录→~/geoip 查找）",
		"未放置 mmdb 时地理位置统计降级为「未知」（日志告警一次）；文件补放后重启生效",
	); err != nil {
		return nil, fmt.Errorf("register GEOIP_MMDB_DIR: %w", err)
	}
	geoRes := geoip.NewResolver(geoipDir)
	geoRes.SetEnabled(geoipEnabled)
	// 开关热更注入（Watch 回调在独立 goroutine 执行）：GEOIP_ENABLED 运行期改值秒级生效，
	// 与其他 GEOIP_* 绑定变量直写热更同一模式。
	cfgMgr.Watch(func(*conf.Config) { geoRes.SetEnabled(geoipEnabled) })

	// ── GeoIP 自动同步间隔（间隔值内置开关语义：0=关闭，不设独立开关）──
	// int 分钟：0=关闭自动同步（手动不受影响）；有效最小 10（防设置过小耗尽资源）；
	// <10（非 0）或非法回落默认 60。生效前置 = GeoIP 服务就绪（GEOIP_ENABLED=true 且 mmdb 已加载，
	// 未就绪则定时器不启动，见下方启动处）；运行中改值下一轮生效（每轮触发时重读本变量当前值，含 0=关闭的动态判定）。
	if err := cfgMgr.Register(&geoipSyncIntervalMin, "GEOIP_SYNC_INTERVAL", "60",
		"GeoIP 关联表（geoip_list）自动同步间隔（分钟；0=关闭自动同步，手动同步不受影响；最小 10，更小值回落默认 60）",
		"生效前置：GeoIP 服务就绪（GEOIP_ENABLED=true 且 mmdb 已加载；未就绪时定时器不启动）；运行中修改下一轮生效",
	); err != nil {
		return nil, fmt.Errorf("register GEOIP_SYNC_INTERVAL: %w", err)
	}

	// ── GeoIP 同步单趟配额（趟长有界化：回填提速与锁窗口的折中，两上限先到为准）──
	// 0=不限（一趟扫到表尾为止）；负数回落默认。增量稳态下每趟新 IP 远够不着配额，行为不变；
	// 存量回填时默认约为旧行为 10 倍吞吐，极端情况可临时调 0 跑无限趟、或调小限流。
	// 运行中改值下一趟生效（每趟开始时经 geoSyncQuotaLimits 重读）。
	if err := cfgMgr.Register(&geoSyncIPsPerRun, "GEOIP_SYNC_IPS_PER_RUN", "50000",
		"GeoIP 同步单趟处理的缺失 IP 上限（0=不限；负数回落默认 50000）",
		"调大可减少存量回填趟数（下轮同步自动断点续扫）；增量稳态用不到配额，保持默认即可",
	); err != nil {
		return nil, fmt.Errorf("register GEOIP_SYNC_IPS_PER_RUN: %w", err)
	}
	if err := cfgMgr.Register(&geoSyncScanCapRows, "GEOIP_SYNC_SCAN_CAP", "1000000",
		"GeoIP 同步单趟扫描日志行数上限（0=不限；负数回落默认 1000000）",
		"即使缺失行极稀疏也保证单趟耗时可控；配额先到即收工，下趟断点续扫",
	); err != nil {
		return nil, fmt.Errorf("register GEOIP_SYNC_SCAN_CAP: %w", err)
	}

	// ── WAF 拦截监控统计 ───────────────────────────────────────────────
	// 拦截请求在 shield 处短路（obs 在 Tail 槽位看不到），故记录器必须在 shield 拦截点采集。
	// 装配方式：DB 就绪后经 setter 注入（shield.New 签名不变，保持挂件独立性）；
	// dataDB 未就绪时跳过——内存滑动窗口计数仍可用，仅明细落库/查询端点降级。
	var recorder *shield.EventRecorder
	var autoBan *shield.AutoBanEngine // 自动拉黑引擎（dataDB 就绪时创建，按配置启动）
	// ★ 有意无条件装配（与 SHIELD_ENABLED 解耦）：SHIELD_ENABLED 支持配置热更，
	// 若仅在启用时才创建 recorder，热更 false→true 后 recorder 不会出现，故始终装配；
	// shield 禁用时仅两个空转 goroutine（flush/prune）+ 空表，成本可忽略。
	if dataDB != nil {
		recorder = shield.NewEventRecorder(cfgMgr, dataDB)
		shieldMw.SetEventRecorder(recorder) // 拦截点 → Record()（nil 安全，重复注入以最后一次为准）
		recorder.SetGeoip(geoRes)           // 写时解析 geo 列（共享实例）

		// WAF 方案：动态 IP 黑白名单 DB 化（管理面 CRUD/导入 + 拦截链路快照合并）。
		// 注入即建表 + 重建快照 + 启动 TTL 兜底刷新（60s）；nil 场景由 SetIPListStores 内部跳过。
		shieldMw.SetIPListStores(
			shield.NewIPListStore(dataDB.EasyDB(), dataDB, true),
			shield.NewIPListStore(dataDB.EasyDB(), dataDB, false),
		)
		// 攻击证据归档表（WAF 方案 §4.3：本期仅建表，无业务逻辑）。
		if err := shield.EnsureAttackArchiveTable(dataDB.EasyDB(), dataDB); err != nil {
			log.Warn("shield: 攻击证据归档表初始化失败（本期仅建表，不影响防护）", "err", err.Error())
		}

		// IP 黑名单增强 STEP A4：自动拉黑引擎（配置项无条件注册，default.env 全量快照恒含）。
		// 引擎无条件启动（Start 移至装配完成后）：disabled 时循环空转（零 DB 开销），
		// 运行期开关热更 true/false 均下一轮即生效（每轮开始读配置最新值，开关/阈值/窗口/TTL 均支持热更）。
		// 若仅 Enabled() 时才 Start，false 启动后运行期改 true 永远不会启动 goroutine。
		autoBan = shield.NewAutoBanEngine(cfgMgr, shieldMw, recorder)
	}

	obsMw := obs.New(cfgMgr, dataDB) // 访问日志/指标 → chain.Tail(+ResponseHook)
	obsMw.SetGeoip(geoRes)           // 写时解析 geo 列（共享实例）
	mgr.RegisterMiddleware(obsMw)
	mgr.RegisterMiddleware(copy.New(cfgMgr))   // 请求抄送 → chain.Tail(+ResponseHook)
	mgr.RegisterMiddleware(result.New(cfgMgr)) // L3 结果 → chain.Tail(+ResponseHook)

	// 独立组件（RegisterComponent）：config/registry/object 无条件注册。
	mgr.RegisterComponent(config.New(cfgMgr))   // KV 配置服务
	mgr.RegisterComponent(registry.New(cfgMgr)) // 服务注册中心
	mgr.RegisterComponent(object.New(cfgMgr))   // 对象存储（OBJECT_BASE_DIR 配置根目录）

	// mq 条件装配：MQ_ENABLED=true 时注册；outbox 表建于统一数据访问层业务库（DB_DRIVER/DB_DSN），
	// 与架构一致（outbox 与业务数据同库，支持 stbiz 本地事务同提交）。
	// dataDB 未就绪时跳过注册（组件降级，不阻断底座）。
	// ★ MQ_* 运行参数与 MQ_ENABLED 一起无条件注册（不随开关分支），保证 default.env 全量快照恒含 MQ_ 全组。
	var mqEnabled bool
	if err := cfgMgr.Register(&mqEnabled, "MQ_ENABLED", "false", "是否启用异步消息组件（Outbox 表建于统一数据访问层业务库，DB_DRIVER/DB_DSN）"); err != nil {
		return nil, fmt.Errorf("register MQ_ENABLED: %w", err)
	}
	var mqPollIntervalMS, mqMaxRetries, mqBaseBackoffMS int
	var mqConsumerBaseURL string
	if err := cfgMgr.Register(&mqPollIntervalMS, "MQ_POLL_INTERVAL", "1000", "消息投递轮询间隔(毫秒)", "修改后需重启服务生效"); err != nil {
		return nil, fmt.Errorf("register MQ_POLL_INTERVAL: %w", err)
	}
	if err := cfgMgr.Register(&mqMaxRetries, "MQ_MAX_RETRIES", "3", "消息投递最大重试次数（超限转死信；0 视为未设置，回落默认 3）", "修改后需重启服务生效"); err != nil {
		return nil, fmt.Errorf("register MQ_MAX_RETRIES: %w", err)
	}
	if err := cfgMgr.Register(&mqBaseBackoffMS, "MQ_BASE_BACKOFF", "100", "消息重试指数退避基数(毫秒)", "修改后需重启服务生效"); err != nil {
		return nil, fmt.Errorf("register MQ_BASE_BACKOFF: %w", err)
	}
	if err := cfgMgr.Register(&mqConsumerBaseURL, "MQ_CONSUMER_BASE_URL", "", "消息默认消费方地址（未命中 topic 路由时使用）", "修改后需重启服务生效"); err != nil {
		return nil, fmt.Errorf("register MQ_CONSUMER_BASE_URL: %w", err)
	}
	if mqEnabled {
		if dataDB == nil {
			log.Warn("mq: 数据访问层未就绪（DB_DRIVER/DB_DSN），mq 组件未注册")
		} else {
			// 复用 dataDB 连接与 SQL 脚本源：同一方言（sql/<dbtype>/ 逐级加载），
			// mq.OutboxStore 已按方言兼容 LastInsertId（postgres 走 RETURNING）。
			// 运行参数（上述已无条件注册）注入 Options。
			mqComp := mq.New(dataDB.EasyDB().GetSqlDB(), "outbox")
			mqComp.SetSQLSource(dataDB)
			mqComp.SetOptions(mq.Options{
				Interval:        time.Duration(mqPollIntervalMS) * time.Millisecond,
				ConsumerBaseURL: mqConsumerBaseURL,
				MaxRetries:      mqMaxRetries,
				BaseBackoff:     time.Duration(mqBaseBackoffMS) * time.Millisecond,
			})
			mgr.RegisterComponent(mqComp)
			log.Info("mq component registered", "driver", dataDB.Driver())
		}
	}

	// 排空判定注入（§6.3）：Adapter.ActiveCount。
	mgr.SetDrainCheck(eng.ActiveCount)

	// 5a. admin API + 挂件端点注入（§8.1；挂件 handler 经 RegisterPlugin 注入）。
	// 管理接口用户认证复用统一数据访问层（dataDB），dataDB 未就绪时降级为静态 token/回环信任。
	var adminEDB *easydb.EasyDb
	if dataDB != nil {
		adminEDB = dataDB.EasyDB()
	}
	adminSrv := adminapi.New(cfgMgr.Current().AdminAddr, cfgMgr, mgr, adminEDB)
	adminSrv.SetCatalog(catalog.DefaultComponents(), catalog.DefaultServices()) // WebUI 全局组件/服务说明
	// 注入构建期版本信息（--version 同源，经 -ldflags 注入 main 包变量），供 WebUI 左上角展示。
	adminSrv.SetVersionInfo(Version, BuildTime, GoVersion)
	// 任务来源白名单追加本层拥有的提交点（内存态）：migrate/schema_apply/sql_exec 已由
	// adminapi 创建时注册；只有注册过的来源可提交任务（防未注册调用方混入）。
	// 名单经 GET /admin/tasks 透出，WebUI「后台任务」页以下拉框呈现。
	adminSrv.TaskCenter().RegisterCreators("geoip_sync")
	if dataDB == nil && dbOpenErr != nil {
		// 数据层不可用原因透传管理接口：WebUI 登录页据此展示降级引导（而非误导性注册面板）。
		adminSrv.SetStoreDegraded(fmt.Sprintf("数据库初始化失败（driver=%s）：%s", dbDriver, dbOpenErr.Error()))
	}
	if dataDB != nil {
		adminSrv.SetSQLSource(dataDB) // 用户存储 SQL 脚本源（sql/<dbtype>/admin_users_*.sql）
		// 表结构同步：表清单在装配处注册（表名在这里已知，无法从脚本文件名推断），
		// 数据连接与清单一并注入（详见 buildTableSpecs）。
		adminSrv.SetTableSpecs(dataDB, buildTableSpecs(db.TableShieldEvent))
		// 定时任务只读登记：装配期 upsert 登记（系统级整行重置），
		// geoSyncAll 收口经 geoSyncOnDone 回写 geoip_sync 行运行状态（单点回写）。
		schedReg := NewScheduleRegistry(dataDB)
		if err := schedReg.EnsureTable(); err != nil {
			log.Warn("schedule: 登记表初始化失败（定时任务页降级）", "err", err.Error())
		} else {
			RegisterScheduleRows(schedReg, geoRes.Ready())
			geoSyncOnDone = func(status, message string) {
				if err := schedReg.UpdateRunStatus(schedGeoipSync, status, message); err != nil {
					log.Warn("schedule: geoip_sync 状态回写失败", "err", err.Error())
				}
			}
			geoSyncOnSkip = func(message string) {
				if err := schedReg.UpdateSkipStatus(schedGeoipSync, message); err != nil {
					log.Warn("schedule: geoip_sync 跳过登记失败", "err", err.Error())
				}
			}
			if err := adminSrv.RegisterPlugin(PathScheduleList, ScheduleList(schedReg, cfgMgr, geoRes)); err != nil {
				log.Warn("schedule: 端点注册失败", "err", err.Error())
			}
		}
		// GeoIP 增量同步（数据库页「表数据」页签入口，POST 才生效）：增量构建/刷新 geoip_list 关联表。
		// 后台任务模式：提交任务即返回任务 ID，前端轮询任务详情展示进度与结果；无时间预算
		// （后台任务默认无超时），取消经任务中心统一取消端点送达。互斥组 = 运行库数据面。
		// 手动同步是特殊场景的异步 DB 维护任务：不受 GEOIP_ENABLED 限制（关闭态允许单独
		// 操作补齐关联表），仅 mmdb 未加载时 503 引导（先放置数据文件并重启）。
		adminSrv.RegisterPlugin("/admin/db/geoip_sync", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "同步为维护操作，仅接受 POST", http.StatusMethodNotAllowed)
				return
			}
			if geoRes == nil || !geoRes.SyncReady() {
				http.Error(w, "geoip: mmdb 未加载，无法同步；请放置 GeoLite2 mmdb（见概览页引导卡）并重启服务后重试", http.StatusServiceUnavailable)
				return
			}
			taskID, err := adminSrv.SubmitTask(geoSyncTaskSpec("GeoIP 数据同步", dataDB, geoRes, obsMw.PurgeTrafficCache))
			if err != nil {
				http.Error(w, "提交 GeoIP 同步任务失败："+err.Error(), http.StatusConflict)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"task_id": taskID,
			})
		})

		// 启动缺列检测：访问/拦截两表缺列只告警不自动迁移
		// （结构同步始终人工确认），提示管理员经 WebUI 补齐。
		for table, cols := range missingLogColumns(dataDB, buildTableSpecs(db.TableShieldEvent)) {
			log.Warn("db: 访问/拦截日志表缺列，统计与落库将受影响",
				"table", table, "missing", strings.Join(cols, ","),
				"howto", "登录 WebUI 打开「数据库」页查看表结构 diff，人工确认执行补列 SQL 后恢复（缺列期间两表落库暂停，转发不受影响）")
		}
	}
	scriptAdmin := script.NewAdminHandler(mgr)
	if err := adminSrv.RegisterPlugin(script.PathPublish, scriptAdmin.Publish); err != nil {
		return nil, fmt.Errorf("register script publish: %w", err)
	}
	if err := adminSrv.RegisterPlugin(script.PathRollback, scriptAdmin.Rollback); err != nil {
		return nil, fmt.Errorf("register script rollback: %w", err)
	}
	if err := adminSrv.RegisterPlugin(script.PathList, scriptAdmin.List); err != nil {
		return nil, fmt.Errorf("register script list: %w", err)
	}
	obsAdmin := obs.NewAdminHandler(mgr)
	obs.SetBlockAvailable(recorder != nil && recorder.LoggingEnabled())
	if err := adminSrv.RegisterPlugin(obs.PathTrafficSummary, obsAdmin.TrafficSummary); err != nil {
		return nil, fmt.Errorf("register traffic summary: %w", err)
	}
	if err := adminSrv.RegisterPlugin(obs.PathTrafficSeries, obsAdmin.TrafficSeries); err != nil {
		return nil, fmt.Errorf("register traffic series: %w", err)
	}
	if err := adminSrv.RegisterPlugin(obs.PathTrafficGeo, obsAdmin.TrafficGeo); err != nil {
		return nil, fmt.Errorf("register traffic geo: %w", err)
	}
	if err := adminSrv.RegisterPlugin(obs.PathTrafficCacheClear, obsAdmin.ClearCache); err != nil {
		return nil, fmt.Errorf("register traffic cache clear: %w", err)
	}
	if err := adminSrv.RegisterPlugin("/admin/metrics", obsAdmin.Metrics); err != nil {
		return nil, fmt.Errorf("register obs metrics: %w", err)
	}
	if err := adminSrv.RegisterPlugin("/admin/logs", obsAdmin.Logs); err != nil {
		return nil, fmt.Errorf("register obs logs: %w", err)
	}
	if err := adminSrv.RegisterPlugin("/admin/logs/storage", obsAdmin.Storage); err != nil {
		return nil, fmt.Errorf("register obs storage: %w", err)
	}
	if err := adminSrv.RegisterPlugin("/admin/logs/prune", obsAdmin.Prune); err != nil {
		return nil, fmt.Errorf("register obs logs prune: %w", err)
	}

	// shield 管理端点（WAF 监控统计）：metrics 实时计数 / events 明细 / stats 聚合 / prune 手动清理。
	// handler 经 mgr.GetMiddleware("shield") 拿实例，recorder 未注入时相应端点自动 503。
	shieldAdmin := shield.NewAdminHandler(mgr)
	if err := adminSrv.RegisterPlugin(shield.PathShieldMetrics, shieldAdmin.Metrics); err != nil {
		return nil, fmt.Errorf("register shield metrics: %w", err)
	}
	if err := adminSrv.RegisterPlugin(shield.PathShieldTotal, shieldAdmin.Total); err != nil {
		return nil, fmt.Errorf("register shield total: %w", err)
	}
	if err := adminSrv.RegisterPlugin(shield.PathShieldEvents, shieldAdmin.Events); err != nil {
		return nil, fmt.Errorf("register shield events: %w", err)
	}
	if err := adminSrv.RegisterPlugin(shield.PathShieldStats, shieldAdmin.Stats); err != nil {
		return nil, fmt.Errorf("register shield stats: %w", err)
	}
	if err := adminSrv.RegisterPlugin(shield.PathShieldPrune, shieldAdmin.Prune); err != nil {
		return nil, fmt.Errorf("register shield prune: %w", err)
	}
	// 小黑屋（当前在押的限时封禁条目预览；IP_BLACKLIST_PLAN §3.7）。
	if err := adminSrv.RegisterPlugin(shield.PathShieldJail, shieldAdmin.Jail); err != nil {
		return nil, fmt.Errorf("register shield jail: %w", err)
	}
	// 动态 IP 黑白名单管理（WAF 方案 §6.1）：列表 GET + 新增 POST 共用 path；
	// 更新/软删/恢复/导入为独立 POST 端点。DB 未配置时端点统一 503。
	for _, ep := range []struct {
		path string
		h    http.HandlerFunc
	}{
		{shield.PathBlacklist, shieldAdmin.Blacklist()},
		{shield.PathBlacklistUpdate, shieldAdmin.BlacklistUpdate()},
		{shield.PathBlacklistDelete, shieldAdmin.BlacklistDelete()},
		{shield.PathBlacklistRestore, shieldAdmin.BlacklistRestore()},
		{shield.PathBlacklistImport, shieldAdmin.BlacklistImport()},
		{shield.PathBlacklistSyncFile, shieldAdmin.BlacklistSyncFile()},
		{shield.PathBlacklistBan, shieldAdmin.BlacklistBan()},
		{shield.PathWhitelist, shieldAdmin.Whitelist()},
		{shield.PathWhitelistUpdate, shieldAdmin.WhitelistUpdate()},
		{shield.PathWhitelistDelete, shieldAdmin.WhitelistDelete()},
		{shield.PathWhitelistRestore, shieldAdmin.WhitelistRestore()},
		{shield.PathWhitelistImport, shieldAdmin.WhitelistImport()},
	} {
		if err := adminSrv.RegisterPlugin(ep.path, ep.h); err != nil {
			return nil, fmt.Errorf("register shield %s: %w", ep.path, err)
		}
	}
	// WAF 规则文件管理（WebUI「文件编辑」页签）：清单 / 读文件 / 保存外挂覆写。
	// 保存落点 HOT_SCRIPTS_DIR/rules/，ScriptHub 监控自动热更，无需重启。
	for _, ep := range []struct {
		path string
		h    http.HandlerFunc
	}{
		{shield.PathShieldRules, shieldAdmin.Rules},
		{shield.PathShieldRulesFile, shieldAdmin.RuleFile},
		{shield.PathShieldRulesSave, shieldAdmin.RuleSave()},
	} {
		if err := adminSrv.RegisterPlugin(ep.path, ep.h); err != nil {
			return nil, fmt.Errorf("register shield %s: %w", ep.path, err)
		}
	}

	// 可信代理文件在线编辑（WebUI「可信代理」页）：清单 / 读文件 / 保存外挂覆写。
	// 保存落点 HOT_SCRIPTS_DIR/trusted_proxies/，ScriptHub 监控自动热更，无需重启。
	proxyAdmin, err := netutil.NewProxiesAdmin(scriptHub, trustedProxiesFile)
	if err != nil {
		return nil, fmt.Errorf("new proxies admin: %w", err)
	}
	for _, ep := range []struct {
		path string
		h    http.HandlerFunc
	}{
		{netutil.PathProxyTrusted, proxyAdmin.List},
		{netutil.PathProxyTrustedFile, proxyAdmin.File},
		{netutil.PathProxyTrustedSave, proxyAdmin.Save()},
	} {
		if err := adminSrv.RegisterPlugin(ep.path, ep.h); err != nil {
			return nil, fmt.Errorf("register proxy %s: %w", ep.path, err)
		}
	}

	// 路由分发管理端点（仿 shield admin 模式）：规则/均衡器/节点/
	// 标签四组 CRUD + 命中测试 + 枚举字典 + 全局重载 + 健康快照，共 19 个。
	// DB 未配置时端点统一 503 降级（handler 内部自检）。
	dispatchAdmin := dispatch.NewAdminHandler(dispatchMain, dataDB)
	for _, ep := range []struct {
		path string
		h    http.HandlerFunc
	}{
		{dispatch.PathDispatchRules, dispatchAdmin.Rules},
		{dispatch.PathDispatchRulesUpdate, dispatchAdmin.RulesUpdate()},
		{dispatch.PathDispatchRulesDelete, dispatchAdmin.RulesDelete()},
		{dispatch.PathDispatchRulesRestore, dispatchAdmin.RulesRestore()},
		{dispatch.PathDispatchMatchTest, dispatchAdmin.RulesMatchTest()},
		{dispatch.PathDispatchRulesMeta, dispatchAdmin.RulesMeta},
		{dispatch.PathDispatchReload, dispatchAdmin.Reload()},
		{dispatch.PathDispatchUpstreams, dispatchAdmin.Upstreams},
		{dispatch.PathDispatchUpstreamsUpdate, dispatchAdmin.UpstreamsUpdate()},
		{dispatch.PathDispatchUpstreamsDelete, dispatchAdmin.UpstreamsDelete()},
		{dispatch.PathDispatchUpstreamsRestore, dispatchAdmin.UpstreamsRestore()},
		{dispatch.PathDispatchNodes, dispatchAdmin.Nodes},
		{dispatch.PathDispatchNodesUpdate, dispatchAdmin.NodesUpdate()},
		{dispatch.PathDispatchNodesDelete, dispatchAdmin.NodesDelete()},
		{dispatch.PathDispatchNodesRestore, dispatchAdmin.NodesRestore()},
		{dispatch.PathDispatchTags, dispatchAdmin.Tags},
		{dispatch.PathDispatchTagsUpdate, dispatchAdmin.TagsUpdate()},
		{dispatch.PathDispatchTagsDelete, dispatchAdmin.TagsDelete()},
		{dispatch.PathDispatchHealth, dispatchAdmin.Health},
	} {
		if err := adminSrv.RegisterPlugin(ep.path, ep.h); err != nil {
			return nil, fmt.Errorf("register dispatch %s: %w", ep.path, err)
		}
	}

	// 5b. WebUI 管理控制台静态资源（内嵌单页，根路径 / 打开）。
	if err := adminSrv.RegisterWebUI(webui.FS); err != nil {
		return nil, fmt.Errorf("register webui static: %w", err)
	}

	// 挂件自动开关映射：中间件名 → XXX_ENABLED 配置键。
	// 统一语义：XXX_ENABLED = 该挂件是否生效（默认 false 全关）——
	//   .env 写 true → 重启自动挂载（ApplyAutoEnable 初始同步）；
	//   switch on/off → adminapi 持久化回 .env（重启后按配置恢复）；
	//   配置热更 → hotswap.applyAutoEnable 联动挂载/摘除（两态永不分裂）。
	// ★ 独立组件（config/registry/object）无"HTTP 流动/观测"行为、无 ENABLED 概念，不纳入；
	//   mq 已由 MQ_ENABLED 条件装配控制，维持现状。
	autoEnableMap := map[string]string{
		"shield":        "SHIELD_ENABLED",
		"trace":         "TRACE_ENABLED",
		"auth":          "AUTH_ENABLED",
		"dispatch":      "DISPATCH_ENABLED", // 主件（Middle）
		"dispatch-tail": "DISPATCH_ENABLED", // 收尾件（Tail）：与主件同键联动启停
		"rewrite":       "REWRITE_ENABLED",
		"script":        "SCRIPT_ENABLED",
		"obs":           "OBS_ENABLED",
		"copy":          "COPY_ENABLED",
		"result":        "RESULT_ENABLED",
	}
	mgr.SetAutoEnableMap(autoEnableMap)
	adminSrv.SetAutoEnableMap(autoEnableMap) // switch on/off 持久化到 .env（重启后按配置恢复）
	// 启动初始同步：按 .env/环境变量中的 XXX_ENABLED 自动挂载对应中间件（默认全 false → 全不挂载）。
	mgr.ApplyAutoEnable()

	// 启动外挂文件统一内容中枢监控循环（★ 全部子目录已注册完成：sql/rules/trusted_proxies；
	// 幂等，重复调用无害）。此后外挂文件变更 ≤ HOT_FILES_WATCH_INTERVAL 内自动生效。
	scriptHub.Start()

	// 配置中心红线：装配完成后同步工作目录 default.env（开发规范下即 bin/default.env）为全量默认值快照（代表代码真实兜底行为）。
	// 同步失败不阻断启动（default.env 仅兜底快照，不影响运行）。
	if err := cfgMgr.SyncDefaultFile(); err != nil {
		log.Warn("sync default.env", "err", err.Error())
	}

	// 自动拉黑引擎在装配全部完成后再启动：装配期配置注册与引擎读配置（conf List）
	// 并发会构成数据竞争（easyconf 装配路径非并发安全），装配完成后仅剩持锁热更写。
	if autoBan != nil {
		autoBan.Start()
	}

	// GeoIP 自动同步定时器：服务就绪（功能开启且 mmdb 已加载）才启动（生效前置）；
	// 手动端点与定时触发收敛 geoSyncAll 唯一入口，同步成功后清流量统计缓存。
	// 运行期热更关闭（GEOIP_ENABLED=false 或间隔改 0）后每轮就绪复查自动停摆，
	// 恢复后自动续跑；启动时即禁用的定时器不会创建（动态拉起需重启，已知边界）。
	var geoSyncStop chan struct{}
	if dataDB != nil {
		// 定时同步与手动同步同为任务中心实例（工厂到点生产实例），受分组互斥统一管控：
		// 同互斥组有任务在跑（人工长任务/上一轮未完）时该轮跳过并登记 skipped（不动最近执行时间），
		// 下轮到点自动续接（游标增量，无数据丢失）。
		geoSyncStop = startGeoSyncTimer(geoRes, func(ctx context.Context) {
			taskID, err := adminSrv.SubmitTask(geoSyncTaskSpec("GeoIP 数据同步（定时）", dataDB, geoRes, obsMw.PurgeTrafficCache))
			if err != nil {
				if geoSyncOnSkip != nil {
					geoSyncOnSkip("本轮到点未执行（跳过）：" + err.Error())
				}
				log.Warn("geoip: 定时同步本轮跳过", "err", err.Error())
				return
			}
			log.Info("geoip: 定时同步任务已提交", "task_id", taskID)
		})
		if geoSyncStop == nil {
			if !geoRes.Enabled() {
				log.Info("geoip: 功能未开启（GEOIP_ENABLED=false），自动同步定时器未启动（开启后需重启拉起；手动同步即时可用）")
			} else {
				log.Info("geoip: mmdb 未加载，自动同步未启动（放置 GeoLite2 mmdb 并重启后生效）")
			}
		}
	}

	return &Server{
		cfgMgr:      cfgMgr,
		chain:       ch,
		eng:         eng,
		mgr:         mgr,
		adminSrv:    adminSrv,
		dataDB:      dataDB,
		recorder:    recorder,
		autoBan:     autoBan,
		geoSyncStop: geoSyncStop,
	}, nil
}

// slogLevel 将 LogLevel 配置字符串映射为 slog.Level：debug/info/warn/error；未知值默认 Info
func slogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// printVersion 打印版本信息到标准输出（--version/-version 命令）。
func printVersion() {
	fmt.Printf("Version: %s\n", Version)
	fmt.Printf("BuildTime: %s\n", BuildTime)
	fmt.Printf("GoVersion: %s\n", GoVersion)
}

// genDefaultEnv 装配全部配置项后生成全量 default.env（默认值快照），不启动服务。
// default.env 写入当前工作目录（开发规范：make gen-env 在 bin/ 目录运行，故实际为 bin/default.env）。
// DB_DSN 指向内存库，避免产生 rocksys.db 文件副作用。
func genDefaultEnv(args []string) error {
	os.Setenv("DB_DSN", ":memory:")
	srv, err := buildServer(args)
	if err != nil {
		return err
	}
	// buildServer 尾部已同步一次；此处显式再调并检查，保证 gen-env 失败可感知、可退出。
	if err := srv.cfgMgr.SyncDefaultFile(); err != nil {
		return err
	}
	fmt.Printf("default.env 已生成（基于当前工作目录）: %s\n", conf.DefaultEnvPath())
	return nil
}

// missingLogColumns 启动缺列检测：返回 访问/拦截两表 → 缺失列名列表
// （表名取 specs 实值；只看 create 脚本为两表者）。DiffTable 对不存在表返回 A 级缺表项，
// 本检测只关心缺列（Actual == "列不存在"），缺表交给既有建表流程。
func missingLogColumns(d *db.DB, specs []db.TableSpec) map[string][]string {
	const (
		accessDDL = "access_log_create_table.sql"
		shieldDDL = "shield_event_create_table.sql"
	)
	res := map[string][]string{}
	for _, spec := range specs {
		if spec.CreateScript != accessDDL && spec.CreateScript != shieldDDL {
			continue
		}
		ddl, err := d.SQL(spec.CreateScript)
		if err != nil {
			log.Warn("db: 缺列检测读取建表脚本失败（跳过该表）", "script", spec.CreateScript, "err", err.Error())
			continue
		}
		ddl = strings.ReplaceAll(ddl, "{table}", spec.Table)
		cols, err := d.CatalogColumns(context.Background(), spec.Table)
		if err != nil {
			log.Warn("db: 缺列检测查询 catalog 失败（跳过该表）", "table", spec.Table, "err", err.Error())
			continue
		}
		for _, it := range db.DiffTable(db.DiffInput{Table: spec.Table, ExpectedDDL: ddl, ActualCols: cols}) {
			if it.Actual == "列不存在" {
				res[spec.Table] = append(res[spec.Table], it.Object)
			}
		}
	}
	return res
}

// buildTableSpecs 表结构同步表清单（装配处单一事实来源，见 docs/DB_SCHEMA_SYNC_PLAN.md §3.1）。
// 表名无法从脚本文件名推断，来源已在设计期逐一核实：
//   - ip_blacklist / ip_whitelist：IPListStore 构造内字面量（plugins/shield/ip_list_store.go）
//   - shield_event：固定表名（表名不开放配置）
//   - access_log（plugins/obs）、admin_users（adminapi userstore）、attack_archive（shield）：
//     各组件建表处字面量/常量
//   - outbox：mq.New(..., "outbox") 字面量（mq_create_table.sql 文件名 ≠ 表名）
//
// 防漏防漂移：cmd/rocksys/main_test.go 的一致性单测比对三方言脚本文件集合与本清单。
func buildTableSpecs(shieldEventTable string) []db.TableSpec {
	if strings.TrimSpace(shieldEventTable) == "" {
		shieldEventTable = db.TableShieldEvent
	}
	return []db.TableSpec{
		{Table: "admin_users", CreateScript: "admin_users_create_table.sql"},
		{Table: "ip_blacklist", CreateScript: "ip_blacklist_create_table.sql", IndexScript: "ip_blacklist_create_index.sql"},
		{Table: "ip_whitelist", CreateScript: "ip_whitelist_create_table.sql", IndexScript: "ip_whitelist_create_index.sql"},
		{Table: "attack_archive", CreateScript: "attack_archive_create_table.sql", IndexScript: "attack_archive_create_index.sql"},
		{Table: shieldEventTable, CreateScript: "shield_event_create_table.sql", IndexScript: "shield_event_create_index.sql"},
		{Table: db.TableAccessLog, CreateScript: "access_log_create_table.sql", IndexScript: "access_log_create_index.sql"},
		{Table: "sql_exec_log", CreateScript: "sql_exec_log_create_table.sql", IndexScript: "sql_exec_log_create_index.sql"},
		{Table: "outbox", CreateScript: "mq_create_table.sql", IndexScript: "mq_create_index.sql"},
		{Table: db.TableGeoipList, CreateScript: "geoip_list_create_table.sql", IndexScript: "geoip_list_create_index.sql"},
		{Table: db.TableScheduleList, CreateScript: "schedule_list_create_table.sql"},
		// 路由分发六表（dispatch 插件，docs/DATA_DICT.md 同源）
		{Table: "dispatch_rule", CreateScript: "dispatch_rule_create_table.sql", IndexScript: "dispatch_rule_create_index.sql"},
		{Table: "dispatch_upstream", CreateScript: "dispatch_upstream_create_table.sql", IndexScript: "dispatch_upstream_create_index.sql"},
		{Table: "dispatch_node", CreateScript: "dispatch_node_create_table.sql", IndexScript: "dispatch_node_create_index.sql"},
		{Table: "dispatch_upstream_node", CreateScript: "dispatch_upstream_node_create_table.sql", IndexScript: "dispatch_upstream_node_create_index.sql"},
		{Table: "dispatch_tag", CreateScript: "dispatch_tag_create_table.sql", IndexScript: "dispatch_tag_create_index.sql"},
		{Table: "dispatch_rule_tag", CreateScript: "dispatch_rule_tag_create_table.sql", IndexScript: "dispatch_rule_tag_create_index.sql"},
	}
}

// configValue 从配置项清单读取指定 key 的当前值（未注册/未设置返回空串）。
func configValue(items []conf.ConfigItem, key string) string {
	for _, it := range items {
		if it.Key == key {
			return it.Current
		}
	}
	return ""
}
