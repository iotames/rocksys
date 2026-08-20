// shield 插件管理端点（WAF 监控统计；数据字典见 docs/DATA_DICT.md）。
//
// 端点由 cmd/rocksys 装配时经 adminapi.RegisterPlugin 注入（仿 obs 三端点）：
//   - GET  /admin/shield/events  拦截明细查询（JSONL，支持时间/类别/IP 过滤）
//   - GET  /admin/shield/stats   聚合统计（查询时 GROUP BY：按日 × 类别 + Top IP）
//   - GET  /admin/shield/metrics 实时计数（读内存滑动窗口，秒级，无需查库）
//   - POST /admin/shield/prune   手动触发拦截明细清理（按保留天数删旧记录）
package shield

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"rocksys/internal/hotswap"

	"github.com/iotames/easyserver/log"
)

// 管理端点路径常量（main.go 装配引用）。
const (
	PathShieldEvents  = "/admin/shield/events"
	PathShieldStats   = "/admin/shield/stats"
	PathShieldMetrics = "/admin/shield/metrics"
	PathShieldPrune   = "/admin/shield/prune"
)

// AdminHandler shield 插件端点 handler。
type AdminHandler struct {
	shield *Shield // 通过 mgr.GetMiddleware("shield") 拿到实例
}

// NewAdminHandler 从热运维管理器获取 shield 实例构造 handler。
// 未注册 shield 时返回空 handler，端点返回 503。
func NewAdminHandler(mgr *hotswap.Manager) *AdminHandler {
	h := &AdminHandler{}
	if s, ok := mgr.GetMiddleware("shield").(*Shield); ok {
		h.shield = s
	}
	return h
}

// Metrics GET /admin/shield/metrics → 1 分钟窗口实时计数（内存读取，无需查库）。
// recorder 未注入（DB 未配置）时 counters 部分仍可用，落库计数为 0。
func (h *AdminHandler) Metrics(w http.ResponseWriter, r *http.Request) {
	if h.shield == nil {
		http.Error(w, "shield 未注册", http.StatusServiceUnavailable)
		return
	}
	snap := h.shield.Counter().Snapshot(time.Now())
	written, dropped := int64(0), int64(0)
	if rec := h.shield.Recorder(); rec != nil {
		written, dropped = rec.Stats()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"window_seconds": 60,
		"total":          snap.Total,
		"by_type":        snap.ByType,
		"written":        written, // 累计落库条数
		"dropped":        dropped, // 累计丢弃条数（通道满降级）
	})
}

