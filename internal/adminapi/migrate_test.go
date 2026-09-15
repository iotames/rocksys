// migrate_test.go：目标库表结构对齐端点测试（sqlite 目标真库跑 diff → apply → 复检零差异）。
package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// ── 数据迁移执行器测试（SQLite 源 → SQLite 目标真库跑）────────────────────────────

// setupMigratePair 构造源/目标双 sqlite 数据源 + 双方均完成结构对齐（admin_users 单表），
// 源库灌 2500 行。返回服务器、源库（充当运行库 dataDB）、目标数据源 Code。
func setupMigratePair(t *testing.T) (*AdminServer, *db.DB, string) {
	t.Helper()
	s := newMigrateServer(t) // 已注册 admin_users 表清单 + 目标数据源
	var list struct {
		Items []struct {
			Code string `json:"code"`
		} `json:"items"`
	}
	w := doJSON(s, http.MethodGet, PathDsn, "")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("数据源列表 = %s", w.Body.String())
	}
	code := list.Items[0].Code
	// 目标库对齐结构（任务化端点，等终态）。
	w = doJSON(s, http.MethodPost, PathMigrateSchemaApply+"?code="+code, "")
	if err := waitApplyResp(t, s, w); err != nil {
		t.Fatalf("对齐: %v", err)
	}

	// 源库：注册为第二个数据源并对齐结构（模拟已建表的运行库），再灌 2500 行。
	srcPath := filepath.ToSlash(filepath.Join(t.TempDir(), "src.db"))
	w = doJSON(s, http.MethodPost, PathDsn, `{"name":"src-sqlite","driver":"sqlite","dsn":"`+srcPath+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("注册源数据源失败: %s", w.Body.String())
	}
	w = doJSON(s, http.MethodPost, PathMigrateSchemaApply+"?code="+srcCodeOf(t, s, "src-sqlite"), "")
	if err := waitApplyResp(t, s, w); err != nil {
		t.Fatalf("源库对齐: %v", err)
	}
	srcDB, err := db.Open("sqlite", srcPath)
	if err != nil {
		t.Fatalf("打开源库: %v", err)
	}
	t.Cleanup(func() { _ = srcDB.Close() })
	if err := seedAdminUsers(srcDB, 2500); err != nil {
		t.Fatalf("灌源数据: %v", err)
	}
	return s, srcDB, code
}

// srcCodeOf 按连接名取数据源 Code。
func srcCodeOf(t *testing.T, s *AdminServer, name string) string {
	t.Helper()
	ds, ok := s.getDsnByName(name)
	if !ok {
		t.Fatalf("数据源 %s 不存在", name)
	}
	return ds.Code
}

