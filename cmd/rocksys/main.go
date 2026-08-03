// 依据 DEV_HANDBOOK.md 第 7 章实现：底座唯一入口，装配全部 internal + plugins 并启动。
// 除装配外无任何业务代码。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"rocksys/internal/adminapi"
	"rocksys/internal/chain"
	"rocksys/internal/conf"
	"rocksys/internal/engine"
	"rocksys/internal/hotswap"

	"rocksys/plugins/auth"
	"rocksys/plugins/config"
	"rocksys/plugins/dispatch"
	"rocksys/plugins/mq"
	"rocksys/plugins/object"
	"rocksys/plugins/obs"
	"rocksys/plugins/registry"
	"rocksys/plugins/result"
	"rocksys/plugins/script"
	"rocksys/plugins/shield"
	"rocksys/plugins/trace"

	"github.com/iotames/easyserver/log"

	_ "modernc.org/sqlite"
)

// shutdownTimeout 优雅停机总时限（§7.1 步骤 8）。
const shutdownTimeout = 30 * time.Second

// scriptTimeout Lua 脚本执行超时（§15）。
const scriptTimeout = 100 * time.Millisecond

// Server 已装配的运行单元（engine + admin + mgr），供 main 与测试复用。
type Server struct {
	cfgMgr   conf.Manager
	chain    *chain.Chain
	eng      *engine.Engine
	mgr      *hotswap.Manager
	adminSrv *adminapi.AdminServer
	mqDB     *sql.DB // 条件装配（MQ_ENABLED && MQ_DSN）时打开，停机关闭
}

func main() {
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
	if srv.mqDB != nil {
		_ = srv.mqDB.Close()
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
	mgr.RegisterMiddleware(script.New(scriptTimeout)) // Lua 策略 → chain.Middle
	mgr.RegisterMiddleware(obs.New(cfgMgr))           // 访问日志/指标 → chain.Tail(+ResponseHook)
	mgr.RegisterMiddleware(result.New(cfgMgr))        // L3 结果 → chain.Tail(+ResponseHook)

	// 独立组件（RegisterComponent）：config/registry/object 无条件注册。
	mgr.RegisterComponent(config.New(cfgMgr))   // KV 配置服务
	mgr.RegisterComponent(registry.New(cfgMgr)) // 服务注册中心
	mgr.RegisterComponent(object.New())         // 对象存储

	// mq 条件装配：仅当 MQ_ENABLED=true 且 MQ_DSN 非空时打开 sqlite 并注册；否则跳过。
	var mqDB *sql.DB
	var mqEnabled bool
	var mqDSN string
	if err := cfgMgr.Register(&mqEnabled, "MQ_ENABLED", "false", "是否启用 mq 异步消息组件"); err != nil {
		return nil, fmt.Errorf("register MQ_ENABLED: %w", err)
	}
	if err := cfgMgr.Register(&mqDSN, "MQ_DSN", "", "mq sqlite 数据库 DSN"); err != nil {
		return nil, fmt.Errorf("register MQ_DSN: %w", err)
	}
	if mqEnabled && mqDSN != "" {
		db, err := sql.Open("sqlite", mqDSN)
		if err != nil {
			return nil, fmt.Errorf("mq: 打开 sqlite(%s) 失败: %w", mqDSN, err)
		}
		mqDB = db
		mgr.RegisterComponent(mq.New(db, "outbox"))
		log.Info("mq component registered", "dsn", mqDSN)
	}

	// 排空判定注入（§6.3）：Adapter.ActiveCount。
	mgr.SetDrainCheck(eng.ActiveCount)

	// 5a. admin API + 挂件端点注入（§8.1；挂件 handler 经 RegisterPlugin 注入）。
	adminSrv := adminapi.New(cfgMgr.Current().AdminAddr, cfgMgr, mgr)
	scriptAdmin := script.NewAdminHandler(mgr)
	if err := adminSrv.RegisterPlugin(script.PathPublish, scriptAdmin.Publish); err != nil {
		return nil, fmt.Errorf("register script publish: %w", err)
	}
	if err := adminSrv.RegisterPlugin(script.PathRollback, scriptAdmin.Rollback); err != nil {
		return nil, fmt.Errorf("register script rollback: %w", err)
	}
	obsAdmin := obs.NewAdminHandler(mgr)
	if err := adminSrv.RegisterPlugin("/admin/metrics", obsAdmin.Metrics); err != nil {
		return nil, fmt.Errorf("register obs metrics: %w", err)
	}
	if err := adminSrv.RegisterPlugin("/admin/logs", obsAdmin.Logs); err != nil {
		return nil, fmt.Errorf("register obs logs: %w", err)
	}

	return &Server{
		cfgMgr:   cfgMgr,
		chain:    ch,
		eng:      eng,
		mgr:      mgr,
		adminSrv: adminSrv,
		mqDB:     mqDB,
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
