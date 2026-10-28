// checkin.go 智谱清言的签到能力声明。
//
// 上游签到与对话联动：签到页文案为「对话后记得回来领取打卡进度」，
// 即当日必须先与清言对话才能领取。本工具不产生对话，因此该渠道
// 不提供签到能力，由 provider.SupportsPureCheckin 排除。
package glm

import (
	"errors"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// errCheckinUnsupported 本渠道签到依赖对话，查看器不提供。
var errCheckinUnsupported = errors.New("glm：签到依赖对话，当前不支持")

// DailyCheckin 本渠道签到依赖对话，不支持。
func (c *Client) DailyCheckin(_ *auth.Auth) error {
	return errCheckinUnsupported
}

// DailyCheckinReport 本渠道签到依赖对话，不支持。
func (c *Client) DailyCheckinReport(_ *auth.Auth) (provider.CheckinReport, error) {
	return provider.CheckinReport{Status: provider.CheckinError, Msg: errCheckinUnsupported.Error()}, errCheckinUnsupported
}
