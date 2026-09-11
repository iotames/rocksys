// traffic.go：流量统计读侧端点（TRAFFIC_ANALYSIS D1/D6/D10）。
//
// 端点（cmd/rocksys 装配时经 adminapi.RegisterPlugin 注入）：
//   - GET /admin/obs/traffic/summary?from=&to=           指标标量 + 率 + computed_at/cache_ttl
//   - GET /admin/obs/traffic/series?from=&to=&bucket=    访问/拦截时间桶趋势（hour|day，缺省自适应）
//   - GET /admin/obs/traffic/geo?from=&to=&source=       地区分布（access|blocked）
//     level=country（缺省）→ 按国家聚合，输出 {country,cnt}（ISO 码）；
//     level=province → 只统计 country='CN' 按省聚合（city 列「省/市」前缀），输出 {region,cnt}。
//
// 指标口径（闭合定义见 PLAN §3.4，验收"数字互洽"以此为准）：
//
//	req_ok=放行总数（含静态资源、含放行后 4xx/5xx）；请求次数=req_ok+block_total；
//	req_pv=req_ok 去静态资源（后缀清单硬编码于 SQL）；uv=COUNT(DISTINCT client_ip,user_agent)。
//
// 时间口径：from/to 由前端换算为 UTC 传入，统一 time >= from AND time <= to（保证各桶之和=总数可对账）；
// 桶标签以 UTC 返回、前端原样展示并标注 UTC。
package obs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iotames/easyserver/log"

	"rocksys/internal/db"
)

// 管理端点路径常量（main.go 装配引用）。
const (
	PathTrafficSummary = "/admin/obs/traffic/summary"
	PathTrafficSeries  = "/admin/obs/traffic/series"
	PathTrafficGeo     = "/admin/obs/traffic/geo"

	// geoTopLimit 地区分布返回条数（国家数有限，写死即可）。
	geoTopLimit = 10

	// PathTrafficCacheClear 清空流量统计结果缓存（POST；WebUI 流量统计卡「清空缓存」按钮）。
	PathTrafficCacheClear = "/admin/obs/traffic/cache_clear"
)

// trafficShieldTable / trafficBlockAvailable（包级常量/状态，不进热路径）：
// 表名固定 shield_event（表名不开放配置——配置面只会增加测试与同步负担，无业务收益）；
// SHIELD_EVENT_LOG_ENABLED=false 时拦截侧字段输出 null（前端显示"—"）。
const trafficShieldTable = db.TableShieldEvent

var trafficBlockAvailable = true

// SetBlockAvailable 注入拦截侧可用性（SHIELD_EVENT_LOG_ENABLED 实值；false=统计输出 null）。
func SetBlockAvailable(ok bool) { trafficBlockAvailable = ok }

// trafficScript 读统计脚本并替换 {table}(access_log)/{table2}(shield_event) 占位符。
func (o *Obs) trafficScript(name string) (string, error) {
	txt, err := o.dataDB.SQL(name)
	if err != nil {
		return "", fmt.Errorf("obs: 读取统计脚本 %s 失败: %w", name, err)
	}
	txt = strings.ReplaceAll(txt, "{table}", accessLogTable)
	return strings.ReplaceAll(txt, "{table2}", trafficShieldTable), nil
}

