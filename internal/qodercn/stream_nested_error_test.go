// 流内业务错误（HTTP 200 + 信封内嵌 statusCodeValue>=400）的守门测试（R108）。
// 帧原文取自 2026-10-21 本机抓包 wildwork-dump/qodercn/…-0021-post-….http（不含凭据）。
package qodercn

import (
	"testing"

	"wild-view/internal/provider"
)

// nestedRateLimitFrame 上游真实的排队/限流拒绝帧：HTTP 200，失败信息在信封里。
const nestedRateLimitFrame = `data:{"headers":{"Content-Type":["application/json"]},"body":"{\"code\":\"403\",\"message\":\"{\\\"code\\\":\\\"10605\\\",\\\"message\\\":\\\"{\\\\\\\"isQueued\\\\\\\":true,\\\\\\\"modelKey\\\\\\\":\\\\\\\"dfmodel\\\\\\\",\\\\\\\"queueCount\\\\\\\":0,\\\\\\\"queueType\\\\\\\":\\\\\\\"p3\\\\\\\",\\\\\\\"retryAfterSeconds\\\\\\\":30,\\\\\\\"serviceAvailable\\\\\\\":false,\\\\\\\"subCode\\\\\\\":\\\\\\\"RESOURCE_LIMIT\\\\\\\",\\\\\\\"waitTime\\\\\\\":30}\\\",\\\"subCode\\\":\\\"RESOURCE_LIMIT\\\"}\"}","statusCodeValue":403,"statusCode":"FORBIDDEN"}` + "\n\n"

func TestClassifyResourceLimitIsSoftRate(t *testing.T) {
	// 带队列明细（10605 + isQueued）→ Queued：比 ErrSoftRate 更具体，冷却粒度是
	// 「号×模型」而不是整号（本仓 R109/R111 口径）。
	msg := `{"code":"10605","message":"{\"isQueued\":true,\"modelKey\":\"dfmodel\",\"retryAfterSeconds\":30,\"serviceAvailable\":false,\"subCode\":\"RESOURCE_LIMIT\"}","subCode":"RESOURCE_LIMIT"}`
	if got := Classify(403, msg); got != provider.ErrQueued {
		t.Errorf("Classify(403, 10605/isQueued)=%v 期望 ErrQueued", got)
	}
	// 只有 subCode.RESOURCE_LIMIT、没有队列明细 → 软限流（吸收上游 R108 判据）。
	if got := Classify(403, `{"subCode":"RESOURCE_LIMIT"}`); got != provider.ErrSoftRate {
		t.Errorf("Classify(403, 仅 RESOURCE_LIMIT)=%v 期望 ErrSoftRate", got)
	}
	if got := Classify(403, `{"code":"10605","serviceAvailable":false}`); got != provider.ErrQueued {
		t.Errorf("仅 10605 + serviceAvailable 期望 ErrQueued，实际 %v", got)
	}
}

// TestClassifyNotFooedByDigitsInIDs 负例：request_id/trace_id 里含 "10605" 五个数字
// 不得被判成限流（裸 Contains 的历史教训，与 11115 同源）。
func TestClassifyNotFooedByDigitsInIDs(t *testing.T) {
	body := `{"error":"boom","request_id":"a10605b","trace_id":"e75b73939da3c10605123"}`
	if got := Classify(403, body); got == provider.ErrSoftRate {
		t.Fatal("无关文本里的 10605 数字串被误判成限流")
	}
	if got := Classify(400, body); got == provider.ErrSoftRate {
		t.Fatal("无关文本里的 10605 数字串被误判成限流(400)")
	}
}
