// 依据 DEV_HANDBOOK.md 第 7 章实现：底座唯一入口，装配全部 internal + plugins 并启动。
// 除装配外无任何业务代码。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"rocksys/internal/adminapi"
	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/db"
	"rocksys/internal/engine"
	"rocksys/internal/hotswap"

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

// scriptTimeout Lua 脚本执行超时（§15）。
const scriptTimeout = 100 * time.Millisecond

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
	dataDB   *db.DB // 统一数据访问层（DB_DRIVER/DB_DSN），mq 等插件复用；nil 表示未启用
}

func main() {
	// --version / -version：打印版本信息后退出，不启动服务。
	if len(os.Args) > 1 {
		if arg := os.Args[1]; arg == "--version" || arg == "-version" {
			printVersion()
			return
		}
	}

	srv, err := buildServer(os.Args[1:])
	if err != nil {
		log.Error("rocksys assemble failed", "err", err)
		os.Exit(1)
	}

	// 6. 启动配置热更监听（★ 始终启动：默认监听 .env；--config 指定时额外监听该文件，见 §2.4）
	if err := srv.cfgMgr.StartWatcher(); err != nil {
		log.Error("start config watcher", "err", err)
	}

	// 5a. 启动 admin API（独立 listener，回环地址，见第 8 章）
	go func() {
		if err := srv.adminSrv.ListenAndServe(); err != nil {
			log.Error("admin server error", "err", err)
		}
	}()

	// 7. 启动 HTTP 监听
	go func() {
		if err := srv.eng.ListenAndServe(); err != nil {
			log.Error("server error", "err", err)
		}
	}()

	// 8. 等待信号，优雅停机
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("rocksys shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// 8a. 停止接收新请求（主代理 + admin listener）
	_ = srv.eng.Shutdown(ctx)
	_ = srv.adminSrv.Shutdown(ctx)

	// 8b. 关闭挂件（逆序：先停 obs flush 日志，再停 hotswap 排空组件，最后停配置热更）
	_ = srv.mgr.Shutdown(ctx)
	_ = srv.cfgMgr.Shutdown(ctx)
	if srv.dataDB != nil {
		_ = srv.dataDB.Close() // mq/obs/admin 均复用 dataDB 连接，一并关闭
	}
}

// buildServer 装配全部组件（§7.1 步骤 1-5）：加载配置 → 建链 → 建引擎 → 建 hotswap → 注册全部挂件。
func buildServer(args []string) (*Server, error) {
	// 1. 加载配置
	cfgMgr, err := conf.Load(args)
	if err != nil {
		return nil, err
	}
	log.SetLevel(slogLevel(cfgMgr.Current().LogLevel))
	log.Info("rocksys starting",
		"upstream", cfgMgr.Current().DefaultUpstream,
		"listen", cfgMgr.Current().ListenAddr,
		"admin", cfgMgr.Current().AdminAddr)

	// 2. 创建转发链（初始为空）
	ch := chain.New()

	// 3. 创建引擎（内部调用 easyserver.NewServer + AddMiddleHead 注册 chain 适配器）
	eng := engine.New(cfgMgr, ch)

	// 4. 创建 hotswap 管理器（订阅配置热更）
	mgr := hotswap.NewManager(ch, cfgMgr)

	// 5. 注册挂件（链中间件用 RegisterMiddleware，独立组件用 RegisterComponent；见 §6.1）
	// 链中间件执行顺序：Head/Middle 槽位按注册顺序；Tail 槽位逆序（§4.4）——
	// 故 Tail 上 obs 先注册、result 后注册 → result 先改写响应、obs 后记录最终状态。
	shieldMw, err := shield.New(cfgMgr)
	if err != nil {
		return nil, fmt.Errorf("shield.New: %w", err)
	}
	mgr.RegisterMiddleware(shieldMw)                  // L1 防护 → chain.Head
	mgr.RegisterMiddleware(trace.New(&cfgMgr))        // trace 透传 → chain.Head
	mgr.RegisterMiddleware(auth.New(&cfgMgr))         // JWT 认证 → chain.Head
	mgr.RegisterMiddleware(dispatch.New(cfgMgr))      // L2 路由 → chain.Middle
	mgr.RegisterMiddleware(rewrite.New(cfgMgr))       // L2 转发前改写 → chain.Middle
	mgr.RegisterMiddleware(script.New(scriptTimeout)) // Lua 策略 → chain.Middle

	// 统一数据访问层（§? 数据访问层）：为可插拔组件（obs/mq 等）提供 easydb 数据操作 + SQL 脚本逐级加载。
	// 配置：DB_DRIVER（默认 sqlite，零配置）/ DB_DSN（默认 rocksys.db）/ SQL_DIR（默认 sql，外置脚本目录）。
	// 打开失败不阻断底座启动（底座仅反向代理），仅记录警告；obs 的默认 db 存储与 mq 等依赖方因此不可用。
	// ★ 必须先于 obs 创建：默认 OBS_STORE=db，obs 复用本数据访问层；未就绪时回退 file 并告警（file 已弃用）。
	var dataDB *db.DB
	var dbDriver, dbDSN, sqlDir string
	if err := cfgMgr.Register(&dbDriver, "DB_DRIVER", "sqlite", "数据库驱动名（sqlite/mysql/postgres）"); err != nil {
		return nil, fmt.Errorf("register DB_DRIVER: %w", err)
	}
	if err := cfgMgr.Register(&dbDSN, "DB_DSN", "rocksys.db", "数据库连接串（sqlite 为文件路径）"); err != nil {
		return nil, fmt.Errorf("register DB_DSN: %w", err)
	}
	if err := cfgMgr.Register(&sqlDir, "SQL_DIR", "sql", "外置 SQL 脚本目录（优先加载，嵌入文件兜底）"); err != nil {
		return nil, fmt.Errorf("register SQL_DIR: %w", err)
	}
	if d, err := db.Open(dbDriver, dbDSN, sqlDir); err != nil {
		log.Warn("db: 数据访问层初始化失败（不阻断底座）", "driver", dbDriver, "err", err.Error())
	} else {
		dataDB = d
		log.Info("db: 数据访问层已就绪", "driver", dataDB.Driver())
	}

	mgr.RegisterMiddleware(obs.New(cfgMgr, dataDB))   // 访问日志/指标 → chain.Tail(+ResponseHook)
	mgr.RegisterMiddleware(copy.New(cfgMgr))          // 请求抄送 → chain.Tail(+ResponseHook)
	mgr.RegisterMiddleware(result.New(cfgMgr))        // L3 结果 → chain.Tail(+ResponseHook)

	// 独立组件（RegisterComponent）：config/registry/object 无条件注册。
	mgr.RegisterComponent(config.New(cfgMgr))   // KV 配置服务
	mgr.RegisterComponent(registry.New(cfgMgr)) // 服务注册中心
	mgr.RegisterComponent(object.New())         // 对象存储

	// mq 条件装配：MQ_ENABLED=true 时注册；outbox 表建于统一数据访问层业务库（DB_DRIVER/DB_DSN），
	// 与架构一致（outbox 与业务数据同库，支持 stbiz 本地事务同提交）。
	// dataDB 未就绪时跳过注册（组件降级，不阻断底座）。
	var mqEnabled bool
	if err := cfgMgr.Register(&mqEnabled, "MQ_ENABLED", "false", "是否启用 mq 异步消息组件（outbox 表建于统一数据访问层业务库，DB_DRIVER/DB_DSN）"); err != nil {
		return nil, fmt.Errorf("register MQ_ENABLED: %w", err)
	}
	if mqEnabled {
		if dataDB == nil {
			log.Warn("mq: 数据访问层未就绪（DB_DRIVER/DB_DSN），mq 组件未注册")
		} else {
			// 复用 dataDB 连接与 SQL 脚本源：同一方言（sql/<dbtype>/ 逐级加载），
			// mq.OutboxStore 已按方言兼容 LastInsertId（postgres 走 RETURNING）。
			mqComp := mq.New(dataDB.EasyDB().GetSqlDB(), "outbox")
			mqComp.SetSQLSource(dataDB)
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
	if dataDB != nil {
		adminSrv.SetSQLSource(dataDB) // 用户存储 SQL 脚本源（sql/<dbtype>/admin_users_*.sql）
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
	if err := adminSrv.RegisterPlugin("/admin/metrics", obsAdmin.Metrics); err != nil {
		return nil, fmt.Errorf("register obs metrics: %w", err)
	}
	if err := adminSrv.RegisterPlugin("/admin/logs", obsAdmin.Logs); err != nil {
		return nil, fmt.Errorf("register obs logs: %w", err)
	}
	if err := adminSrv.RegisterPlugin("/admin/logs/storage", obsAdmin.Storage); err != nil {
		return nil, fmt.Errorf("register obs storage: %w", err)
	}

	// 5b. WebUI 管理控制台静态资源（内嵌单页，根路径 / 打开）。
	if err := adminSrv.RegisterWebUI(webui.FS); err != nil {
		return nil, fmt.Errorf("register webui static: %w", err)
	}

	return &Server{
		cfgMgr:   cfgMgr,
		chain:    ch,
		eng:      eng,
		mgr:      mgr,
		adminSrv: adminSrv,
		dataDB:   dataDB,
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