// trafficQueryCtx 带超时的统计查询：经 QueryContext 下推取消到底层驱动，
// 超时后数据库侧终止执行，慢 SQL 不再持续占用连接与算力（防过度占用系统资源）。
// 超时阈值为代码常量 trafficQueryTimeoutConst（25 秒，非配置项）。
//
// 上下文与调用方解耦：结果缓存（tcache）会把同 key 并发请求合并为**一次**共享计算，
// 若直接绑定某个调用方的请求上下文，该请求断开就会让所有搭车请求一起失败。
// 故先用 WithoutCancel 剥离取消信号（仅保留值），再叠加固定预算——查询寿命只由预算决定，
// 客户端断开只结束它自己的等待，共享计算的产物照常入缓存供后续命中。
func (o *Obs) trafficQueryCtx(ctx context.Context, name string, args ...any) ([]map[string]any, error) {
	sel, err := o.trafficScript(name)
	if err != nil {
		return nil, err
	}
	if o.trafficQueryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), o.trafficQueryTimeout)
		defer cancel()
	}
	rows, err := o.dataDB.EasyDB().GetSqlDB().QueryContext(ctx, sel, args...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("obs: 统计查询 %s 超时（%s），可缩小时间范围或稍后重试: %w", name, o.trafficQueryTimeout, err)
		}
		return nil, fmt.Errorf("obs: 统计查询 %s 失败: %w", name, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("obs: 统计查询 %s 取列失败: %w", name, err)
	}
	out := make([]map[string]any, 0, 16)
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("obs: 统计查询 %s 扫描行失败: %w", name, err)
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok { // 驱动以 []byte 返回的文本统一转 string（与 easydb 扫描口径一致）
				v = string(b)
			}
			row[c] = v
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// roundTrafficRange 缓存友好取整：跨度 ≤48h 两端对齐到整小时，更大对齐到 UTC 整天。
// 结果范围是查询入参的一部分（取整后参与 SQL），缓存 key 随之稳定：
// 「近 24 小时」一小时内重复查询命中同一 key，「近 7 天/30 天」一天内命中，缓存命中率不再随分钟漂移归零。
// 代价是统计范围末端最多回退一个取整粒度（当前不完整的小时/天不计入），属可接受的口径换稳。
// 前提：取整不得把区间压成零宽——跨度不足一个粒度（如「今日」在 00:00–00:59 内、同小时自定义范围）
// 时两端会截到同一整点，此时保持原区间不取整，避免查询命中不到任何行。
func roundTrafficRange(from, to time.Time) (time.Time, time.Time) {
	span := to.Sub(from)
	granularity := 24 * time.Hour
	if span <= 48*time.Hour {
		granularity = time.Hour
	}
	if span < granularity {
		return from, to
	}
	f, t := from.Truncate(granularity), to.Truncate(granularity)
	if !t.After(f) { // 兜底：任何情况下都不产出零宽区间
		return from, to
	}
	return f, t
}

// parseTrafficRange 解析 from/to（复用 logs 的时间解析，转 UTC 口径）。
func parseTrafficRange(q interface{ Get(string) string }, now time.Time) (time.Time, time.Time, error) {
	from, to, err := parseTimeRange(q.Get("from"), q.Get("to"), now)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return from.UTC(), to.UTC(), nil
}

// trafficTTL 当前缓存 TTL（秒；0=禁用）。
func (o *Obs) trafficTTL() time.Duration {
	return time.Duration(o.trafficCacheTTL) * time.Second
}

