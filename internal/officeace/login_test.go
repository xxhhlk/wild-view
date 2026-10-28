// login_test.go 自助 OAuth 登录的**离线**回归测试：全程零真实网络。
//
// 三个上游（agentarts 控制面 / 华为 STS / 授权页）全部指向 httptest；
// 历史教训见文件末尾各用例注释——每一条都对应一个「用户点了新增账号却不成功」的真实断点。
package officeace

import (
	"net/http/httptest"
	"net/url"

	"sync"
)

// ---------------------------------------------------------------------------
// 假上游
// ---------------------------------------------------------------------------

// fakeUpstream 是 AgentArts 控制面 + 华为 STS 的最小仿真。
// 它**按华为云真实契约**回应（而不是按本仓旧的错误假设），用于锁定客户端侧的一致性。
type fakeUpstream struct {
	*httptest.Server

	mu sync.Mutex

	// state 端点
	stateStatus int
	stateBody   string
	stateCalls  int

	// code 端点（轮询）
	codePending   int // 前 N 次回 202
	codeHTTP      int // 非 0 时覆盖：直接回这个状态码
	codeBody      string
	codeCalls     int
	codeQueries   []string
	codeGetField  string // 非 "" 时用 {"authorizationCode": ...} 这种驼峰字段回应
	codeAuthzCode string

	// STS 端点
	stsStatus    int
	stsBody      string
	stsForm      url.Values
	stsDPoP      string
	stsCalls     int
	stsDpopTries int

	// validate 端点
	valStatus int
	valBody   string
	valCalls  int
	valAuth   string
}

// pointLoginAtProduction 把三个上游地址还原成生产值（t.Cleanup 兜底，防串测）。
func pointLoginAtProduction() {
	agentartsBase = productionAgentartsBase
	stsTokenURL = productionStsTokenURL
	authBase = productionAuthBase
}

// pointLoginAt 把登录链路的三个上游全部指向假服务。
func (f *fakeUpstream) pointLoginAt() {
	agentartsBase = f.URL
	stsTokenURL = f.URL + "/v1/oauth2/tokens"
	authBase = f.URL
}

func (f *fakeUpstream) counts() (state, code, sts, val int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stateCalls, f.codeCalls, f.stsCalls, f.valCalls
}

// ---------------------------------------------------------------------------
// 正常路径：7 步全部走通
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 缺陷 A（本轮实测复现）：code 端点响应字段大小写不一致 ⇒ 永远取不到 code
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 缺陷 B（本轮实测复现）：run() 空转 —— 没有 sleep、fails 不增长
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 缺陷 C（本轮修复）：会话终态后上一轮「幽灵会话」卡住后续新增
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 缺陷 D：上游 4xx 必须如实透传（不能变成「未知失败」）
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 取消：不得落盘、不得调 onDone
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 授权 URL 契约
// ---------------------------------------------------------------------------
