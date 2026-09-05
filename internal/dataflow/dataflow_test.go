package dataflow

import (
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/iotames/easyserver/httpsvr"
)

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// newTestDF 构造 inner DataFlow 与当前请求。
func newTestDF(t *testing.T, headers map[string]string) *DataFlow {
	t.Helper()
	req := httptest.NewRequest("GET", "http://localhost/api/test", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return New(httpsvr.NewDataFlow(), req)
}

// 耗时分解精度：ShieldMs + BizMs ≈ TotalMs（误差 < 1ms）。
func TestMsDecompositionPrecision(t *testing.T) {
	df := newTestDF(t, nil)
	begin := df.BeginAt()

	df.SetBeginBizAt(begin.Add(5 * time.Millisecond))
	df.SetDoneBizAt(begin.Add(20 * time.Millisecond))

	shield, biz, total := df.ShieldMs(), df.BizMs(), df.TotalMs()
	if diff := total - shield - biz; diff > 1 || diff < -1 {
		t.Fatalf("ShieldMs+BizMs(%d) 与 TotalMs(%d) 误差过大: %dms", shield+biz, total, diff)
	}
}

// SetBeginBizAt 重复调用被忽略（第二次调用不覆盖第一次）。
func TestSetBeginBizAtWriteOnce(t *testing.T) {
	df := newTestDF(t, nil)
	first := time.Now()
	df.SetBeginBizAt(first)
	df.SetBeginBizAt(first.Add(10 * time.Millisecond))

	if got := df.BeginBizAt(); !got.Equal(first) {
		t.Fatalf("BeginBizAt 被重复调用覆盖: got=%v want=%v", got, first)
	}
}

// SetDoneBizAt 同理，仅写一次。
func TestSetDoneBizAtWriteOnce(t *testing.T) {
	df := newTestDF(t, nil)
	first := time.Now()
	df.SetDoneBizAt(first)
	df.SetDoneBizAt(first.Add(10 * time.Millisecond))

	if got := df.DoneBizAt(); !got.Equal(first) {
		t.Fatalf("DoneBizAt 被重复调用覆盖: got=%v want=%v", got, first)
	}
}

// SetDoneAt 仅写一次，重复调用忽略。
func TestSetDoneAtWriteOnce(t *testing.T) {
	df := newTestDF(t, nil)
	if !df.DoneAt().IsZero() {
		t.Fatalf("未记录时 DoneAt 应为零值: %v", df.DoneAt())
	}
	first := time.Now()
	df.SetDoneAt(first)
	df.SetDoneAt(first.Add(10 * time.Millisecond))

	if got := df.DoneAt(); !got.Equal(first) {
		t.Fatalf("DoneAt 被重复调用覆盖: got=%v want=%v", got, first)
	}
}

// EgressMs：DoneAt 已记录时 = DoneAt - DoneBizAt；未记录（零值）时返回 0。
func TestEgressMs(t *testing.T) {
	df := newTestDF(t, nil)
	begin := df.BeginAt()
	df.SetBeginBizAt(begin.Add(5 * time.Millisecond))
	df.SetDoneBizAt(begin.Add(20 * time.Millisecond))

	if got := df.EgressMs(); got != 0 {
		t.Fatalf("DoneAt 未记录时 EgressMs 应为 0: got=%d", got)
	}
	df.SetDoneAt(begin.Add(35 * time.Millisecond))
	if got := df.EgressMs(); got != 15 {
		t.Fatalf("EgressMs = DoneAt-DoneBizAt: got=%d want=15", got)
	}
}

// EgressMs 防御：DoneAt 已记录但 DoneBizAt 未记录时返回 0，不产生距零值时间的垃圾值。
func TestEgressMsWithoutDoneBizAt(t *testing.T) {
	df := newTestDF(t, nil)
	df.SetDoneAt(df.BeginAt().Add(35 * time.Millisecond))
	if got := df.EgressMs(); got != 0 {
		t.Fatalf("DoneBizAt 零值时 EgressMs 应为 0: got=%d", got)
	}
}

// TotalMs 新旧口径：DoneAt 已记录时 = DoneAt - BeginAt；零值回落 DoneBizAt - BeginAt。
func TestTotalMsSemantics(t *testing.T) {
	df := newTestDF(t, nil)
	begin := df.BeginAt()
	df.SetBeginBizAt(begin.Add(5 * time.Millisecond))
	df.SetDoneBizAt(begin.Add(20 * time.Millisecond))

	if got := df.TotalMs(); got != 20 {
		t.Fatalf("DoneAt 零值应回落旧口径: got=%d want=20", got)
	}
	df.SetDoneAt(begin.Add(35 * time.Millisecond))
	if got := df.TotalMs(); got != 35 {
		t.Fatalf("DoneAt 已记录应为新口径: got=%d want=35", got)
	}
}

// 四段拆解守恒：入网 + 转发（业务） + 出网 ≈ 总耗时（取整误差 < 1ms）。
func TestFourSegmentConservation(t *testing.T) {
	df := newTestDF(t, nil)
	begin := df.BeginAt()
	df.SetBeginBizAt(begin.Add(5 * time.Millisecond))
	df.SetDoneBizAt(begin.Add(20 * time.Millisecond))
	df.SetDoneAt(begin.Add(33 * time.Millisecond))

	sum := df.ShieldMs() + df.BizMs() + df.EgressMs()
	if diff := df.TotalMs() - sum; diff > 1 || diff < -1 {
		t.Fatalf("入网+业务+出网(%d) 与 TotalMs(%d) 误差过大: %dms", sum, df.TotalMs(), diff)
	}
}

// TraceID 为空时自动生成 32 位 hex。
func TestTraceIDAutoGenerate(t *testing.T) {
	df := newTestDF(t, nil)
	id := df.TraceID()
	if !hex32.MatchString(id) {
		t.Fatalf("TraceID 非 32 位 hex: %q", id)
	}
	// 全程幂等：多次调用返回同一值。
	if id2 := df.TraceID(); id2 != id {
		t.Fatalf("TraceID 不幂等: %q != %q", id2, id)
	}
}

// TraceID 优先使用请求头 X-Trace-Id。
func TestTraceIDFromHeader(t *testing.T) {
	df := newTestDF(t, map[string]string{"X-Trace-Id": "abc123"})
	if id := df.TraceID(); id != "abc123" {
		t.Fatalf("TraceID 未从请求头读取: got=%q want=abc123", id)
	}
}

// SetTraceID 优先返回已设置的 TraceID。
func TestTraceIDSetExplicit(t *testing.T) {
	df := newTestDF(t, map[string]string{"X-Trace-Id": "abc123"})
	df.SetTraceID("explicit")
	if id := df.TraceID(); id != "explicit" {
		t.Fatalf("TraceID 未优先返回显式设置值: got=%q want=explicit", id)
	}
}

// 通用 KV 与专有字段读写。
func TestGenericKV(t *testing.T) {
	df := newTestDF(t, nil)
	if v, ok := df.Get("rocksys:none"); ok {
		t.Fatalf("不存在的 key 应返回 ok=false: %v", v)
	}
	df.Set("rocksys:custom", "hello")
	if v, ok := df.Get("rocksys:custom"); !ok || v != "hello" {
		t.Fatalf("通用 KV 读写异常: ok=%v v=%v", ok, v)
	}
}

// 专有字段读写。
func TestTenantAndTarget(t *testing.T) {
	df := newTestDF(t, nil)
	df.SetTenantID("t-001")
	df.SetTarget("http://127.0.0.1:9000")
	if df.TenantID() != "t-001" {
		t.Fatalf("TenantID 读写异常: %q", df.TenantID())
	}
	if df.Target() != "http://127.0.0.1:9000" {
		t.Fatalf("Target 读写异常: %q", df.Target())
	}
}
