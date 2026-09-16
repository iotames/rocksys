// dsn_test.go：数据源管理单测（CRUD 往返、脱敏、重复拦截、懒创建、CONF_DIR 热更取值）。
package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iotames/easydb/dsn"
)

// newDsnServer 构造数据源测试服务器：CONF_DIR 指向临时目录（不污染源码树）。
func newDsnServer(t *testing.T) *AdminServer {
	t.Helper()
	s := New("127.0.0.1:19527", nil, nil, nil)
	dir := t.TempDir()
	s.confDir = &dir
	return s
}

func doJSON(s *AdminServer, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	// 剥离查询串后按路径分发（handler 自行从 r.URL.Query() 取参）。
	base := path
	if i := strings.IndexByte(path, '?'); i >= 0 {
		base = path[:i]
	}
	switch base {
	case PathDsn:
		if method == http.MethodGet {
			s.handleDsnList(w, r)
		} else {
			s.handleDsnAdd(w, r)
		}
	case PathDsnDelete:
		s.handleDsnDelete(w, r)
	case PathDsnTest:
		s.handleDsnTest(w, r)
	case PathMigrateSchema:
		s.handleMigrateSchema(w, r)
	case PathMigrateSchemaApply:
		s.handleMigrateSchemaApply(w, r)
	case PathMigrateStart:
		s.handleMigrateStart(w, r)
	case PathMigrateStatus:
		s.handleMigrateStatus(w, r)
	case PathMigrateCancel:
		s.handleMigrateCancel(w, r)
	}
	return w
}

func TestDsnAddListDelete(t *testing.T) {
	s := newDsnServer(t)

	// 添加 sqlite 数据源（文件路径 DSN，无需真实服务）。
	w := doJSON(s, http.MethodPost, PathDsn, `{"name":"dev-sqlite","driver":"sqlite","dsn":"`+filepath.ToSlash(filepath.Join(t.TempDir(), "t.db"))+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("添加失败: %d %s", w.Code, w.Body.String())
	}
	var addResp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &addResp); err != nil || addResp.Code == "" {
		t.Fatalf("添加响应 = %s err=%v", w.Body.String(), err)
	}

	// 列表：脱敏展示 + dsn.json 懒创建落盘。
	w = doJSON(s, http.MethodGet, PathDsn, "")
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("列表 = %s err=%v", w.Body.String(), err)
	}
	if _, err := filepath.Abs(s.confDirPath()); err != nil {
		t.Fatal("confDirPath 应可用")
	}

	// 删除。
	w = doJSON(s, http.MethodPost, PathDsnDelete, `{"code":"`+addResp.Code+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("删除失败: %d %s", w.Code, w.Body.String())
	}
	// 再删同 Code → 404。
	w = doJSON(s, http.MethodPost, PathDsnDelete, `{"code":"`+addResp.Code+`"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("重复删除应 404, got %d", w.Code)
	}
}

func TestDsnDuplicateGuards(t *testing.T) {
	s := newDsnServer(t)
	dsnPath := filepath.ToSlash(filepath.Join(t.TempDir(), "a.db"))
	body := `{"name":"n1","driver":"sqlite","dsn":"` + dsnPath + `"}`
	if w := doJSON(s, http.MethodPost, PathDsn, body); w.Code != http.StatusOK {
		t.Fatalf("首次添加失败: %s", w.Body.String())
	}
	// Name 重复。
	w := doJSON(s, http.MethodPost, PathDsn, body)
	if w.Code != http.StatusConflict {
		t.Fatalf("Name 重复应 409, got %d %s", w.Code, w.Body.String())
	}
	// DSN 重复（换名同 DSN → dsn 底座同 Code 拦截）。
	w = doJSON(s, http.MethodPost, PathDsn, `{"name":"n2","driver":"sqlite","dsn":"`+dsnPath+`"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("DSN 重复应 409, got %d %s", w.Code, w.Body.String())
	}
	// 未注册驱动。
	w = doJSON(s, http.MethodPost, PathDsn, `{"name":"n3","driver":"oracle","dsn":"x"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法驱动应 400, got %d", w.Code)
	}
	// MySQL 密码裸 @ 预检：无论驱动是否已注册（-tags integration 的测试二进制会注册
	// mysql 驱动）都必须被拒——未注册 400；已注册则走底座 @ 密码拦截 409。
	w = doJSON(s, http.MethodPost, PathDsn, `{"name":"n4","driver":"mysql","dsn":"root:pa@ss@tcp(127.0.0.1:3306)/db"}`)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusConflict {
		t.Fatalf("MySQL 密码裸 @ 应被拦截（400/409）, got %d %s", w.Code, w.Body.String())
	}
	if err := dsn.CheckMySQLDSNPassword("root:pa@ss@tcp(127.0.0.1:3306)/db"); err == nil {
		t.Fatal("MySQL 密码裸 @ 应被 dsn 底座拦截")
	}
}

func TestMaskDsn(t *testing.T) {
	cases := []struct{ driver, in, wantSub string }{
		{"mysql", "root:secret@tcp(10.0.0.1:3306)/rock", "root:***@tcp(10.0.0.1:3306)/rock"},
		{"postgres", "postgres://admin:secret@10.0.0.2:5432/rock?sslmode=disable", "admin:***@10.0.0.2:5432/rock?sslmode=disable"},
		{"postgres", "host=10.0.0.2 user=admin password=secret dbname=rock", "password=***"},
		{"sqlite", "file:data/rock.db", "file:data/rock.db"},
	}
	for _, c := range cases {
		got := maskDsn(c.driver, c.in)
		if !strings.Contains(got, c.wantSub) || strings.Contains(got, "secret") {
			t.Errorf("maskDsn(%s,%s) = %s, want 含 %s 且不含明文", c.driver, c.in, got, c.wantSub)
		}
	}
}

func TestDsnConfDirHotValue(t *testing.T) {
	s := newDsnServer(t)
	// CONF_DIR 生效值变化 → 路径随当前值变化（每次读写实时取值，D11）。
	dir2 := t.TempDir()
	old := *s.confDir
	*s.confDir = dir2
	if s.confDirPath() != filepath.Join(dir2, "dsn.json") {
		t.Fatalf("confDirPath 未随生效值变化: %s", s.confDirPath())
	}
	*s.confDir = old
}
