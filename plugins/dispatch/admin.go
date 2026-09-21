// dispatch 插件管理端点（数据字典见 docs/DATA_DICT.md）。
//
// 端点由 cmd/rocksys 装配时经 adminapi.RegisterPlugin 注入（仿 shield admin 模式），
// 全部路径前缀 /admin/dispatch/（副作用一律 POST，GET 无副作用，防本机恶意页面触发）：
//
//	GET/POST /admin/dispatch/rules            规则列表（分页+筛选，X-Total-Count）/ 新增
//	POST     /admin/dispatch/rules/update     规则整行更新（含启停、标签整组替换）
//	POST     /admin/dispatch/rules/delete     规则软删
//	POST     /admin/dispatch/rules/restore    规则恢复（校验均衡器引用仍存在，悬空拒绝）
//	POST     /admin/dispatch/rules/match-test 命中测试（只读无副作用：不动游标不计在途）
//	GET      /admin/dispatch/rules/meta       枚举字典（路径类型/策略/优先级/序号建议）
//	POST     /admin/dispatch/reload           手动重载全局四表快照（Rebuild，多实例出口）
//	GET/POST /admin/dispatch/upstreams        均衡器列表（含节点关系、被引用数）/ 新增（含关系组）
//	POST     /admin/dispatch/upstreams/update · /delete（409 引用保护）· /restore（查重 name）
//	GET/POST /admin/dispatch/nodes            节点列表（含被引用数、实时健康）/ 新增
//	POST     /admin/dispatch/nodes/update     · /delete（409 引用保护）· /restore（查重 url）
//	GET/POST /admin/dispatch/tags             标签全量列表 / 新建（name 归一小写、活跃唯一）
//	POST     /admin/dispatch/tags/update · /delete（同步软删关系行；不触发 Rebuild，无 restore）
//	GET      /admin/dispatch/health           节点实时健康快照 + 在途计数（读内存 registry）
//
// 热更口径：路由四表（规则/均衡器/节点/关系）任一写端点成功后自动触发
// Dispatch.Rebuild()（保存即热更）；Rebuild 失败保留旧快照并报错回前端。
// 标签两表仅影响管理展示，变更不触发 Rebuild。
package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rocksys/internal/db"

	"github.com/iotames/easyserver/log"
)

// 管理端点路径常量（main.go 装配引用）。
const (
	PathDispatchRules            = "/admin/dispatch/rules"
	PathDispatchRulesUpdate      = "/admin/dispatch/rules/update"
	PathDispatchRulesDelete      = "/admin/dispatch/rules/delete"
	PathDispatchRulesRestore     = "/admin/dispatch/rules/restore"
	PathDispatchMatchTest        = "/admin/dispatch/rules/match-test"
	PathDispatchRulesMeta        = "/admin/dispatch/rules/meta"
	PathDispatchReload           = "/admin/dispatch/reload"
	PathDispatchUpstreams        = "/admin/dispatch/upstreams"
	PathDispatchUpstreamsUpdate  = "/admin/dispatch/upstreams/update"
	PathDispatchUpstreamsDelete  = "/admin/dispatch/upstreams/delete"
	PathDispatchUpstreamsRestore = "/admin/dispatch/upstreams/restore"
	PathDispatchNodes            = "/admin/dispatch/nodes"
	PathDispatchNodesUpdate      = "/admin/dispatch/nodes/update"
	PathDispatchNodesDelete      = "/admin/dispatch/nodes/delete"
	PathDispatchNodesRestore     = "/admin/dispatch/nodes/restore"
	PathDispatchTags             = "/admin/dispatch/tags"
	PathDispatchTagsUpdate       = "/admin/dispatch/tags/update"
	PathDispatchTagsDelete       = "/admin/dispatch/tags/delete"
	PathDispatchHealth           = "/admin/dispatch/health"
)

// AdminHandler dispatch 管理端点 handler（d 为主件实例：Rebuild/快照/registry 消费；
// data 为统一数据访问层：nil = DB 未配置，端点降级 503）。
type AdminHandler struct {
	d    *Dispatch
	data *db.DB
}

// NewAdminHandler 构造管理端点 handler（d/data 均可为 nil，端点返回 503 降级）。
func NewAdminHandler(d *Dispatch, data *db.DB) *AdminHandler {
	return &AdminHandler{d: d, data: data}
}

// ── 通用响应与请求工具 ────────────────────────────────────────────────

// writeJSONErr 统一 JSON 错误响应：{"ok":false,"error":msg}（文案三要素由调用方组织）。
func writeJSONErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}

// writeJSONOK 统一 JSON 成功响应（附加 ok:true）。
func writeJSONOK(w http.ResponseWriter, v map[string]any) {
	if v == nil {
		v = map[string]any{}
	}
	v["ok"] = true
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// decodeBody 解析 JSON 请求体（空体合法：字段缺省走默认值校验）。
func decodeBody(r *http.Request, v any) {
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(v)
	}
}

// postOnly 仅 POST 副作用端点包装（副作用一律 POST，防本机恶意页面无凭证触发）。
func postOnly(fn func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONErr(w, http.StatusMethodNotAllowed, "仅支持 POST（副作用端点禁止 GET 触发）")
			return
		}
		fn(w, r)
	}
}

// ready 检查依赖可用性：dispatch 主件与数据访问层就绪才可服务；未就绪 503 降级。
func (h *AdminHandler) ready(w http.ResponseWriter) bool {
	if h.d == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "路由分发组件未注册（DISPATCH_ENABLED 未装配或组件被移除）。请检查装配配置后重启服务")
		return false
	}
	if h.data == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "路由分发管理不可用（数据库未配置）。请先在「系统管理 → 数据库」完成数据库配置后重试")
		return false
	}
	return true
}

// rowInt64 归一化任意值为 int64（sqlite/mysql/pg 驱动数值扫描类型不一）。
func rowInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case []byte:
		if i, err := strconv.ParseInt(strings.TrimSpace(string(n)), 10, 64); err == nil {
			return i
		}
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i
		}
	}
	return 0
}

// rowString 归一化任意值为字符串（时间列等）。
func rowString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	case time.Time:
		return s.UTC().Format(time.RFC3339)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", s)
	}
}

// normalizeRow 列表行归一：整数列 → int64、时间列 → RFC3339 字符串（仿 shield）。
func normalizeRow(row map[string]any, intCols []string) {
	for _, k := range intCols {
		if v, ok := row[k]; ok {
			row[k] = rowInt64(v)
		}
	}
	for _, k := range []string{"created_at", "updated_at", "deleted_at"} {
		if v, ok := row[k]; ok {
			if v == nil {
				row[k] = ""
			} else {
				row[k] = rowString(v)
			}
		}
	}
}

// ── DB 存取层（消费 sql/ 建表与查询脚本组；{table} 占位符运行时替换） ─────────

const (
	tblRule      = "dispatch_rule"
	tblUpstream  = "dispatch_upstream"
	tblNode      = "dispatch_node"
	tblRel       = "dispatch_upstream_node"
	tblTag       = "dispatch_tag"
	tblRuleTag   = "dispatch_rule_tag"
	defaultLimit = 500
	maxLimit     = 10000
)

// sqlText 读脚本并替换 {table} 表名占位符（表名为编译期常量，非用户输入）；
// order 非空时同步替换 {order} 排序占位符（调用方经白名单映射注入，杜绝注入面）。
// 脚本名缺省补 .sql 后缀（脚本源按 <表>_<动作>.sql 组织）。
func (h *AdminHandler) sqlText(name string, order ...string) (string, error) {
	if !strings.HasSuffix(name, ".sql") {
		name += ".sql"
	}
	txt, err := h.data.SQL(name)
	if err != nil {
		return "", fmt.Errorf("dispatch: 读取 SQL 脚本 %s 失败: %w", name, err)
	}
	table := tblRule
	switch {
	case strings.HasPrefix(name, "dispatch_upstream_node"):
		table = tblRel
	case strings.HasPrefix(name, "dispatch_upstream"):
		table = tblUpstream
	case strings.HasPrefix(name, "dispatch_node"):
		table = tblNode
	case strings.HasPrefix(name, "dispatch_rule_tag"):
		table = tblRuleTag
	case strings.HasPrefix(name, "dispatch_tag"):
		table = tblTag
	}
	txt = strings.ReplaceAll(txt, "{table}", table)
	if len(order) > 0 {
		txt = strings.ReplaceAll(txt, "{order}", order[0])
	}
	return txt, nil
}

// execScript 执行写脚本（{table} 已替换；参数化防注入）。
func (h *AdminHandler) execScript(name string, args ...any) error {
	txt, err := h.sqlText(name)
	if err != nil {
		return err
	}
	if _, err := h.data.EasyDB().Exec(txt, args...); err != nil {
		return fmt.Errorf("dispatch: 执行 %s 失败: %w", name, err)
	}
	return nil
}