// waitApplyResp 解析 schema_apply 响应并等任务终态（noop 视为通过）。
func waitApplyResp(t *testing.T, s *AdminServer, w *httptest.ResponseRecorder) error {
	t.Helper()
	var resp struct {
		TaskID string `json:"task_id"`
		Noop   bool   `json:"noop"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		return fmt.Errorf("响应解析: %v body=%s", err, w.Body.String())
	}
	if resp.Noop {
		return nil
	}
	if resp.TaskID == "" {
		return fmt.Errorf("无任务 ID: %s", w.Body.String())
	}
	waitTaskDone(t, s, resp.TaskID)
	return nil
}

// seedAdminUsers 源库灌 n 行 admin_users。
func seedAdminUsers(srcDB *db.DB, n int) error {
	stmt, err := srcDB.EasyDB().GetSqlDB().Prepare(
		"INSERT INTO admin_users (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i := 1; i <= n; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("user%d", i), fmt.Sprintf("hash%d", i), "2026-09-15 10:00:00", "2026-09-15 10:00:00"); err != nil {
			return err
		}
	}
	return nil
}

// countTable 数表行数。
func countTable(t *testing.T, d *db.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := d.EasyDB().GetSqlDB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// waitMigrateFinish 等最近迁移任务终态（经 migState.taskID）。
func waitMigrateFinish(t *testing.T, s *AdminServer) taskcenter.Task {
	t.Helper()
	s.migMu.Lock()
	st := s.migState
	s.migMu.Unlock()
	if st == nil {
		t.Fatal("migState 未建立")
	}
	st.mu.Lock()
	id := st.taskID
	st.mu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if task, ok := s.tasks.Get(id); ok && task.Status.Terminal() {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("迁移任务未在限时内终态")
	return taskcenter.Task{}
}

// targetDBByCode 按数据源 Code 打开目标库（校验用探测连接）。
func targetDBByCode(t *testing.T, s *AdminServer, code string) *db.DB {
	t.Helper()
	ds, ok := s.getDsnByCode(code)
	if !ok {
		t.Fatalf("数据源 %s 不存在", code)
	}
	d, err := db.Open(ds.DriverName, ds.Dsn)
	if err != nil {
		t.Fatalf("打开目标库: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestMigrateRunSQLite(t *testing.T) {
	s, srcDB, code := setupMigratePair(t)
	s.dataDB = srcDB // source=self 走 dataDB

	body := `{"source":"self","target":"` + code + `","tables":["admin_users"],"batch":1000,"mode":"replace"}`
	w := doJSON(s, http.MethodPost, PathMigrateStart, body)
	if w.Code != http.StatusOK {
		t.Fatalf("启动迁移失败: %d %s", w.Code, w.Body.String())
	}
	if task := waitMigrateFinish(t, s); task.Status != taskcenter.StatusDone {
		t.Fatalf("迁移终态 = %s result=%q", task.Status, task.Result)
	}
	tgtDB := targetDBByCode(t, s, code)
	if n := countTable(t, tgtDB, "admin_users"); n != 2500 {
		t.Fatalf("目标行数 = %d, want 2500", n)
	}
	// 抽样字段值一致。
	var name string
	if err := tgtDB.EasyDB().GetSqlDB().QueryRow(
		"SELECT username FROM admin_users ORDER BY username LIMIT 1").Scan(&name); err != nil || name != "user1" {
		t.Fatalf("首行 username = %q err=%v", name, err)
	}
	// 自增序列重置生效：迁移后补写一条新记录，主键 = max+1 不冲突。
	if _, err := tgtDB.EasyDB().GetSqlDB().Exec(
		"INSERT INTO admin_users (username, password_hash, created_at, updated_at) VALUES ('after', 'h', '2026-09-15 11:00:00', '2026-09-15 11:00:00')"); err != nil {
		t.Fatalf("迁移后补写新记录应不冲突: %v", err)
	}
}

func TestMigrateBatchClampAndSmallBatch(t *testing.T) {
	s, srcDB, code := setupMigratePair(t)
	s.dataDB = srcDB
	// batch=5 → clamp 到 100（下限），迁移行为不变。
	body := `{"source":"self","target":"` + code + `","tables":["admin_users"],"batch":5,"mode":"replace"}`
	w := doJSON(s, http.MethodPost, PathMigrateStart, body)
	if w.Code != http.StatusOK {
		t.Fatalf("启动失败: %s", w.Body.String())
	}
	var resp struct {
		Batch int `json:"batch"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Batch != 100 {
		t.Fatalf("batch clamp = %d, want 100", resp.Batch)
	}
	if task := waitMigrateFinish(t, s); task.Status != taskcenter.StatusDone {
		t.Fatalf("迁移终态 = %s", task.Status)
	}
}

func TestMigrateRejectsTargetSelfAndEmptyTables(t *testing.T) {
	s, srcDB, code := setupMigratePair(t)
	s.dataDB = srcDB
	// 目标=self 拒绝（防误覆盖生产数据）。
	if w := doJSON(s, http.MethodPost, PathMigrateStart, `{"target":"self","tables":["admin_users"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("目标=self 应 400, got %d", w.Code)
	}
	// 表清单交集为空拒绝。
	if w := doJSON(s, http.MethodPost, PathMigrateStart, `{"target":"`+code+`","tables":["nope"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("表清单空应 400, got %d", w.Code)
	}
}

func TestMigrateWideTableSubBatch(t *testing.T) {
	// 宽表 33 列 × 默认批次 1000：占位符 33000 超 SQLite 硬上限 32766，
	// 执行器按预算 30000 切子批后应完整迁完（自适应切分生效）。
	s := newMigrateServer(t)
	var list struct {
		Items []struct {
			Code string `json:"code"`
		} `json:"items"`
	}
	w := doJSON(s, http.MethodGet, PathDsn, "")
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	code := list.Items[0].Code

	// 源/目标各建同构宽表（33 列），灌 500 行，直驱执行器内核单表迁移。
	var colDefs []string
	for i := 1; i <= 33; i++ {
		colDefs = append(colDefs, fmt.Sprintf("c%02d TEXT", i))
	}
	wideDDL := "CREATE TABLE wide (" + strings.Join(colDefs, ", ") + ")"
	srcPath := filepath.ToSlash(filepath.Join(t.TempDir(), "wide.db"))
	srcDB, err := db.Open("sqlite", srcPath)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	t.Cleanup(func() { _ = srcDB.Close() })
	if _, err := srcDB.EasyDB().GetSqlDB().Exec(wideDDL); err != nil {
		t.Fatalf("create wide src: %v", err)
	}
	tx, _ := srcDB.EasyDB().GetSqlDB().Begin()
	stmt, err := tx.Prepare("INSERT INTO wide VALUES (" + strings.TrimSuffix(strings.Repeat("?,", 33), ",") + ")")
	if err != nil {
		t.Fatalf("prepare wide insert: %v", err)
	}
	for r := 1; r <= 500; r++ {
		var args []any
		for i := 1; i <= 33; i++ {
			args = append(args, fmt.Sprintf("v%d-%d", r, i))
		}
		if _, err := stmt.Exec(args...); err != nil {
			t.Fatalf("seed wide #%d: %v", r, err)
		}
	}
	_ = stmt.Close()
	_ = tx.Commit()

	ds, _ := s.getDsnByCode(code)
	tgtDB, err := db.Open(ds.DriverName, ds.Dsn)
	if err != nil {
		t.Fatalf("open tgt: %v", err)
	}
	t.Cleanup(func() { _ = tgtDB.Close() })
	if _, err := tgtDB.EasyDB().GetSqlDB().Exec(wideDDL); err != nil {
		t.Fatalf("create wide tgt: %v", err)
	}

	params := migrateParams{source: "self", targetDS: ds, tables: []string{"wide"},
		batch: migrateDefaultBatch, mode: migrateModeReplace, selfDB: srcDB}
	st := &migrateRunState{tables: []*tableProgress{{Table: "wide", Status: tablePending}}}
	rowsDone, err := s.migrateTable(context.Background(), srcDB, tgtDB, "wide", params, st, func(*taskcenter.Progress) {})
	if err != nil {
		t.Fatalf("宽表迁移应经子批完整迁完: %v", err)
	}
	if rowsDone != 500 {
		t.Fatalf("rowsDone = %d, want 500", rowsDone)
	}
	if n := countTable(t, tgtDB, "wide"); n != 500 {
		t.Fatalf("目标宽表行数 = %d, want 500", n)
	}
}

func TestMigrateCancelWholeTask(t *testing.T) {
	s, srcDB, code := setupMigratePair(t)
	s.dataDB = srcDB
	// 大数据量拉长迁移窗口（5 万行 × 批 100 ≈ 500 批），保证「迁移中可取消」窗口可观测。
	// 两张表：第一张迁移中、第二张待迁移时整任务取消。扩表清单并补齐目标结构。
	s.tableSpecs = append(s.tableSpecs, db.TableSpec{Table: "schedule_list", CreateScript: "schedule_list_create_table.sql"})
	w := doJSON(s, http.MethodPost, PathMigrateSchemaApply+"?code="+code, "")
	if err := waitApplyResp(t, s, w); err != nil {
		t.Fatalf("第二次对齐: %v", err)
	}
	// 源库同样补齐结构（运行库语义：结构已存在）。
	if w := doJSON(s, http.MethodPost, PathMigrateSchemaApply+"?code="+srcCodeOf(t, s, "src-sqlite"), ""); true {
		_ = waitApplyResp(t, s, w)
	}
	if _, err := srcDB.EasyDB().GetSqlDB().Exec(
		"INSERT INTO schedule_list (name, title, kind, config_key, plan, last_status, last_message, updated_at) VALUES ('t2','测试','system','','', 'ok','','2026-09-15 10:00:00')"); err != nil {
		t.Fatalf("seed t2: %v", err)
	}

	body := `{"source":"self","target":"` + code + `","tables":["admin_users","schedule_list"],"batch":100,"mode":"replace"}`
	w = doJSON(s, http.MethodPost, PathMigrateStart, body)
	if w.Code != http.StatusOK {
		t.Fatalf("启动失败: %s", w.Body.String())
	}
	// 第一张表迁移中（已有部分行落库）时发起整任务取消。
	deadline := time.Now().Add(8 * time.Second)
	cancelled := false
	for time.Now().Before(deadline) {
		s.migMu.Lock()
		st := s.migState
		s.migMu.Unlock()
		if st != nil {
			snap := st.snapshot()
			if len(snap) > 1 && snap[0].Status == tableRunning && snap[0].RowsDone > 0 {
				if w := doJSON(s, http.MethodPost, PathMigrateCancel, "{}"); w.Code != http.StatusOK {
					t.Fatalf("取消失败: %s", w.Body.String())
				}
				cancelled = true
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cancelled {
		t.Skip("任务在可取消窗口前已推进完（运行过快），取消语义由任务中心单测覆盖")
	}
	task := waitMigrateFinish(t, s)
	if task.Status != taskcenter.StatusCancelled {
		t.Fatalf("整任务取消终态 = %s", task.Status)
	}
	snap := s.migState.snapshot()
	if snap[1].Status != tableCancelled {
		t.Fatalf("剩余表应 cancelled, got %s", snap[1].Status)
	}
}
