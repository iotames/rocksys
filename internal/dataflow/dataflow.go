// Package dataflow 请求级数据流：trace_id/四时间戳/租户（串联）。
package dataflow

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/iotames/easyserver/httpsvr"
)

// rocksys 专有字段在 inner KV 中使用的 key 前缀。
const (
	keyTraceID   = "rocksys:trace_id"
	keyBeginBiz  = "rocksys:begin_biz"
	keyDoneBiz   = "rocksys:done_biz"
	keyDoneAt    = "rocksys:done_at"
	keyTenantID  = "rocksys:tenant_id"
	keyTarget    = "rocksys:target"
	traceIDHdr   = "X-Trace-Id"
	traceIDBytes = 16
)

// DataFlow 包装 httpsvr.DataFlow，添加 rocksys 专有字段。
type DataFlow struct {
	inner *httpsvr.DataFlow
	r     *http.Request

	// 专有字段存储在 inner 的 KV 中（使用 SetData/GetData）：
	// key: "rocksys:trace_id"  → TraceID
	// key: "rocksys:begin_biz" → BeginBizAt
	// key: "rocksys:done_biz"  → DoneBizAt
	// key: "rocksys:done_at"   → DoneAt（出网时刻：响应写回客户端完成）
	// key: "rocksys:tenant_id" → TenantID
	// key: "rocksys:target"    → Target
}

// New 包装已有的 httpsvr.DataFlow。
// r 为当前请求：TraceID() 入口需从 X-Trace-Id 请求头读取（r 不可为 nil）。
func New(inner *httpsvr.DataFlow, r *http.Request) *DataFlow {
	return &DataFlow{inner: inner, r: r}
}

// BeginAt 记录请求开始时间（= inner.GetStartAt()，由 easyserver 自动记录）。
func (df *DataFlow) BeginAt() time.Time {
	return df.inner.GetStartAt()
}

// setOnce 仅写一次：key 已存在则忽略，不存在才写入。
func (df *DataFlow) setOnce(key string, val any) {
	if _, ok := df.Get(key); ok {
		return
	}
	_ = df.inner.SetData(key, val)
}

// SetBeginBizAt 记录业务开始时间，仅写一次，重复调用忽略。
func (df *DataFlow) SetBeginBizAt(t time.Time) {
	df.setOnce(keyBeginBiz, t)
}

// BeginBizAt 返回业务开始时间；未记录时返回零值。
func (df *DataFlow) BeginBizAt() time.Time {
	v, ok := df.Get(keyBeginBiz)
	if !ok {
		return time.Time{}
	}
	if t, ok := v.(time.Time); ok {
		return t
	}
	return time.Time{}
}

// SetDoneBizAt 记录业务结束时间，仅写一次，重复调用忽略。
func (df *DataFlow) SetDoneBizAt(t time.Time) {
	df.setOnce(keyDoneBiz, t)
}

// DoneBizAt 返回业务结束时间；未记录时返回零值。
func (df *DataFlow) DoneBizAt() time.Time {
	v, ok := df.Get(keyDoneBiz)
	if !ok {
		return time.Time{}
	}
	if t, ok := v.(time.Time); ok {
		return t
	}
	return time.Time{}
}

// SetDoneAt 记录出网时刻（响应写回客户端完成），仅写一次，重复调用忽略。
func (df *DataFlow) SetDoneAt(t time.Time) {
	df.setOnce(keyDoneAt, t)
}

// DoneAt 返回出网时刻；未记录时返回零值。
func (df *DataFlow) DoneAt() time.Time {
	v, ok := df.Get(keyDoneAt)
	if !ok {
		return time.Time{}
	}
	if t, ok := v.(time.Time); ok {
		return t
	}
	return time.Time{}
}

