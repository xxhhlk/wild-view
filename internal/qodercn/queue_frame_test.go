// queue_frame_test.go 锁定「上游排队帧」的识别语义（2026-10-20 生产事故）。
//
// 背景：qodercn deepseek-flash 在网关侧按模型排队，上游用 **HTTP 200 + 一帧业务错误**
// 下发排队提示（三层嵌套 JSON，内层 code 10605 / isQueued）。旧实现把它当普通 chunk
// 透传 ⇒ 客户端只有一个空 delta，界面停在「正在等待模型…」，直到网关 180s 后掐断
// （实测 180.3/180.7/180.8s 三次），零 token、零提示。
//
// 本文件锁定：
//  1. 排队帧必须在写出**任何字节之前**被识别并上抛 *provider.Queued；
//  2. 解析出的 retryAfter / queueCount / waitTime 必须准确（冷却时长据此决定）；
//  3. 非排队的错误帧（statusCode>=400）同样上抛，不许当 chunk 透传。
package qodercn

import (
	"encoding/json"

	"testing"

	"wild-view/internal/provider"
)

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// queueFrame 构造与生产同形的排队帧（三层套娃 + 外层 headers 带 Retry-After）。
func queueFrame(t *testing.T) string {
	t.Helper()
	queue := mustMarshal(t, map[string]any{
		"isQueued": true, "modelKey": "dfmodel", "queueCount": 8407, "queueType": "p3",
		"retryAfterSeconds": 30, "serviceAvailable": true, "waitTime": 509,
	})
	lvl2 := mustMarshal(t, map[string]any{"code": "10605", "message": queue})
	lvl1 := mustMarshal(t, map[string]any{"code": "403", "message": lvl2})
	env := mustMarshal(t, map[string]any{
		"headers":         map[string][]string{"Content-Type": {"application/json"}, "Retry-After": {"30"}},
		"body":            lvl1,
		"statusCodeValue": 403,
		"statusCode":      "FORBIDDEN",
	})
	return "data:" + env + "\n\n"
}

// TestClassifyQueueBody 上游若改用 4xx + 排队 body（另一种下发形态），
// Classify 也必须归到 ErrQueued，否则会被当普通 403/429 罚整号。
func TestClassifyQueueBody(t *testing.T) {
	body := `{"code":"403","message":"{\"code\":\"10605\",\"message\":\"{\\\"isQueued\\\":true}\"}"}`
	if got := Classify(403, body); got != provider.ErrQueued {
		t.Fatalf("10605 排队体应归 ErrQueued，实际 %v", got)
	}
	if got := Classify(403, "blocked by security policy"); got == provider.ErrQueued {
		t.Fatalf("普通 403 不应误判为排队，实际 %v", got)
	}
}
