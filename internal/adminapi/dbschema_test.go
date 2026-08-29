// dbschema_test.go：表结构同步端点单测（sqlite 内存库构造缺表/缺列/执行中断场景）。
package adminapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rocksys/internal/db"
)

// setupSchemaServer 构造带表清单的内存库管理服务器（真实脚本建全部表）。
func setupSchemaServer(t *testing.T, specs []db.TableSpec) (*AdminServer, *db.DB) {
	t.Helper()
	d, err := db.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("打开内存库: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s := New("0.0.0.0:19527", nil, nil, d.EasyDB())
	s.SetSQLSource(d)
	s.SetTableSpecs(d, specs)
	return s, d
}

// execAll 用真实脚本在内存库建表/建索引（逐条执行，与运行时组件同语义）。
func execAll(t *testing.T, d *db.DB, names ...string) {
	t.Helper()
	for _, name := range names {
		txt, err := d.SQL(name)
		if err != nil {
			t.Fatalf("读取脚本 %s: %v", name, err)
		}
		tbl := "ip_blacklist"
		if strings.Contains(name, "shield_event") {
			tbl = "shield_event"
		}
		txt = strings.ReplaceAll(txt, "{table}", tbl)
		for _, stmt := range db.SplitScriptStatements(txt) {
			if _, err := d.EasyDB().GetSqlDB().Exec(stmt); err != nil {
				t.Fatalf("执行 %s 语句失败: %v\n%s", name, err, stmt)
			}
		}
	}
}

func testSpecs() []db.TableSpec {
	return []db.TableSpec{
		{Table: "admin_users", CreateScript: "admin_users_create_table.sql"}, // New() 内 userstore 已用真实脚本建此表
		{Table: "shield_event", CreateScript: "shield_event_create_table.sql", IndexScript: "shield_event_create_index.sql"},
		{Table: "ip_blacklist", CreateScript: "ip_blacklist_create_table.sql", IndexScript: "ip_blacklist_create_index.sql"},
	}
}

// callHandler 直接调用 handler（回环免登录语义，端点逻辑单测不穿透鉴权层）。
func callHandler(t *testing.T, h func(http.ResponseWriter, *http.Request), method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// TestDBSchemaNoDiff 真实脚本建的库应零差异（items 空且无生成 SQL）。
func TestDBSchemaNoDiff(t *testing.T) {
	s, d := setupSchemaServer(t, testSpecs())
	execAll(t, d, "shield_event_create_table.sql", "shield_event_create_index.sql",
		"ip_blacklist_create_table.sql", "ip_blacklist_create_index.sql")

	rec := callHandler(t, s.handleDBSchema, http.MethodGet, "/admin/db/schema", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"driver":"sqlite"`) {
		t.Errorf("响应应含 driver=sqlite: %s", body)
	}
	if strings.Contains(body, `"level"`) {
		t.Errorf("真实脚本建的库不应有差异项: %s", body)
	}
}

// TestDBSchemaMissingColumn 缺列场景：旧版表 → B 级差异 + ALTER 生成 SQL。
func TestDBSchemaMissingColumn(t *testing.T) {
	s, d := setupSchemaServer(t, testSpecs())
	// 旧版 ip_blacklist（缺 ip——UNIQUE 列应产出 C 级；缺多个普通列应产出 B 级）
	_, err := d.EasyDB().GetSqlDB().Exec("CREATE TABLE ip_blacklist (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL)")
	if err != nil {
		t.Fatalf("建旧版表: %v", err)
	}
	execAll(t, d, "shield_event_create_table.sql", "shield_event_create_index.sql")

	rec := callHandler(t, s.handleDBSchema, http.MethodGet, "/admin/db/schema", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"level":"B"`) || !strings.Contains(body, `"object":"hit_count"`) {
		t.Errorf("缺列应产出 B 级 hit_count 差异: %s", body)
	}
	if !strings.Contains(body, "ALTER TABLE ip_blacklist ADD COLUMN hit_count") {
		t.Errorf("应生成 ADD COLUMN 语句: %s", body)
	}
	// 期望结构缺失的列属于 C 级（ip 为 UNIQUE）
	if !strings.Contains(body, `"level":"C"`) {
		t.Errorf("缺 UNIQUE 列应产出 C 级需人工项: %s", body)
	}
}

// TestDBSchemaMissingTable 缺表场景：A 级差异 + 建表原文。
func TestDBSchemaMissingTable(t *testing.T) {
	s, d := setupSchemaServer(t, testSpecs())
	execAll(t, d, "shield_event_create_table.sql", "shield_event_create_index.sql")
	// ip_blacklist 未建 → A 级
	if _, err := d.EasyDB().GetSqlDB().Exec("DROP TABLE IF EXISTS ip_blacklist"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := callHandler(t, s.handleDBSchema, http.MethodGet, "/admin/db/schema", "")
	body := rec.Body.String()
	if !strings.Contains(body, `"level":"A"`) || !strings.Contains(body, `"table":"ip_blacklist"`) {
		t.Errorf("缺表应产出 A 级差异: %s", body)
	}
	if !strings.Contains(body, "CREATE TABLE IF NOT EXISTS ip_blacklist") {
		t.Errorf("A 级应生成建表原文: %s", body)
	}
}

// TestDBSchemaNotAssembled 未装配表清单 → 503 明确文案。
func TestDBSchemaNotAssembled(t *testing.T) {
	s, _ := setupSchemaServer(t, testSpecs())
	s.SetTableSpecs(nil, nil) // 模拟未装配
	rec := callHandler(t, s.handleDBSchema, http.MethodGet, "/admin/db/schema", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("未装配应返回 503，got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "未装配") {
		t.Errorf("文案应说明原因: %s", rec.Body.String())
	}
}

// TestDBExecSuccessAndAbort 执行端点：成功逐条；失败遇错即停并返回三要素文案。
func TestDBExecSuccessAndAbort(t *testing.T) {
	s, d := setupSchemaServer(t, nil)

	// 成功：两条语句全过
	rec := callHandler(t, s.handleDBExec, http.MethodPost, "/admin/db/exec",
		`{"sql":"CREATE TABLE exec_ok (id INTEGER);\nCREATE TABLE exec_ok2 (id INTEGER);"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"executed":2`) {
		t.Fatalf("成功执行应 executed=2: %d %s", rec.Code, rec.Body.String())
	}

	// 中断：第 2 条失败即停，第 3 条不执行
	rec = callHandler(t, s.handleDBExec, http.MethodPost, "/admin/db/exec",
		`{"sql":"CREATE TABLE exec_a (id INTEGER);\nINSERT INTO no_such_table VALUES(1);\nCREATE TABLE exec_b (id INTEGER);"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"executed":1`) || !strings.Contains(body, `"failed":1`) {
		t.Errorf("中断应 executed=1 failed=1: %s", body)
	}
	if !strings.Contains(body, "第 2 条执行失败") {
		t.Errorf("失败文案应指明第几条（三要素）: %s", body)
	}
	if !strings.Contains(body, "仅重发剩余") {
		t.Errorf("失败文案应给出下一步: %s", body)
	}
	// 第 3 条不得已执行
	if _, err := d.EasyDB().GetSqlDB().Query("SELECT * FROM exec_b"); err == nil {
		t.Errorf("遇错即停后第 3 条不应已执行")
	}

	// 空 body → 400 三要素
	rec = callHandler(t, s.handleDBExec, http.MethodPost, "/admin/db/exec", `{"sql":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("空 body 应 400，got %d", rec.Code)
	}
}