// insertReturning 插入并取回自增 id：PG（lib/pq 不支持 LastInsertId）走
// insert_returning_id 脚本 RETURNING 分支；其余方言 Exec + LastInsertId（shield 同款）。
func (h *AdminHandler) insertReturning(table string, args ...any) (int64, error) {
	name := table + "_insert_returning_id.sql"
	txt, err := h.sqlText(name)
	if err != nil {
		return 0, err
	}
	if h.data.Driver() == "postgres" {
		var id int64
		if err := h.data.EasyDB().QueryRow(txt, args...).Scan(&id); err != nil {
			return 0, fmt.Errorf("dispatch: 插入 %s 失败（RETURNING id）: %w", table, err)
		}
		return id, nil
	}
	res, err := h.data.EasyDB().Exec(txt, args...)
	if err != nil {
		return 0, fmt.Errorf("dispatch: 插入 %s 失败: %w", table, err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// queryScript 执行查询脚本到 map 行切片（{table} 已替换；order 非空替换 {order}）。
func (h *AdminHandler) queryScript(name string, args []any, order ...string) ([]map[string]any, error) {
	txt, err := h.sqlText(name, order...)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := h.data.EasyDB().GetMany(txt, &rows, args...); err != nil {
		return nil, fmt.Errorf("dispatch: 查询 %s 失败: %w", name, err)
	}
	return rows, nil
}

// queryCount 执行可移植的标量计数查询（存在性/引用计数等应用层校验专用；
// 跨方言纯 SELECT，参数化防注入）。
func (h *AdminHandler) queryCount(query string, args ...any) (int64, error) {
	var rows []map[string]any
	if err := h.data.EasyDB().GetMany(query, &rows, args...); err != nil {
		return 0, fmt.Errorf("dispatch: 校验查询失败: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rowInt64(rows[0]["n"]), nil
}

// ── 列表分页与排序白名单 ──────────────────────────────────────────────

// listPage 解析通用分页参数（limit 缺省 500 上限 10000；offset 非负）。
func listPage(r *http.Request) (limit, offset int, ok bool) {
	limit, offset = defaultLimit, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > maxLimit {
			return 0, 0, false
		}
		limit = n
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}

// orderOf 排序白名单映射：{order} 占位符经白名单注入（非用户输入直拼，杜绝注入面）；
// 非法/缺省回默认表达式（脚本注释声明的缺省排序）。
func orderOf(whitelist map[string]string, sort, def string) string {
	if expr, ok := whitelist[sort]; ok {
		return expr
	}
	return def
}

var (
	ruleOrderMap = map[string]string{
		"match_order": "match_order ASC, id ASC", "-match_order": "match_order DESC, id DESC",
		"id": "id ASC", "-id": "id DESC",
		"updated_at": "updated_at ASC, id ASC", "-updated_at": "updated_at DESC, id DESC",
	}
	idOrderMap = map[string]string{
		"id": "id ASC", "-id": "id DESC",
		"name": "name ASC", "-name": "name DESC",
		"updated_at": "updated_at ASC, id ASC", "-updated_at": "updated_at DESC, id DESC",
	}
)

// boolToInt 启用开关 → 1/0（缺省启用）。
// 注意仅用于 enabled 类字段；sticky_enabled 缺省应为停用，走 stickyToInt。
func boolToInt(b *bool) int {
	if b != nil && !*b {
		return 0
	}
	return 1
}

// ── 规则端点 ─────────────────────────────────────────────────────────

// ruleBody 规则新增/更新请求体（Tags 为标签名集合：先 upsert 标签实体再整组替换关系行）。
type ruleBody struct {
	ID         int64    `json:"id"`
	MatchOrder int      `json:"match_order"`
	Domain     string   `json:"domain"`
	PathType   int      `json:"path_type"`
	PathValue  string   `json:"path_value"`
	Title      string   `json:"title"`
	UpstreamID int64    `json:"upstream_id"`
	Enabled    *bool    `json:"enabled"`
	Remark     string   `json:"remark"`
	Tags       []string `json:"tags"`
}

// Rules GET 列表 / POST 新增共用端点。
func (h *AdminHandler) Rules(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listRules(w, r)
	case http.MethodPost:
		h.addRule(w, r)
	default:
		writeJSONErr(w, http.StatusMethodNotAllowed, "仅支持 GET（列表）/ POST（新增）")
	}
}

// listRules 规则列表：关键词（domain/path_value/title 模糊）+ path_type + enabled + include_deleted 筛选，分页。
func (h *AdminHandler) listRules(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := listPage(r)
	if !ok {
		writeJSONErr(w, http.StatusBadRequest, "分页参数非法：limit 应为 1-10000 的整数、offset 应为非负整数。请修正查询参数后重试")
		return
	}
	q := r.URL.Query()
	keyword := q.Get("keyword")
	pathType := 0
	if v := q.Get("path_type"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 3 {
			writeJSONErr(w, http.StatusBadRequest, "path_type 参数非法：应为 0-3 的整数（0=不限；1=前缀 2=精确 3=模式）。请修正后重试")
			return
		}
		pathType = n
	}
	enabled := 0
	if v := q.Get("enabled"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 1 {
			writeJSONErr(w, http.StatusBadRequest, "enabled 参数非法：应为 0-1 的整数（0=不限；1=仅启用）。请修正后重试")
			return
		}
		enabled = n
	}
	incDel := 0
	if v := q.Get("include_deleted"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 1 {
			writeJSONErr(w, http.StatusBadRequest, "include_deleted 参数非法：应为 0-1 的整数（0=仅活跃行；1=仅已删除行）。请修正后重试")
			return
		}
		incDel = n
	}
	// 关键词占位符个数随方言：sqlite/mysql 脚本重复 ? 四次，PG 脚本复用 $2（仅需两个）。
	kwN := 4
	if h.data.Driver() == "postgres" {
		kwN = 2
	}
	// include_deleted 为首参（谓词位于 WHERE 首行）；关键词占位符个数随方言：
	// sqlite/mysql 脚本重复 ? 四次，PG 脚本复用 $2（仅需两个）。
	args := make([]any, 0, kwN+7)
	args = append(args, incDel)
	for i := 0; i < kwN; i++ {
		args = append(args, keyword)
	}
	args = append(args, pathType, pathType, enabled, enabled, limit, offset)
	rows, err := h.queryScript("dispatch_rule_query_list", args, orderOf(ruleOrderMap, q.Get("sort"), "match_order ASC, id ASC"))
	if err != nil {
		log.Error("dispatch: 规则列表查询失败", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "路由规则列表查询失败（数据库异常），请稍后重试；若持续出现请检查数据库状态或查看服务日志")
		return
	}
	cnt, err := h.queryScript("dispatch_rule_count", args[:len(args)-2])
	if err != nil {
		log.Error("dispatch: 规则列表计数失败", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "路由规则列表计数失败（数据库异常），请稍后重试")
		return
	}
	for _, row := range rows {
		normalizeRow(row, []string{"id", "match_order", "path_type", "upstream_id", "enabled"})
	}
	total := int64(0)
	if len(cnt) > 0 {
		total = rowInt64(cnt[0]["total"])
	}
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	writeJSONOK(w, map[string]any{"total": total, "rows": rows})
}

// addRule 新增规则：校验 → 落库 → 标签整组保存 → 触发 Rebuild。
func (h *AdminHandler) addRule(w http.ResponseWriter, r *http.Request) {
	var body ruleBody
	decodeBody(r, &body)
	warns, err := h.validateRule(&body, 0)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	id, err := h.insertReturning(tblRule, body.MatchOrder, body.Domain, body.PathType, body.PathValue,
		body.Title, body.UpstreamID, boolToInt(body.Enabled), body.Remark, now, now)
	if err != nil {
		log.Error("dispatch: 新增规则失败", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "路由规则新增失败（数据库写入异常），请稍后重试")
		return
	}
	// 标签随表单整体保存：先 upsert 标签实体，再整组替换关系行（标签变更不触发 Rebuild）。
	if err := h.saveRuleTags(id, body.Tags); err != nil {
		log.Error("dispatch: 规则标签保存失败", "rule_id", id, "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "规则已新增，但标签保存失败（数据库异常），请编辑该规则重新提交标签")
		return
	}
	h.afterWrite(w, map[string]any{"id": id, "warnings": warns})
}

// RulesUpdate 规则整行更新（含启停、标签整组替换）。
func (h *AdminHandler) RulesUpdate() http.HandlerFunc {
	return postOnly(h.updateRule)
}

func (h *AdminHandler) updateRule(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	var body ruleBody
	decodeBody(r, &body)
	if body.ID <= 0 {
		writeJSONErr(w, http.StatusBadRequest, "规则更新失败：id 必填且应为正整数。请从列表行操作入口重试")
		return
	}
	warns, err := h.validateRule(&body, body.ID)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err = h.execScript("dispatch_rule_update", body.MatchOrder, body.Domain, body.PathType, body.PathValue,
		body.Title, body.UpstreamID, boolToInt(body.Enabled), body.Remark, time.Now().UTC(), body.ID)
	if err != nil {
		log.Error("dispatch: 更新规则失败", "id", body.ID, "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "路由规则更新失败（数据库写入异常），请稍后重试")
		return
	}
	if err := h.saveRuleTags(body.ID, body.Tags); err != nil {
		log.Error("dispatch: 规则标签保存失败", "rule_id", body.ID, "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "规则已更新，但标签保存失败（数据库异常），请编辑该规则重新提交标签")
		return
	}
	h.afterWrite(w, map[string]any{"warnings": warns})
}

// RulesDelete 规则软删（保留配置资产，可恢复）。
func (h *AdminHandler) RulesDelete() http.HandlerFunc {
	return postOnly(func(w http.ResponseWriter, r *http.Request) {
		if !h.ready(w) {
			return
		}
		id := bodyID(w, r)
		if id <= 0 {
			return
		}
		now := time.Now().UTC()
		if err := h.execScript("dispatch_rule_soft_delete", now, now, id); err != nil {
			log.Error("dispatch: 删除规则失败", "id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "路由规则删除失败（数据库写入异常），请稍后重试")
			return
		}
		h.afterWrite(w, nil)
	})
}

// RulesRestore 规则恢复：恢复前校验其均衡器引用仍存在（悬空拒绝——引用不存在的规则不可用，恢复即产生坏规则）。
func (h *AdminHandler) RulesRestore() http.HandlerFunc {
	return postOnly(func(w http.ResponseWriter, r *http.Request) {
		if !h.ready(w) {
			return
		}
		id := bodyID(w, r)
		if id <= 0 {
			return
		}
		upID, found, err := h.softDeletedRuleUpstream(id)
		if err != nil {
			log.Error("dispatch: 恢复规则前置查询失败", "id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "路由规则恢复失败（数据库异常），请稍后重试")
			return
		}
		if !found {
			writeJSONErr(w, http.StatusBadRequest, "路由规则恢复失败：该 id 不存在或不是已删除状态。请刷新列表确认后重试")
			return
		}
		exists, err := h.activeUpstreamExists(upID)
		if err != nil {
			log.Error("dispatch: 恢复规则前置查询失败", "id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "路由规则恢复失败（数据库异常），请稍后重试")
			return
		}
		if !exists {
			writeJSONErr(w, http.StatusBadRequest,
				fmt.Sprintf("路由规则恢复失败：该规则引用的均衡器（upstream_id=%d）已不存在（被删除或未恢复）。请先在「负载均衡器」视图恢复或重建该均衡器，再恢复本规则", upID))
			return
		}
		if err := h.execScript("dispatch_rule_restore", time.Now().UTC(), id); err != nil {
			log.Error("dispatch: 恢复规则失败", "id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "路由规则恢复失败（数据库写入异常），请稍后重试")
			return
		}
		h.afterWrite(w, nil)
	})
}

// afterWrite 路由四表写端点成功后的统一收尾：触发 Rebuild（保存即热更）。
// Rebuild 失败说明新数据非法（构建失败保留旧快照），须报错回前端。
func (h *AdminHandler) afterWrite(w http.ResponseWriter, resp map[string]any) {
	if err := h.d.Rebuild(); err != nil {
		log.Error("dispatch: 写入后重建快照失败（保留旧快照）", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError,
			"已保存到数据库，但路由快照重建失败："+err.Error()+"。旧快照仍在生效（不影响现有转发）；请核对刚保存的数据（常见原因：均衡器没有任何节点关系、引用了不存在的均衡器），修正后再次保存或到页面点击「重载」")
		return
	}
	writeJSONOK(w, resp)
}

// bodyID 解析 {id} 请求体（正整数；非法直接回写 400 并返回 0）。
func bodyID(w http.ResponseWriter, r *http.Request) int64 {
	var body struct {
		ID int64 `json:"id"`
	}
	decodeBody(r, &body)
	if body.ID <= 0 {
		writeJSONErr(w, http.StatusBadRequest, "操作失败：id 必填且应为正整数。请从列表行操作入口重试")
		return 0
	}
	return body.ID
}

// ── 规则校验 ─────────────────────────────────────────────────────────

// validateRule 规则输入校验（新增/更新共用；excludeID 为更新时排除自身）：
// match_order 1–999、path_type 枚举、path_value 以 / 开头且按类型合法、domain 归一落库
// 并拒绝端口与通配、upstream_id 引用存在（活跃行）；同序号非阻断提示（warnings）。
func (h *AdminHandler) validateRule(b *ruleBody, excludeID int64) (warnings []string, err error) {
	if b.MatchOrder < 1 || b.MatchOrder > 999 {
		return nil, errors.New("规则校验失败：match_order 应为 1-999 的整数（升序匹配、命中即停；域名默认兜底规则建议 999）。请修正序号后重试")
	}
	if !PathType(b.PathType).Valid() {
		return nil, errors.New("规则校验失败：path_type 应为 1/2/3（1=前缀、2=精确、3=模式）。请从下拉选择合法类型后重试")
	}
	b.PathValue = strings.TrimSpace(b.PathValue)
	if !strings.HasPrefix(b.PathValue, "/") {
		return nil, errors.New("规则校验失败：path_value 必须以 / 开头（如 /api、/api/order/:id）。请修正路径值后重试")
	}
	if strings.Contains(b.PathValue, "//") {
		return nil, errors.New("规则校验失败：path_value 含空路径段（//）。请修正路径值后重试")
	}
	if b.PathType == int(PathTypeMode) {
		for _, seg := range strings.Split(strings.Trim(b.PathValue, "/"), "/") {
			if seg == "" {
				return nil, errors.New("规则校验失败：模式路径含空段。请修正后重试")
			}
			if strings.HasPrefix(seg, ":") && len(seg) == 1 {
				return nil, errors.New("规则校验失败：模式参数段缺少参数名（如 :id）。请修正后重试")
			}
		}
	}
	// domain 保存时归一落库（转小写），并拒绝端口与通配（domain 走精确匹配语义，端口/通配无意义故不接受）。
	b.Domain = strings.ToLower(strings.TrimSpace(b.Domain))
	if b.Domain != "" {
		if strings.ContainsAny(b.Domain, ":/ \t") || strings.Contains(b.Domain, "*") {
			return nil, errors.New("规则校验失败：domain 仅支持精确域名（不带端口、不支持通配符）。端口维度无需填写（匹配时自动剥端口）；如需泛域名请拆为多条精确域名规则。请修正域名后重试")
		}
	}
	if b.UpstreamID <= 0 {
		return nil, errors.New("规则校验失败：upstream_id 必填（命中规则后转发到的负载均衡器）。请从均衡器下拉选择后重试")
	}
	exists, err := h.activeUpstreamExists(b.UpstreamID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("规则校验失败：引用的均衡器（upstream_id=%d）不存在或已删除。请刷新均衡器下拉后重新选择", b.UpstreamID)
	}
	// 同序号非阻断提示：列出同序号的其他活跃规则（先建者先匹配，防全局兜底静默吞掉域名专属规则）。
	rows, err := h.queryCountRows(
		"SELECT id, title, domain, path_value FROM "+tblRule+" WHERE match_order = "+h.sqlPh(1)+" AND id <> "+h.sqlPh(2)+" AND deleted_at IS NULL",
		b.MatchOrder, excludeID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		dupID := rowInt64(row["id"])
		if excludeID != 0 && dupID == excludeID {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("序号 %d 已被规则 #%d（%s %s）占用：同序号按创建先后匹配，先建者先命中。若本规则被其遮蔽，请调整序号",
			b.MatchOrder, dupID, rowString(row["domain"]), rowString(row["path_value"])))
	}
	return warnings, nil
}

// sqlPh 按驱动生成第 i 个占位符（sqlite/mysql 用 ?，postgres 用 $n——
// 与 sql/ 脚本源的同款方言约定一致；i 从 1 起）。
func (h *AdminHandler) sqlPh(i int) string {
	if h.data.Driver() == "postgres" {
		return "$" + strconv.Itoa(i)
	}
	return "?"
}

// sqlSet 可变参数的方言占位符序列（如 3 个参数 → ?,?,? / $1,$2,$3）。
func (h *AdminHandler) sqlSet(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = h.sqlPh(i + 1)
	}
	return strings.Join(parts, ", ")
}

// queryCountRows 可移植 SELECT 多行（应用层校验专用；跨方言纯 SELECT，参数化防注入）。
func (h *AdminHandler) queryCountRows(query string, args ...any) ([]map[string]any, error) {
	var rows []map[string]any
	if err := h.data.EasyDB().GetMany(query, &rows, args...); err != nil {
		return nil, fmt.Errorf("dispatch: 校验查询失败: %w", err)
	}
	return rows, nil
}

// activeUpstreamExists 均衡器活跃行（未软删，含停用）是否存在。
func (h *AdminHandler) activeUpstreamExists(id int64) (bool, error) {
	n, err := h.queryCount("SELECT COUNT(*) AS n FROM "+tblUpstream+" WHERE id = "+h.sqlPh(1)+" AND deleted_at IS NULL", id)
	return n > 0, err
}

// activeNodeExists 节点活跃行是否存在。
func (h *AdminHandler) activeNodeExists(id int64) (bool, error) {
	n, err := h.queryCount("SELECT COUNT(*) AS n FROM "+tblNode+" WHERE id = "+h.sqlPh(1)+" AND deleted_at IS NULL", id)
	return n > 0, err
}

// softDeletedRuleUpstream 取软删规则行引用的均衡器 id（恢复校验用）。
func (h *AdminHandler) softDeletedRuleUpstream(id int64) (upID int64, found bool, err error) {
	rows, err := h.queryCountRows(
		"SELECT upstream_id FROM "+tblRule+" WHERE id = "+h.sqlPh(1)+" AND deleted_at IS NOT NULL", id)
	if err != nil || len(rows) == 0 {
		return 0, false, err
	}
	return rowInt64(rows[0]["upstream_id"]), true, nil
}

// ── 标签随规则表单整组保存 ───────────────────────────────────────────

// saveRuleTags 标签整组保存：按名 upsert 标签实体（归一小写、活跃唯一），再整组替换
// 该规则的关系行（软删旧行 + 插入新行）。属标签链路变更，不触发 Rebuild。
func (h *AdminHandler) saveRuleTags(ruleID int64, names []string) error {
	// 整组替换第一步：软删该规则现有活跃关系行。
	rels, err := h.queryScript("dispatch_rule_tag_query_list", []any{ruleID, ruleID, 0, 0, maxLimit, 0}, "id ASC")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, rel := range rels {
		if err := h.execScript("dispatch_rule_tag_soft_delete", now, now, rowInt64(rel["id"])); err != nil {
			return err
		}
	}
	// 按名 upsert 标签实体（去重、归一），再插入新关系行。
	seen := make(map[string]bool, len(names))
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		tagID, err := h.upsertTag(name, now)
		if err != nil {
			return err
		}
		if _, err := h.insertReturning(tblRuleTag, ruleID, tagID, now, now); err != nil {
			return err
		}
	}
	return nil
}

// upsertTag 按名取活跃标签 id，不存在则新建（name 已归一小写）。
func (h *AdminHandler) upsertTag(name string, now time.Time) (int64, error) {
	rows, err := h.queryCountRows(
		"SELECT id FROM "+tblTag+" WHERE name = "+h.sqlPh(1)+" AND deleted_at IS NULL", name)
	if err != nil {
		return 0, err
	}
	if len(rows) > 0 {
		return rowInt64(rows[0]["id"]), nil
	}
	return h.insertReturning(tblTag, name, now, now)
}

// ── 规则 meta 枚举字典 ───────────────────────────────────────────────

// RulesMeta GET 枚举字典（路径类型/策略/优先级说明/默认序号建议），供表单下拉与说明文字。
func (h *AdminHandler) RulesMeta(w http.ResponseWriter, r *http.Request) {
	writeJSONOK(w, map[string]any{
		"path_types": []map[string]any{
			{"value": int(PathTypePrefix), "name": "前缀", "desc": "段对齐前缀匹配且命中自身：/api 命中 /api 与 /api/x，不匹配 /apix；/ 命中一切路径"},
			{"value": int(PathTypeExact), "name": "精确", "desc": "路径全等匹配，不做尾斜杠归一：/api 只命中 /api"},
			{"value": int(PathTypeMode), "name": "模式", "desc": "段匹配：:name 捕获单段参数（注入 X-Route-Param-* 请求头）、* 通配其后剩余路径"},
		},
		"algos": []map[string]any{
			{"value": int(AlgoRoundRobin), "name": "round_robin", "desc": "平滑加权轮询（权重默认 1 即纯轮询）"},
			{"value": int(AlgoLeastConn), "name": "least_conn", "desc": "在途请求最少的节点优先（平局回落轮询游标）"},
		},
		"priorities": []map[string]any{
			{"value": int(PriorityPrimary), "name": "高优", "desc": "参与常规负载均衡（默认）"},
			{"value": int(PriorityBackup), "name": "备份", "desc": "高优健康集全不健康时才启用（NGINX backup 同款语义）"},
		},
		"match_order": map[string]any{
			"min": 1, "max": 999, "step": 10,
			"suggestion": "从 10 起按 10 递增预留插入空间；域名默认兜底规则（domain 留空 + 路径 /）建议使用 999 殿后",
		},
		"domain": map[string]any{
			"rule": "留空 = 匹配任意域名；非空 = 精确匹配（保存时归一转小写，匹配时自动剥端口），不支持端口与通配符",
		},
	})
}

// ── match-test 命中测试（只读无副作用） ───────────────────────────────

// RulesMatchTest POST {host, path} → 命中结果（规则摘要/均衡器/所选节点或兜底链位置）。
// 只读实现：匹配走只读快照（Match 本身无副作用）；选点复用 pickHealthy 但**不**经
// count() 收口——轮询游标不推进、在途计数 +0（对照 Handle 的 AcquireNode 有副作用路径）。
// 所选节点为动态参考值（随健康态/在途/游标变化），不要求与实请求一致。
func (h *AdminHandler) RulesMatchTest() http.HandlerFunc {
	return postOnly(h.matchTest)
}

func (h *AdminHandler) matchTest(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	var body struct {
		Host string `json:"host"`
		Path string `json:"path"`
	}
	decodeBody(r, &body)
	if body.Path == "" {
		body.Path = "/"
	}
	snap := h.d.snapshot()
	rule, params := Match(snap, body.Host, body.Path)
	resp := map[string]any{"hit": false, "position": "default_upstream"}
	if rule == nil {
		resp["message"] = "未命中任何路由规则，请求走默认 upstream（Adapter 兜底；无默认 upstream 时直接放行）"
		writeJSONOK(w, resp)
		return
	}
	resp["hit"] = true
	resp["rule"] = map[string]any{
		"id": rule.ID, "match_order": rule.MatchOrder, "domain": rule.Domain,
		"path_type": int(rule.PathType), "path_value": rule.PathValue, "title": rule.Title,
	}
	if len(params) > 0 {
		resp["params"] = params
	}
	u := rule.Upstream
	resp["upstream"] = map[string]any{
		"id": u.ID, "name": u.Name, "algo": int(u.Algo),
		"sticky_enabled": u.StickyEnabled, "enabled": u.Enabled,
	}
	switch {
	case !u.Enabled:
		resp["position"] = "upstream_disabled"
		resp["message"] = "命中规则，但其引用的均衡器已停用（fail-closed）：实请求将返回 503 中断链"
	default:
		// 只读选点：直接调 pickHealthy（不经 count() 收口），游标不推进、在途不计数。
		n := pickHealthy(u, h.d.reg, PriorityPrimary)
		if n == nil {
			n = pickHealthy(u, h.d.reg, PriorityBackup)
		}
		if n == nil {
			resp["position"] = "no_healthy_node"
			resp["message"] = "命中规则，但均衡器无可用健康节点（高优与备份健康集均为空）：实请求将返回 503 中断链"
		} else {
			resp["position"] = "node"
			resp["node"] = map[string]any{"id": n.ID, "url": n.URL, "priority": int(n.Priority)}
			resp["message"] = "命中规则并选出节点（动态参考值，随健康态/在途/游标变化，不要求与实请求一致）"
		}
	}
	writeJSONOK(w, resp)
}

// ── reload 手动重载 ──────────────────────────────────────────────────

// Reload POST 手动重载全局四表快照（多实例/外部改库出口）。
func (h *AdminHandler) Reload() http.HandlerFunc {
	return postOnly(func(w http.ResponseWriter, r *http.Request) {
		if !h.ready(w) {
			return
		}
		if err := h.d.Rebuild(); err != nil {
			log.Error("dispatch: 手动重载失败", "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError,
				"路由快照重载失败："+err.Error()+"。旧快照仍在生效（不影响现有转发）；请先在路由分发页修正数据（常见原因：均衡器没有任何节点关系、引用了不存在的均衡器）后重试")
			return
		}
		writeJSONOK(w, map[string]any{"ready": h.d.Ready()})
	})
}

// ── 均衡器端点 ───────────────────────────────────────────────────────

// relBody 关系行（均衡器表单节点关系编辑器的一行）。
type relBody struct {
	NodeID   int64 `json:"node_id"`
	Weight   int   `json:"weight"`
	Priority int   `json:"priority"`
}

// upstreamBody 均衡器新增/更新请求体（Nodes 为节点关系组：整体替换语义）。
type upstreamBody struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Algo          int       `json:"algo"`
	StickyEnabled *bool     `json:"sticky_enabled"`
	StickyCookie  string    `json:"sticky_cookie"`
	Enabled       *bool     `json:"enabled"`
	Remark        string    `json:"remark"`
	Nodes         []relBody `json:"nodes"`
}

// Upstreams GET 列表 / POST 新增共用端点。
func (h *AdminHandler) Upstreams(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listUpstreams(w, r)
	case http.MethodPost:
		postOnly(h.addUpstream)(w, r)
	default:
		writeJSONErr(w, http.StatusMethodNotAllowed, "仅支持 GET（列表）/ POST（新增）")
	}
}

// listUpstreams 均衡器列表：名称模糊 + enabled + include_deleted 筛选；附节点关系（含节点名/URL/实时健康）
// 与被引用规则数。
func (h *AdminHandler) listUpstreams(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := listPage(r)
	if !ok {
		writeJSONErr(w, http.StatusBadRequest, "分页参数非法：limit 应为 1-10000 的整数、offset 应为非负整数。请修正查询参数后重试")
		return
	}
	q := r.URL.Query()
	keyword := q.Get("keyword")
	enabled := atoiDefault(q.Get("enabled"))
	if q.Get("enabled") != "" && (enabled < 0 || enabled > 1) {
		writeJSONErr(w, http.StatusBadRequest, "enabled 参数非法：应为 0-1 的整数（0=不限；1=仅启用）。请修正后重试")
		return
	}
	incDel := 0
	if v := q.Get("include_deleted"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 1 {
			writeJSONErr(w, http.StatusBadRequest, "include_deleted 参数非法：应为 0-1 的整数（0=仅活跃行；1=仅已删除行）。请修正后重试")
			return
		}
		incDel = n
	}
	// include_deleted 为首参（谓词位于 WHERE 首行）。
	args := []any{incDel, keyword, keyword, enabled, enabled, limit, offset}
	rows, err := h.queryScript("dispatch_upstream_query_list", args, orderOf(idOrderMap, q.Get("sort"), "id DESC"))
	if err != nil {
		log.Error("dispatch: 均衡器列表查询失败", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "负载均衡器列表查询失败（数据库异常），请稍后重试；若持续出现请检查数据库状态或查看服务日志")
		return
	}
	cnt, err := h.queryScript("dispatch_upstream_count", args[:5])
	if err != nil {
		log.Error("dispatch: 均衡器列表计数失败", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "负载均衡器列表计数失败（数据库异常），请稍后重试")
		return
	}
	total := int64(0)
	if len(cnt) > 0 {
		total = rowInt64(cnt[0]["total"])
	}
	// 实时健康三态读内存 registry（单一事实源，请求路径零 DB 查询）。
	for _, row := range rows {
		normalizeRow(row, []string{"id", "algo", "sticky_enabled", "enabled"})
		upID := rowInt64(row["id"])
		row["rule_refs"] = h.mustRuleRefs(upID)
		rels, err := h.queryScript("dispatch_upstream_node_query_list", []any{upID, upID, 0, 0, maxLimit, 0}, "id ASC")
		if err != nil {
			log.Error("dispatch: 关系查询失败", "upstream_id", upID, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "负载均衡器节点关系查询失败（数据库异常），请稍后重试")
			return
		}
		nodes := make([]map[string]any, 0, len(rels))
		for _, rel := range rels {
			normalizeRow(rel, []string{"id", "upstream_id", "node_id", "weight", "priority"})
			nodeID := rowInt64(rel["node_id"])
			rel["health"] = healthName(h.d.reg.Health(nodeID))
			rel["inflight"] = h.d.reg.Inflight(nodeID)
			nodes = append(nodes, rel)
		}
		row["nodes"] = nodes
	}
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	writeJSONOK(w, map[string]any{"total": total, "rows": rows})
}

// softDeleteRelationsAt 软删均衡器的全部活跃关系行（统一时间戳标记，
// 供 restoreRelationsAt 按 deleted_at 精确恢复本删除批次）。
func (h *AdminHandler) softDeleteRelationsAt(upID int64, now time.Time) error {
	rels, err := h.queryScript("dispatch_upstream_node_query_list", []any{upID, upID, 0, 0, maxLimit, 0}, "id ASC")
	if err != nil {
		return err
	}
	for _, rel := range rels {
		if err := h.execScript("dispatch_upstream_node_soft_delete", now, now, rowInt64(rel["id"])); err != nil {
			return err
		}
	}
	return nil
}

// restoreRelationsAt 恢复指定批次软删的关系行（deleted_at 与批次时间戳精确相等）。
// 关系表无 restore 脚本（整组替换语义），此处为均衡器恢复的级联回滚，可移植 SQL。
func (h *AdminHandler) restoreRelationsAt(upID int64, at, now time.Time) error {
	q := "UPDATE " + tblRel + " SET deleted_at = NULL, updated_at = " + h.sqlPh(1) +
		" WHERE upstream_id = " + h.sqlPh(2) + " AND deleted_at = " + h.sqlPh(3)
	_, err := h.data.EasyDB().Exec(q, now.UTC(), upID, at)
	if err != nil {
		return fmt.Errorf("dispatch: 恢复关系行失败: %w", err)
	}
	return nil
}

// mustRuleRefs 均衡器被未软删规则引用数（含停用规则——停用规则删除即失其配置，引用保护须一并计数；失败返回 -1 由前端显示未知）。
func (h *AdminHandler) mustRuleRefs(upID int64) int64 {
	n, err := h.queryCount("SELECT COUNT(*) AS n FROM "+tblRule+" WHERE upstream_id = "+h.sqlPh(1)+" AND deleted_at IS NULL", upID)
	if err != nil {
		return -1
	}
	return n
}

// mustRelRefs 节点被未软删关系引用数。
func (h *AdminHandler) mustRelRefs(nodeID int64) int64 {
	n, err := h.queryCount("SELECT COUNT(*) AS n FROM "+tblRel+" WHERE node_id = "+h.sqlPh(1)+" AND deleted_at IS NULL", nodeID)
	if err != nil {
		return -1
	}
	return n
}

// healthName 健康三态 → 展示名（绿/红/灰）。
func healthName(s HealthState) string {
	switch s {
	case HealthOK:
		return "ok"
	case HealthBad:
		return "bad"
	default:
		return "unknown"
	}
}

// atoiDefault 整数解析（非法回 0 = 不限）。
func atoiDefault(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// addUpstream 新增均衡器（含节点关系组）：校验 → 落库 → 关系组写入 → Rebuild。
func (h *AdminHandler) addUpstream(w http.ResponseWriter, r *http.Request) {
	var body upstreamBody
	decodeBody(r, &body)
	rels, err := h.validateUpstream(&body, 0)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	id, err := h.insertReturning(tblUpstream, body.Name, body.Algo,
		stickyToInt(body.StickyEnabled), body.StickyCookie, boolToInt(body.Enabled), body.Remark, now, now)
	if err != nil {
		log.Error("dispatch: 新增均衡器失败", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "负载均衡器新增失败（数据库写入异常），请稍后重试")
		return
	}
	if err := h.replaceRelations(id, rels, now); err != nil {
		log.Error("dispatch: 关系组写入失败", "upstream_id", id, "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "均衡器已新增，但节点关系保存失败（数据库异常），请编辑该均衡器重新提交节点关系")
		return
	}
	h.afterWrite(w, map[string]any{"id": id})
}

// UpstreamsUpdate 更新均衡器（含节点关系组整体替换）。
func (h *AdminHandler) UpstreamsUpdate() http.HandlerFunc {
	return postOnly(func(w http.ResponseWriter, r *http.Request) {
		if !h.ready(w) {
			return
		}
		var body upstreamBody
		decodeBody(r, &body)
		if body.ID <= 0 {
			writeJSONErr(w, http.StatusBadRequest, "均衡器更新失败：id 必填且应为正整数。请从列表行操作入口重试")
			return
		}
		rels, err := h.validateUpstream(&body, body.ID)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := h.execScript("dispatch_upstream_update", body.Name, body.Algo,
			stickyToInt(body.StickyEnabled), body.StickyCookie, boolToInt(body.Enabled), body.Remark,
			time.Now().UTC(), body.ID); err != nil {
			log.Error("dispatch: 更新均衡器失败", "id", body.ID, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "负载均衡器更新失败（数据库写入异常），请稍后重试")
			return
		}
		if err := h.replaceRelations(body.ID, rels, time.Now().UTC()); err != nil {
			log.Error("dispatch: 关系组替换失败", "upstream_id", body.ID, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "均衡器已更新，但节点关系保存失败（数据库异常），请编辑该均衡器重新提交节点关系")
			return
		}
		h.afterWrite(w, nil)
	})
}

// UpstreamsDelete 软删均衡器：存在未软删规则引用（含停用规则，引用保护）→ 409 拒绝。
func (h *AdminHandler) UpstreamsDelete() http.HandlerFunc {
	return postOnly(func(w http.ResponseWriter, r *http.Request) {
		if !h.ready(w) {
			return
		}
		id := bodyID(w, r)
		if id <= 0 {
			return
		}
		refs, err := h.mustRuleRefsErr(id)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "删除前置校验失败（数据库异常），请稍后重试")
			return
		}
		if refs > 0 {
			writeJSONErr(w, http.StatusConflict,
				fmt.Sprintf("删除被拒绝：该均衡器仍被 %d 条路由规则引用（含停用规则）。请先编辑或删除引用它的规则（规则列表可按均衡器筛选），再删除本均衡器", refs))
			return
		}
		now := time.Now().UTC()
		if err := h.execScript("dispatch_upstream_soft_delete", now, now, id); err != nil {
			log.Error("dispatch: 删除均衡器失败", "id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "负载均衡器删除失败（数据库写入异常），请稍后重试")
			return
		}
		// 级联软删其关系行：软删均衡器后关系行不再有效（Rebuild 校验会拒绝
		// 「关系引用不存在的均衡器」）；统一用同一 now 标记，restore 时按批次精确回滚。
		if err := h.softDeleteRelationsAt(id, now); err != nil {
			log.Error("dispatch: 级联软删关系行失败", "upstream_id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "均衡器已删除，但其节点关系清理失败（数据库异常），请到页面点击「重载」或联系运维核查")
			return
		}
		h.afterWrite(w, nil)
	})
}

// UpstreamsRestore 恢复均衡器：恢复前查重 name 活跃行唯一性（冲突拒绝，防 PG 部分唯一索引裸 DB 错）。
func (h *AdminHandler) UpstreamsRestore() http.HandlerFunc {
	return postOnly(func(w http.ResponseWriter, r *http.Request) {
		if !h.ready(w) {
			return
		}
		id := bodyID(w, r)
		if id <= 0 {
			return
		}
		name, found, err := h.softDeletedUpstreamName(id)
		if err != nil {
			log.Error("dispatch: 恢复均衡器前置查询失败", "id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "负载均衡器恢复失败（数据库异常），请稍后重试")
			return
		}
		if !found {
			writeJSONErr(w, http.StatusBadRequest, "负载均衡器恢复失败：该 id 不存在或不是已删除状态。请刷新列表确认后重试")
			return
		}
		taken, err := h.upstreamNameTaken(name, id)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "负载均衡器恢复失败（数据库异常），请稍后重试")
			return
		}
		if taken {
			writeJSONErr(w, http.StatusConflict,
				fmt.Sprintf("恢复被拒绝：已存在同名活跃均衡器「%s」（软删行不参与判重，但活跃行 name 必须唯一）。请先将同名活跃均衡器改名或删除，再恢复本条；或直接使用现有同名均衡器", name))
			return
		}
		// 取软删时间戳（级联恢复同批次关系行的匹配键），随后恢复均衡器行。
		delAt, err := h.softDeletedAt(tblUpstream, id)
		if err != nil {
			log.Error("dispatch: 恢复均衡器前置查询失败", "id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "负载均衡器恢复失败（数据库异常），请稍后重试")
			return
		}
		now := time.Now().UTC()
		if err := h.execScript("dispatch_upstream_restore", now, id); err != nil {
			log.Error("dispatch: 恢复均衡器失败", "id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "负载均衡器恢复失败（数据库写入异常），请稍后重试")
			return
		}
		// 级联恢复删除时一并软删的关系行（无关系行的均衡器会导致 Rebuild 校验失败）。
		if !delAt.IsZero() {
			if err := h.restoreRelationsAt(id, delAt, now); err != nil {
				log.Error("dispatch: 级联恢复关系行失败", "upstream_id", id, "err", err.Error())
				writeJSONErr(w, http.StatusInternalServerError, "均衡器已恢复，但其节点关系恢复失败（数据库异常），请编辑该均衡器重新提交节点关系")
				return
			}
		}
		h.afterWrite(w, nil)
	})
}

// ── 均衡器校验与关系组 ───────────────────────────────────────────────

// stickyToInt 会话保持开关 → 1/0（缺省停用，与建表默认值 sticky_enabled DEFAULT 0 同源）。
func stickyToInt(b *bool) int {
	if b != nil && *b {
		return 1
	}
	return 0
}

// validateUpstream 均衡器输入校验：name 非空且活跃行唯一、algo 枚举合法、
// sticky_cookie 缺省、关系组逐行校验（节点存在、weight 正整数、priority 枚举、节点不重复）。
func (h *AdminHandler) validateUpstream(b *upstreamBody, excludeID int64) ([]relBody, error) {
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" {
		return nil, errors.New("均衡器校验失败：name 必填（如「订单服务-会话保持池」）。请填写名称后重试")
	}
	taken, err := h.upstreamNameTaken(b.Name, excludeID)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, fmt.Errorf("均衡器校验失败：名称「%s」已存在（软删行不参与判重，活跃行 name 必须唯一）。请换一个名称，或到列表中恢复/复用同名已删条目", b.Name)
	}
	if !AlgoKind(b.Algo).Valid() {
		return nil, errors.New("均衡器校验失败：algo 应为 1/2（1=round_robin 平滑加权轮询、2=least_conn 最小连接优先）。请从下拉选择合法策略后重试")
	}
	b.StickyCookie = strings.TrimSpace(b.StickyCookie)
	if b.StickyCookie == "" {
		b.StickyCookie = DefaultStickyCookie // 缺省 Cookie 名（建表默认值同源）
	}
	// 关系组逐行校验 + 节点去重（同一均衡器内节点唯一）。
	seen := make(map[int64]bool, len(b.Nodes))
	for i, rel := range b.Nodes {
		if rel.NodeID <= 0 {
			return nil, fmt.Errorf("均衡器校验失败：第 %d 行节点关系缺少 node_id。请从节点下拉选择后重试", i+1)
		}
		if seen[rel.NodeID] {
			return nil, fmt.Errorf("均衡器校验失败：节点 #%d 在关系列表中重复。同一均衡器内每个节点只能出现一行，请合并为一行并调整权重", rel.NodeID)
		}
		seen[rel.NodeID] = true
		exists, err := h.activeNodeExists(rel.NodeID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("均衡器校验失败：第 %d 行引用的节点（node_id=%d）不存在或已删除。请刷新节点下拉后重新选择", i+1, rel.NodeID)
		}
		if rel.Weight <= 0 {
			b.Nodes[i].Weight = 1 // 权重缺省 1（纯轮询）
		}
		if !Priority(rel.Priority).Valid() {
			return nil, fmt.Errorf("均衡器校验失败：第 %d 行 priority 应为 0/1（0=高优、1=备份）。请修正后重试", i+1)
		}
	}
	return b.Nodes, nil
}

// upstreamNameTaken 均衡器 name 活跃行查重（excludeID：更新时排除自身）。
func (h *AdminHandler) upstreamNameTaken(name string, excludeID int64) (bool, error) {
	n, err := h.queryCount(
		"SELECT COUNT(*) AS n FROM "+tblUpstream+" WHERE name = "+h.sqlPh(1)+" AND id <> "+h.sqlPh(2)+" AND deleted_at IS NULL",
		name, excludeID)
	return n > 0, err
}

// softDeletedAt 取软删行的 deleted_at 原值（级联恢复批次匹配键；行不存在返回零值）。
func (h *AdminHandler) softDeletedAt(table string, id int64) (time.Time, error) {
	rows, err := h.queryCountRows("SELECT deleted_at FROM "+table+" WHERE id = "+h.sqlPh(1)+" AND deleted_at IS NOT NULL", id)
	if err != nil || len(rows) == 0 {
		return time.Time{}, err
	}
	switch t := rows[0]["deleted_at"].(type) {
	case time.Time:
		return t, nil
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05Z07:00"} {
			if v, err := time.Parse(layout, t); err == nil {
				return v, nil
			}
		}
	}
	return time.Time{}, nil
}

// softDeletedUpstreamName 取软删均衡器行 name（恢复查重用）。
func (h *AdminHandler) softDeletedUpstreamName(id int64) (string, bool, error) {
	rows, err := h.queryCountRows(
		"SELECT name FROM "+tblUpstream+" WHERE id = "+h.sqlPh(1)+" AND deleted_at IS NOT NULL", id)
	if err != nil || len(rows) == 0 {
		return "", false, err
	}
	return rowString(rows[0]["name"]), true, nil
}

// replaceRelations 节点关系组整体替换：软删该均衡器现有活跃关系行，再插入新行
// （关系表无单行 update，整组替换语义；sql/ 脚本同款约定）。
func (h *AdminHandler) replaceRelations(upID int64, rels []relBody, now time.Time) error {
	old, err := h.queryScript("dispatch_upstream_node_query_list", []any{upID, upID, 0, 0, maxLimit, 0}, "id ASC")
	if err != nil {
		return err
	}
	for _, rel := range old {
		if err := h.execScript("dispatch_upstream_node_soft_delete", now, now, rowInt64(rel["id"])); err != nil {
			return err
		}
	}
	for _, rel := range rels {
		if _, err := h.insertReturning(tblRel, upID, rel.NodeID, rel.Weight, rel.Priority, now, now); err != nil {
			return err
		}
	}
	return nil
}

// mustRuleRefsErr 带错误的引用计数（删除保护用）。
func (h *AdminHandler) mustRuleRefsErr(upID int64) (int64, error) {
	return h.queryCount("SELECT COUNT(*) AS n FROM "+tblRule+" WHERE upstream_id = "+h.sqlPh(1)+" AND deleted_at IS NULL", upID)
}

// ── 节点端点 ─────────────────────────────────────────────────────────

// nodeBody 节点新增/更新请求体。
type nodeBody struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	HCIntervalMS *int   `json:"hc_interval_ms"`
	HCTimeoutMS  *int   `json:"hc_timeout_ms"`
	HCPath       string `json:"hc_path"`
	Enabled      *bool  `json:"enabled"`
	Remark       string `json:"remark"`
}

// Nodes GET 列表 / POST 新增共用端点。
func (h *AdminHandler) Nodes(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.listNodes(w, r)
	case http.MethodPost:
		postOnly(h.addNode)(w, r)
	default:
		writeJSONErr(w, http.StatusMethodNotAllowed, "仅支持 GET（列表）/ POST（新增）")
	}
}

// listNodes 节点列表：名称/URL 模糊 + enabled + include_deleted 筛选；附被引用数与实时健康。
func (h *AdminHandler) listNodes(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := listPage(r)
	if !ok {
		writeJSONErr(w, http.StatusBadRequest, "分页参数非法：limit 应为 1-10000 的整数、offset 应为非负整数。请修正查询参数后重试")
		return
	}
	q := r.URL.Query()
	nameKw := q.Get("keyword")
	urlKw := q.Get("url")
	enabled := atoiDefault(q.Get("enabled"))
	if q.Get("enabled") != "" && (enabled < 0 || enabled > 1) {
		writeJSONErr(w, http.StatusBadRequest, "enabled 参数非法：应为 0-1 的整数（0=不限；1=仅启用）。请修正后重试")
		return
	}
	incDel := 0
	if v := q.Get("include_deleted"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 1 {
			writeJSONErr(w, http.StatusBadRequest, "include_deleted 参数非法：应为 0-1 的整数（0=仅活跃行；1=仅已删除行）。请修正后重试")
			return
		}
		incDel = n
	}
	// include_deleted 为首参（谓词位于 WHERE 首行）。
	args := []any{incDel, nameKw, nameKw, urlKw, urlKw, enabled, enabled, limit, offset}
	rows, err := h.queryScript("dispatch_node_query_list", args, orderOf(idOrderMap, q.Get("sort"), "id DESC"))
	if err != nil {
		log.Error("dispatch: 节点列表查询失败", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "上游节点列表查询失败（数据库异常），请稍后重试；若持续出现请检查数据库状态或查看服务日志")
		return
	}
	cnt, err := h.queryScript("dispatch_node_count", args[:7])
	if err != nil {
		log.Error("dispatch: 节点列表计数失败", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "上游节点列表计数失败（数据库异常），请稍后重试")
		return
	}
	total := int64(0)
	if len(cnt) > 0 {
		total = rowInt64(cnt[0]["total"])
	}
	for _, row := range rows {
		normalizeRow(row, []string{"id", "hc_interval_ms", "hc_timeout_ms", "enabled"})
		nodeID := rowInt64(row["id"])
		row["rel_refs"] = h.mustRelRefs(nodeID)
		row["health"] = healthName(h.d.reg.Health(nodeID))
		row["inflight"] = h.d.reg.Inflight(nodeID)
	}
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	writeJSONOK(w, map[string]any{"total": total, "rows": rows})
}

// addNode 新增节点。
func (h *AdminHandler) addNode(w http.ResponseWriter, r *http.Request) {
	var body nodeBody
	decodeBody(r, &body)
	if err := h.validateNode(&body, 0); err != nil {
		writeJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	id, err := h.insertReturning(tblNode, body.Name, body.URL,
		*body.HCIntervalMS, *body.HCTimeoutMS, body.HCPath, boolToInt(body.Enabled), body.Remark, now, now)
	if err != nil {
		log.Error("dispatch: 新增节点失败", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "上游节点新增失败（数据库写入异常），请稍后重试")
		return
	}
	h.afterWrite(w, map[string]any{"id": id})
}

// NodesUpdate 更新节点。
func (h *AdminHandler) NodesUpdate() http.HandlerFunc {
	return postOnly(func(w http.ResponseWriter, r *http.Request) {
		if !h.ready(w) {
			return
		}
		var body nodeBody
		decodeBody(r, &body)
		if body.ID <= 0 {
			writeJSONErr(w, http.StatusBadRequest, "节点更新失败：id 必填且应为正整数。请从列表行操作入口重试")
			return
		}
		if err := h.validateNode(&body, body.ID); err != nil {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := h.execScript("dispatch_node_update", body.Name, body.URL,
			*body.HCIntervalMS, *body.HCTimeoutMS, body.HCPath, boolToInt(body.Enabled), body.Remark,
			time.Now().UTC(), body.ID); err != nil {
			log.Error("dispatch: 更新节点失败", "id", body.ID, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "上游节点更新失败（数据库写入异常），请稍后重试")
			return
		}
		h.afterWrite(w, nil)
	})
}

// NodesDelete 软删节点：存在未软删关系引用 → 409 拒绝（引用保护）。
func (h *AdminHandler) NodesDelete() http.HandlerFunc {
	return postOnly(func(w http.ResponseWriter, r *http.Request) {
		if !h.ready(w) {
			return
		}
		id := bodyID(w, r)
		if id <= 0 {
			return
		}
		refs, err := h.mustRelRefsErr(id)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "删除前置校验失败（数据库异常），请稍后重试")
			return
		}
		if refs > 0 {
			writeJSONErr(w, http.StatusConflict,
				fmt.Sprintf("删除被拒绝：该节点仍被 %d 个均衡器的节点关系引用。请先到「负载均衡器」编辑对应均衡器，将其从节点列表中移除，再删除本节点", refs))
			return
		}
		now := time.Now().UTC()
		if err := h.execScript("dispatch_node_soft_delete", now, now, id); err != nil {
			log.Error("dispatch: 删除节点失败", "id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "上游节点删除失败（数据库写入异常），请稍后重试")
			return
		}
		h.afterWrite(w, nil)
	})
}

// NodesRestore 恢复节点：恢复前查重 url 活跃行唯一性（冲突拒绝，防 PG 部分唯一索引裸 DB 错）。
func (h *AdminHandler) NodesRestore() http.HandlerFunc {
	return postOnly(func(w http.ResponseWriter, r *http.Request) {
		if !h.ready(w) {
			return
		}
		id := bodyID(w, r)
		if id <= 0 {
			return
		}
		nodeURL, found, err := h.softDeletedNodeURL(id)
		if err != nil {
			log.Error("dispatch: 恢复节点前置查询失败", "id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "上游节点恢复失败（数据库异常），请稍后重试")
			return
		}
		if !found {
			writeJSONErr(w, http.StatusBadRequest, "上游节点恢复失败：该 id 不存在或不是已删除状态。请刷新列表确认后重试")
			return
		}
		taken, err := h.nodeURLTaken(nodeURL, id)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "上游节点恢复失败（数据库异常），请稍后重试")
			return
		}
		if taken {
			writeJSONErr(w, http.StatusConflict,
				fmt.Sprintf("恢复被拒绝：已存在同地址的活跃节点「%s」（软删行不参与判重，活跃行 url 必须唯一）。请先将同地址活跃节点删除或改地址，再恢复本条；或直接使用现有同地址节点", nodeURL))
			return
		}
		if err := h.execScript("dispatch_node_restore", time.Now().UTC(), id); err != nil {
			log.Error("dispatch: 恢复节点失败", "id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "上游节点恢复失败（数据库写入异常），请稍后重试")
			return
		}
		h.afterWrite(w, nil)
	})
}

// ── 节点校验 ─────────────────────────────────────────────────────────

// validateNode 节点输入校验：URL http(s):// 合法且活跃行唯一、探活参数缺省与范围、
// hc_path 空（= 不探活视为健康）或以 / 开头。
func (h *AdminHandler) validateNode(b *nodeBody, excludeID int64) error {
	b.URL = strings.TrimSpace(b.URL)
	u, err := url.Parse(b.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("节点校验失败：url 必须为合法的 http(s)://host[:port] 地址（如 http://10.0.0.1:9001）。请修正地址后重试")
	}
	taken, err := h.nodeURLTaken(b.URL, excludeID)
	if err != nil {
		return err
	}
	if taken {
		return fmt.Errorf("节点校验失败：地址「%s」已存在（软删行不参与判重，活跃行 url 必须唯一——登记一次、全局探活去重的前提）。请换地址，或到列表中恢复/复用同地址已删条目", b.URL)
	}
	if b.HCIntervalMS == nil || *b.HCIntervalMS <= 0 {
		def := 20000 // 建表默认值同源（hc_interval_ms DEFAULT 20000）
		b.HCIntervalMS = &def
	}
	if b.HCTimeoutMS == nil || *b.HCTimeoutMS <= 0 {
		def := 5000 // 建表默认值同源（hc_timeout_ms DEFAULT 5000）
		b.HCTimeoutMS = &def
	}
	if b.HCTimeoutMS != nil && b.HCIntervalMS != nil && *b.HCTimeoutMS >= *b.HCIntervalMS {
		return errors.New("节点校验失败：hc_timeout_ms（探活超时）应小于 hc_interval_ms（探活周期），否则探活请求会互相堆积。请调整后重试")
	}
	b.HCPath = strings.TrimSpace(b.HCPath)
	if b.HCPath != "" && !strings.HasPrefix(b.HCPath, "/") {
		return errors.New("节点校验失败：hc_path（探活路径）留空 = 不主动探活（该节点始终视为健康），非空时必须以 / 开头（如 /healthz）。请修正后重试")
	}
	return nil
}

// nodeURLTaken 节点 url 活跃行查重（excludeID：更新时排除自身）。
func (h *AdminHandler) nodeURLTaken(nodeURL string, excludeID int64) (bool, error) {
	n, err := h.queryCount(
		"SELECT COUNT(*) AS n FROM "+tblNode+" WHERE url = "+h.sqlPh(1)+" AND id <> "+h.sqlPh(2)+" AND deleted_at IS NULL",
		nodeURL, excludeID)
	return n > 0, err
}

// softDeletedNodeURL 取软删节点行 url（恢复查重用）。
func (h *AdminHandler) softDeletedNodeURL(id int64) (string, bool, error) {
	rows, err := h.queryCountRows(
		"SELECT url FROM "+tblNode+" WHERE id = "+h.sqlPh(1)+" AND deleted_at IS NOT NULL", id)
	if err != nil || len(rows) == 0 {
		return "", false, err
	}
	return rowString(rows[0]["url"]), true, nil
}

// mustRelRefsErr 带错误的节点关系引用计数（删除保护用）。
func (h *AdminHandler) mustRelRefsErr(nodeID int64) (int64, error) {
	return h.queryCount("SELECT COUNT(*) AS n FROM "+tblRel+" WHERE node_id = "+h.sqlPh(1)+" AND deleted_at IS NULL", nodeID)
}

// ── 标签端点（不触发 Rebuild；无 restore） ────────────────────────────

// tagBody 标签请求体。
type tagBody struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Tags GET 全量列表（筛选下拉与 chip 数据源）/ POST 新建。
func (h *AdminHandler) Tags(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := h.queryScript("dispatch_tag_query_all", nil)
		if err != nil {
			log.Error("dispatch: 标签列表查询失败", "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "标签列表查询失败（数据库异常），请稍后重试；若持续出现请检查数据库状态或查看服务日志")
			return
		}
		for _, row := range rows {
			normalizeRow(row, []string{"id"})
		}
		writeJSONOK(w, map[string]any{"rows": rows, "total": int64(len(rows))})
	case http.MethodPost:
		postOnly(h.addTag)(w, r)
	default:
		writeJSONErr(w, http.StatusMethodNotAllowed, "仅支持 GET（列表）/ POST（新建）")
	}
}

// normalizeTagName 标签名归一：转小写 + 去首尾空白（保存时归一，GitLab label 同款）。
func normalizeTagName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// addTag 新建标签（name 归一小写、活跃行唯一）。
func (h *AdminHandler) addTag(w http.ResponseWriter, r *http.Request) {
	var body tagBody
	decodeBody(r, &body)
	body.Name = normalizeTagName(body.Name)
	if body.Name == "" {
		writeJSONErr(w, http.StatusBadRequest, "标签新建失败：name 必填（保存时自动转小写）。请填写标签名后重试")
		return
	}
	taken, err := h.tagNameTaken(body.Name, 0)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "标签新建失败（数据库异常），请稍后重试")
		return
	}
	if taken {
		writeJSONErr(w, http.StatusConflict,
			fmt.Sprintf("标签新建被拒绝：标签「%s」已存在（活跃行唯一）。请直接在规则表单的标签下拉中选用现有标签", body.Name))
		return
	}
	now := time.Now().UTC()
	id, err := h.insertReturning(tblTag, body.Name, now, now)
	if err != nil {
		log.Error("dispatch: 新建标签失败", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "标签新建失败（数据库写入异常），请稍后重试")
		return
	}
	// 标签变更不触发 Rebuild（标签不进运行时快照，仅管理展示）。
	writeJSONOK(w, map[string]any{"id": id})
}

// TagsUpdate 标签重命名（一次生效）。
func (h *AdminHandler) TagsUpdate() http.HandlerFunc {
	return postOnly(func(w http.ResponseWriter, r *http.Request) {
		if !h.ready(w) {
			return
		}
		var body tagBody
		decodeBody(r, &body)
		if body.ID <= 0 {
			writeJSONErr(w, http.StatusBadRequest, "标签更新失败：id 必填且应为正整数。请从列表行操作入口重试")
			return
		}
		body.Name = normalizeTagName(body.Name)
		if body.Name == "" {
			writeJSONErr(w, http.StatusBadRequest, "标签更新失败：name 必填（保存时自动转小写）。请填写标签名后重试")
			return
		}
		taken, err := h.tagNameTaken(body.Name, body.ID)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "标签更新失败（数据库异常），请稍后重试")
			return
		}
		if taken {
			writeJSONErr(w, http.StatusConflict,
				fmt.Sprintf("标签更新被拒绝：标签「%s」已存在（活跃行唯一）。请换一个名称", body.Name))
			return
		}
		if err := h.execScript("dispatch_tag_update", body.Name, time.Now().UTC(), body.ID); err != nil {
			log.Error("dispatch: 更新标签失败", "id", body.ID, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "标签更新失败（数据库写入异常），请稍后重试")
			return
		}
		// 标签变更不触发 Rebuild。
		writeJSONOK(w, nil)
	})
}

// TagsDelete 软删标签并同步软删其 dispatch_rule_tag 关系行（标签是纯管理辅助，
// 无运行态影响）；不做恢复——需要时重建同名实体。
func (h *AdminHandler) TagsDelete() http.HandlerFunc {
	return postOnly(func(w http.ResponseWriter, r *http.Request) {
		if !h.ready(w) {
			return
		}
		id := bodyID(w, r)
		if id <= 0 {
			return
		}
		now := time.Now().UTC()
		// 同步软删关系行：按 tag_id 拉活跃关系行逐条软删（无按 tag_id 的专用脚本，
		// 复用 query_list 过滤 + soft_delete，与整组替换同款语义）。
		rels, err := h.queryScript("dispatch_rule_tag_query_list", []any{0, 0, id, id, maxLimit, 0}, "id ASC")
		if err != nil {
			log.Error("dispatch: 标签关系查询失败", "tag_id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "标签删除失败（数据库异常），请稍后重试")
			return
		}
		for _, rel := range rels {
			if err := h.execScript("dispatch_rule_tag_soft_delete", now, now, rowInt64(rel["id"])); err != nil {
				log.Error("dispatch: 标签关系软删失败", "tag_id", id, "err", err.Error())
				writeJSONErr(w, http.StatusInternalServerError, "标签删除失败（数据库写入异常），请稍后重试")
				return
			}
		}
		if err := h.execScript("dispatch_tag_soft_delete", now, now, id); err != nil {
			log.Error("dispatch: 删除标签失败", "id", id, "err", err.Error())
			writeJSONErr(w, http.StatusInternalServerError, "标签删除失败（数据库写入异常），请稍后重试")
			return
		}
		// 标签变更不触发 Rebuild。
		writeJSONOK(w, nil)
	})
}

// tagNameTaken 标签名活跃行查重（excludeID：更新时排除自身）。
func (h *AdminHandler) tagNameTaken(name string, excludeID int64) (bool, error) {
	n, err := h.queryCount(
		"SELECT COUNT(*) AS n FROM "+tblTag+" WHERE name = "+h.sqlPh(1)+" AND id <> "+h.sqlPh(2)+" AND deleted_at IS NULL",
		name, excludeID)
	return n > 0, err
}

// ── health 实时健康快照 ──────────────────────────────────────────────

// Health GET 节点实时健康快照 + 在途计数（读内存 registry，供节点视图与均衡器编辑器；
// 快照三态：ok=健康 / bad=不健康 / unknown=未探活）。附 dispatch.ready 状态透出。
func (h *AdminHandler) Health(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	rows, err := h.queryCountRows(
		"SELECT id, name, url, enabled FROM " + tblNode + " WHERE deleted_at IS NULL ORDER BY id ASC")
	if err != nil {
		log.Error("dispatch: 健康快照节点查询失败", "err", err.Error())
		writeJSONErr(w, http.StatusInternalServerError, "健康快照查询失败（数据库异常），请稍后重试")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		nodeID := rowInt64(row["id"])
		out = append(out, map[string]any{
			"id":       nodeID,
			"name":     rowString(row["name"]),
			"url":      rowString(row["url"]),
			"enabled":  rowInt64(row["enabled"]),
			"health":   healthName(h.d.reg.Health(nodeID)),
			"inflight": h.d.reg.Inflight(nodeID),
		})
	}
	writeJSONOK(w, map[string]any{"ready": h.d.Ready(), "rows": out, "total": int64(len(out))})
}
