// 访问日志维度模型：每条 HTTP 请求的跟踪数据 = 一组"维度"。
//
// 设计原则（面向大数据运维方向）：
//  1. 每个字段对应一个具体维度，维度注册表 Dims 是全部字段的唯一清单——
//     新增字段先在此登记（名称/数据类型/存储形态/语义），再决定采集与展示；
//  2. 索引维度（indexed）承载查询过滤/排序：固定 struct 字段 + DB 固定列，
//     类型安全、可走索引；负载维度（payload）仅记录与展示：写 Extras map，
//     DB 侧入 extra JSON 列，新增零结构改动；
//  3. 存储后端只感知维度名，不感知字段语义，二者解耦。
package obs

import (
	"encoding/json"
	"time"
)

// 维度类型：数据类型的最终定义后期按需增补，注册表先行声明。
type DimType string

const (
	DimString   DimType = "string"   // 字符串
	DimInt      DimType = "int"      // 整数
	DimDatetime DimType = "datetime" // 时间（DB 原生类型：PG TIMESTAMPTZ / MySQL DATETIME(3) / SQLite DATETIME；读取时 toString 归一为 RFC3339 字符串）
)

// DimKind 维度存储形态。
type DimKind string

const (
	DimIndexed DimKind = "indexed" // 索引维度：固定列，支持过滤/排序
	DimPayload DimKind = "payload" // 负载维度：JSON 扩展，仅记录/展示
)

// DimSpec 单个维度定义（注册表条目）。
type DimSpec struct {
	Name string  // 维度名（JSON 键名 / DB 列名）
	Type DimType // 数据类型
	Kind DimKind // 存储形态
	Desc string  // 语义说明
}

// 维度名常量（代码引用维度时不得写裸字符串）。
const (
	DimTime       = "time"
	DimTraceID    = "trace_id"
	DimTenantID   = "tenant_id"
	DimPath       = "path"
	DimMethod     = "method"
	DimClientIP   = "client_ip"
	DimStatusCode = "status_code"
	DimUpstream   = "upstream"
	DimShieldMs   = "shield_ms"
	DimBizMs      = "biz_ms"
	DimTotalMs    = "total_ms"
	DimEgressMs   = "egress_ms"
	DimReqBytes   = "req_bytes"
	DimRespBytes  = "resp_bytes"
	// 预留负载维度（仅注册，本次不采集，后期启用）：
	// DimRequestBody = "request_body"  // 纯文本 POST 请求体
)

// Dims 维度注册表：访问记录字段清单（新增维度必须在此登记）。
// 顺序即文档/前端展示顺序。
var Dims = []DimSpec{
	{DimTime, DimDatetime, DimIndexed, "请求完成时间"},
	{DimTraceID, DimString, DimIndexed, "链路标识"},
	{DimTenantID, DimString, DimIndexed, "租户标识（可空）"},
	{DimPath, DimString, DimIndexed, "请求路径"},
	{DimMethod, DimString, DimIndexed, "HTTP 方法"},
	{DimClientIP, DimString, DimIndexed, "客户端地址"},
	{DimStatusCode, DimInt, DimIndexed, "响应状态码"},
	{DimUpstream, DimString, DimIndexed, "最终转发目标"},
	{DimShieldMs, DimInt, DimIndexed, "入网耗时（毫秒）"},
	{DimBizMs, DimInt, DimIndexed, "转发（业务）耗时（毫秒）"},
	{DimTotalMs, DimInt, DimIndexed, "总耗时（毫秒）"},
	{DimEgressMs, DimInt, DimIndexed, "出网耗时（毫秒）"},
	{DimReqBytes, DimInt, DimIndexed, "请求流量（字节）"},
	{DimRespBytes, DimInt, DimIndexed, "响应流量（字节）"},
}

// dimIndex 维度名 → 注册条目（初始化时构建，只读）。
var dimIndex = func() map[string]DimSpec {
	m := make(map[string]DimSpec, len(Dims))
	for _, d := range Dims {
		m[d.Name] = d
	}
	return m
}()

// IsPayloadDim 判断维度是否为负载形态（写入 Extras 的维度）。
func IsPayloadDim(name string) bool {
	spec, ok := dimIndex[name]
	return ok && spec.Kind == DimPayload
}

// AccessRecord 一条 HTTP 访问记录（维度化的日志条目）。
// 索引维度为固定字段；负载维度写入 Extras（可扩展 map，新增零结构改动）。
type AccessRecord struct {
	Time       time.Time // DimTime
	TraceID    string    // DimTraceID
	TenantID   string    // DimTenantID
	Path       string    // DimPath
	Method     string    // DimMethod
	ClientIP   string    // DimClientIP
	StatusCode int       // DimStatusCode
	Upstream   string    // DimUpstream
	ShieldMs   int64     // DimShieldMs（入网耗时）
	BizMs      int64     // DimBizMs（转发（业务）耗时）
	TotalMs    int64     // DimTotalMs
	EgressMs   int64     // DimEgressMs（出网耗时）
	ReqBytes   int64     // DimReqBytes
	RespBytes  int64     // DimRespBytes
	// Extras 负载维度集合（key 必须先在 Dims 注册为 payload 维度）。
	// 序列化时平铺进顶层 JSON，DB 侧存 extra 列。
	Extras map[string]any
}

// ToFlatMap 将记录平铺为维度名 → 值的 map（存储/查询/展示统一的数据形态）。
// 索引维度按固定键写入，负载维度合并平铺。
func (r *AccessRecord) ToFlatMap() map[string]any {
	m := make(map[string]any, len(Dims)+len(r.Extras))
	m[DimTime] = r.Time.Format(time.RFC3339)
	m[DimTraceID] = r.TraceID
	m[DimTenantID] = r.TenantID
	m[DimPath] = r.Path
	m[DimMethod] = r.Method
	m[DimClientIP] = r.ClientIP
	m[DimStatusCode] = r.StatusCode
	m[DimUpstream] = r.Upstream
	m[DimShieldMs] = r.ShieldMs
	m[DimBizMs] = r.BizMs
	m[DimTotalMs] = r.TotalMs
	m[DimEgressMs] = r.EgressMs
	m[DimReqBytes] = r.ReqBytes
	m[DimRespBytes] = r.RespBytes
	for k, v := range r.Extras {
		m[k] = v
	}
	return m
}

// extrasJSON 序列化负载维度集合为 JSON 文本（DB 侧 extra 列）。
func (r *AccessRecord) extrasJSON() (string, error) {
	if len(r.Extras) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(r.Extras)
	if err != nil {
		return "{}", err
	}
	return string(b), nil
}

// mergeExtras 将 DB extra 列 JSON 解析并入平铺 map，返回去除 extra 键后的结果。
func mergeExtras(row map[string]any) {
	if raw, ok := row["extra"]; ok {
		var m map[string]any
		if s, isStr := raw.(string); isStr && s != "" && s != "{}" {
			if err := json.Unmarshal([]byte(s), &m); err == nil {
				for k, v := range m {
					row[k] = v
				}
			}
		}
		delete(row, "extra")
	}
	delete(row, "id") // DB 自增主键不对外
}
