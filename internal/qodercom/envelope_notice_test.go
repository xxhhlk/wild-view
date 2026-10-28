// envelope_notice_test.go 锁定 qodercom 的「HTTP 200 里包着业务错误」帧识别。
//
// 生产事故（2026-10-20 21:09/21:12）：qodercom/deepseek-flash 请求卡死 180.2/180.4s，
// 日志 err=upstream stream truncated: unexpected EOF，客户端停在「正在等待模型…」。
// 抓上游原始帧（单发实测）：
//
//	data:{"headers":{"Content-Type":["application/json"]},
//	      "body":"{\"code\":\"112\",\"message\":\"{\\\"pricingUrl\\\":\\\"https://qoder.com/pricing?client=qoder\\\"}\"}",
//	      "statusCodeValue":403,"statusCode":"FORBIDDEN"}
//
// 另一形态是 code 110「Billing daily count exceeded」。两者都是**账号级额度耗尽**，
// 证据链：① A/B 单发（同号同 body，只换模型）deepseek-flash 与 glm-5.3-flash **都回 112**
// ⇒ 不是模型级；② 同时刻 /api/v2/quota/usage 五个号全部 isQuotaExceeded=true、remaining=0。
//
// 本文件锁定：
//  1. 110/112/pricingUrl/isQuotaExceeded → ErrHardCredit（整号硬冷却 + 换号），不是排队；
//  2. 10605/isQueued → *provider.Queued（只冷「号×模型」）；
//  3. 这类帧必须在写出任何字节之前上抛（否则客户端只收到空 delta）；
//  4. 正常 delta（带 choices）不受影响。
package qodercom

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

// envelopeFrame 构造与生产同形的 envelope 帧：外层 200 外壳 + 内层业务错误字符串。
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

// TestClassifyQuotaCodes 4xx 直发的同类 body 也要归 ErrHardCredit（不只是 envelope 形态）。
func TestClassifyQuotaCodes(t *testing.T) {
	if got := Classify(403, `{"code":"112","message":"{}"}`); got != provider.ErrHardCredit {
		t.Fatalf("code 112 应归 ErrHardCredit，实际 %v", got)
	}
	if got := Classify(403, `{"code":"110","message":"Billing daily count exceeded"}`); got != provider.ErrHardCredit {
		t.Fatalf("code 110 应归 ErrHardCredit，实际 %v", got)
	}
	if got := Classify(403, "blocked by security policy"); got != provider.ErrContentBlocked {
		t.Fatalf("普通内容拦截不应被误判，实际 %v", got)
	}
}
