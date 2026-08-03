// Package result L3 结果处理（转发链中间件）。
package result

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rocksys/internal/chain"
)

// buildResult 构造已 Start 的 Result 实例（直构造，跳过 conf 注册）。
func buildResult(t *testing.T, maskFields string, wrap bool) *Result {
	t.Helper()
	r := &Result{}
	r.maskFields = maskFields
	r.wrap = wrap
	if err := r.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return r
}

// newJSONCtx 手工构造 JSON 响应的 chain.Context。
func newJSONCtx(t *testing.T, body string) (*chain.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	ctx := &chain.Context{
		RespCode:   200,
		RespHeader: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		RespBody:   []byte(body),
		RespW:      w,
	}
	return ctx, w
}

// TestOnResponseWrap 开启 wrap：Envelope 含脱敏后的 phone（§11.4 验收 1）。
func TestOnResponseWrap(t *testing.T) {
	r := buildResult(t, "phone", true)
	ctx, w := newJSONCtx(t, `{"user":"test","phone":"13812345678"}`)

	if err := r.OnResponse(ctx); err != nil {
		t.Fatalf("OnResponse: %v", err)
	}
	if !strings.Contains(w.Body.String(), `"code":0`) {
		t.Errorf("缺少 code:0，实际 %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"msg":"ok"`) {
		t.Errorf("缺少 msg:ok，实际 %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"phone":"138****5678"`) {
		t.Errorf("phone 未脱敏，实际 %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"user":"test"`) {
		t.Errorf("user 不应被改动，实际 %s", w.Body.String())
	}
}

// TestOnResponseMaskOnly 关闭 wrap（仅脱敏）：直接输出脱敏后的原始 JSON。
func TestOnResponseMaskOnly(t *testing.T) {
	r := buildResult(t, "phone", false)
	ctx, w := newJSONCtx(t, `{"user":"test","phone":"13812345678"}`)

	if err := r.OnResponse(ctx); err != nil {
		t.Fatalf("OnResponse: %v", err)
	}
	got := w.Body.String()
	if !strings.Contains(got, `"phone":"138****5678"`) {
		t.Errorf("phone 未脱敏，实际 %s", got)
	}
	if strings.Contains(got, `"code"`) {
		t.Errorf("未开启 wrap 不应出现 code 字段，实际 %s", got)
	}
}

// TestOnResponseNestedMask 嵌套结构体中的字段也要脱敏。
func TestOnResponseNestedMask(t *testing.T) {
	r := buildResult(t, "id_card", false)
	ctx, w := newJSONCtx(t, `{"user":{"id_card":"110101199001011234","name":"test"}}`)

	if err := r.OnResponse(ctx); err != nil {
		t.Fatalf("OnResponse: %v", err)
	}
	if !strings.Contains(w.Body.String(), `"id_card":"110****1234"`) {
		t.Errorf("嵌套 id_card 未脱敏，实际 %s", w.Body.String())
	}
}

// TestOnResponseShortMask 短字符串全 ****。
func TestOnResponseShortMask(t *testing.T) {
	r := buildResult(t, "token", false)
	ctx, w := newJSONCtx(t, `{"token":"abc1234"}`)

	if err := r.OnResponse(ctx); err != nil {
		t.Fatalf("OnResponse: %v", err)
	}
	if !strings.Contains(w.Body.String(), `"token":"****"`) {
		t.Errorf("短字符串应全 ****，实际 %s", w.Body.String())
	}
}

// TestOnResponseNonJSON 非 JSON body → 原样透传，不调 WriteFinal。
func TestOnResponseNonJSON(t *testing.T) {
	r := buildResult(t, "phone", true)
	w := httptest.NewRecorder()
	ctx := &chain.Context{
		RespCode:   200,
		RespHeader: http.Header{"Content-Type": []string{"text/plain"}},
		RespBody:   []byte("hello"),
		RespW:      w,
	}

	if err := r.OnResponse(ctx); err != nil {
		t.Fatalf("OnResponse: %v", err)
	}
	if w.Body.Len() != 0 {
		t.Errorf("非 JSON 不应写响应，实际 %q", w.Body.String())
	}
}

// TestOnResponseNoMaskNoWrap 未配置脱敏且未 wrap：仅重排 JSON 格式。
func TestOnResponseNoMaskNoWrap(t *testing.T) {
	r := buildResult(t, "", false)
	ctx, w := newJSONCtx(t, `{"user":"test"}`)

	if err := r.OnResponse(ctx); err != nil {
		t.Fatalf("OnResponse: %v", err)
	}
	if !strings.Contains(w.Body.String(), `"user":"test"`) {
		t.Errorf("JSON 内容应保留，实际 %s", w.Body.String())
	}
}

// TestExtractCodeMsg 从上游 JSON 自动取 code/msg。
func TestExtractCodeMsg(t *testing.T) {
	r := buildResult(t, "", true)
	ctx, w := newJSONCtx(t, `{"code":1001,"msg":"rate limited","data":"x"}`)

	if err := r.OnResponse(ctx); err != nil {
		t.Fatalf("OnResponse: %v", err)
	}
	got := w.Body.String()
	if !strings.Contains(got, `"code":1001`) {
		t.Errorf("应沿用上游 code，实际 %s", got)
	}
	if !strings.Contains(got, `"msg":"rate limited"`) {
		t.Errorf("应沿用上游 msg，实际 %s", got)
	}
}
