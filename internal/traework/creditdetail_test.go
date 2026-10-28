package traework

import (
	"testing"
)

// entUsageBody 构造一份 web_user_ent_usage 响应：
//   - product_id=221 每月登录（可用，500 限额已用 40）
//   - product_id=208 每日签到 150 档（可用，150 限额未用）
//   - product_id=209 每日签到 200 档（可用：2026-09-26 起升级为通用积分，issue #44）
//   - ep=1 每日签到（不可用：available_endpoint 历史兜底判据）
//   - 限额 0 的免费包（应被明细过滤掉）
//
// expire_time 用固定值便于断言日期格式化。
const entUsageBody = `{"is_credits_billing":true,"user_entitlement_pack_list":[
 {"display_desc":"每月登录赠送","group_name":"每月登录积分","group_type":3,"expire_time":1790783999,
  "entitlement_base_info":{"available_endpoint":0,"product_id":221,"quota":{"credits_limit":500}},
  "usage":{"credits_amount":40}},
 {"display_desc":"签到奖励","group_name":"每日签到","group_type":1,"expire_time":1789779374,
  "entitlement_base_info":{"available_endpoint":0,"product_id":208,"quota":{"credits_limit":150}},
  "usage":{}},
 {"display_desc":"签到奖励","group_name":"每日签到","group_type":1,"expire_time":1789779374,
  "entitlement_base_info":{"available_endpoint":0,"product_id":209,"quota":{"credits_limit":200}},
  "usage":{}},
 {"display_desc":"签到奖励","group_name":"每日签到","group_type":1,"expire_time":1789779374,
  "entitlement_base_info":{"available_endpoint":1,"product_id":208,"quota":{"credits_limit":300}},
  "usage":{}},
 {"display_desc":"免费","group_type":0,"expire_time":1790783999,
  "entitlement_base_info":{"available_endpoint":0,"product_id":0,"quota":{"credits_limit":0}},
  "usage":{}}
]}`

// TestEntPackageUsable 直接钉住可用性判据：仅 ep=1 不可用（issue #44 撤回 pid==209 判据）。
func TestEntPackageUsable(t *testing.T) {
	cases := []struct {
		name string
		ep   int
		pid  int
		want bool
	}{
		{"每月登录221", 0, 221, true},
		{"150签到208", 0, 208, true},
		{"200签到209已通用", 0, 209, true},
		{"ep1历史兜底", 1, 208, false},
		{"免费订阅0", 0, 0, true},
	}
	for _, c := range cases {
		p := entPackage{}
		p.EntitlementBaseInfo.AvailableEndpoint = c.ep
		p.EntitlementBaseInfo.ProductID = c.pid
		if got := p.usable(); got != c.want {
			t.Errorf("%s usable()=%v want %v", c.name, got, c.want)
		}
	}
}

// item 测试内使用的明细投影。
type item struct {
	remain int64
	expire string
	usable bool
}

// TestPackRemainClampsNegative 上游脏数据（used > limit）剩余应钳 0，不得为负。
func TestPackRemainClampsNegative(t *testing.T) {
	p := entPackage{}
	p.EntitlementBaseInfo.Quota.CreditsLimit = 100
	p.Usage.CreditsAmount = 150
	if got := packRemain(p); got != 0 {
		t.Errorf("packRemain=%d want 0 (钳负)", got)
	}
}
