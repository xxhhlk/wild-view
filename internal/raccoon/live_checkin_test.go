//go:build live

// live_checkin_test.go 小浣熊签到（setting_info）实盘探针。
//
// 背景：签到接入（R65）的核心判据是「调 setting_info 会触发每日积分惰性落账」，
// 这是 2026-10-15 从三账号对照实验得出的结论，本探针把它固化为可重跑的实盘验证。
//
// ⚠️ 本探针**只读**：不 refresh、不落盘、不改账号文件（与 live_probe_test.go 不同）。
// 唯一副作用是签到本身——即上游的每日积分落账（这正是验证目标）。
//
// 运行方式（仓库根目录）：
//
//	WILDWORK_AUTHDIR=~/Library/Application\ Support/WildWork/auths \
//	  go test -tags live ./internal/raccoon/ -run TestLiveCheckin -v -timeout 300s
//
// 验证三件事：
//
//  1. setting_info 可调通（HTTP 200 + code 0 + available_points 可读）；
//  2. 幂等语义：连调两次，第二次的 point_grant_popups 必为空
//     （若非空 ⇒ 上游改成了「每次调用都发放」= 严重问题，必须立刻发现）；
//  3. 状态判定与余额一致性：claimed 时 Amount 应与 bills 的最新 daily_grant 吻合。
package raccoon

import (
	"testing"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// TestLiveCheckinSessionDeadShape 守门：用坏 token 调 setting_info 必须是 401 +
// 类型化 ErrSessionDead（调度器自愈依赖它，不变量 19）。
// 这是唯一会「故意失败」的实盘用例，不需要有效账号。
func TestLiveCheckinSessionDeadShape(t *testing.T) {
	c := New()
	bad := &auth.Auth{AccessToken: "invalid.token.for.probe", UID: "probe"}
	_, err := c.DailyCheckinReport(bad)
	if err == nil {
		t.Fatal("坏 token 应返回错误")
	}
	var pe *provider.Error
	if !asProviderError(err, &pe) {
		t.Fatalf("错误必须是 *provider.Error，实际 %T: %v", err, err)
	}
	if pe.Kind != provider.ErrSessionDead {
		t.Fatalf("Kind = %v, want session_dead（调度器 errors.As 自愈依赖）", pe.Kind)
	}
	t.Logf("✅ 坏 token 正确落 session_dead: %s", pe.Msg)
}
