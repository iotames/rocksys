// tasks.go：任务执行中心 HTTP 端点。
//
//	GET  /admin/tasks              —— 任务列表（含 running 与保留期内终态，供前端轮询与页面恢复）
//	GET  /admin/tasks/{id}         —— 单任务详情（不存在 → 404，D26）
//	POST /admin/tasks/{id}/cancel  —— 统一取消（已终态返回终态与提示、不报错；不存在 → 404）
//
// 路由实现说明：easyserver 路由为精确匹配、不支持路径参数，故本组端点经头部中间件
// 前缀拦截（/admin/tasks/...），其余请求原样放行；鉴权与其他内建端点同轨（auth.check）。
// 端点脱离 /admin/db 前缀：任务中心是全局模块而非数据库域专属，未来非数据库域长任务接入不改路径。
package adminapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/iotames/easyserver/httpsvr"

	"rocksys/internal/taskcenter"
)

// tasksDefaultLimit 任务列表默认返回的终态条数上限（?limit 可覆盖；running 不受限）。
const tasksDefaultLimit = 20

// PathTasksPrefix 任务中心端点前缀。
const PathTasksPrefix = "/admin/tasks"

// newTasksMiddleware 构造任务端点头部中间件（AdminServer.New 内注册）。
func (s *AdminServer) newTasksMiddleware() httpsvr.MiddleHandle {
	return httpsvr.NewMiddle(func(w http.ResponseWriter, r *http.Request, _ *httpsvr.DataFlow) bool {
		if !strings.HasPrefix(r.URL.Path, PathTasksPrefix) {
			return true // 非任务端点：放行进路由链
		}
		if !s.auth.check(httpsvr.Context{Writer: w, Request: r}) {
			return false
		}
		s.routeTasks(w, r)
		return false // 已写响应，中断链
	})
}

// routeTasks 按路径分发任务端点。
func (s *AdminServer) routeTasks(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil {
		http.Error(w, "任务执行中心不可用：未装配（请联系管理员检查装配）", http.StatusServiceUnavailable)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, PathTasksPrefix)
	path = strings.Trim(path, "/")
	switch {
	case path == "" && r.Method == http.MethodGet:
		// 并发管控配置一并透出（内存态），后台任务页原样展示：来源白名单 + 互斥规则三件套。
		// 列表用轻量分页：终态任务不带进度明细（轮询高频，明细是死数据），且默认只取最近
		// tasksDefaultLimit 条（?limit=N 自定义，0/缺省=全量；running 始终全带不受限），
		// 需要明细或更早记录时经单查/全量参数获取。
		limit := tasksDefaultLimit
		if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				limit = n // 0 = 不限量（拉全量）
			}
		}
		items, hasMore := s.tasks.ListLite(limit)
		_ = writeJSON(w, map[string]any{
			"items":            items,
			"has_more":         hasMore,
			"allow_creators":   s.tasks.AllowCreators(),
			"mutex_task_field": s.tasks.MutexTaskField(),
			"mutex_list":       s.tasks.MutexList(),
			"mutex_map":        s.tasks.MutexMap(),
		}, http.StatusOK)
	case path == "":
		http.Error(w, "任务列表仅接受 GET", http.StatusMethodNotAllowed)
	case r.Method == http.MethodGet:
		s.handleTaskGet(w, path)
	case strings.HasSuffix(path, "/cancel") && r.Method == http.MethodPost:
		s.handleTaskCancel(w, strings.TrimSuffix(path, "/cancel"))
	default:
		http.Error(w, "未知的任务端点："+r.URL.Path+"（可用：GET /admin/tasks、GET /admin/tasks/{id}、POST /admin/tasks/{id}/cancel）", http.StatusNotFound)
	}
}

// handleTaskGet 单任务详情：不存在（含已被终态限量淘汰的记录）一律 404。
func (s *AdminServer) handleTaskGet(w http.ResponseWriter, id string) {
	task, ok := s.tasks.Get(id)
	if !ok {
		http.Error(w, "任务不存在（ID="+id+"）：可能已被终态限量淘汰或服务已重启（任务记录不持久化）", http.StatusNotFound)
		return
	}
	_ = writeJSON(w, task, http.StatusOK)
}

// handleTaskCancel 统一取消：已终态返回终态与提示（HTTP 200），不存在 404。
func (s *AdminServer) handleTaskCancel(w http.ResponseWriter, id string) {
	task, msg, ok := s.tasks.Cancel(id)
	if !ok {
		http.Error(w, "任务不存在（ID="+id+"）：可能已被终态限量淘汰或服务已重启（任务记录不持久化）", http.StatusNotFound)
		return
	}
	_ = writeJSON(w, map[string]any{"ok": true, "message": msg, "task": task}, http.StatusOK)
}

// SubmitTask 供各业务接入点提交长任务（装配处 main.go 亦经此提交，如 geoip_sync）。
func (s *AdminServer) SubmitTask(spec taskcenter.Spec) (string, error) {
	return s.tasks.Submit(spec)
}

// TaskCenter 暴露中心实例（只读查询场景；业务提交一律走 SubmitTask）。
func (s *AdminServer) TaskCenter() *taskcenter.Center { return s.tasks }
