//go:build integration

// pgschema_isolation_integration_test.go：PG 集成测试的 schema 级隔离。
//
// Why：结构同步类测试断言「全库零差异」（F 级多余表也计差异），隐含假设 PG_TEST_DSN 指向
// **专用干净库**。共享 postgres 库混有其他项目的表时必然误报失败（实测踩坑）。PG 的 catalog
// 查询（schema_query_*.sql）全部按 current_schema() 过滤，故为本轮测试随机建一个专用
// schema、经 DSN search_path 切入即可完全隔离——不要求使用者清库，也不碰库中既有数据。
package db_test

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

// pgIsolateSchema 在 dsn 指向的 PG 实例上创建随机名专用 schema，返回带 search_path 的
// 隔离 DSN；测试结束（t.Cleanup）DROP SCHEMA CASCADE。非 postgres DSN（空值等）原样返回。
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
	re := regexp.MustCompile(`search_path=[^&\s]*`)
	if re.MatchString(dsn) {
		return re.ReplaceAllString(dsn, "search_path="+schema)
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

// pgTestDSN 取 PG_TEST_DSN 并做 schema 隔离（供结构同步类全库断言测试使用）。
func pgTestDSN(t *testing.T) string {
	t.Helper()
	return pgIsolateSchema(t, os.Getenv("PG_TEST_DSN"))
}
