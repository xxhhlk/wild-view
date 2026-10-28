// checkin_variant_test.go QoderCOM 签到「列表响应变体」回归测试。
//
// 取证来源（离线，未打上游）：本机 Qoder 桌面端日志
// %APPDATA%\com.qoder.app.stable\logs\<session>\main.log 的
// "[Campaign] 活动状态请求返回" 行，2026-10-17 07:47:46Z 起连续三次
// GET /sash/api/v1/me/campaigns 的 payload 一致为：
//
//	{"showCampaign":true,"claimable":true,
//	 "campaigns":[{"campaignId":"01a130ef-…","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMABLE", …},
//	              {"campaignId":"01a05bce-…","actionType":"VIEW_DETAILS","claimStatus":"CLAIMED"}]}
//
// 同一账号在 07:29/07:59 的自查余额恒为 100。wild-work 的 app.log 在该账号上
// 3537 次签到全部落到 no_campaign，从未出现 claimed —— 说明解析层把「可领取」
// 判成了「无活动」。本文件用上述真实 payload 形状驱动 httptest 假上游，锁定该契约。
package qodercom

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"testing"
	"time"
)

// desktopCampaignsPayload 桌面端 main.log 中裁剪出的真实响应（字段形状原样保留）。
// 关键点：claim 状态在**嵌套对象** claimStatus:{status:…} 里，不是扁平字符串。
//
// ⚠️ 活动窗口用**相对时间**（昨天→明天）而非抓包原值：R95 起签到按活动有效窗口判定，
// 写死的时间戳过几天就会变成「已结束」→ 本用例会退化成时间炸弹（2026-10-21 实测踩到）。
var desktopCampaignsPayload = fmt.Sprintf(`{
  "uid": "01a05725-40bb-74e2-8534-858686c422c6",
  "showCampaign": true,
  "claimable": true,
  "campaignUrl": "https://openapi.qoder.sh/growth-page/activity-iframe",
  "campaigns": [
    {
      "campaignId": "01a130ef-2c3d-7ee6-a290-90c3207b536d",
      "campaignKey": "act-20261013-119",
      "actionType": "CLAIM_BENEFIT",
      "startAt": %d,
      "endAt": %d,
      "claimStatus": {"status": "CLAIMABLE"},
      "benefit": {"kind": "CREDITS", "amount": 100,
                  "validity": {"mode": "RELATIVE_DAYS", "days": 30}},
      "placements": [{"type": "POPUP", "campaignUrl": "https://openapi.qoder.sh/growth-page/activity-iframe"}]
    },
    {
      "campaignId": "01a05bce-e800-7494-a002-806e4438f483",
      "campaignKey": "act-20260901-493",
      "actionType": "VIEW_DETAILS",
      "startAt": %d,
      "endAt": %d,
      "claimStatus": {"status": "CLAIMED"},
      "placements": [{"type": "POPUP", "campaignUrl": "https://openapi.qoder.sh/growth-page/activity-iframe"}]
    }
  ]
}`,
	time.Now().Add(-24*time.Hour).Unix(), time.Now().Add(24*time.Hour).Unix(),
	time.Now().Add(-48*time.Hour).Unix(), time.Now().Add(48*time.Hour).Unix())

// desktopClaimOkPayload claim 接口的成功回执（桌面端同族字段形状；benefit 为嵌套对象）。
const desktopClaimOkPayload = `{"campaignId":"01a130ef-2c3d-7ee6-a290-90c3207b536d","status":"CLAIMED","replayed":false,"benefit":{"kind":"CREDITS","amount":100,"validity":{"mode":"RELATIVE_DAYS","days":30}}}`

// withCheckinServer 起一个假上游并把 checkinHost 指过去；handler 收到未知路径即失败。
func withCheckinServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	old := checkinHost
	checkinHost = srv.URL
	t.Cleanup(func() { checkinHost = old })
}
