package obs

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"rocksys/internal/hotswap"
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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"qps":        s.QPS,
		"p95_ms":     s.P95,
		"p50_ms":     s.P50,
		"p99_ms":     s.P99,
		"error_rate": s.ErrorRate,
	})
}

// Logs GET /admin/logs?from=YYYY-MM-DD&to=YYYY-MM-DD → 按天读取访问日志返回 JSONL（§14 Admin API）。
// from/to 缺省为当天；文件不存在则跳过；参数非法返回 400。
func (h *AdminHandler) Logs(w http.ResponseWriter, r *http.Request) {
	if h.obs == nil {
		http.Error(w, "obs 未注册", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	today := time.Now().Format("2006-01-02")
	from, to, err := parseDayRange(q.Get("from"), q.Get("to"), today)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		path := filepath.Join(h.obs.logDir, "access-"+d.Format("2006-01-02")+".jsonl")
		f, err := os.Open(path)
		if err != nil {
			continue // 当天无日志/文件不存在，跳过
		}
		_, _ = io.Copy(w, f)
		_ = f.Close()
	}
}

// parseDayRange 解析 from/to 日期范围；空值回退到 today，并校验格式与先后顺序。
func parseDayRange(fromStr, toStr, today string) (from, to time.Time, err error) {
	if fromStr == "" {
		fromStr = today
	}
	if toStr == "" {
		toStr = today
	}
	from, err = time.Parse("2006-01-02", fromStr)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("from 参数格式应为 YYYY-MM-DD")
	}
	to, err = time.Parse("2006-01-02", toStr)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("to 参数格式应为 YYYY-MM-DD")
	}
	if from.After(to) {
		return time.Time{}, time.Time{}, errors.New("from 不能晚于 to")
	}
	return from, to, nil
}
