// Copyright © 管理接口用户存储：超级管理员账号（登录/注册/重置）。
//
// 设计要点：
//  1. 超管仅一个，存 easydb（sqlite）admin_users 表，密码只存 PBKDF2 哈希；
//  2. 哈希用 Go 标准库 crypto/pbkdf2（HMAC-SHA256 + 随机盐 + 迭代），
//     不引入第三方库（项目铁律：非必要不引入依赖）；
//  3. 存储格式：pbkdf2$<iter>$<salt_b64>$<hash_b64>，兼容性可扩展；
//  4. 内联 SQL（管理接口专属表，无外置热改需求）。
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
)

// pbkdf2Iter PBKDF2 迭代次数：100k 次 HMAC-SHA256，兼顾安全与登录延迟。
const pbkdf2Iter = 100_000

// passwordHashPrefix 密码哈希存储前缀（格式：pbkdf2$<iter>$<salt>$<hash>）。
const passwordHashPrefix = "pbkdf2$"

// adminUser 管理接口超级管理员用户记录（超管仅一个）。
type adminUser struct {
	ID           int64  `db:"id"`
	Username     string `db:"username"`
	PasswordHash string `db:"password_hash"`
	CreatedAt    string `db:"created_at"`
	UpdatedAt    string `db:"updated_at"`
}

// userStore 管理接口用户存储：封装 easydb 操作 admin_users 表。
type userStore struct {
	edb *easydb.EasyDb
}

// newUserStore 打开用户存储：校验连接并确保表存在。edb 为 nil 时返回错误。
func newUserStore(edb *easydb.EasyDb) (*userStore, error) {
	if edb == nil {
		return nil, errors.New("adminapi: 用户存储依赖的数据库连接为空")
	}
	s := &userStore{edb: edb}
	if err := s.ensureTable(); err != nil {
		return nil, err
	}
	return s, nil
}

// ensureTable 建表（幂等）。
func (s *userStore) ensureTable() error {
	const ddl = `CREATE TABLE IF NOT EXISTS admin_users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		username      TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL
	)`
	_, err := s.edb.Exec(ddl)
	return err
}

// count 返回已注册管理员数量（0 = 尚未初始化）。
func (s *userStore) count() (int, error) {
	row := make(map[string]any, 1)
	if err := s.edb.GetOneData("SELECT COUNT(*) AS n FROM admin_users", &row); err != nil {
		return 0, err
	}
	n, _ := row["n"].(int64)
	return int(n), nil
}

// get 返回唯一管理员（超管只有一个）。无记录时返回 nil, nil。
func (s *userStore) get() (*adminUser, error) {
	u := &adminUser{}
	if err := s.edb.GetOneData(
		"SELECT id, username, password_hash, created_at, updated_at FROM admin_users LIMIT 1",
		u,
	); err != nil {
		return nil, err
	}
	if u.ID == 0 {
		return nil, nil
	}
	return u, nil
}

// getByUsername 按用户名查用户（登录用）。未找到返回 nil, nil。
func (s *userStore) getByUsername(username string) (*adminUser, error) {
	u := &adminUser{}
	if err := s.edb.GetOneData(
		"SELECT id, username, password_hash, created_at, updated_at FROM admin_users WHERE username = $1",
		u, username,
	); err != nil {
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
	now := time.Now().UTC().Format(time.RFC3339)
	if existing != nil {
		_, err = s.edb.Exec(
			"UPDATE admin_users SET username = $1, password_hash = $2, updated_at = $3 WHERE id = $4",
			username, passwordHash, now, existing.ID,
		)
		return err
	}
	_, err = s.edb.Exec(
		"INSERT INTO admin_users (username, password_hash, created_at, updated_at) VALUES ($1, $2, $3, $3)",
		username, passwordHash, now,
	)
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
