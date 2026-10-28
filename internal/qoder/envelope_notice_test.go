// envelope_notice_test.go 锁定 qoder（QoderWork）的「HTTP 200 里包着业务错误」帧识别。
//
// 三渠道（qoder / qodercn / qodercom）的 sse.go 是同源代码复制，本渠道此前也没有
// 帧识别 ⇒ 上游拒绝服务时会照常 200 开流、把错误塞进一帧没有 choices 的 envelope，
// 然后挂住不关连接：客户端只收到一个空 delta，界面停在「正在等待模型…」直到 180s
// 后网关掐断（qodercom 实测 180.2/180.4s，同一套外壳）。
//
// 本渠道**各写一份实现、不共享 qodercom 的代码**（R58：本仓渠道包历来不共享 SSE）。
package qoder

import (
	"encoding/json"

	"testing"

	"wild-view/internal/provider"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func envelopeFrame(t *testing.T, inner string, statusCode int, headers map[string][]string) string {
	t.Helper()
	env := mustJSON(t, map[string]any{
		"headers":         headers,
		"body":            inner,
		"statusCodeValue": statusCode,
		"statusCode":      "FORBIDDEN",
	})
	return "data:" + env + "\n\n"
}

// TestClassifyQuotaCodes 4xx 直发的同类 body 也要归 ErrHardCredit。
func TestClassifyQuotaCodes(t *testing.T) {
	if got := Classify(403, `{"code":"112","message":"{}"}`); got != provider.ErrHardCredit {
		t.Fatalf("code 112 应归 ErrHardCredit，实际 %v", got)
	}
	if got := Classify(403, `{"code":"110","message":"Billing daily count exceeded"}`); got != provider.ErrHardCredit {
		t.Fatalf("code 110 应归 ErrHardCredit，实际 %v", got)
	}
}