// Events GET /admin/shield/events → 按条件查询拦截明细，返回 JSONL。
//
// 查询参数（均可选）：
//   - from / to：时间范围，支持 YYYY-MM-DD（当日全天）或 YYYY-MM-DDTHH:MM（精确到分）；
//     缺省 from = 当天 00:00，缺省 to = 当天 23:59；
//   - block_type：拦截类别（1-10，缺省 0 = 全部）；
//   - client_ip：客户端 IP 精确匹配；
//   - limit：返回上限（缺省 500）。
//
// 响应：application/x-ndjson，每行一个平铺 JSON；参数非法返回 400。
func (h *AdminHandler) Events(w http.ResponseWriter, r *http.Request) {
	if h.shield == nil {
		http.Error(w, "shield 未注册", http.StatusServiceUnavailable)
		return
	}
	rec := h.shield.Recorder()
	if rec == nil {
		http.Error(w, "拦截事件记录器未启用（DB 未配置）", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	from, to, err := parseEventTimeRange(q.Get("from"), q.Get("to"), time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	bt := BlockType(0)
	if v := q.Get("block_type"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || !BlockType(n).Valid() {
			http.Error(w, "block_type 应为 1-10 的整数", http.StatusBadRequest)
			return
		}
		bt = BlockType(n)
	}
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 10000 {
			http.Error(w, "limit 应为 1-10000 的整数", http.StatusBadRequest)
			return
		}
		limit = n
	}
	rows, err := rec.QueryEvents(EventQuery{
		From:      from,
		To:        to,
		BlockType: bt,
		ClientIP:  q.Get("client_ip"),
		Limit:     limit,
	})
	if err != nil {
		log.Error("shield: events 查询失败", "err", err.Error())
		http.Error(w, "events 查询失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	enc := json.NewEncoder(w)
	for _, row := range rows {
		_ = enc.Encode(row)
	}
}

// Stats GET /admin/shield/stats → 查询时聚合统计。
//
// 查询参数（均可选）：
//   - days：统计天数（缺省 7，上限 90——与拦截明细保留期一致）；
//   - top：Top IP 返回条数（缺省 10，上限 100）。
//
// 响应：{"days":..,"total":..,"daily":[{day,block_type,type_name,cnt},...],
// "top_ips":[{client_ip,cnt},...]}。daily 为按日 × 类别的明细行，前端自行透视。
func (h *AdminHandler) Stats(w http.ResponseWriter, r *http.Request) {
	if h.shield == nil {
		http.Error(w, "shield 未注册", http.StatusServiceUnavailable)
		return
	}
	rec := h.shield.Recorder()
	if rec == nil {
		http.Error(w, "拦截事件记录器未启用（DB 未配置）", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	days := 7
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 90 {
			http.Error(w, "days 应为 1-90 的整数（拦截明细保留 90 天）", http.StatusBadRequest)
			return
		}
		days = n
	}
	top := 10
	if v := q.Get("top"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 100 {
			http.Error(w, "top 应为 1-100 的整数", http.StatusBadRequest)
			return
		}
		top = n
	}
	from := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	daily, err := rec.StatsDaily(from)
	if err != nil {
		log.Error("shield: stats 查询失败", "err", err.Error())
		http.Error(w, "stats 查询失败", http.StatusInternalServerError)
		return
	}
	topIPs, err := rec.StatsTopIP(from, top)
	if err != nil {
		log.Error("shield: stats 查询失败", "err", err.Error())
		http.Error(w, "stats 查询失败", http.StatusInternalServerError)
		return
	}
	// 聚合总量并给 daily 行附类别中文名（block_type 数值稳定，前端亦可自行映射）。
	var total int64
	for _, row := range daily {
		total += eventToInt64(row["cnt"])
		if bt := eventToInt64(row["block_type"]); BlockType(bt).Valid() {
			row["type_name"] = BlockType(bt).String()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"days":    days,
		"total":   total,
		"daily":   daily,
		"top_ips": topIPs,
	})
}

// Prune POST /admin/shield/prune → 手动触发拦截明细清理。
// 请求体（可选）：{"days":90}——清理 N 天前的记录，缺省用配置的保留天数。
// 响应：{"ok":true,"deleted":N}。
func (h *AdminHandler) Prune(w http.ResponseWriter, r *http.Request) {
	// 仅 POST（RegisterPlugin 同时注册 GET+POST，挂件 handler 自行校验方法；
	// GET 无副作用，防止本机恶意页面无凭证触发清理）。
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	if h.shield == nil {
		http.Error(w, "shield 未注册", http.StatusServiceUnavailable)
		return
	}
	rec := h.shield.Recorder()
	if rec == nil {
		http.Error(w, "拦截事件记录器未启用（DB 未配置）", http.StatusServiceUnavailable)
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
	n, err := rec.Prune(body.Days)
	if err != nil {
		log.Error("shield: prune 失败", "err", err.Error())
		http.Error(w, "prune 失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "deleted": n})
}

// ── 时间参数解析（与 obs admin.go parseTimeRange 同语义，插件间不互相依赖）──

// 时间参数支持的两种格式。
const (
	eventTimeFmtMinute = "2006-01-02T15:04" // 精确到分
	eventTimeFmtDate   = "2006-01-02"       // 日期（当日全天）
)

// parseEventTimeRange 解析 from/to 时间范围（分钟精度，含端点）。
// 空值回退当天全天；格式非法或 from 晚于 to 返回错误。
func parseEventTimeRange(fromStr, toStr string, now time.Time) (from, to time.Time, err error) {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	from, err = parseEventRangeStart(fromStr, dayStart)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err = parseEventRangeEnd(toStr, dayStart.Add(24*time.Hour))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if from.After(to) {
		return time.Time{}, time.Time{}, errors.New("from 不能晚于 to")
	}
	return from, to, nil
}

// parseEventRangeStart 解析起点：空值用 def；日期 → 当日 00:00:00；分钟 → 该分整点。
func parseEventRangeStart(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	if t, err := time.ParseInLocation(eventTimeFmtMinute, s, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation(eventTimeFmtDate, s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("from 格式应为 YYYY-MM-DD 或 YYYY-MM-DDTHH:MM")
}

// parseEventRangeEnd 解析终点：空值用 def；日期 → 当日末；分钟 → 该分末（含整分钟记录）。
func parseEventRangeEnd(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def.Add(-time.Nanosecond), nil
	}
	if t, err := time.ParseInLocation(eventTimeFmtMinute, s, time.Local); err == nil {
		return t.Add(time.Minute - time.Nanosecond), nil
	}
	if t, err := time.ParseInLocation(eventTimeFmtDate, s, time.Local); err == nil {
		return t.Add(24*time.Hour - time.Nanosecond), nil
	}
	return time.Time{}, errors.New("to 格式应为 YYYY-MM-DD 或 YYYY-MM-DDTHH:MM")
}
