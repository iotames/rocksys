//go:build integration

// pgschema_isolation_integration_test.go：PG 集成测试的 schema 级隔离（与 internal/db 同款方案）。
//
// Why：TestExecLogStorePG 断言 sql_exec_log 临时表「总数=N」，表建在共享 dev 库/运行库中时，
// 既有行（如 rocksys 自身产生的审计流水）会污染全表计数导致误报（实测踩坑）。PG 的查询均落
// 在 search_path 指向的 schema，为本轮测试随机建专用 schema 经 DSN 切入即完全隔离。
package adminapi

import (
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq" // 注册 postgres 驱动（建/删 schema 走原始连接）
)

// searchPathRe 匹配 keyword 形态与 URL 形态 DSN 中的 search_path 参数。
var searchPathRe = regexp.MustCompile(`search_path=[^&\s]*`)

// pgIsolateSchema 在 dsn 指向的 PG 实例上创建随机名专用 schema，返回带 search_path 的
// 隔离 DSN；测试结束（t.Cleanup）DROP SCHEMA CASCADE。空 DSN 原样返回（交由调用方跳过）。
func pgIsolateSchema(t *testing.T, dsn string) string {
	t.Helper()
	if dsn == "" || !strings.Contains(dsn, "postgres") {
		return dsn
	}
	base, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("pgIsolateSchema: 打开基础连接失败: %v", err)
	}
	defer base.Close()
	schema := fmt.Sprintf("rocksys_it_%d", time.Now().UnixNano()%1e9+int64(rand.Intn(1000)))
	if _, err = base.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("pgIsolateSchema: 创建隔离 schema 失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
	})
	return withSearchPath(dsn, schema)
}

// withSearchPath 把 DSN 的 search_path 覆盖/追加为 schema（兼容 URL 与 keyword 两种形态）。
func withSearchPath(dsn, schema string) string {
	if searchPathRe.MatchString(dsn) {
		return searchPathRe.ReplaceAllString(dsn, "search_path="+schema)
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + "search_path=" + schema
	}
	return dsn + " search_path=" + schema
}

// pgTestDSN 取 PG_TEST_DSN 并做 schema 隔离（隔离测试统一入口）。
func pgTestDSN(t *testing.T) string {
	t.Helper()
	return pgIsolateSchema(t, pgEnvDSN())
}

// pgEnvDSN 读环境变量门控值。
func pgEnvDSN() string { return os.Getenv("PG_TEST_DSN") }
