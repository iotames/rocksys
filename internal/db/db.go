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

// defaultExternalDir 外置 SQL 目录默认值（相对工作目录，与项目根 sql/ 结构一致）。
const defaultExternalDir = "sql"

// DB 统一数据访问层：easydb 数据操作 + ScriptDir 脚本逐级加载。
type DB struct {
	edb     *easydb.EasyDb
	scripts *hotswap.ScriptDir
	driver  string
}

// 编译期断言：*DB 实现 SQLSource。
var _ SQLSource = (*DB)(nil)

// Open 打开数据库连接并初始化 SQL 脚本访问。
//
// driver：数据库驱动名（sqlite/mysql/postgres 等，须已注册并内嵌 sql/<driver>/ 脚本目录）；
// dsn：数据库连接串；sqlDir：外置脚本目录（优先），为空时用默认 sql/；
// 外置目录不存在时，脚本自动回退到编译期嵌入的 sql/。
//
// 错误语义：驱动未注册、内嵌目录缺少 sql/<driver>/、连接失败均直接报错——
// 其中"内嵌目录缺少脚本"正是"切换数据库找不到对应脚本即报错"的实现。
func Open(driver, dsn, sqlDir string) (*DB, error) {
	driver = strings.ToLower(strings.TrimSpace(driver))
	if driver == "" {
		return nil, fmt.Errorf("db: 数据库驱动名不能为空")
	}

	sub, err := fs.Sub(sqlfiles.FS, "sql")
	if err != nil {
		return nil, fmt.Errorf("db: 读取内嵌 sql/ 目录失败: %w", err)
	}
	// 校验内嵌目录存在 sql/<driver>/；缺失即拒绝（至少支持 1 种数据库由 sql/sqlite 保证）
	if _, err := fs.ReadDir(sub, driver); err != nil {
		return nil, fmt.Errorf(
			"db: 内嵌 SQL 脚本缺少 sql/%s/ 目录，无法支持驱动 %s（可补全脚本后重新编译，或在 SQL_DIR 外置目录中提供）: %w",
			driver, driver, err)
	}

	sqldb, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("db: 打开驱动 %s 失败: %w", driver, err)
	}
	if err := sqldb.Ping(); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("db: 连接 %s 失败: %w", driver, err)
	}

	if strings.TrimSpace(sqlDir) == "" {
		sqlDir = defaultExternalDir
	}
	return &DB{
		edb:     easydb.NewEasyDbBySqlDB(sqldb),
		scripts: hotswap.NewScriptDir(sub, sqlDir),
		driver:  driver,
	}, nil
}

// EmbeddedSQLSource 返回仅使用编译期嵌入脚本的 SQLSource（driver 指定方言目录）。
// 用于测试与"无外置目录"场景。外置目录被设为不存在路径，GetScriptBytes 自动回退嵌入文件。
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
		scripts: hotswap.NewScriptDir(sub, ".nonexistent-external-sql-dir"),
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
func (d *DB) SQL(name string) (string, error) {
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
