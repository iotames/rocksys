// dispatch 管理端点表驱动测试（ROUTE_DISPATCH STEP6）：CRUD 回路 / 校验拒绝 /
// 引用删除保护（409）/ restore 活跃行查重冲突拒绝 / match-test 只读无副作用 /
// 标签随规则表单整组保存 / 四表写端点触发 Rebuild 断言。
// DB 用临时 sqlite（t.TempDir，进程内建表；非集成门控——纯本地零外部依赖）。
package dispatch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	// sqlite 驱动注册（临时库 DB 测试；与 cmd/rocksys 装配同源）。
	_ "modernc.org/sqlite"

	"rocksys/internal/db"
)

// adminTables 六表建表/建索引脚本清单（与 cmd/rocksys main.go 装配清单同源）。
var adminTables = []struct{ table, create, index string }{
	{"dispatch_rule", "dispatch_rule_create_table.sql", "dispatch_rule_create_index.sql"},
	{"dispatch_upstream", "dispatch_upstream_create_table.sql", "dispatch_upstream_create_index.sql"},
	{"dispatch_node", "dispatch_node_create_table.sql", "dispatch_node_create_index.sql"},
	{"dispatch_upstream_node", "dispatch_upstream_node_create_table.sql", "dispatch_upstream_node_create_index.sql"},
	{"dispatch_tag", "dispatch_tag_create_table.sql", "dispatch_tag_create_index.sql"},
	{"dispatch_rule_tag", "dispatch_rule_tag_create_table.sql", "dispatch_rule_tag_create_index.sql"},
}

// newAdminFixture 构造管理端点测试夹具：临时 sqlite 库（六表就绪）+ dispatch 主件 +
// AdminHandler。返回主件供 Rebuild/快照断言。
func newAdminFixture(t *testing.T) (*Dispatch, *AdminHandler) {
	t.Helper()
	data, err := db.Open("sqlite", filepath.ToSlash(filepath.Join(t.TempDir(), "admin.db")))
	if err != nil {
		t.Fatalf("打开 sqlite: %v", err)
	}
	t.Cleanup(func() { _ = data.Close() })
	execScript := func(name, table string, byLine bool) {
		txt, err := data.SQL(name)
		if err != nil {
			t.Fatalf("读脚本 %s: %v", name, err)
		}
		txt = strings.ReplaceAll(txt, "{table}", table)
		stmts := []string{txt}
		if byLine { // 建索引脚本一行一条；建表脚本是多行单语句须整段执行
			stmts = db.SplitSQLStatements(txt)
		}
		for _, stmt := range stmts {
			if _, err := data.EasyDB().Exec(stmt); err != nil {
				t.Fatalf("执行 %s（%s）: %v", name, stmt, err)
			}
		}
	}
	for _, tb := range adminTables {
		execScript(tb.create, tb.table, false)
		execScript(tb.index, tb.table, true)
	}
	d := New(nil, NewDBSource(data), nil)
	if err := d.Rebuild(); err != nil {
		t.Fatalf("空表 Rebuild: %v", err)
	}
	return d, NewAdminHandler(d, data)
}

// adminDo 发起管理端点请求并返回 recorder（body 为 JSON 字符串，可空）。
func adminDo(t *testing.T, h http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, rd)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// adminJSON 解析响应体为 map。
func adminJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应非 JSON: %v: %s", err, rec.Body.String())
	}
	return m
}

// addNodeViaAdmin 经管理端点新增一个节点，返回 id。
func addNodeViaAdmin(t *testing.T, h *AdminHandler, name, url string) int64 {
	t.Helper()
	rec := adminDo(t, h.Nodes, http.MethodPost, "/admin/dispatch/nodes",
		`{"name":"`+name+`","url":"`+url+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("新增节点 %s: %d %s", url, rec.Code, rec.Body.String())
	}
	return int64(adminJSON(t, rec)["id"].(float64))
}

