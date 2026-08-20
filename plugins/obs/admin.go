package obs

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"rocksys/internal/hotswap"

	"github.com/iotames/easyserver/log"
)

// AdminHandler obs 插件端点 handler（§8.1 插件端点注册机制）。
// 端点由 cmd/rocksys 装配时经 adminapi.RegisterPlugin 注入。
type AdminHandler struct {
	obs *Obs // 通过 mgr.GetMiddleware("obs") 拿到实例
}

// NewAdminHandler 从热运维管理器获取 obs 实例构造 handler。
// 未注册 obs 时返回空 handler，端点返回 503。
func NewAdminHandler(mgr *hotswap.Manager) *AdminHandler {
	h := &AdminHandler{}
	if o, ok := mgr.GetMiddleware("obs").(*Obs); ok {
		h.obs = o
	}
	return h
}

// Metrics GET /admin/metrics → {"qps":...,"p95_ms":...,"error_rate":...}（§14 Admin API）。
func (h *AdminHandler) Metrics(w http.ResponseWriter, r *http.Request) {
	if h.obs == nil {
		http.Error(w, "obs 未注册", http.StatusServiceUnavailable)
		return
	}
	s := h.obs.Metrics().Snapshot(time.Now())
	dropCount, consecutiveFails := h.obs.StoreStats()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"qps":               s.QPS,
		"p95_ms":            s.P95,
		"p50_ms":            s.P50,
		"p99_ms":            s.P99,
		"error_rate":        s.ErrorRate,
		"drop_count":        dropCount,
		"consecutive_fails": consecutiveFails,
	})
}

// Logs GET /admin/logs → 按条件查询访问日志返回 JSONL（§14 Admin API）。
//
// 查询参数（均可选）：
//   - from / to：时间范围，支持 YYYY-MM-DD（当日全天）或 YYYY-MM-DDTHH:MM（精确到分）；
//     缺省 from = 当天 00:00，缺省 to = 当天 23:59；
//   - path：请求路径精确匹配；
//   - path_like：请求路径模糊匹配（子串包含）；
//   - trace_id：链路标识模糊匹配（API 层保留，WebUI 已移除该输入框）。
//
// 响应：application/x-ndjson，每行一个平铺维度 JSON；参数非法返回 400。
func (h *AdminHandler) Logs(w http.ResponseWriter, r *http.Request) {
	if h.obs == nil {
		http.Error(w, "obs 未注册", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	from, to, err := parseTimeRange(q.Get("from"), q.Get("to"), time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := h.obs.Query(Query{
		From:     from,
		To:       to,
		Path:     q.Get("path"),
		PathLike: q.Get("path_like"),
		TraceID:  q.Get("trace_id"),
		Limit:    defaultQueryLimit,
	})
	if err != nil {
		log.Error("obs: logs 查询失败", "err", err.Error())
		http.Error(w, "logs 查询失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	enc := json.NewEncoder(w)
	for _, row := range rows {
		_ = enc.Encode(row)
	}
}

// Storage GET /admin/logs/storage → 日志存储总占用（§14 Admin API）。
// 响应：{"file_bytes":..,"db_bytes":..,"total_bytes":..}（file 与 db 独立统计后求和）。
func (h *AdminHandler) Storage(w http.ResponseWriter, r *http.Request) {
	if h.obs == nil {
		http.Error(w, "obs 未注册", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.obs.StorageSize())
}

// Prune POST /admin/logs/prune → 手动触发访问日志清理（按保留天数删 access_log 旧记录）。
// 请求体（可选）：{"days":7}——清理 N 天前的记录，缺省用配置的 OBS_LOG_RETENTION_DAYS。
// 响应：{"ok":true,"deleted":N}。与 shield_event 清理（POST /admin/shield/prune）各管各的。
func (h *AdminHandler) Prune(w http.ResponseWriter, r *http.Request) {
	// 仅 POST（RegisterPlugin 同时注册 GET+POST，挂件 handler 自行校验方法；
	// GET 无副作用，防止本机恶意页面无凭证触发清理）。
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	if h.obs == nil {
		http.Error(w, "obs 未注册", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Days int `json:"days"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // 空请求体合法（用配置默认值）
	}
	if body.Days < 0 || body.Days > 3650 {
		http.Error(w, "days 应为 0-3650 的整数", http.StatusBadRequest)
		return
	}
	n, err := h.obs.PruneLog(body.Days)
	if err != nil {
		log.Error("obs: prune 失败", "err", err.Error())
		http.Error(w, "prune 失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "deleted": n})
}

// 时间参数支持的两种格式。
const (
	timeFmtMinute = "2006-01-02T15:04" // 精确到分
	timeFmtDate   = "2006-01-02"       // 日期（当日全天）
)

// parseTimeRange 解析 from/to 时间范围（分钟精度，含端点）。
// 支持 YYYY-MM-DD（当日 00:00:00 ~ 23:59:59.999）或 YYYY-MM-DDTHH:MM（HH:MM:00 ~ HH:MM:59.999）。
// 空值回退当天全天；格式非法或 from 晚于 to 返回错误。
func parseTimeRange(fromStr, toStr string, now time.Time) (from, to time.Time, err error) {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	from, err = parseRangeStart(fromStr, dayStart)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err = parseRangeEnd(toStr, dayStart.Add(24*time.Hour))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if from.After(to) {
		return time.Time{}, time.Time{}, errors.New("from 不能晚于 to")
	}
	return from, to, nil
}

// parseRangeStart 解析起点：空值用 def；日期 → 当日 00:00:00；分钟 → 该分整点。
func parseRangeStart(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	if t, err := time.ParseInLocation(timeFmtMinute, s, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation(timeFmtDate, s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("from 格式应为 YYYY-MM-DD 或 YYYY-MM-DDTHH:MM")
}

// parseRangeEnd 解析终点：空值用 def；日期 → 当日 23:59:59.999；分钟 → 该分末（含整分钟记录）。
func parseRangeEnd(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def.Add(-time.Nanosecond), nil
	}
	if t, err := time.ParseInLocation(timeFmtMinute, s, time.Local); err == nil {
		return t.Add(time.Minute - time.Nanosecond), nil
	}
	if t, err := time.ParseInLocation(timeFmtDate, s, time.Local); err == nil {
		return t.Add(24*time.Hour - time.Nanosecond), nil
	}
	return time.Time{}, errors.New("to 格式应为 YYYY-MM-DD 或 YYYY-MM-DDTHH:MM")
}
