// Tail 收尾件单测（STEP4）：直通放行 / 递减按节点 id 跨 Rebuild 不失配 / 无键与饱和兜底。
package dispatch

import (
	"net/http/httptest"
	"testing"

	"rocksys/internal/chain"
	"rocksys/internal/dataflow"

	"github.com/iotames/easyserver/httpsvr"
)

// tailfinCtx 构造带 DataFlow 的链上下文。
func tailfinCtx(t *testing.T) *chain.Context {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/x", nil)
	return &chain.Context{
		W:  httptest.NewRecorder(),
		R:  r,
		DF: dataflow.New(httpsvr.NewDataFlow(), r),
	}
}

// tailfinUpstream 构造单节点均衡器（least_conn，递减语义的目标场景）。
func tailfinUpstream(nodeID int64) *UpstreamRT {
	return &UpstreamRT{
		ID:    1,
		Algo:  AlgoKindLeastConn,
		Nodes: []*NodeRT{{ID: nodeID, URL: "http://n7:9001", Weight: 1}},
		rr:    rrCursor{current: make([]int, 1)},
	}
}

// TestTailfinHandlePassthrough Handle 直通放行返回 true 且不写响应。
func TestTailfinHandlePassthrough(t *testing.T) {
	tf := NewTailFin(NewRegistry())
	ctx := tailfinCtx(t)
	if !tf.Handle(ctx) {
		t.Fatal("Handle 必须直通放行返回 true")
	}
	rec := ctx.W.(*httptest.ResponseRecorder)
	if rec.Body.Len() != 0 || rec.Code != 200 {
		t.Fatal("直通放行不得写响应")
	}
}

// TestTailfinDecrementAcrossRebuild 递减按 DF 节点 id：旧快照选中、新快照（Rebuild
// 替换后的全新 UpstreamRT 对象）替换后递减仍命中同节点 id，不失配。
func TestTailfinDecrementAcrossRebuild(t *testing.T) {
	reg := NewRegistry()
	tf := NewTailFin(reg)
	ctx := tailfinCtx(t)

	// 旧快照选点：least_conn 选中节点 7，在途 +1，节点 id 写入 DF。
	// （registry 预设健康态——灰态节点不参与选点。）
	reg.Ensure(7, HealthOK)
	oldUp := tailfinUpstream(7)
	n, ok := SelectNode(oldUp, reg)
	if !ok || n.ID != 7 {
		t.Fatalf("旧快照应选中节点 7，got ok=%v n=%v", ok, n)
	}
	ctx.DF.Set(DFKeyDispatchNodeID, n.ID)
	if got := reg.Inflight(7); got != 1 {
		t.Fatalf("选点后在途应为 1，got %d", got)
	}

	// Rebuild：新快照全新 UpstreamRT/NodeRT 对象（同节点 id），旧对象随在途请求弃用。
	_ = tailfinUpstream(7) // 新快照（不参与本断言，仅示意替换）

	if err := tf.OnResponse(ctx); err != nil {
		t.Fatalf("OnResponse 不应返回错误: %v", err)
	}
	if got := reg.Inflight(7); got != 0 {
		t.Fatalf("跨 Rebuild 递减后节点 7 在途应为 0，got %d", got)
	}
}

// TestTailfinNoKeyNoOp DF 未写节点 id（未命中路由等）时 OnResponse 静默跳过。
func TestTailfinNoKeyNoOp(t *testing.T) {
	reg := NewRegistry()
	tf := NewTailFin(reg)
	ctx := tailfinCtx(t)
	if err := tf.OnResponse(ctx); err != nil {
		t.Fatalf("无键应静默成功: %v", err)
	}
	// 键存在但类型异常：同样跳过，不得 panic。
	ctx.DF.Set(DFKeyDispatchNodeID, "7")
	if err := tf.OnResponse(ctx); err != nil {
		t.Fatalf("类型异常应静默成功: %v", err)
	}
}

// TestTailfinSaturatedDec DF 键存在但记录为 0 / 缺失：饱和与 no-op 兜底，不为负。
func TestTailfinSaturatedDec(t *testing.T) {
	reg := NewRegistry()
	tf := NewTailFin(reg)
	ctx := tailfinCtx(t)
	ctx.DF.Set(DFKeyDispatchNodeID, int64(7))
	if err := tf.OnResponse(ctx); err != nil {
		t.Fatalf("OnResponse 不应返回错误: %v", err)
	}
	if got := reg.Inflight(7); got != 0 {
		t.Fatalf("饱和递减不得为负，got %d", got)
	}
}

// TestTailfinLifecycleNoop Start/Stop 为 no-op（生命周期由主件统一驱动）。
func TestTailfinLifecycleNoop(t *testing.T) {
	tf := NewTailFin(NewRegistry())
	if err := tf.Start(nil); err != nil {
		t.Fatalf("Start 应为 no-op: %v", err)
	}
	if err := tf.Stop(); err != nil {
		t.Fatalf("Stop 应为 no-op: %v", err)
	}
	if got := tf.Name(); got != "dispatch-tail" {
		t.Fatalf("Name 应为独立标识 dispatch-tail，got %q", got)
	}
}
