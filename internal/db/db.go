// Package db 统一数据访问层：为可插拔组件（plugins/*）提供数据库操作能力。
//
// 设计原则：
//  1. 数据操作以 easydb（github.com/iotames/easydb）为主，本包做轻量装配与脚本化；
//  2. 所有 SQL 语句外置到 sql/<dbtype>/ 目录（见 sqlfiles 包），运行时经 ScriptDir
//     逐级加载（外置目录优先、编译期嵌入兜底），实现"改 SQL 不重新编译"；
//  3. 切换数据库驱动时，若 sql/<dbtype>/ 下找不到对应脚本，SQL() 直接报错；
//  4. 底座（反向代理转发引擎）不直连业务数据库，本层仅服务内部组件（mq 等）。
package db

import (
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iotames/easydb"

	sqlfiles "rocksys" // 根目录 sqlfiles.go：embed sql/ 目录，包名 sqlfiles
	"rocksys/internal/hotswap"
)

// SQLSource 提供按文件名读取 SQL 脚本的能力（外置目录优先、嵌入文件兜底）。
// 可插拔组件（如 mq）依赖此接口把 SQL 外置到 sql/<dbtype>/，实现运行时热改。
type SQLSource interface {
	// SQL 返回指定脚本文件名的文本（自动按驱动方言选择 sql/<driver>/ 目录）。
	SQL(name string) (string, error)
}

// scriptSubDir db 业务在 HOT_SCRIPTS_DIR 统一外挂根下的固定子目录：
// 外挂 SQL 覆写目录 = HOT_SCRIPTS_DIR/sql（默认 hotscripts/sql，相对工作目录），
// 与嵌入的 sql/ 目录结构一致（sql/<dbtype>/）。★ 统一收敛：不再提供独立 SQL_DIR 配置。
const scriptSubDir = "sql"

// sqlite DSN 自动补全参数（modernc.org/sqlite 驱动 DSN 参数，见其源码 sqlite.go dsnPick）。
const (
	sqliteBusyTimeout = 5000  // ms，PRAGMA busy_timeout（每连接生效）
	sqliteJournalMode = "WAL" // PRAGMA journal_mode
)

// sqlitePragmaParams 自动补全的完整参数字段（常量拼装，禁止魔数散落多处）。
var sqlitePragmaParams = "_busy_timeout=" + strconv.Itoa(sqliteBusyTimeout) + "&_journal_mode=" + sqliteJournalMode

// ensureSQLitePragma 对 sqlite DSN 自动补全 busy_timeout 与 WAL 参数。
// 仅 driver=="sqlite" 时由 Open 调用；DSN 已含任一 _ 前缀参数（modernc 驱动所有 DSN
// 参数均以 _ 开头：_busy_timeout/_journal_mode/_pragma/_timeout/_journal/_sync/_fk/
// _vacuum/_auto_vacuum 等）则原样返回（尊重显式配置，用户自行管理 pragma）。
// 拼接：裸路径用 "?" 连接；已有 "?" 参数用 "&" 连接；追加 sqlitePragmaParams。
// 边界：空串、":memory:"/"file::memory:" 前缀原样返回（内存库无文件锁竞争，补参无意义）。
func ensureSQLitePragma(dsn string) string {
	if dsn == "" {
		return ""
	}
	if strings.HasPrefix(dsn, ":memory:") || strings.HasPrefix(dsn, "file::memory:") {
		return dsn
	}
	_, query, hasQuery := strings.Cut(dsn, "?")
	if !hasQuery {
		return dsn + "?" + sqlitePragmaParams
	}
	// 已有参数段：仅当显式配置了 pragma 类参数（_ 前缀）时尊重原样；
	// 否则用 & 追加。ParseQuery 错误仅影响参数存在性判定，不因非法转义拒绝 DSN。
	if vals, err := url.ParseQuery(query); err == nil {
		for k := range vals {
			if strings.HasPrefix(k, "_") {
				return dsn
			}
		}
	}
	return dsn + "&" + sqlitePragmaParams
}

// DB 统一数据访问层：easydb 数据操作 + ScriptDir 脚本逐级加载。
type DB struct {
	edb     *easydb.EasyDb
	scripts *hotswap.ScriptDir
	hub     *hotswap.ScriptHub // 外挂脚本统一内容中枢（nil = 未注入，SQL 回落 scripts 直读）
	driver  string
}

// 编译期断言：*DB 实现 SQLSource。
var _ SQLSource = (*DB)(nil)

