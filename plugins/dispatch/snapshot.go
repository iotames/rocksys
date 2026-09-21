// 路由快照构建与参数注入辅助（ROUTE_DISPATCH 三层模型，STEP3 旁路新建）。
//
// 职责：
//   - BuildSnapshot：启用且未软删的规则行 + 全量均衡器/节点/关系行 → STEP2 对象图
//     快照（复用 BuildGraph：内含 (match_order, id) 稳定升序排序与 domain 构建期
//     归一转小写放行）；
//   - fail-closed 红线：不做引用有效性剔除——停用均衡器/停用节点不吞规则，规则行
//     一律保留在快照中（命中后由转发层按均衡器状态 503），构建期仅做引用存在性校验；
//   - RouteParamHeaders：命中模式规则时把捕获参数转为 X-Route-Param-* 请求头键值对
//     （键名沿用旧 DSL Handle 注入惯例），供 STEP5 Handle 写入 DataFlow 与请求头。
package dispatch

// BuildSnapshot 构建路由匹配快照。入参约定：
//   - in.Rules：仅启用且未软删的规则行（query_active 口径，调用方过滤）；
//   - in.Upstreams/Nodes/Relations：全量行（含停用）——fail-closed，不做引用
//     有效性剔除，停用均衡器不导致规则丢失。
//
// 校验失败返回 error（调用方保留旧快照）；成功返回的快照规则已按
// (match_order, id) 稳定升序、domain 已归一转小写。
func BuildSnapshot(in *GraphInput) (*RouteSnapshot, error) {
	return BuildGraph(in)
}

// RouteParamHeaderPrefix 模式匹配参数注入请求头的前缀（沿用旧 DSL Handle 惯例）。
const RouteParamHeaderPrefix = "X-Route-Param-"

// RouteParamHeaders 把命中模式规则捕获的参数转为请求头键值对：
// 参数名 name → 头 X-Route-Param-<name>（值原样透传）。空参数返回 nil。
func RouteParamHeaders(params map[string]string) map[string]string {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]string, len(params))
	for k, v := range params {
		out[RouteParamHeaderPrefix+k] = v
	}
	return out
}