// Summary GET /admin/obs/traffic/summary?from=&to=。
// 返回指标标量与率（率在 Go 侧计算，分母 0 防护输出 0）+ computed_at/cache_ttl。
func (h *AdminHandler) TrafficSummary(w http.ResponseWriter, r *http.Request) {
	o := h.obsReady(w)
	if o == nil {
		return
	}
	if o.dataDB == nil {
		http.Error(w, "obs 数据访问层未就绪（DB_DRIVER/DB_DSN）", http.StatusServiceUnavailable)
		return
	}
	from, to, err := parseTrafficRange(r.URL.Query(), time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	from, to = roundTrafficRange(from, to) // 取整参与查询与缓存 key：一小时/一天内重复查询直接命中缓存
	ttl := o.trafficTTL()
	key := fmt.Sprintf("summary|%d|%d", from.UnixMilli(), to.UnixMilli())
	data, cached, err := o.tcache.do(key, ttl, func() (any, error) {
		// 4 参（from,to ×2：access_log 与 shield_event 各一对），对应 sql/*/traffic_summary.sql
		// CTE 单次扫描版（原 13 子查询 26 参已收敛，口径不变）。
		rows, err := o.trafficQueryCtx(r.Context(), "traffic_summary.sql", from, to, from, to)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, fmt.Errorf("obs: 统计脚本 traffic_summary.sql 未返回行")
		}
		row := rows[0]
		n := func(k string) float64 { return trafficAsF(row[k]) }
		reqOK, blockTotal := n("req_ok"), n("block_total")
		total := reqOK + blockTotal
		return map[string]any{
			"req_ok":        int64(reqOK),
			"req_pv":        int64(n("req_pv")),
			"uv":            int64(n("uv")),
			"ip_all":        int64(n("ip_all")),
			"block_total":   trafficBlockField(int64(blockTotal)),
			"attack_ips":    trafficBlockField(int64(n("attack_ips"))),
			"err4xx":        int64(n("err4xx")),
			"err5xx":        int64(n("err5xx")),
			"block4xx":      trafficBlockField(int64(n("block4xx"))),
			"err4xx_rate":   safeRate(n("err4xx"), total),
			"err5xx_rate":   safeRate(n("err5xx"), total),
			"block4xx_rate": safeRate(n("block4xx"), blockTotal),
			"lat_avg":       trafficNumField(row["lat_avg"]),
			"lat_p50":       trafficNumField(row["lat_p50"]),
			"lat_p95":       trafficNumField(row["lat_p95"]),
			"lat_p99":       trafficNumField(row["lat_p99"]),
			"computed_at":   time.Now().UTC().Format(time.RFC3339),
			"from":          from.Format(time.RFC3339),
			"to":            to.Format(time.RFC3339),
			"cache_ttl_sec": int(ttl.Seconds()),
		}, nil
	})
	if err != nil {
		log.Error("obs: traffic summary 查询失败", "err", err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 缓存命中时保持首次 computed_at（在缓存值内），cache_hit 供前端/验收判断。
	// 用浅拷贝承载 cache_hit：缓存条目在多请求间共享，直接写会污染缓存值并在并发请求下
	// 触发「并发写 map」（缓存契约是调用方只读）。
	cached0 := data.(map[string]any)
	m := make(map[string]any, len(cached0)+1)
	for k, v := range cached0 {
		m[k] = v
	}
	m["cache_hit"] = cached
	writeJSON(w, m)
}

// Series GET /admin/obs/traffic/series?from=&to=&bucket=hour|day。
// 桶宽缺省自适应（跨度 ≤48h 用 hour）；两表 UNION ALL 各桶输出 ok_count/blocked_count。
func (h *AdminHandler) TrafficSeries(w http.ResponseWriter, r *http.Request) {
	o := h.obsReady(w)
	if o == nil {
		return
	}
	if o.dataDB == nil {
		http.Error(w, "obs 数据访问层未就绪（DB_DRIVER/DB_DSN）", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	from, to, err := parseTrafficRange(q, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	from, to = roundTrafficRange(from, to)
	bucket := q.Get("bucket")
	if bucket == "" { // 自适应：跨度 ≤48h 用小时桶
		if to.Sub(from) <= 48*time.Hour {
			bucket = "hour"
		} else {
			bucket = "day"
		}
	}
	if bucket != "hour" && bucket != "day" {
		http.Error(w, "bucket 参数非法（可选 hour/day）", http.StatusBadRequest)
		return
	}
	ttl := o.trafficTTL()
	key := fmt.Sprintf("series|%d|%d|%s", from.UnixMilli(), to.UnixMilli(), bucket)
	data, cached, err := o.tcache.do(key, ttl, func() (any, error) {
		script := "traffic_series_hour.sql"
		if bucket == "day" {
			script = "traffic_series_day.sql"
		}
		rows, err := o.trafficQueryCtx(r.Context(), script, from, to, from, to)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, map[string]any{
				"bucket":        row["bucket"],
				"ok_count":      int64(trafficAsF(row["ok_count"])),
				"blocked_count": trafficBlockField(int64(trafficAsF(row["blocked_count"]))),
			})
		}
		return out, nil
	})
	if err != nil {
		log.Error("obs: traffic series 查询失败", "err", err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"bucket":        bucket,
		"series":        data,
		"cache_hit":     cached,
		"cache_ttl_sec": int(ttl.Seconds()),
	})
}

// Geo GET /admin/obs/traffic/geo?from=&to=&source=access|blocked&level=country|province。
// level=country（缺省）：按 country GROUP BY 计数倒序，空串计「未知」参与排序（读侧映射，不悄悄丢量）。
// level=province：只统计 country='CN'，按 city 前缀（首个 "/" 前的省名）聚合（详见 traffic_geo_province_top.sql 头注释）；
// 展示兜底链"市空退省/省空退国"只做在读侧显示（region 空串显示「中国」），库列保持真实语义。
func (h *AdminHandler) TrafficGeo(w http.ResponseWriter, r *http.Request) {
	o := h.obsReady(w)
	if o == nil {
		return
	}
	if o.dataDB == nil {
		http.Error(w, "obs 数据访问层未就绪（DB_DRIVER/DB_DSN）", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	from, to, err := parseTrafficRange(q, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	from, to = roundTrafficRange(from, to)
	source := q.Get("source")
	if source == "" {
		source = "access"
	}
	if source != "access" && source != "blocked" {
		http.Error(w, "source 参数非法（可选 access/blocked）", http.StatusBadRequest)
		return
	}
	level := q.Get("level")
	if level == "" {
		level = "country"
	}
	if level != "country" && level != "province" {
		http.Error(w, "level 参数非法（可选 country/province）", http.StatusBadRequest)
		return
	}
	script := "traffic_geo_top.sql"
	if level == "province" {
		script = "traffic_geo_province_top.sql"
	}
	ttl := o.trafficTTL()
	key := fmt.Sprintf("geo|%d|%d|%s|%s", from.UnixMilli(), to.UnixMilli(), source, level)
	data, cached, err := o.tcache.do(key, ttl, func() (any, error) {
		// 占位符方言差异：PG 占位符可复用传 5 参；sqlite/mysql 的 ? 不可复用（UNION 两分支各带 from/to/source），传 7 参
		args := []any{from, to, source, from, to, source, geoTopLimit}
		if o.dataDB.Driver() == "postgres" {
			args = []any{from, to, source, source, geoTopLimit}
		}
		rows, err := o.trafficQueryCtx(r.Context(), script, args...)
		if err != nil {
			return nil, err
		}
		nameKey := "country"
		if level == "province" {
			nameKey = "region"
		}
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			region, _ := row[nameKey].(string)
			// 展示兜底链（只做在最终显示，库列保持真实语义）：
			//   市→省：city 列存「省/市」，市缺失时列值即「省」，无需映射；
			//   省→国：province 级查询已限定 country='CN'，region 空串展示为「中国」；
			//   country 级空串无父级可退，计「未知」参与排序（不悄悄丢量）。
			if region == "" {
				if level == "province" {
					region = "中国"
				} else {
					region = "未知"
				}
			}
			out = append(out, map[string]any{
				nameKey: region,
				"cnt":   int64(trafficAsF(row["cnt"])),
			})
		}
		return out, nil
	})
	if err != nil {
		log.Error("obs: traffic geo 查询失败", "err", err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"source":    source,
		"level":     level,
		"geo":       data,
		"cache_hit": cached,
		// geo 数据就绪信号（D17 引导卡判定）：未装配 mmdb 或加载失败时为 false，
		// 前端据此显示常驻警告引导卡（缺哪个文件、去哪下载、重启生效）。
		"geo_ready": o.geo != nil && o.geo.Ready(),
	})
}

// ClearCache POST /admin/obs/traffic/cache_clear：清空流量统计结果缓存（只清缓存不改数据）。
// 场景：刚写入的日志想立即见统计、或大表聚合中途想放弃旧结果强制重算。
func (h *AdminHandler) ClearCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "清空缓存仅接受 POST", http.StatusMethodNotAllowed)
		return
	}
	o := h.obsReady(w)
	if o == nil {
		return
	}
	o.tcache.purge()
	writeJSON(w, map[string]any{"ok": true, "text": "流量统计缓存已清空，下次查询将重新聚合"})
}

// obsReady 取 obs 实例（未注册或未启用输出 503 引导态并返回 nil；豁免 toast 红线②，
// 前端按行内引导卡渲染「功能未开启」）。
func (h *AdminHandler) obsReady(w http.ResponseWriter) *Obs {
	if h.obs == nil {
		http.Error(w, "obs 未注册", http.StatusServiceUnavailable)
		return nil
	}
	if !h.obs.enabled {
		http.Error(w, "obs 未启用（OBS_ENABLED=false），流量统计不可用；可在「插件」页开启后重试", http.StatusServiceUnavailable)
		return nil
	}
	return h.obs
}

// trafficBlockField 拦截侧字段：SHIELD_EVENT_LOG_ENABLED=false 时输出 null（前端显示"—"）。
func trafficBlockField(v int64) any {
	if !trafficBlockAvailable {
		return nil
	}
	return v
}

// safeRate 率计算，分母 0 防护输出 0。
func safeRate(num, den float64) float64 {
	if den <= 0 {
		return 0
	}
	return num / den
}

// trafficAsF 平铺行数值取数（方言返回类型不一，统一 float64 再取整）。
func trafficAsF(v any) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n)
	case float64:
		return n
	case int:
		return float64(n)
	case []byte:
		f, _ := strconv.ParseFloat(string(n), 64)
		return f
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
}

// trafficNumField 可空数值字段：NULL（范围内无行）透传 nil，数值归一 int64（ms）。
func trafficNumField(v any) any {
	if v == nil {
		return nil
	}
	return int64(trafficAsF(v))
}

// writeJSON 统一 JSON 输出。
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