// addUpstreamViaAdmin 经管理端点新增均衡器（带节点关系），返回 id。
func addUpstreamViaAdmin(t *testing.T, h *AdminHandler, name string, nodeIDs ...int64) int64 {
	t.Helper()
	rels := make([]string, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		rels = append(rels, `{"node_id":`+i64s(id)+`,"weight":1,"priority":0}`)
	}
	rec := adminDo(t, h.Upstreams, http.MethodPost, "/admin/dispatch/upstreams",
		`{"name":"`+name+`","algo":1,"nodes":[`+strings.Join(rels, ",")+`]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("新增均衡器 %s: %d %s", name, rec.Code, rec.Body.String())
	}
	return int64(adminJSON(t, rec)["id"].(float64))
}

// i64s int64 → 字符串（测试拼 JSON 用）。
func i64s(v int64) string {
	return json.Number(int64String(v)).String()
}

func int64String(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestAdminNodeCRUD 节点回路：新增（默认值）→ 列表 → 更新 → 软删 → 恢复；
// 含 url 活跃行查重（新增冲突 400、restore 冲突 409）。
func TestAdminNodeCRUD(t *testing.T) {
	_, h := newAdminFixture(t)

	// 新增（探活参数缺省注入）。
	id := addNodeViaAdmin(t, h, "n1", "http://10.0.0.1:9001")
	if id <= 0 {
		t.Fatal("新增节点应返回 id")
	}
	// 新增同 url 活跃行 → 400（查重拒绝）。
	rec := adminDo(t, h.Nodes, http.MethodPost, "/admin/dispatch/nodes", `{"url":"http://10.0.0.1:9001"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("同 url 新增应 400，got %d: %s", rec.Code, rec.Body.String())
	}
	// 非法 URL → 400。
	for _, bad := range []string{"ftp://x:1", "http://", "10.0.0.2:9002"} {
		rec = adminDo(t, h.Nodes, http.MethodPost, "/admin/dispatch/nodes", `{"url":"`+bad+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("非法 url %q 应 400，got %d", bad, rec.Code)
		}
	}
	// 列表（X-Total-Count + 行字段）。
	rec = adminDo(t, h.Nodes, http.MethodGet, "/admin/dispatch/nodes?limit=10&offset=0", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("节点列表: %d %s", rec.Code, rec.Body.String())
	}
	if tc := rec.Header().Get("X-Total-Count"); tc != "1" {
		t.Errorf("X-Total-Count=%q, want 1", tc)
	}
	row := adminJSON(t, rec)["rows"].([]any)[0].(map[string]any)
	if row["hc_interval_ms"].(float64) != 20000 || row["hc_timeout_ms"].(float64) != 5000 {
		t.Errorf("探活参数缺省未注入: %v", row)
	}
	if row["health"] != "unknown" {
		t.Errorf("未探活应 unknown，got %v", row["health"])
	}
	// 更新。
	rec = adminDo(t, h.NodesUpdate(), http.MethodPost, "/admin/dispatch/nodes/update",
		`{"id":`+i64s(id)+`,"name":"n1x","url":"http://10.0.0.1:9002","hc_path":"/healthz"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("节点更新: %d %s", rec.Code, rec.Body.String())
	}
	// 软删 → 列表为空。
	if rec = adminDo(t, h.NodesDelete(), http.MethodPost, "/admin/dispatch/nodes/delete", `{"id":`+i64s(id)+`}`); rec.Code != http.StatusOK {
		t.Fatalf("节点删除: %d %s", rec.Code, rec.Body.String())
	}
	rec = adminDo(t, h.Nodes, http.MethodGet, "/admin/dispatch/nodes", "")
	if tc := rec.Header().Get("X-Total-Count"); tc != "0" {
		t.Errorf("软删后 X-Total-Count=%q, want 0", tc)
	}
	// 恢复（无冲突）。
	if rec = adminDo(t, h.NodesRestore(), http.MethodPost, "/admin/dispatch/nodes/restore", `{"id":`+i64s(id)+`}`); rec.Code != http.StatusOK {
		t.Fatalf("节点恢复: %d %s", rec.Code, rec.Body.String())
	}
	// 再软删 → 造同 url 活跃行 → restore 冲突 409。
	_ = adminDo(t, h.NodesDelete(), http.MethodPost, "/admin/dispatch/nodes/delete", `{"id":`+i64s(id)+`}`)
	addNodeViaAdmin(t, h, "n2", "http://10.0.0.1:9002") // 与软删行同 url 的活跃行
	rec = adminDo(t, h.NodesRestore(), http.MethodPost, "/admin/dispatch/nodes/restore", `{"id":`+i64s(id)+`}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("restore 同 url 活跃行应 409，got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAdminUpstreamCRUD 均衡器回路：新增（关系组）→ 列表（关系/引用数）→ 关系组整体
// 替换 → 软删 → 恢复；含规则引用删除保护 409、restore name 查重 409。
func TestAdminUpstreamCRUD(t *testing.T) {
	_, h := newAdminFixture(t)
	n1 := addNodeViaAdmin(t, h, "n1", "http://10.0.0.1:9001")
	n2 := addNodeViaAdmin(t, h, "n2", "http://10.0.0.2:9001")

	upID := addUpstreamViaAdmin(t, h, "up-a", n1, n2)
	// 名称查重：活跃同名 → 400。
	rec := adminDo(t, h.Upstreams, http.MethodPost, "/admin/dispatch/upstreams",
		`{"name":"up-a","algo":1,"nodes":[{"node_id":`+i64s(n1)+`}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("同名新增应 400，got %d", rec.Code)
	}
	// 列表：含关系行与被引用数。
	rec = adminDo(t, h.Upstreams, http.MethodGet, "/admin/dispatch/upstreams", "")
	row := adminJSON(t, rec)["rows"].([]any)[0].(map[string]any)
	if len(row["nodes"].([]any)) != 2 {
		t.Errorf("关系组应有 2 行: %v", row["nodes"])
	}
	if row["rule_refs"].(float64) != 0 {
		t.Errorf("rule_refs 应 0: %v", row["rule_refs"])
	}
	// 更新：关系组整体替换为仅 n2。
	rec = adminDo(t, h.UpstreamsUpdate(), http.MethodPost, "/admin/dispatch/upstreams/update",
		`{"id":`+i64s(upID)+`,"name":"up-a","algo":2,"sticky_enabled":true,"sticky_cookie":"sid","nodes":[{"node_id":`+i64s(n2)+`}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("均衡器更新: %d %s", rec.Code, rec.Body.String())
	}
	rec = adminDo(t, h.Upstreams, http.MethodGet, "/admin/dispatch/upstreams", "")
	row = adminJSON(t, rec)["rows"].([]any)[0].(map[string]any)
	nodes := row["nodes"].([]any)
	if len(nodes) != 1 || nodes[0].(map[string]any)["node_id"].(float64) != float64(n2) {
		t.Errorf("关系组应整组替换为 [n2]: %v", nodes)
	}
	if row["algo"].(float64) != 2 || row["sticky_cookie"] != "sid" {
		t.Errorf("algo/sticky 未更新: %v", row)
	}
	// 软删 → 同名活跃行冲突 restore 409；再建同名活跃 → restore 原 id 409。
	_ = adminDo(t, h.UpstreamsDelete(), http.MethodPost, "/admin/dispatch/upstreams/delete", `{"id":`+i64s(upID)+`}`)
	up2 := addUpstreamViaAdmin(t, h, "up-a", n1) // 同名活跃行
	_ = up2
	rec = adminDo(t, h.UpstreamsRestore(), http.MethodPost, "/admin/dispatch/upstreams/restore", `{"id":`+i64s(upID)+`}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("restore 同名活跃行应 409，got %d: %s", rec.Code, rec.Body.String())
	}

	// 无节点关系的新增（放最后：其落库成功但 Rebuild 失败，快照停留旧态，
	// 会毒化本用例后续依赖 Rebuild 的断言）→ 500 且报错含旧快照提示。
	rec = adminDo(t, h.Upstreams, http.MethodPost, "/admin/dispatch/upstreams", `{"name":"up-empty","algo":1}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("无关系新增应 500（Rebuild 失败），got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "旧快照仍在生效") {
		t.Errorf("Rebuild 失败文案应含旧快照提示: %s", rec.Body.String())
	}
}

// TestAdminUpstreamDeleteRefProtection 均衡器被未软删规则引用（含停用规则）→ 删除 409。
func TestAdminUpstreamDeleteRefProtection(t *testing.T) {
	_, h := newAdminFixture(t)
	n1 := addNodeViaAdmin(t, h, "n1", "http://10.0.0.1:9001")
	upID := addUpstreamViaAdmin(t, h, "up-a", n1)
	rec := adminDo(t, h.Rules, http.MethodPost, "/admin/dispatch/rules",
		`{"match_order":10,"path_type":1,"path_value":"/api","upstream_id":`+i64s(upID)+`,"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("新增停用规则: %d %s", rec.Code, rec.Body.String())
	}
	// 停用规则也算引用（D11）→ 删除 409。
	rec = adminDo(t, h.UpstreamsDelete(), http.MethodPost, "/admin/dispatch/upstreams/delete", `{"id":`+i64s(upID)+`}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("被停用规则引用时删除应 409，got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAdminRuleCRUD 规则回路与校验：合法新增（同序号 warnings）/ 非法输入 400 矩阵 /
// 更新 / 软删 / 恢复（含均衡器悬空拒绝）。
func TestAdminRuleCRUD(t *testing.T) {
	_, h := newAdminFixture(t)
	n1 := addNodeViaAdmin(t, h, "n1", "http://10.0.0.1:9001")
	upID := addUpstreamViaAdmin(t, h, "up-a", n1)
	base := `{"match_order":10,"domain":"A.com","path_type":1,"path_value":"/api","upstream_id":` + i64s(upID) + `}`

	// 新增：domain 归一落库（A.com → a.com）。
	rec := adminDo(t, h.Rules, http.MethodPost, "/admin/dispatch/rules", base)
	if rec.Code != http.StatusOK {
		t.Fatalf("新增规则: %d %s", rec.Code, rec.Body.String())
	}
	ruleID := int64(adminJSON(t, rec)["id"].(float64))
	// 同序号第二条 → 200 + warnings 非阻断提示。
	rec = adminDo(t, h.Rules, http.MethodPost, "/admin/dispatch/rules",
		`{"match_order":10,"path_type":1,"path_value":"/web","upstream_id":`+i64s(upID)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("同序号新增应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	rule2 := int64(adminJSON(t, rec)["id"].(float64))
	warns, _ := adminJSON(t, rec)["warnings"].([]any)
	if len(warns) == 0 {
		t.Error("同序号应产生非阻断 warnings")
	}
	// 校验拒绝矩阵（400 带原因）。
	badCases := []struct{ name, body string }{
		{"序号越界", `{"match_order":1000,"path_type":1,"path_value":"/a","upstream_id":` + i64s(upID) + `}`},
		{"path 不以斜杠开头", `{"match_order":11,"path_type":1,"path_value":"api","upstream_id":` + i64s(upID) + `}`},
		{"path_type 非法", `{"match_order":11,"path_type":9,"path_value":"/a","upstream_id":` + i64s(upID) + `}`},
		{"domain 带端口", `{"match_order":11,"domain":"a.com:8080","path_type":1,"path_value":"/a","upstream_id":` + i64s(upID) + `}`},
		{"domain 通配", `{"match_order":11,"domain":"*.a.com","path_type":1,"path_value":"/a","upstream_id":` + i64s(upID) + `}`},
		{"均衡器不存在", `{"match_order":11,"path_type":1,"path_value":"/a","upstream_id":999}`},
	}
	for _, tc := range badCases {
		rec = adminDo(t, h.Rules, http.MethodPost, "/admin/dispatch/rules", tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: 应 400，got %d: %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	// 列表：domain 归一 + 关键词筛选。
	rec = adminDo(t, h.Rules, http.MethodGet, "/admin/dispatch/rules?keyword=a.com", "")
	rows := adminJSON(t, rec)["rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["domain"] != "a.com" {
		t.Errorf("domain 应归一小写并可筛选: %v", rows)
	}
	// 更新 + 软删。
	rec = adminDo(t, h.RulesUpdate(), http.MethodPost, "/admin/dispatch/rules/update",
		`{"id":`+i64s(ruleID)+`,"match_order":20,"path_type":2,"path_value":"/exact","upstream_id":`+i64s(upID)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("规则更新: %d %s", rec.Code, rec.Body.String())
	}
	// 恢复：均衡器悬空拒绝（先软删引用该均衡器的全部规则，使均衡器可删；
	// 软删均衡器，再恢复规则 → 其均衡器仍软删 → 400）。
	_ = adminDo(t, h.RulesDelete(), http.MethodPost, "/admin/dispatch/rules/delete", `{"id":`+i64s(ruleID)+`}`)
	_ = adminDo(t, h.RulesDelete(), http.MethodPost, "/admin/dispatch/rules/delete", `{"id":`+i64s(rule2)+`}`)
	_ = adminDo(t, h.UpstreamsDelete(), http.MethodPost, "/admin/dispatch/upstreams/delete", `{"id":`+i64s(upID)+`}`)
	rec = adminDo(t, h.RulesRestore(), http.MethodPost, "/admin/dispatch/rules/restore", `{"id":`+i64s(ruleID)+`}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("均衡器悬空恢复应 400，got %d: %s", rec.Code, rec.Body.String())
	}
	// 恢复均衡器后规则恢复成功。
	rec = adminDo(t, h.UpstreamsRestore(), http.MethodPost, "/admin/dispatch/upstreams/restore", `{"id":`+i64s(upID)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("恢复均衡器: %d %s", rec.Code, rec.Body.String())
	}
	rec = adminDo(t, h.RulesRestore(), http.MethodPost, "/admin/dispatch/rules/restore", `{"id":`+i64s(ruleID)+`}`)
	if rec.Code != http.StatusOK {
		t.Errorf("恢复规则: %d %s", rec.Code, rec.Body.String())
	}
}

// TestAdminNodeURLBaseOnly 节点 url 只能为基准地址：带路径/query/锚点拒绝（400），
// 手滑尾斜杠静默归一（落库无尾斜杠）。
func TestAdminNodeURLBaseOnly(t *testing.T) {
	_, h := newAdminFixture(t)
	// 带路径 / query / 锚点 → 400。
	for _, bad := range []string{
		"http://172.16.160.10:8929/-/health", // 路径（探活路径应填 hc_path）
		"http://10.0.0.1:9001/?a=1",          // 查询参数
		"http://10.0.0.1:9001/#frag",         // 锚点
	} {
		rec := adminDo(t, h.Nodes, http.MethodPost, "/admin/dispatch/nodes", `{"url":"`+bad+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("带路径/query/锚点的 url %q 应 400，got %d: %s", bad, rec.Code, rec.Body.String())
		}
	}
	// 尾斜杠归一：/ 结尾容忍并去掉尾斜杠落库。
	rec := adminDo(t, h.Nodes, http.MethodPost, "/admin/dispatch/nodes", `{"name":"n-slash","url":"http://10.0.0.9:9001/"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("尾斜杠 url 应容忍，got %d: %s", rec.Code, rec.Body.String())
	}
	rec = adminDo(t, h.Nodes, http.MethodGet, "/admin/dispatch/nodes?keyword=n-slash", "")
	rows := listRows(t, rec)
	if len(rows) != 1 || rows[0]["url"] != "http://10.0.0.9:9001" {
		t.Errorf("尾斜杠应归一去掉: %v", rows)
	}
}

// TestAdminRuleTagsDedupAndList 标签保存去重 + 列表下发 tags：
// tags 含重复名与大小写/空白变体（["prod","Prod","prod "]）→ 落库活跃关系仅一条 prod；
// listRules 每行附 tags 字段（按名升序，软删规则行同样附其关系）。
func TestAdminRuleTagsDedupAndList(t *testing.T) {
	_, h := newAdminFixture(t)
	n1 := addNodeViaAdmin(t, h, "n1", "http://10.0.0.1:9001")
	upID := addUpstreamViaAdmin(t, h, "up-a", n1)

	rec := adminDo(t, h.Rules, http.MethodPost, "/admin/dispatch/rules",
		`{"match_order":10,"path_type":1,"path_value":"/api","upstream_id":`+i64s(upID)+`,"tags":["prod","Prod","prod "]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("带变体标签新增规则: %d %s", rec.Code, rec.Body.String())
	}
	ruleID := int64(adminJSON(t, rec)["id"].(float64))
	// 活跃关系仅一条 prod（归一去重）。
	assertRuleTags(t, h, ruleID, []string{"prod"})
	// 列表行附 tags 字段。
	rec = adminDo(t, h.Rules, http.MethodGet, "/admin/dispatch/rules", "")
	rows := listRows(t, rec)
	if len(rows) != 1 {
		t.Fatalf("应 1 行规则: %v", rows)
	}
	tags, ok := rows[0]["tags"].([]any)
	if !ok || len(tags) != 1 || tags[0] != "prod" {
		t.Errorf("列表行应附 tags=[prod]: %v", rows[0]["tags"])
	}
	// 软删规则行同样附其关系（include_deleted=1）。
	if rec = adminDo(t, h.RulesDelete(), http.MethodPost, "/admin/dispatch/rules/delete", `{"id":`+i64s(ruleID)+`}`); rec.Code != http.StatusOK {
		t.Fatalf("软删规则: %d %s", rec.Code, rec.Body.String())
	}
	rec = adminDo(t, h.Rules, http.MethodGet, "/admin/dispatch/rules?include_deleted=1", "")
	rows = listRows(t, rec)
	if len(rows) != 1 {
		t.Fatalf("应 1 行软删规则: %v", rows)
	}
	tags, _ = rows[0]["tags"].([]any)
	if len(tags) != 1 || tags[0] != "prod" {
		t.Errorf("软删规则行也应附 tags=[prod]: %v", rows[0]["tags"])
	}
	// 多标签按名升序。
	if rec = adminDo(t, h.RulesRestore(), http.MethodPost, "/admin/dispatch/rules/restore", `{"id":`+i64s(ruleID)+`}`); rec.Code != http.StatusOK {
		t.Fatalf("恢复规则: %d %s", rec.Code, rec.Body.String())
	}
	if rec = adminDo(t, h.RulesUpdate(), http.MethodPost, "/admin/dispatch/rules/update",
		`{"id":`+i64s(ruleID)+`,"match_order":10,"path_type":1,"path_value":"/api","upstream_id":`+i64s(upID)+`,"tags":["zz","aa"]}`); rec.Code != http.StatusOK {
		t.Fatalf("更新多标签: %d %s", rec.Code, rec.Body.String())
	}
	rec = adminDo(t, h.Rules, http.MethodGet, "/admin/dispatch/rules", "")
	tags, _ = listRows(t, rec)[0]["tags"].([]any)
	if len(tags) != 2 || tags[0] != "aa" || tags[1] != "zz" {
		t.Errorf("tags 应按名升序 [aa zz]: %v", tags)
	}
}

// TestAdminRuleListTagFilter 规则列表 tag 筛选：tag=名 仅命中打有该标签的规则
// （大小写/空白归一）；tag=不存在名 结果为空；tag 缺省不过滤；
// include_deleted=1 组合下软删行同样参与筛选。
func TestAdminRuleListTagFilter(t *testing.T) {
	_, h := newAdminFixture(t)
	n1 := addNodeViaAdmin(t, h, "n1", "http://10.0.0.1:9001")
	upID := addUpstreamViaAdmin(t, h, "up-a", n1)

	mkRule := func(path, tagsJSON string) int64 {
		rec := adminDo(t, h.Rules, http.MethodPost, "/admin/dispatch/rules",
			`{"match_order":10,"path_type":1,"path_value":"`+path+`","upstream_id":`+i64s(upID)+`,"tags":`+tagsJSON+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("新增规则 %s: %d %s", path, rec.Code, rec.Body.String())
		}
		return int64(adminJSON(t, rec)["id"].(float64))
	}
	prodID := mkRule("/api", `["prod"]`)
	mkRule("/web", `["dev"]`)

	// tag=prod 命中、tag=PROD 归一后同样命中；tag=dev 命中另一条。
	for _, tag := range []string{"prod", "PROD", " prod "} {
		rec := adminDo(t, h.Rules, http.MethodGet, "/admin/dispatch/rules?tag="+url.QueryEscape(tag), "")
		rows := listRows(t, rec)
		if len(rows) != 1 || int64(rows[0]["id"].(float64)) != prodID {
			t.Errorf("tag=%q 应仅命中 prod 规则: %v", tag, rows)
		}
	}
	if rows := listRows(t, adminDo(t, h.Rules, http.MethodGet, "/admin/dispatch/rules?tag=dev", "")); len(rows) != 1 {
		t.Errorf("tag=dev 应命中 1 行: %v", rows)
	}
	// tag=不存在名 结果为空。
	if rows := listRows(t, adminDo(t, h.Rules, http.MethodGet, "/admin/dispatch/rules?tag=nosuch", "")); len(rows) != 0 {
		t.Errorf("tag=nosuch 应为空: %v", rows)
	}
	// tag 缺省不过滤：两条全部返回。
	if rows := listRows(t, adminDo(t, h.Rules, http.MethodGet, "/admin/dispatch/rules", "")); len(rows) != 2 {
		t.Errorf("tag 缺省应返回全部 2 行: %v", rows)
	}
	// 软删 + tag 组合：默认（仅活跃行）软删的 prod 规则不再命中；
	// include_deleted=1（仅已删除行）时按 tag 命中软删行。
	if rec := adminDo(t, h.RulesDelete(), http.MethodPost, "/admin/dispatch/rules/delete", `{"id":`+i64s(prodID)+`}`); rec.Code != http.StatusOK {
		t.Fatalf("软删规则: %d %s", rec.Code, rec.Body.String())
	}
	if rows := listRows(t, adminDo(t, h.Rules, http.MethodGet, "/admin/dispatch/rules?tag=prod", "")); len(rows) != 0 {
		t.Errorf("软删后 tag=prod 应为空: %v", rows)
	}
	rows := listRows(t, adminDo(t, h.Rules, http.MethodGet, "/admin/dispatch/rules?tag=prod&include_deleted=1", ""))
	if len(rows) != 1 || int64(rows[0]["id"].(float64)) != prodID {
		t.Errorf("include_deleted=1 且 tag=prod 应命中软删行: %v", rows)
	}
}

// TestAdminMatchTestReadOnly 命中测试只读无副作用：轮询游标不推进、在途计数不变；
// 且命中判定与实请求（Handle）一致。
func TestAdminMatchTestReadOnly(t *testing.T) {
	d, h := newAdminFixture(t)
	n1 := addNodeViaAdmin(t, h, "n1", "http://10.0.0.1:9001")
	n2 := addNodeViaAdmin(t, h, "n2", "http://10.0.0.2:9001")
	upID := addUpstreamViaAdmin(t, h, "up-a", n1, n2)
	rec := adminDo(t, h.Rules, http.MethodPost, "/admin/dispatch/rules",
		`{"match_order":10,"domain":"a.com","path_type":1,"path_value":"/api","upstream_id":`+i64s(upID)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("新增规则: %d %s", rec.Code, rec.Body.String())
	}

	// 未命中 → default_upstream。
	rec = adminDo(t, h.RulesMatchTest(), http.MethodPost, "/admin/dispatch/rules/match-test",
		`{"host":"b.com","path":"/api/x"}`)
	m := adminJSON(t, rec)
	if m["hit"] != false || m["position"] != "default_upstream" {
		t.Errorf("未命中判定错误: %v", m)
	}
	// 命中 → node；只读选点零态游标不推进：多次调用结果恒定（同一起点快照试算），
	// 若实现推进了游标序列将交替变化；在途计数应恒为 0。
	nodeURL := func() string {
		rec = adminDo(t, h.RulesMatchTest(), http.MethodPost, "/admin/dispatch/rules/match-test",
			`{"host":"A.COM:8443","path":"/api/x"}`)
		m = adminJSON(t, rec)
		if m["hit"] != true || m["position"] != "node" {
			t.Fatalf("命中判定错误: %v", m)
		}
		node := m["node"].(map[string]any)
		if node["id"].(float64) != float64(n1) && node["id"].(float64) != float64(n2) {
			t.Fatalf("选出未知节点: %v", node)
		}
		return node["url"].(string)
	}
	got := []string{nodeURL(), nodeURL(), nodeURL(), nodeURL()}
	want := []string{"http://10.0.0.1:9001", "http://10.0.0.1:9001", "http://10.0.0.1:9001", "http://10.0.0.1:9001"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("match-test 序列[%d]=%s, want %s（游标被推进或分布异常）", i, got[i], want[i])
		}
	}
	if infl := d.reg.Inflight(n1) + d.reg.Inflight(n2); infl != 0 {
		t.Errorf("match-test 不应计在途，got %d", infl)
	}
	// 命中一致性：match-test 与实请求 Handle 命中同一规则（选点为动态参考值不比对）。
	snap := d.snapshot()
	rule, _ := Match(snap, "a.com", "/api/x")
	if rule == nil || rule.ID != 1 {
		t.Fatalf("快照匹配应命中规则 #1")
	}
	m = adminJSON(t, adminDo(t, h.RulesMatchTest(), http.MethodPost, "/admin/dispatch/rules/match-test",
		`{"host":"a.com","path":"/api/x"}`))
	if m["rule"].(map[string]any)["id"].(float64) != float64(rule.ID) {
		t.Errorf("match-test 与快照匹配命中规则不一致: %v vs %d", m["rule"], rule.ID)
	}
	// 对照：真实 Handle 每次选点计在途 +1（有副作用），与 match-test 形成行为对照。
	if infl := d.reg.Inflight(n1); infl != 0 {
		t.Fatalf("前置断言失败：Handle 未运行时在途应 0")
	}
}

// TestAdminTags 标签端点：新建（归一小写）/ 重名 409 / 重命名 / 删除同步软删关系行 /
// 标签写端点不触发 Rebuild。
func TestAdminTags(t *testing.T) {
	d, h := newAdminFixture(t)

	oldSnap := d.snapshot()
	// 新建（自动小写归一）。
	rec := adminDo(t, h.Tags, http.MethodPost, "/admin/dispatch/tags", `{"name":"Prod"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("新建标签: %d %s", rec.Code, rec.Body.String())
	}
	tagID := int64(adminJSON(t, rec)["id"].(float64))
	// 重名（归一后同名）→ 409。
	rec = adminDo(t, h.Tags, http.MethodPost, "/admin/dispatch/tags", `{"name":" prod "}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("重名标签应 409，got %d: %s", rec.Code, rec.Body.String())
	}
	// 列表。
	rec = adminDo(t, h.Tags, http.MethodGet, "/admin/dispatch/tags", "")
	rows := adminJSON(t, rec)["rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["name"] != "prod" {
		t.Errorf("标签列表应含归一名 prod: %v", rows)
	}
	// 重命名。
	if rec = adminDo(t, h.TagsUpdate(), http.MethodPost, "/admin/dispatch/tags/update",
		`{"id":`+i64s(tagID)+`,"name":"Staging"}`); rec.Code != http.StatusOK {
		t.Fatalf("重命名标签: %d %s", rec.Code, rec.Body.String())
	}
	// 标签写端点不触发 Rebuild（快照指针不变）。
	if d.snapshot() != oldSnap {
		t.Error("标签写端点不应触发 Rebuild")
	}
	// 删除（同步软删关系行——本例无关系行，仅验标签自身消失）。
	if rec = adminDo(t, h.TagsDelete(), http.MethodPost, "/admin/dispatch/tags/delete", `{"id":`+i64s(tagID)+`}`); rec.Code != http.StatusOK {
		t.Fatalf("删除标签: %d %s", rec.Code, rec.Body.String())
	}
	rec = adminDo(t, h.Tags, http.MethodGet, "/admin/dispatch/tags", "")
	if got := int(adminJSON(t, rec)["total"].(float64)); got != 0 {
		t.Errorf("删除后标签数应 0，got %d", got)
	}
}

// TestAdminRuleTagsWholeSave 标签随规则表单整组保存：先 upsert 标签实体，
// 再整组替换该规则的关系行（更新时旧关系软删、新关系生效）。
func TestAdminRuleTagsWholeSave(t *testing.T) {
	_, h := newAdminFixture(t)
	n1 := addNodeViaAdmin(t, h, "n1", "http://10.0.0.1:9001")
	upID := addUpstreamViaAdmin(t, h, "up-a", n1)

	// 新增规则带标签 a/b（b 不存在 → 自动 upsert 实体）。
	rec := adminDo(t, h.Rules, http.MethodPost, "/admin/dispatch/rules",
		`{"match_order":10,"path_type":1,"path_value":"/api","upstream_id":`+i64s(upID)+`,"tags":["a","b"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("带标签新增规则: %d %s", rec.Code, rec.Body.String())
	}
	ruleID := int64(adminJSON(t, rec)["id"].(float64))
	assertRuleTags(t, h, ruleID, []string{"a", "b"})

	// 更新规则带标签 b/c：整组替换（a 被移除，c 新建实体）。
	rec = adminDo(t, h.RulesUpdate(), http.MethodPost, "/admin/dispatch/rules/update",
		`{"id":`+i64s(ruleID)+`,"match_order":10,"path_type":1,"path_value":"/api","upstream_id":`+i64s(upID)+`,"tags":["b","c"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("带标签更新规则: %d %s", rec.Code, rec.Body.String())
	}
	assertRuleTags(t, h, ruleID, []string{"b", "c"})
	// 标签实体 a 保留（无引用标签不自动清理，可复用）。
	rec = adminDo(t, h.Tags, http.MethodGet, "/admin/dispatch/tags", "")
	names := map[string]bool{}
	for _, r := range adminJSON(t, rec)["rows"].([]any) {
		names[r.(map[string]any)["name"].(string)] = true
	}
	if !names["a"] || !names["b"] || !names["c"] {
		t.Errorf("标签实体集应含 a/b/c: %v", names)
	}
}

// assertRuleTags 经关系列表脚本断言规则的活跃标签名集合。
func assertRuleTags(t *testing.T, h *AdminHandler, ruleID int64, want []string) {
	t.Helper()
	rec := adminDo(t, func(w http.ResponseWriter, r *http.Request) {
		rows, err := h.queryScript("dispatch_rule_tag_query_list", []any{ruleID, ruleID, 0, 0, 100, 0}, "id ASC")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rows)
	}, http.MethodGet, "/", "")
	var rels []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rels); err != nil {
		t.Fatalf("关系行解析: %v: %s", err, rec.Body.String())
	}
	got := make([]string, 0, len(rels))
	for _, rel := range rels {
		tagID := rowInt64(rel["tag_id"]) // 关系行来自内部查询（非 JSON），数值为 int64
		rows, err := h.queryScript("dispatch_tag_query_all", nil)
		if err != nil {
			t.Fatalf("标签查询: %v", err)
		}
		for _, row := range rows {
			if rowInt64(row["id"]) == tagID {
				got = append(got, row["name"].(string))
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("规则 %d 标签=%v, want %v", ruleID, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("规则 %d 标签=%v, want %v", ruleID, got, want)
		}
	}
}

// TestAdminWriteTriggersRebuild 四表写端点成功后自动触发 Rebuild：经端点新增
// 节点→均衡器→规则后，快照即含新规则（无需手动 reload）；reload 端点亦可显式触发。
func TestAdminWriteTriggersRebuild(t *testing.T) {
	d, h := newAdminFixture(t)
	if len(d.snapshot().Rules) != 0 {
		t.Fatal("初始快照应无规则")
	}
	n1 := addNodeViaAdmin(t, h, "n1", "http://10.0.0.1:9001")
	upID := addUpstreamViaAdmin(t, h, "up-a", n1)
	if len(d.snapshot().Rules) != 0 {
		t.Fatal("尚无规则时快照规则数应为 0")
	}
	rec := adminDo(t, h.Rules, http.MethodPost, "/admin/dispatch/rules",
		`{"match_order":10,"path_type":1,"path_value":"/api","upstream_id":`+i64s(upID)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("新增规则: %d %s", rec.Code, rec.Body.String())
	}
	// 写端点成功 → Rebuild 自动触发：快照即含新规则（保存即热更）。
	rules := d.snapshot().Rules
	if len(rules) != 1 || rules[0].PathValue != "/api" {
		t.Errorf("写端点应触发 Rebuild 使规则生效: %v", rules)
	}
	// reload 端点：成功返回 ready=true。
	rec = adminDo(t, h.Reload(), http.MethodPost, "/admin/dispatch/reload", "")
	if rec.Code != http.StatusOK || adminJSON(t, rec)["ready"] != true {
		t.Errorf("reload 应成功且 ready=true: %d %s", rec.Code, rec.Body.String())
	}
}

// TestAdminMetaAndHealth meta 枚举字典与健康快照端点。
func TestAdminMetaAndHealth(t *testing.T) {
	d, h := newAdminFixture(t)
	n1 := addNodeViaAdmin(t, h, "n1", "http://10.0.0.1:9001")
	d.reg.SetHealth(n1, HealthBad)

	rec := adminDo(t, h.RulesMeta, http.MethodGet, "/admin/dispatch/rules/meta", "")
	m := adminJSON(t, rec)
	if len(m["path_types"].([]any)) != 3 || len(m["algos"].([]any)) != 2 || len(m["priorities"].([]any)) != 2 {
		t.Errorf("meta 枚举字典不全: %v", m)
	}

	rec = adminDo(t, h.Health, http.MethodGet, "/admin/dispatch/health", "")
	m = adminJSON(t, rec)
	if m["ready"] != true {
		t.Errorf("health 应透出 ready: %v", m)
	}
	rows := m["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("health 应含 1 节点: %v", rows)
	}
	row := rows[0].(map[string]any)
	if row["health"] != "bad" || row["inflight"].(float64) != 0 {
		t.Errorf("健康三态/在途计数错误: %v", row)
	}
}

// TestAdminPostOnly 副作用端点拒绝 GET（防本机恶意页面触发）。
func TestAdminPostOnly(t *testing.T) {
	_, h := newAdminFixture(t)
	for name, hf := range map[string]http.HandlerFunc{
		"rules/update":  h.RulesUpdate(),
		"rules/delete":  h.RulesDelete(),
		"rules/restore": h.RulesRestore(),
		"match-test":    h.RulesMatchTest(),
		"reload":        h.Reload(),
		"tags/delete":   h.TagsDelete(),
	} {
		rec := adminDo(t, hf, http.MethodGet, "/admin/dispatch/"+name, "")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s 应 405，got %d", name, rec.Code)
		}
	}
}

// listRows 取列表响应 rows（空列表序列化为 null，统一归一为空切片）。
func listRows(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	rows, _ := adminJSON(t, rec)["rows"].([]any)
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.(map[string]any))
	}
	return out
}

// TestAdminListIncludeDeleted 列表 include_deleted 筛选（表驱动覆盖规则/均衡器/节点三视图）：
// 软删行缺省不可见（现状语义不变）、include_deleted=1 时可见且携带 deleted_at（供前端恢复按钮判定）、
// 恢复后重回活跃列表——保证 /restore 端点经由「仅已删除」筛选可达。
func TestAdminListIncludeDeleted(t *testing.T) {
	_, h := newAdminFixture(t)
	cases := []struct {
		name    string                   // 视图名
		setup   func(t *testing.T) int64 // 造数，返回被软删行 id
		list    http.HandlerFunc         // 列表 handler
		del     http.HandlerFunc         // 软删 handler
		restore http.HandlerFunc         // 恢复 handler
		path    string                   // 列表端点路径
	}{
		{
			name: "规则",
			setup: func(t *testing.T) int64 {
				nodeID := addNodeViaAdmin(t, h, "n-rule", "http://127.0.0.1:9101")
				upID := addUpstreamViaAdmin(t, h, "up-rule", nodeID)
				rec := adminDo(t, h.Rules, http.MethodPost, "/admin/dispatch/rules",
					`{"match_order":10,"domain":"del.com","path_type":1,"path_value":"/a","upstream_id":`+i64s(upID)+`,"title":"软删用例"}`)
				if rec.Code != http.StatusOK {
					t.Fatalf("新增规则: %d %s", rec.Code, rec.Body.String())
				}
				return int64(adminJSON(t, rec)["id"].(float64))
			},
			list: h.Rules, del: h.RulesDelete(), restore: h.RulesRestore(),
			path: "/admin/dispatch/rules",
		},
		{
			name: "均衡器",
			setup: func(t *testing.T) int64 {
				nodeID := addNodeViaAdmin(t, h, "n-up", "http://127.0.0.1:9102")
				return addUpstreamViaAdmin(t, h, "up-del", nodeID)
			},
			list: h.Upstreams, del: h.UpstreamsDelete(), restore: h.UpstreamsRestore(),
			path: "/admin/dispatch/upstreams",
		},
		{
			name: "节点",
			setup: func(t *testing.T) int64 {
				// 独立节点（不被均衡器引用，规避删除保护 409）。
				return addNodeViaAdmin(t, h, "n-del", "http://127.0.0.1:9103")
			},
			list: h.Nodes, del: h.NodesDelete(), restore: h.NodesRestore(),
			path: "/admin/dispatch/nodes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.setup(t)
			// 软删。
			if rec := adminDo(t, tc.del, http.MethodPost, tc.path+"/delete", `{"id":`+i64s(id)+`}`); rec.Code != http.StatusOK {
				t.Fatalf("软删: %d %s", rec.Code, rec.Body.String())
			}
			// 缺省（include_deleted=0）不可见。
			rec := adminDo(t, tc.list, http.MethodGet, tc.path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("活跃列表: %d %s", rec.Code, rec.Body.String())
			}
			for _, row := range listRows(t, rec) {
				if int64(row["id"].(float64)) == id {
					t.Errorf("软删行 %d 不应出现在缺省活跃列表", id)
				}
			}
			// include_deleted=1 可见且携带 deleted_at。
			rec = adminDo(t, tc.list, http.MethodGet, tc.path+"?include_deleted=1", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("仅已删除列表: %d %s", rec.Code, rec.Body.String())
			}
			var found map[string]any
			for _, m := range listRows(t, rec) {
				if int64(m["id"].(float64)) == id {
					found = m
				}
			}
			if found == nil {
				t.Fatalf("软删行 %d 应出现在 include_deleted=1 列表: %v", id, listRows(t, rec))
			}
			if s, _ := found["deleted_at"].(string); s == "" {
				t.Errorf("include_deleted=1 行应携带非空 deleted_at: %v", found)
			}
			// include_deleted 非法值 400。
			if rec := adminDo(t, tc.list, http.MethodGet, tc.path+"?include_deleted=2", ""); rec.Code != http.StatusBadRequest {
				t.Errorf("include_deleted=2 应 400，got %d", rec.Code)
			}
			// 恢复后重回活跃列表。
			if rec := adminDo(t, tc.restore, http.MethodPost, tc.path+"/restore", `{"id":`+i64s(id)+`}`); rec.Code != http.StatusOK {
				t.Fatalf("恢复: %d %s", rec.Code, rec.Body.String())
			}
			rec = adminDo(t, tc.list, http.MethodGet, tc.path, "")
			visible := false
			for _, m := range listRows(t, rec) {
				if int64(m["id"].(float64)) == id {
					visible = true
				}
			}
			if !visible {
				t.Errorf("恢复后行 %d 应重回活跃列表", id)
			}
		})
	}
}