// TraceID 优先返回已设置的 TraceID；否则从请求头 X-Trace-Id 读取；
// 仍无则生成 32 位 hex 并缓存。全程幂等——同一请求内多次调用返回同一值。
func (df *DataFlow) TraceID() string {
	if g := df.inner.GetData(keyTraceID); g.Value != "" {
		if v, ok := g.Value.(string); ok && v != "" {
			return v
		}
	}
	if hdr := df.r.Header.Get(traceIDHdr); hdr != "" {
		df.SetTraceID(hdr)
		return hdr
	}
	id := genTraceID()
	df.SetTraceID(id)
	return id
}

// SetTraceID 记录 trace_id。
func (df *DataFlow) SetTraceID(id string) {
	df.setOnce(keyTraceID, id)
}

// TenantID 返回租户 ID，由 auth 挂件设置。
func (df *DataFlow) TenantID() string {
	return df.inner.GetStr(keyTenantID)
}

// SetTenantID 记录租户 ID。
func (df *DataFlow) SetTenantID(id string) {
	df.setOnce(keyTenantID, id)
}

// Target 返回转发目标，由 dispatch 挂件设置。
func (df *DataFlow) Target() string {
	return df.inner.GetStr(keyTarget)
}

// SetTarget 记录转发目标。
func (df *DataFlow) SetTarget(t string) {
	df.setOnce(keyTarget, t)
}

// Set 通用 KV，穿透到 inner.SetData。
func (df *DataFlow) Set(key string, val any) {
	_ = df.inner.SetData(key, val)
}

// Get 通用 KV，穿透到 inner.GetData，返回 (Value, 是否存在)。
func (df *DataFlow) Get(key string) (any, bool) {
	gd := df.inner.GetData(key)
	return gd.Value, gd.Value != nil
}

// ShieldMs 入网耗时：BeginBizAt - BeginAt（毫秒），即全部前置中间件执行耗时；
// 仅当中间链只挂 shield 时等价于防护耗时（存储列名 shield_ms 保持稳定）。
func (df *DataFlow) ShieldMs() int64 {
	return ms(df.BeginBizAt().Sub(df.BeginAt()))
}

// BizMs 转发（业务）耗时：DoneBizAt - BeginBizAt（毫秒）；
// 含网关↔上游网络往返，内网部署、网络稳定时约等于业务真实处理耗时。
func (df *DataFlow) BizMs() int64 {
	return ms(df.DoneBizAt().Sub(df.BeginBizAt()))
}

// EgressMs 出网耗时：DoneAt - DoneBizAt（毫秒），即响应写回客户端完成的时刻差；
// 含客户端网络传输时间，慢客户端会撑大该值。DoneAt 未记录时返回 0。
func (df *DataFlow) EgressMs() int64 {
	if df.DoneAt().IsZero() {
		return 0
	}
	return ms(df.DoneAt().Sub(df.DoneBizAt()))
}

// TotalMs 总耗时（毫秒）。DoneAt 已记录时 = DoneAt - BeginAt（到达→出网，真·总耗时）；
// 未记录（如未埋点路径、单元测试构造的 DF）时回落旧口径 DoneBizAt - BeginAt，行为不变。
func (df *DataFlow) TotalMs() int64 {
	if !df.DoneAt().IsZero() {
		return ms(df.DoneAt().Sub(df.BeginAt()))
	}
	return ms(df.DoneBizAt().Sub(df.BeginAt()))
}

// ms 将 duration 转为毫秒（向下取整）。
func ms(d time.Duration) int64 {
	return d.Milliseconds()
}

// genTraceID 使用 crypto/rand 生成 16 字节 → hex 编码（32 字符）。
func genTraceID() string {
	b := make([]byte, traceIDBytes)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败时回退纳秒时间戳打底，保证幂等返回非空且恰好 32 字符。
		ts := uint64(time.Now().UnixNano())
		for i := 0; i < traceIDBytes; i++ {
			b[i] = byte(ts >> (i*8) & 0xff)
		}
	}
	return hex.EncodeToString(b)
}