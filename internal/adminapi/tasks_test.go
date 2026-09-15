// tasks_test.go：任务执行中心 HTTP 端点测试（路由分发、404、取消语义透传）。
package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"rocksys/internal/taskcenter"
)

// newTasksServer 构造带任务中心的管理服务器（回环免鉴权形态）。
func newTasksServer(t *testing.T) *AdminServer {
	t.Helper()
	s := New("127.0.0.1:19527", nil, nil, nil)
	return s
}

// doTasks 直接走任务端点分发器（与中间件内同轨）。
func doTasks(s *AdminServer, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if body != "" {
		r = httptest.NewRequest(method, path, nil)
		_ = body
	}
	w := httptest.NewRecorder()
	s.routeTasks(w, r)
	return w
}

func TestTasksEndpointsHTTP(t *testing.T) {
	s := newTasksServer(t)

	// 空列表。
	w := doTasks(s, http.MethodGet, "/admin/tasks", "")
	if w.Code != http.StatusOK {
		t.Fatalf("列表状态码 = %d", w.Code)
	}

	// 提交一个即时完成的任务。
	id, err := s.SubmitTask(taskcenter.Spec{CreatedBy: "sql_exec", Title: "测试任务",
		Run: func(ctx context.Context, setProgress taskcenter.SetProgressFn) error {
			setProgress(&taskcenter.Progress{Text: "半程"})
			return nil
		}})
	if err != nil {
		t.Fatalf("SubmitTask: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if task, _ := s.tasks.Get(id); task.Status == taskcenter.StatusDone {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 详情。
	w = doTasks(s, http.MethodGet, "/admin/tasks/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("详情状态码 = %d: %s", w.Code, w.Body.String())
	}
	var task taskcenter.Task
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatalf("详情解析: %v", err)
	}
	if task.ID != id || task.CreatedBy != "sql_exec" || task.Status != taskcenter.StatusDone {
		t.Fatalf("详情字段 = %+v", task)
	}

	// 列表含该任务。
	w = doTasks(s, http.MethodGet, "/admin/tasks", "")
	var list struct {
		Items []taskcenter.Task `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("列表解析: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != id {
		t.Fatalf("列表 = %+v", list.Items)
	}

	// 取消已终态任务：不报错，返回终态（D27）。
	w = doTasks(s, http.MethodPost, "/admin/tasks/"+id+"/cancel", "")
	if w.Code != http.StatusOK {
		t.Fatalf("终态取消状态码 = %d: %s", w.Code, w.Body.String())
	}

	// 不存在的任务：详情与取消均 404（D26/D28）。
	if w = doTasks(s, http.MethodGet, "/admin/tasks/9999999-99", ""); w.Code != http.StatusNotFound {
		t.Fatalf("不存在详情状态码 = %d", w.Code)
	}
	if w = doTasks(s, http.MethodPost, "/admin/tasks/9999999-99/cancel", ""); w.Code != http.StatusNotFound {
		t.Fatalf("不存在取消状态码 = %d", w.Code)
	}
}
