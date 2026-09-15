// migrate_test.go：目标库表结构对齐端点测试（sqlite 目标真库跑 diff → apply → 复检零差异）。
package adminapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rocksys/internal/db"
	"rocksys/internal/taskcenter"
)

// newMigrateServer 构造带数据源与表清单的服务器：CONF_DIR 指向临时目录，
// 表清单取 sqlite 方言的 admin_users 单表（内嵌脚本，无需外部服务）。
func newMigrateServer(t *testing.T) *AdminServer {
	t.Helper()
	s := New("127.0.0.1:19527", nil, nil, nil)
	dir := t.TempDir()
	s.confDir = &dir
	s.dataDB = nil
	s.tableSpecs = []db.TableSpec{{Table: "admin_users", CreateScript: "admin_users_create_table.sql"}}

	targetPath := filepath.ToSlash(filepath.Join(t.TempDir(), "target.db"))
	w := doJSON(s, http.MethodPost, PathDsn, `{"name":"target-sqlite","driver":"sqlite","dsn":"`+targetPath+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("准备目标数据源失败: %d %s", w.Code, w.Body.String())
	}
	return s
}

// waitTaskDone 轮询任务至终态并断言为 done（超时 fatal，附最后快照便于排查）。
func waitTaskDone(t *testing.T, s *AdminServer, id string) taskcenter.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last taskcenter.Task
	for time.Now().Before(deadline) {
		var ok bool
		if last, ok = s.tasks.Get(id); ok && last.Status.Terminal() {
			if last.Status != taskcenter.StatusDone {
				t.Fatalf("任务终态 = %s result=%q", last.Status, last.Result)
			}
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("任务未在限时内终态: %+v", last)
	return last
}

func TestMigrateSchemaDiffAndApply(t *testing.T) {
	s := newMigrateServer(t)
	var list struct {
		Items []struct {
			Code string `json:"code"`
		} `json:"items"`
	}
	w := doJSON(s, http.MethodGet, PathDsn, "")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("数据源列表 = %s err=%v", w.Body.String(), err)
	}
	code := list.Items[0].Code

	// ① 差异预览：空目标库 → 有差异、有 DDL。
	w = doJSON(s, http.MethodGet, PathMigrateSchema+"?code="+code, "")
	if w.Code != http.StatusOK {
		t.Fatalf("diff 预览失败: %d %s", w.Code, w.Body.String())
	}
	var diff struct {
		Items []db.DiffItem `json:"items"`
		SQL   string        `json:"sql"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &diff); err != nil {
		t.Fatalf("diff 解析: %v", err)
	}
	if len(diff.Items) == 0 || !strings.Contains(diff.SQL, "CREATE TABLE") {
		t.Fatalf("空目标库应有建表差异: items=%d", len(diff.Items))
	}

	// ② 执行对齐：后台任务，提交即返回任务 ID。
	w = doJSON(s, http.MethodPost, PathMigrateSchemaApply+"?code="+code, "")
	if w.Code != http.StatusOK {
		t.Fatalf("apply 提交失败: %d %s", w.Code, w.Body.String())
	}
	var applyResp struct {
		TaskID string `json:"task_id"`
		Noop   bool   `json:"noop"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &applyResp); err != nil || applyResp.TaskID == "" {
		t.Fatalf("apply 响应 = %s err=%v", w.Body.String(), err)
	}
	task := waitTaskDone(t, s, applyResp.TaskID)
	if task.CreatedBy != "schema_apply" {
		t.Fatalf("CreatedBy = %q", task.CreatedBy)
	}

	// ③ 复检：diff 为空；再提交 apply → noop。
	w2 := doJSON(s, http.MethodGet, PathMigrateSchema+"?code="+code, "")
	var diff2 struct {
		Items []db.DiffItem `json:"items"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &diff2); err != nil || len(diff2.Items) != 0 {
		t.Fatalf("对齐后应零差异: %s", w.Body.String())
	}
	w = doJSON(s, http.MethodPost, PathMigrateSchemaApply+"?code="+code, "")
	var noopResp struct {
		Noop bool `json:"noop"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &noopResp); err != nil || !noopResp.Noop {
		t.Fatalf("零差异再提交应 noop: %s", w.Body.String())
	}
}

func TestMigrateSchemaErrors(t *testing.T) {
	s := newMigrateServer(t)
	// 未知 Code。
	w := doJSON(s, http.MethodGet, PathMigrateSchema+"?code=no-such", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未知数据源应 400, got %d", w.Code)
	}
	// 缺 code。
	w = doJSON(s, http.MethodGet, PathMigrateSchema, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 code 应 400, got %d", w.Code)
	}
}
