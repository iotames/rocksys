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
	"io"
	"net/http"
	"strconv"
	"strings"
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

	// 动态 IP 黑白名单管理（WAF 方案 §6.1；副作用一律 POST，防本机恶意页面无凭证触发）。
	PathBlacklist         = "/admin/shield/blacklist"
	PathBlacklistUpdate   = "/admin/shield/blacklist/update"
	PathBlacklistDelete   = "/admin/shield/blacklist/delete"
	PathBlacklistRestore  = "/admin/shield/blacklist/restore"
	PathBlacklistImport   = "/admin/shield/blacklist/import"
	PathBlacklistSyncFile = "/admin/shield/blacklist/sync_file"
	PathBlacklistBan      = "/admin/shield/blacklist/ban"
	PathShieldJail        = "/admin/shield/jail"
	PathWhitelist         = "/admin/shield/whitelist"
	PathWhitelistUpdate   = "/admin/shield/whitelist/update"
	PathWhitelistDelete   = "/admin/shield/whitelist/delete"
	PathWhitelistRestore  = "/admin/shield/whitelist/restore"
	PathWhitelistImport   = "/admin/shield/whitelist/import"
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
//   - limit：单页条数（缺省 500，最大 10000）；
//   - offset：分页偏移（缺省 0）。
//
// 响应：application/x-ndjson，每行一个平铺 JSON（含 in_blacklist 标记：该行 IP 是否在
// 黑名单快照中，供前端行内封禁按钮置灰）；总数经 X-Total-Count 响应头回传
// （与 limit/offset 配合实现服务端分页）；参数非法返回 400。
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
	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "offset 应为非负整数", http.StatusBadRequest)
			return
		}
		offset = n
	}
	eq := EventQuery{
		From:      from,
		To:        to,
		BlockType: bt,
		ClientIP:  q.Get("client_ip"),
		Limit:     limit,
		Offset:    offset,
	}
	rows, err := rec.QueryEvents(eq)
	if err != nil {
		log.Error("shield: events 查询失败", "err", err.Error())
		http.Error(w, "events 查询失败", http.StatusInternalServerError)
		return
	}
	total, err := rec.CountEvents(eq)
	if err != nil {
		log.Error("shield: events 计数失败", "err", err.Error())
		http.Error(w, "events 查询失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	enc := json.NewEncoder(w)
	// 逐行附「是否在黑名单」标记（与 stats TOP 同源：内存快照 InBlacklist，支持 CIDR，零 DB 查询），
	// 供 WebUI 拦截明细行内「IP封禁」按钮置灰（已在黑名单的行禁选）。
	for _, row := range rows {
		ip, _ := row["client_ip"].(string)
		row["in_blacklist"] = h.shield.InBlacklist(ip)
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
	// 给 Top IP 行附「是否在黑名单」（与拦截判定同源：外挂文件 ∪ DB 活跃条目），
	// 供 WebUI 攻击源表标注与批量加黑（已在黑名单的行禁选）。
	for _, row := range topIPs {
		ip, _ := row["client_ip"].(string)
		row["in_blacklist"] = h.shield.InBlacklist(ip)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"days":              days,
		"total":             total,
		"daily":             daily,
		"top_ips":           topIPs,
		"blacklist_addable": h.shield.IPListEnabled(true), // DB 黑名单可用 → WebUI 显示勾选列与批量加黑按钮
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

// ── 动态 IP 黑白名单管理端点（WAF 方案 §6.1）────────────────────────
// 路径与语义（副作用一律 POST，GET 无副作用防本机恶意页面无凭证触发）：
//
//	GET  /admin/shield/blacklist         列表（query: ip 模糊 / block_type / valid_only / limit / offset）
//	POST /admin/shield/blacklist         新增（body: ip/title/block_type/expires_at）
//	POST /admin/shield/blacklist/update  更新（body: id/title/block_type/expires_at）
//	POST /admin/shield/blacklist/delete  软删（body: id）
//	POST /admin/shield/blacklist/restore 恢复（body: id）
//	POST /admin/shield/blacklist/import  批量导入（body: text 每行一个 IP/CIDR + title/block_type）
//	POST /admin/shield/blacklist/sync_file 从外挂规则文件 rules/ip_blacklist.txt 同步入库（幂等）
//	POST /admin/shield/blacklist/ban     专用封禁端点（body: ip/title/block_type/duration="24h"|"permanent"；
//	                                     无记录新增、软删/过期恢复续封 warn_times+1，活跃报"已在黑名单"）
//	GET  /admin/shield/jail              小黑屋（当前在押的限时封禁条目；query: limit 默认 20 上限 100）
//	/admin/shield/whitelist 同构（无 block_type/expires_at）
//
// 全部变更写库成功后主动重建快照（立即生效，不等 TTL 兜底）。

// 黑白名单端点入口（main.go 装配引用；返回 http.HandlerFunc 供 RegisterPlugin）。
func (h *AdminHandler) Blacklist() http.HandlerFunc { return h.listOrAdd(true) }
func (h *AdminHandler) BlacklistUpdate() http.HandlerFunc {
	return h.postOnly(func(w http.ResponseWriter, r *http.Request) { h.updateIPList(w, r, true) })
}
func (h *AdminHandler) BlacklistDelete() http.HandlerFunc {
	return h.postOnly(func(w http.ResponseWriter, r *http.Request) { h.deleteIPList(w, r, true) })
}
func (h *AdminHandler) BlacklistRestore() http.HandlerFunc {
	return h.postOnly(func(w http.ResponseWriter, r *http.Request) { h.restoreIPList(w, r, true) })
}
func (h *AdminHandler) BlacklistImport() http.HandlerFunc {
	return h.postOnly(func(w http.ResponseWriter, r *http.Request) { h.importIPList(w, r, true) })
}
func (h *AdminHandler) BlacklistSyncFile() http.HandlerFunc {
	return h.postOnly(h.syncBlacklistFile)
}
func (h *AdminHandler) BlacklistBan() http.HandlerFunc {
	return h.postOnly(h.banIPList)
}
func (h *AdminHandler) Whitelist() http.HandlerFunc { return h.listOrAdd(false) }
func (h *AdminHandler) WhitelistUpdate() http.HandlerFunc {
	return h.postOnly(func(w http.ResponseWriter, r *http.Request) { h.updateIPList(w, r, false) })
}
func (h *AdminHandler) WhitelistDelete() http.HandlerFunc {
	return h.postOnly(func(w http.ResponseWriter, r *http.Request) { h.deleteIPList(w, r, false) })
}
func (h *AdminHandler) WhitelistRestore() http.HandlerFunc {
	return h.postOnly(func(w http.ResponseWriter, r *http.Request) { h.restoreIPList(w, r, false) })
}
func (h *AdminHandler) WhitelistImport() http.HandlerFunc {
	return h.postOnly(func(w http.ResponseWriter, r *http.Request) { h.importIPList(w, r, false) })
}

// listOrAdd 列表（GET）/ 新增（POST）共用 handler（RegisterPlugin 同 path 注册两方法）。
func (h *AdminHandler) listOrAdd(isBlack bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.shield == nil {
			http.Error(w, "shield 未注册", http.StatusServiceUnavailable)
			return
		}
		if !h.shield.IPListEnabled(isBlack) {
			http.Error(w, "IP 黑白名单未启用（DB 未配置）", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			h.listIPList(w, r, isBlack)
		case http.MethodPost:
			h.addIPList(w, r, isBlack)
		default:
			http.Error(w, "仅支持 GET/POST", http.StatusMethodNotAllowed)
		}
	}
}

// postOnly 仅 POST 副作用端点（update/delete/restore/import）。
func (h *AdminHandler) postOnly(fn func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
			return
		}
		if h.shield == nil {
			http.Error(w, "shield 未注册", http.StatusServiceUnavailable)
			return
		}
		if !h.shield.IPListEnabled(false) && !h.shield.IPListEnabled(true) {
			http.Error(w, "IP 黑白名单未启用（DB 未配置）", http.StatusServiceUnavailable)
			return
		}
		fn(w, r)
	}
}

// listIPList 列表：分页 + ip 模糊 / block_type（黑）/ valid_only 过滤。
func (h *AdminHandler) listIPList(w http.ResponseWriter, r *http.Request, isBlack bool) {
	q := r.URL.Query()
	f := ListFilter{IP: q.Get("ip"), Limit: 500}
	if v := q.Get("block_type"); v != "" && isBlack {
		bt, err := strconv.Atoi(v)
		if err != nil || bt < 0 || bt > int(BlockManual) {
			http.Error(w, "block_type 应为 0-11 的整数（0=不限；11=人工收录）", http.StatusBadRequest)
			return
		}
		f.BlockType = BlockType(bt)
	}
	if v := q.Get("valid_only"); v == "1" || v == "true" {
		f.ValidOnly = true
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			http.Error(w, "limit 应为正整数", http.StatusBadRequest)
			return
		}
		f.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "offset 应为非负整数", http.StatusBadRequest)
			return
		}
		f.Offset = n
	}
	// 排序键：经 store 层白名单映射（ip_list_store.go blacklistSortWhitelist），
	// 非法/缺省回默认 id DESC；白名单实现忽略该字段，不受影响。
	f.Sort = q.Get("sort")
	rows, total, err := h.shield.ListIPList(isBlack, f)
	if err != nil {
		log.Error("shield: 黑白名单列表查询失败", "kind", ipListKindName(isBlack), "err", err.Error())
		http.Error(w, "列表查询失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"total": total, "rows": rows})
}

// addIPList 新增（body: ip/title/block_type/expires_at；白名单忽略后两者）。
func (h *AdminHandler) addIPList(w http.ResponseWriter, r *http.Request, isBlack bool) {
	var body struct {
		IP        string `json:"ip"`
		Title     string `json:"title"`
		BlockType int    `json:"block_type"`
		ExpiresAt string `json:"expires_at"` // RFC3339；空 = 永久
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.IP == "" {
		http.Error(w, "ip 必填（精确 IP 或 CIDR）", http.StatusBadRequest)
		return
	}
	exp, err := parseExpiresAt(body.ExpiresAt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// 黑名单 block_type：缺省（未传）→ 11 人工收录；显式值 0-11 由 mgmt 层校验。
	if isBlack && body.BlockType == 0 {
		body.BlockType = int(BlockManual)
	}
	// 显式值越界提前 400（否则落到 mgmt 层通用错误被兜成 500，参数错误不应按服务端故障处理）。
	if isBlack && (body.BlockType < 0 || body.BlockType > int(BlockManual)) {
		http.Error(w, "block_type 应为 0-11 的整数（0=其他；缺省 11=人工收录）", http.StatusBadRequest)
		return
	}
	id, err := h.shield.AddIPList(isBlack, body.IP, body.Title, BlockType(body.BlockType), exp)
	if err != nil {
		writeListErr(w, isBlack, "新增", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": id})
}

// updateIPList 更新（body: id/title/block_type/expires_at）。
func (h *AdminHandler) updateIPList(w http.ResponseWriter, r *http.Request, isBlack bool) {
	var body struct {
		ID        int64  `json:"id"`
		Title     string `json:"title"`
		BlockType int    `json:"block_type"`
		ExpiresAt string `json:"expires_at"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.ID <= 0 {
		http.Error(w, "id 必填", http.StatusBadRequest)
		return
	}
	exp, err := parseExpiresAt(body.ExpiresAt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.shield.UpdateIPList(isBlack, body.ID, body.Title, BlockType(body.BlockType), exp); err != nil {
		writeListErr(w, isBlack, "更新", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// deleteIPList 软删（body: id）。
func (h *AdminHandler) deleteIPList(w http.ResponseWriter, r *http.Request, isBlack bool) {
	var body struct {
		ID int64 `json:"id"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.ID <= 0 {
		http.Error(w, "id 必填", http.StatusBadRequest)
		return
	}
	if err := h.shield.SoftDeleteIPList(isBlack, body.ID); err != nil {
		writeListErr(w, isBlack, "删除", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// restoreIPList 恢复软删（body: id）。
func (h *AdminHandler) restoreIPList(w http.ResponseWriter, r *http.Request, isBlack bool) {
	var body struct {
		ID int64 `json:"id"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.ID <= 0 {
		http.Error(w, "id 必填", http.StatusBadRequest)
		return
	}
	if err := h.shield.RestoreIPList(isBlack, body.ID); err != nil {
		writeListErr(w, isBlack, "恢复", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// importIPList 批量导入：body 为纯文本（每行一个精确 IP/CIDR，兼容外挂文件格式），
// query 可选 title / block_type（黑名单缺省 11 人工收录；显式值 0-11，0=其他兜底来源）。
func (h *AdminHandler) importIPList(w http.ResponseWriter, r *http.Request, isBlack bool) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "读取请求体失败", http.StatusBadRequest)
		return
	}
	text := string(b)
	// 兼容两种客户端：curl 纯文本（每行一个 IP）与前端 api.post 的 JSON 字符串编码。
	if len(text) > 0 {
		var s string
		if json.Unmarshal(b, &s) == nil {
			text = s
		}
	}
	if len(strings.TrimSpace(text)) == 0 {
		http.Error(w, "text 必填（每行一个精确 IP 或 CIDR）", http.StatusBadRequest)
		return
	}
	bt := int(BlockManual) // 黑名单缺省 11 人工收录（IP_BLACKLIST_PLAN §5.3）
	if v := r.URL.Query().Get("block_type"); v != "" && isBlack {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > int(BlockManual) {
			http.Error(w, "block_type 应为 0-11 的整数（0=其他；缺省 11=人工收录）", http.StatusBadRequest)
			return
		}
		bt = n
	}
	lines := strings.Split(text, "\n")
	imported, skipped, err := h.shield.ImportIPList(isBlack, lines, r.URL.Query().Get("title"), BlockType(bt))
	if err != nil {
		writeListErr(w, isBlack, "导入", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "imported": imported, "skipped": skipped})
}

// syncBlacklistFile 从外挂规则文件 rules/ip_blacklist.txt 同步 IP 入库（IP_BLACKLIST_PLAN §3.3）。
// 读取复用 Shield 现有 ruleLoader 路径（外挂优先、内嵌兜底；勿另造读取），
// 解析复用导入语义（# 注释/空行忽略），逐行 validIPEntry 校验后走 ImportIPList
// （title 固定"来自 ip_blacklist.txt 同步"、block_type=11 人工收录）。幂等：重复同步 skipped 递增。
func (h *AdminHandler) syncBlacklistFile(w http.ResponseWriter, r *http.Request) {
	imported, skipped, err := h.shield.SyncBlacklistFile()
	if err != nil {
		// 文案三要素：发生了什么（同步失败/无可同步内容）+ 为什么 + 下一步（检查文件）。
		log.Error("shield: 从文件同步黑名单失败", "err", err.Error())
		http.Error(w, "从 rules/ip_blacklist.txt 同步黑名单失败："+err.Error()+"。请检查外挂目录 hotscripts/rules/ip_blacklist.txt（或内嵌规则文件）是否存在且包含有效 IP 行后重试", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "imported": imported, "skipped": skipped})
}

// banIPList 专用封禁端点（IP_BLACKLIST_PLAN §3.5 决策 10）：body
// {ip, title, block_type(1-11，缺省 11 人工收录), duration("24h"|"permanent"，缺省 24h)}。
// 服务端换算 expires_at；三态：无记录 → 封禁入库（warn_times=1）；
// 活跃 → 400"已在黑名单"+ 去向指引（不过载 addIPList、保留其既有报错 UX）；
// 软删/过期 → 恢复续封（按所选时长 + warn_times+1，满 5 转永久时响应注明 to_permanent）。
// 成功后 BanIP 内部已重建快照（立即生效）。
func (h *AdminHandler) banIPList(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IP        string `json:"ip"`
		Title     string `json:"title"`
		BlockType int    `json:"block_type"`
		Duration  string `json:"duration"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.IP == "" {
		http.Error(w, "ip 必填（精确 IP），请补充后重试", http.StatusBadRequest)
		return
	}
	if body.BlockType == 0 {
		body.BlockType = int(BlockManual) // 缺省 11 人工收录（IP_BLACKLIST_PLAN §3.5）
	}
	if body.BlockType < 1 || body.BlockType > int(BlockManual) {
		http.Error(w, "block_type 应为 1-11 的整数（缺省 11=人工收录），请修正后重试", http.StatusBadRequest)
		return
	}
	if body.Title == "" {
		body.Title = "人工封禁"
	}
	exp, err := banDurationToExpires(body.Duration, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	written, perm, err := h.shield.BanIP(body.IP, body.Title, BlockType(body.BlockType), exp)
	if err != nil {
		switch {
		case errors.Is(err, ErrIPListDisabled):
			http.Error(w, "IP 黑名单未启用（DB 未配置），封禁无法入库。请先配置数据库后重试，或改用外挂文件 rules/ip_blacklist.txt 维护黑名单", http.StatusServiceUnavailable)
		case errors.Is(err, ErrInvalidIP):
			http.Error(w, "封禁失败：ip 应为精确 IP 或 CIDR。请核对来源 IP 后重试", http.StatusBadRequest)
		default:
			log.Error("shield: 封禁入库失败", "ip", body.IP, "err", err.Error())
			http.Error(w, "封禁入库失败（数据库写入异常），请稍后重试", http.StatusInternalServerError)
		}
		return
	}
	if !written {
		// 活跃条目双保险（前端按钮本应置灰）：说清现状 + 指引去向。
		http.Error(w, "该 IP 已在黑名单中且记录当前生效，无需重复封禁。请到「WAF安全防护 → IP 黑名单」列表查看/管理该条目（如需调整时长可修改其过期时间）", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":           true,
		"to_permanent": perm, // true = 累计封禁达 5 次，本次已由限时转永久
	})
}

// banDurationToExpires 封禁时长 → expires_at（服务端换算，前端只传枚举）。
// "24h" → now+24h；"permanent" → nil（永久）；空 → 缺省 24h；其他 → 错误（三要素文案）。
func banDurationToExpires(duration string, now time.Time) (*time.Time, error) {
	switch duration {
	case "", "24h":
		exp := now.Add(24 * time.Hour)
		return &exp, nil
	case "permanent":
		return nil, nil
	default:
		return nil, errors.New(`封禁失败：duration 仅支持 "24h" 或 "permanent"（收到 ` + duration + `）。请改用合法时长后重试`)
	}
}

// Jail GET /admin/shield/jail → 小黑屋（IP_BLACKLIST_PLAN §3.7）：当前在押的
// 限时封禁条目（未软删、未过期、expires_at 非空），临近解封的在前。
// query limit：默认 20、上限 100，非法/越界回默认（首页轻量预览，不报错）。
// 响应 {total, rows}，rows 含 ip/block_type/hit_count/warn_times/created_at/expires_at。
func (h *AdminHandler) Jail(w http.ResponseWriter, r *http.Request) {
	if h.shield == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "shield 未注册")
		return
	}
	if !h.shield.IPListEnabled(true) {
		writeJSONErr(w, http.StatusServiceUnavailable, "IP 黑名单未启用（DB 未配置），小黑屋不可用")
		return
	}
	limit := 0 // 0 → store 层回默认 20（非法/越界同样由 store 收敛）
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n // 非法值（err != nil）保持 0 回默认
		}
	}
	rows, total, err := h.shield.Jail(limit)
	if err != nil {
		log.Error("shield: 小黑屋查询失败", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "小黑屋查询失败（数据库异常），请稍后重试；若持续出现请检查数据库状态或查看服务日志")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"total": total, "rows": rows})
}

// parseExpiresAt 解析过期时间（RFC3339；空 = 永久）。
func parseExpiresAt(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, errors.New("expires_at 应为 RFC3339 时间（如 2026-09-01T00:00:00Z），空 = 永久")
	}
	return &t, nil
}

// writeJSONErr 统一 JSON 错误响应：{"error": msg}，供前端 api.js 解析 error 字段展示。
func writeJSONErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

// writeListErr 统一黑白名单操作错误响应：DB 未启用 503、参数类 400、内部错误 500 通用文案。
func writeListErr(w http.ResponseWriter, isBlack bool, op string, err error) {
	switch {
	case errors.Is(err, ErrIPListDisabled):
		http.Error(w, "IP 黑白名单未启用（DB 未配置）", http.StatusServiceUnavailable)
	case errors.Is(err, ErrIPExists):
		// 唯一约束覆盖软删/过期条目：记录在表里但不在生效快照中，用户会看到"不在黑名单却加不进去"，
		// 文案必须点破原因并给出出路（恢复 / 改过期时间 / 删除后重录），见 docs/webui.md 提示文案原则。
		http.Error(w, "该 IP 已有黑名单记录，但当前未生效（记录可能已被删除或已过期）。请到「WAF安全防护 → IP 黑名单」列表中恢复该记录、修改过期时间，或删除后重新添加", http.StatusBadRequest)
	case errors.Is(err, ErrInvalidIP):
		http.Error(w, "ip 应为精确 IP 或 CIDR", http.StatusBadRequest)
	default:
		log.Error("shield: 黑白名单操作失败", "kind", ipListKindName(isBlack), "op", op, "err", err.Error())
		http.Error(w, ipListKindName(isBlack)+op+"失败", http.StatusInternalServerError)
	}
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
