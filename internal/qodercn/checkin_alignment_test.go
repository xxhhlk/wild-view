// checkin_alignment_test.go QoderCN 与 QoderCOM 签到解析层「对齐」的回归测试。
//
// 背景：本仓对 Qoder 三渠道刻意采用「代码级复制、不共享运行时」的约定，
// 于是两区解析层曾经分叉（COM 改了、CN 落后）。本文件把对齐后的三项契约钉死：
//  1. 线上实际形态（扁平字符串 claimStatus）正常工作；
//  2. 对象形态 {"status":…} 作为**防御性**兼容仍被接受（无线上证据）；
//  3. 未知形态绝不误领，且 no_campaign 的文案带上游响应摘要，能区分
//     「上游没发活动」与「本工具解析没懂」。
package qodercn

import (
	"net/http"
	"net/http/httptest"

	"testing"
)

// withCNCheckinServer 起假上游并把 checkinHost 指过去（与 qodercn_test.go 同款收尾）。
func withCNCheckinServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	old := checkinHost
	checkinHost = srv.URL
	t.Cleanup(func() { checkinHost = old })
}