// Open 打开数据库连接并初始化 SQL 脚本访问。
//
// driver：数据库驱动名（sqlite/mysql/postgres 等，须已注册并内嵌 sql/<driver>/ 脚本目录）；
// dsn：数据库连接串。外挂 SQL 覆写目录统一为 HOT_SCRIPTS_DIR/sql（见 hotswap 收敛入口），
// 缺失时脚本自动回退到编译期嵌入的 sql/。
//
// hubs：可选注入外挂文件统一内容中枢（ScriptHub，实现见 internal/hotswap/hub.go）。
// 注入后 sql/ 子目录注册进中枢：外挂 SQL 变更 ≤3s 自动生效（缓存 + 监控失效，
// 请求路径零文件 I/O），SQL() 读取走统一缓存；未注入时回落 ScriptDir 直读（旧语义）。
//
// 错误语义：驱动未注册、内嵌目录缺少 sql/<driver>/、连接失败均直接报错——
// 其中"内嵌目录缺少脚本"正是"切换数据库找不到对应脚本即报错"的实现。
func Open(driver, dsn string, hubs ...*hotswap.ScriptHub) (*DB, error) {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver == "" {
		return nil, fmt.Errorf("db: 数据库驱动名不能为空")
	}

	sub, err := fs.Sub(sqlfiles.FS, "sql")
	if err != nil {
		return nil, fmt.Errorf("db: 读取内嵌 sql/ 目录失败: %w", err)
	}
	// 校验方言脚本目录：内嵌 sql/<driver>/ 缺失时，若外置目录提供
	// HOT_SCRIPTS_DIR/sql/<driver>/ 则放行（运行时 SQL() 优先读外置），否则拒绝。
	// 至少支持 1 种数据库由 sql/sqlite 保证。
	if _, err := fs.ReadDir(sub, driver); err != nil {
		extDriverDir := filepath.Join(hotswap.HotScriptsDir(), scriptSubDir, driver)
		if _, serr := os.Stat(extDriverDir); serr != nil {
			return nil, fmt.Errorf(
				"db: 内嵌 SQL 脚本缺少 sql/%s/ 目录，且外置目录 %s 亦未提供 sql/%s/，无法支持驱动 %s（请补全脚本后重新编译，或在 HOT_SCRIPTS_DIR(%s)/sql/ 外置目录中提供 sql/%s/）: %w",
				driver, filepath.Join(hotswap.HotScriptsDir(), scriptSubDir), driver, driver, hotswap.HotScriptsDir(), driver, err)
		}
	}

	// sqlite 自动补全 busy_timeout + WAL（消除 SQLITE_BUSY 根源）；mysql/postgres 原样透传。
	if driver == "sqlite" {
		dsn = ensureSQLitePragma(dsn)
	}

	sqldb, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("db: 打开驱动 %s 失败: %w", driver, err)
	}
	if err := sqldb.Ping(); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("db: 连接 %s 失败: %w", driver, err)
	}

	d := &DB{
		edb:     easydb.NewEasyDbBySqlDB(sqldb),
		scripts: hotswap.NewScriptDir(sub, scriptSubDir),
		driver:  driver,
	}
	if len(hubs) > 0 && hubs[0] != nil {
		d.hub = hubs[0]
		// sql/ 子目录注册进统一内容中枢。重复注册是装配缺陷（如重复 Open 注入同一 hub），
		// 尽早暴露；注册失败需关闭已建立连接，避免句柄泄漏。
		if err := hubs[0].Register(scriptSubDir, d.scripts); err != nil {
			_ = sqldb.Close()
			return nil, fmt.Errorf("db: 注册 SQL 脚本子目录进 ScriptHub: %w", err)
		}
	}
	return d, nil
}

// EmbeddedSQLSource 返回仅使用编译期嵌入脚本的 SQLSource（driver 指定方言目录）。
// 用于测试与"无外置目录"场景（无外挂覆写，直接回退嵌入文件）。
func EmbeddedSQLSource(driver string) (SQLSource, error) {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver == "" {
		return nil, fmt.Errorf("db: 数据库驱动名不能为空")
	}
	sub, err := fs.Sub(sqlfiles.FS, "sql")
	if err != nil {
		return nil, fmt.Errorf("db: 读取内嵌 sql/ 目录失败: %w", err)
	}
	if _, err := fs.ReadDir(sub, driver); err != nil {
		return nil, fmt.Errorf("db: 内嵌 SQL 脚本缺少 sql/%s/ 目录: %w", driver, err)
	}
	return &DB{
		scripts: hotswap.EmbeddedScriptDir(sub),
		driver:  driver,
	}, nil
}

// EasyDB 返回底层 easydb 实例（供高级调用与连接复用）。
func (d *DB) EasyDB() *easydb.EasyDb {
	return d.edb
}

// Driver 返回当前数据库驱动名（即 sql/<driver>/ 方言目录名）。
func (d *DB) Driver() string {
	return d.driver
}

// SQL 实现 SQLSource：返回 sql/<driver>/<name> 的脚本文本。
// 外置目录优先、嵌入兜底；两者皆无时返回错误（切换数据库缺脚本即报错）。
// 注入统一内容中枢时经缓存读取（外挂 SQL 变更由中枢监控 ≤3s 刷新，请求路径零文件 I/O），
// 否则回落底层 ScriptDir 直读（旧语义：每次实时读磁盘）。
func (d *DB) SQL(name string) (string, error) {
	if d.hub != nil {
		return d.hub.GetScriptText(scriptSubDir, d.driver+"/"+name)
	}
	return d.scripts.GetScriptText(d.driver + "/" + name)
}

// Exec 读取脚本并执行写操作（参数化，防止 SQL 注入）。
func (d *DB) Exec(name string, args ...any) (sql.Result, error) {
	txt, err := d.SQL(name)
	if err != nil {
		return nil, err
	}
	return d.edb.Exec(txt, args...)
}

// GetMany 读取脚本并查询多行到 dest（切片指针，见 easydb.GetMany）。
func (d *DB) GetMany(name string, dest any, args ...any) error {
	txt, err := d.SQL(name)
	if err != nil {
		return err
	}
	return d.edb.GetMany(txt, dest, args...)
}

// Close 关闭底层数据库连接池。
func (d *DB) Close() error {
	if d.edb == nil {
		return nil
	}
	return d.edb.CloseDb()
}

// SplitSQLStatements 按行拆分多语句 SQL 脚本（约定：每行一条完整语句，允许空行与
// "--" 单行注释行）。用于索引等多语句脚本逐条执行：sqlite（modernc）对多语句
// Exec 报错、MySQL 默认 multiStatements=false、lib/pq 亦不支持多语句，
// 因此建表/建索引脚本必须逐条执行。
func SplitSQLStatements(txt string) []string {
	var out []string
	for _, line := range strings.Split(txt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		out = append(out, line)
	}
	return out
}
