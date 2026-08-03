package trace

import (
	"net/http/httptest"
	"testing"

	"github.com/iotames/easyserver/httpsvr"

	"rocksys/internal/chain"
	"rocksys/internal/dataflow"
)

// TestHandleInjectsTraceID 验证开启时响应头写入 X-Trace-Id，值等于 DF.TraceID()。
func TestHandleInjectsTraceID(t *testing.T) {
	tr := New(nil)
	tr.Start(nil)
	defer tr.Stop()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-Trace-Id", "abc123")
	df := dataflow.New(httpsvr.NewDataFlow(), req)
	ctx := &chain.Context{W: rr, R: req, DF: df}

	if next := tr.Handle(ctx); !next {
		t.Fatalf("Handle 应返回 true 继续转发，got=%v", next)
	}

	want := df.TraceID()
	got := rr.Header().Get("X-Trace-Id")
	if got != want {
		t.Fatalf("X-Trace-Id 响应头不匹配: got=%q want=%q", got, want)
	}
	if got != "abc123" {
		t.Fatalf("应从请求头透传 trace_id: got=%q want=abc123", got)
	}
}

// TestHandleAutoGenerate 验证无请求头时 TraceID 自动生成 32 位 hex 并写入响应头。
func TestHandleAutoGenerate(t *testing.T) {
	tr := New(nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/test", nil)
	df := dataflow.New(httpsvr.NewDataFlow(), req)
	ctx := &chain.Context{W: rr, R: req, DF: df}

	if next := tr.Handle(ctx); !next {
		t.Fatalf("Handle:: 应继续转发")
	}

	id := rr.Header().Get("X-Trace-Id")
	if len(id) != 32 || !isHex(id) {
		t.Fatalf("自动生成的 X-Trace-Id 应为 32 位 hex: got=%q", id)
	}
	if got := df.TraceID(); got != id {
		t.Fatalf("响应头与 DataFlow.TraceID 不一致: got=%q want=%q", id, got)
	}
}

// TestMetadata 验证挂件元信息（Name/Slot）符合第 12 章规格。
func TestMetadata(t *testing.T) {
	tr := New(nil)
	if tr.Name() != "trace" {
		t.Fatalf("Name() 应为 trace, got=%q", tr.Name())
	}
	if tr.Slot() != chain.Head {
		t.Fatalf("Slot() 应为 chain.Head, got=%v", tr.Slot())
	}
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
