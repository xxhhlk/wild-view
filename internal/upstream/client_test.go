package upstream

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   ErrKind
	}{
		{402, ``, ErrHardCredit},
		{400, `{"code":1,"msg":"余额不足"}`, ErrHardCredit},
		{403, `insufficient credits`, ErrHardCredit},
		{200, `{"code":10001,"msg":"积分不足，请充值"}`, ErrHardCredit},
		{400, `{"code":1,"msg":"额度用尽"}`, ErrHardCredit},
		{429, ``, ErrSoftRate},
		// ——— 429/401 分类（本仓与上游 v2.9.0 并集）———
		// 429 + 业务码 14018 = 账号积分耗尽（上游把余额耗尽也走 429 返回）→ 硬冷却弃号。
		{429, `{"code":14018,"msg":"积分耗尽"}`, ErrHardCredit},
		{429, `{"code": 14018, "msg": "credit exhausted"}`, ErrHardCredit},
		// 嵌套形态（codebuddy/workbuddyai 生产实录；本渠道预防性同构特判）：误判软限流
		// 只冷 60s，降级链永不触发。
		{429, `{"error":{"data":{"code":14018,"msg":"Credits exhausted. Please visit the link below to purchase add-on packs and get more credits: https://www.codebuddy.ai/profile/usage ","requestId":"x"}}}`, ErrHardCredit},
		// 匹配防前缀误伤（S2）：code 140180 含子串 "code":14018，须保持软限流；
		// 尾形（code 后紧跟 }，无 msg）是真 14018，须命中。
		{429, `{"error":{"data":{"code":140180,"msg":"future code"}}}`, ErrSoftRate},
		{429, `{"error":{"data":{"code":14018}}}`, ErrHardCredit},
		// 429 无 14018 时仍是软限流：限流 body 高频带 "quota exceeded" 这类跨计费/限流
		// 两界的措辞，靠文案判会把限流误归硬冷却、白扔号约 12h（429 前置的原因）。
		{429, `{"code":10001,"msg":"积分不足"}`, ErrSoftRate},
		{429, `{"code":9999,"msg":"quota exceeded"}`, ErrSoftRate},
		// 回归：6004 模型级限流 / 14003 过频（构造，未实证）均保持软限流。
		{429, `{"code":6004,"msg":"usage exceeds frequency limit ... will reset at ..."}`, ErrSoftRate},
		{429, `{"code":14003,"msg":"too many requests"}`, ErrSoftRate},
		{429, `quota exceeded`, ErrSoftRate},
		{401, `Offline user session not found`, ErrSessionDead},
		{401, `{"code":12153,"msg":"Offline user session not found"}`, ErrSessionDead},
		// 401 统一归登录态失效（#134）：上游拒绝我们的凭据 = 需重新登录
		{401, `{"code":9999,"msg":"bad token"}`, ErrSessionDead},
		// 图片格式/数据无效：请求内容决定，换号结果不变 → 请求级错误（不罚号不轮转）。
		// 第一条的 code 也是 11101，必须判成 ErrImageInvalid 而不是 ErrBadParams。
		{400, `{"code":11101,"msg":"Parse message failed: invalid image_url content at index 2: json: cannot unmarshal string into Go value of type v2.ImageContent"}`, ErrImageInvalid},
		{400, `{"code":11135,"msg":"invalid_image_data"}`, ErrImageInvalid},
		{400, `invalid_image_data`, ErrImageInvalid},
		{400, `{"code": 11135, "msg": "image rejected"}`, ErrImageInvalid},
		{400, `{"code": "11135", "msg": "image rejected"}`, ErrImageInvalid},
		{400, `{"error": {"code": 11135, "message": "image rejected"}}`, ErrImageInvalid},
		{400, `{"code": 11133, "msg": "other business error"}`, ErrClient},
		{400, `{"code": 111350, "msg": "longer code must not hit 11135"}`, ErrClient},
		// 11101 出站 body 畸形：请求级错误（原为 ErrClient，会被 NoteError 罚号）。
		{400, `{"code":11101,"msg":"Unmarshal chat params failed"}`, ErrBadParams},
		{400, `Unmarshal chat params failed`, ErrBadParams},
		// 11115 的 JSON 空白/引号容差（原字面量 marker 只认紧凑形态）。
		{400, `{"code": 11115, "msg": "prompt is too long"}`, ErrPromptTooLong},
		{400, `{"code":"11115"}`, ErrPromptTooLong},
		{404, `{"code": 11115}`, ErrPromptTooLong},
		// 会话头族缺失（11128 conversation id missing）= 上游**按账号灰度**的渠道校验，
		// 归账号级（软冷却 + 换号，见 R116）；同一个 11128 的另一形态（内容指纹
		// unapproved channel）才是请求级问题，归内容拦截不罚号。
		{400, `{"code":11128,"msg":"conversation id missing","requestId":"x"}`, ErrAccountFault},
		{400, `{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`, ErrContentBlocked},
		{500, `boom`, ErrServer},
		{503, `unavailable`, ErrServer},
		{200, ``, ErrNone},
	}
	for _, c := range cases {
		if got := Classify(c.status, c.body); got != c.want {
			t.Errorf("Classify(%d,%q)=%v want %v", c.status, c.body, got, c.want)
		}
	}
}

// TestCodeMarkerTolerance 业务码判定必须容忍 JSON 空白与引号形态，且不得前缀误命中。
//
// 背景：原实现用字面量 strings.Contains(body, `"code":11115`)，上游一旦返回
// `{"code": 11115}`（美化输出）就漏判 → 请求级错误退化成 ErrClient 并被 NoteError 罚号。
func TestCodeMarkerTolerance(t *testing.T) {
	hit := []string{
		`{"code":11135}`,
		`{"code": 11135}`,
		`{"code":"11135"}`,
		`{"code": "11135"}`,
		`{"code": "11135", "msg":"x"}`,
		`{"error":{"code": 11135}}`,
		`{"code":'11135'}`,
		`prefix {"code": 11135} suffix`,
	}
	miss := []string{
		`{"code":111350}`,   // 更长的码，前缀匹配不得命中
		`{"code":"11135a"}`, // 引号内的更长值
		`{"code": 1113}`,    // 更短
		`{"codex": 11135}`,  // 键名不是 code
		`code 11135`,        // 无引号键
		`{"msg":"11135"}`,   // 只出现在 msg 里
		``,                  // 空 body
	}
	for _, s := range hit {
		if !codeMarker(strings.ToLower(s), "11135") {
			t.Errorf("codeMarker(%q) = false, want true", s)
		}
	}
	for _, s := range miss {
		if codeMarker(strings.ToLower(s), "11135") {
			t.Errorf("codeMarker(%q) = true, want false", s)
		}
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
