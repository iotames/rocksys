// Copyright © 管理接口用户存储：超级管理员账号（登录/注册/重置）。
//
// 设计要点：
//  1. 超管仅一个，存统一数据访问层（internal/db，DB_DRIVER/DB_DSN）admin_users 表，
//     密码只存 PBKDF2 哈希；
//  2. 哈希用 Go 标准库 crypto/pbkdf2（HMAC-SHA256 + 随机盐 + 迭代），
//     不引入第三方库（项目铁律：非必要不引入依赖）；
//  3. 存储格式：pbkdf2$<iter>$<salt_b64>$<hash_b64>，兼容性可扩展；
//  4. SQL 全部外置 sql/<dbtype>/（admin_users_*.sql，三方言齐平），
//     经 SQLSource 逐级加载（外置优先、嵌入兜底），遵循数据库铁律。
package adminapi

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/iotames/easydb"

	"rocksys/internal/db"
)

// pbkdf2Iter PBKDF2 迭代次数：100k 次 HMAC-SHA256，兼顾安全与登录延迟。
const pbkdf2Iter = 100_000

// passwordHashPrefix 密码哈希存储前缀（格式：pbkdf2$<iter>$<salt>$<hash>）。
const passwordHashPrefix = "pbkdf2$"

// adminUsersTable 管理接口超级管理员表名（与 sql/<dbtype>/admin_users_*.sql 中 {table} 对应）。
const adminUsersTable = "admin_users"

// adminUser 管理接口超级管理员用户记录（超管仅一个）。
type adminUser struct {
	ID           int64  `db:"id"`
	Username     string `db:"username"`
	PasswordHash string `db:"password_hash"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// userStore 管理接口用户存储：封装 easydb 操作 admin_users 表。
type userStore struct {
	edb       *easydb.EasyDb
	sqls      db.SQLSource // SQL 脚本源（internal/db 数据访问层）
	tableName string
}

// newUserStore 打开用户存储：校验连接与脚本源并确保表存在。
// sqls 为 nil 时返回错误（用户认证降级为静态 token / 回环信任，见 adminapi.New）。
func newUserStore(edb *easydb.EasyDb, sqls db.SQLSource) (*userStore, error) {
	if edb == nil {
		return nil, errors.New("adminapi: 用户存储依赖的数据库连接为空")
	}
	if sqls == nil {
		return nil, errors.New("adminapi: 用户存储缺少 SQL 脚本源（SetSQLSource 未注入）")
	}
	s := &userStore{edb: edb, sqls: sqls, tableName: adminUsersTable}
	if err := s.ensureTable(); err != nil {
		return nil, err
	}
	return s, nil
}

// sqlText 从脚本源读取 SQL 文本并替换 {table} 表名占位符。
func (s *userStore) sqlText(name string) (string, error) {
	txt, err := s.sqls.SQL(name)
	if err != nil {
		return "", fmt.Errorf("adminapi: 读取 SQL 脚本 %s 失败（切换数据库时缺少 sql/<dbtype>/ 下对应脚本）: %w", name, err)
	}
	return strings.ReplaceAll(txt, "{table}", s.tableName), nil
}

// ensureTable 建表（幂等）。
func (s *userStore) ensureTable() error {
	ddl, err := s.sqlText("admin_users_create_table.sql")
	if err != nil {
		return err
	}
	_, err = s.edb.Exec(ddl)
	return err
}

// count 返回已注册管理员数量（0 = 尚未初始化）。
func (s *userStore) count() (int, error) {
	q, err := s.sqlText("admin_users_count.sql")
	if err != nil {
		return 0, err
	}
	row := make(map[string]any, 1)
	if err := s.edb.GetOneData(q, &row); err != nil {
		return 0, err
	}
	n, _ := row["n"].(int64)
	return int(n), nil
}

// get 返回唯一管理员（超管只有一个）。无记录时返回 nil, nil。
func (s *userStore) get() (*adminUser, error) {
	q, err := s.sqlText("admin_users_get.sql")
	if err != nil {
		return nil, err
	}
	u := &adminUser{}
	if err := s.edb.GetOneData(q, u); err != nil {
		return nil, err
	}
	if u.ID == 0 {
		return nil, nil
	}
	return u, nil
}

// getByUsername 按用户名查用户（登录用）。未找到返回 nil, nil。
func (s *userStore) getByUsername(username string) (*adminUser, error) {
	q, err := s.sqlText("admin_users_get_by_username.sql")
	if err != nil {
		return nil, err
	}
	u := &adminUser{}
	if err := s.edb.GetOneData(q, u, username); err != nil {
		return nil, err
	}
	if u.ID == 0 {
		return nil, nil
	}
	return u, nil
}

// save 保存管理员：已存在则更新（支持重置改用户名/密码），否则插入。
func (s *userStore) save(username, passwordHash string) error {
	existing, err := s.get()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if existing != nil {
		upd, err := s.sqlText("admin_users_update.sql")
		if err != nil {
			return err
		}
		_, err = s.edb.Exec(upd, username, passwordHash, now, existing.ID)
		return err
	}
	ins, err := s.sqlText("admin_users_insert.sql")
	if err != nil {
		return err
	}
	_, err = s.edb.Exec(ins, username, passwordHash, now, now)
	return err
}

// hashPassword 以 PBKDF2-SHA256（随机盐 + 迭代）派生密码哈希。
// 存储格式：pbkdf2$<iter>$<salt_b64>$<hash_b64>。
func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("adminapi: 生成盐失败: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iter, 32)
	if err != nil {
		return "", fmt.Errorf("adminapi: PBKDF2 派生失败: %w", err)
	}
	return fmt.Sprintf("%s%d$%s$%s",
		passwordHashPrefix, pbkdf2Iter,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(key),
	), nil
}

// checkPassword 校验密码与存储哈希是否匹配（常量时间比较，防时序攻击）。
func checkPassword(password, stored string) bool {
	if !strings.HasPrefix(stored, passwordHashPrefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(stored, passwordHashPrefix), "$")
	if len(parts) != 3 {
		return false
	}
	iter, err := strconv.Atoi(parts[0])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	expected, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iter, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(key, expected) == 1
}
