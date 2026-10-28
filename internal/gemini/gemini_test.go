// gemini_test.go 假上游回归测试：**全程 httptest，不发任何真实网络请求**
// （AGENTS.md §0.1：验证上游行为一律最小化请求数，本文件连一次都不打）。
//
// 覆盖本渠道每一条"容易写错"的协议事实：
//   - 身份五头齐全、**不带** x-goog-api-key / Accept；
//   - 信封**逐层字母序**序列化（逐字节断言，含 tools schema 与 args 的键序）；
//   - 档位后缀是准入钥匙（`gemini-3.8-flash-<tier>`）、tiered 不发 thinkingBudget；
//   - SSE：单行 data、两种载荷形态（信封 / 裸 Response）、`data: [DONE]` 收尾、
//     思考走 reasoning_content、截断尾帧不丢 finishReason、空响应抛错；
//   - 配额是**窗口百分比**（只认两个 bucketId、resetTime 不可解析则整条丢弃）；
//   - project 探测（固定走 sandbox、探测失败不发推理、真值回写 Extra）；
//   - Classify 判据顺序（429 先于 quota 文案）；
//   - RefreshToken 带 client_secret 且轮换 refresh_token 必须回写。
package gemini

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"

	"strings"
	"testing"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// ---------------------------------------------------------------------------
// 测试脚手架
// ---------------------------------------------------------------------------

// testAuth 构造一个"已有 project、不需探测"的账号（避免测试里意外多发一次探测请求）。
func testAuth() *auth.Auth {
	return &auth.Auth{
		Kind:         Kind.String(),
		AccessToken:  "ya29.FAKE_access_for_test",
		RefreshToken: "1//FAKE_refresh_for_test",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		UID:          "sub-fake-1",
		Nickname:     "tester@example.com",
		Extra:        map[string]string{ExtraKeyProject: "aicode-consumers"},
	}
}

// newTestClient 起一个假上游，所有端点都指向它。
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewWithBase(srv.URL)
}

// sse 把若干载荷拼成合法的 SSE 文本（每帧单行 `data:` + 空行，协议 §2.5 的形态）。
func sse(payloads ...string) string {
	var b strings.Builder
	for _, p := range payloads {
		b.WriteString("data: ")
		b.WriteString(p)
		b.WriteString("\n\n")
	}
	return b.String()
}

// frames 解析 OpenAI SSE 文本里的所有 data 载荷（跳过 [DONE]）。
func frames(t *testing.T, out string) []map[string]any {
	t.Helper()
	var got []map[string]any
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			t.Fatalf("帧不是合法 JSON：%v（%s）", err, payload)
		}
		got = append(got, obj)
	}
	return got
}

// tail 返回字符串最后 n 个字符（用于错误信息里展示流的结尾）。
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ---------------------------------------------------------------------------
// 出站：身份头 + 字母序信封
// ---------------------------------------------------------------------------

// envelopeInput 固定输入：system + user + 一个带 schema 的工具 + temperature/max_tokens。
const envelopeInput = `{"model":"gemini/gemini-3.8-flash","reasoning_effort":"low","max_tokens":123,` +
	`"temperature":0.7,"messages":[{"role":"system","content":"S"},{"role":"user","content":"hi"}],` +
	`"tools":[{"type":"function","function":{"name":"f","description":"d",` +
	`"parameters":{"type":"object","properties":{"b":{"type":"string"},"a":{"type":"integer"}},` +
	`"required":["a"],"additionalProperties":false}}}]}`

// ---------------------------------------------------------------------------
// 入站 SSE
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 配额窗口
// ---------------------------------------------------------------------------

// TestQuotaPercentParsing 配额是**窗口剩余百分比**：只认两个 bucketId、
// resetTime 不可解析则整条丢弃、**必须**带 project。
func TestQuotaPercentParsing(t *testing.T) {
	var (
		gotBody    []byte
		gotHdr     http.Header
		quotaCalls int
	)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case EpQuotaSummary:
			quotaCalls++
			gotHdr = r.Header.Clone()
			gotBody, _ = io.ReadAll(r.Body)
			_, _ = io.WriteString(w, `{"groups":[`+
				`{"displayName":"Gemini Models","buckets":[`+
				`{"bucketId":"gemini-weekly","window":"weekly","resetTime":"2026-10-20T00:00:00Z","remainingFraction":0.5},`+
				`{"bucketId":"gemini-5h","window":"5h","resetTime":"2026-10-17T15:00:00Z","remainingFraction":0.99},`+
				`{"bucketId":"gemini-5h","window":"5h","resetTime":"not-a-time","remainingFraction":0.1}]},`+
				`{"displayName":"Claude and GPT models","buckets":[`+
				`{"bucketId":"3p-claude","window":"5h","resetTime":"2026-10-17T15:00:00Z","remainingFraction":1}]}]}`)
		case EpLoadCodeAssist:
			_, _ = io.WriteString(w, `{"cloudaicompanionProject":"aicode-consumers"}`)
		default:
			t.Errorf("意外的出站路径：%s", r.URL.Path)
		}
	})

	remain, items, err := c.UserResourceDetail(testAuth())
	if err != nil {
		t.Fatalf("UserResourceDetail 失败：%v", err)
	}
	// 本渠道没有「可消耗积分」：标量恒 0（配额由 InfoOnly 条目承载）。
	if remain != 0 {
		t.Errorf("UserResource 应恒 0（本渠道无积分概念），得 %d", remain)
	}
	if len(items) != 2 {
		t.Fatalf("应只认两个 bucketId（3p-* 与 resetTime 不可解析的桶都丢弃），得 %d 条：%+v",
			len(items), items)
	}
	fiveHour, weekly := items[0], items[1]
	if fiveHour.Key != BucketFiveHour || fiveHour.Remain != 99 || fiveHour.Total != 100 || fiveHour.Used != 1 {
		t.Errorf("5 小时窗口条目错误：%+v", fiveHour)
	}
	if fiveHour.Name != "5 小时窗口（%）" {
		t.Errorf("单位必须写进名字（ResourceItem 没有单位字段）：%q", fiveHour.Name)
	}
	if !fiveHour.Usable || !fiveHour.InfoOnly {
		t.Errorf("配额条目应 Usable=true（真实可用）+ InfoOnly=true（不是积分，不参与小计）：%+v", fiveHour)
	}
	// resetTime 2026-10-17T15:00:00Z 换算到 UTC+8 墙钟 = 23:00。
	if fiveHour.ExpireAt != "2026-10-17T23:00:00+08:00" {
		t.Errorf("ExpireAt 应按 UTC+8 墙钟：%q", fiveHour.ExpireAt)
	}
	if weekly.Key != BucketWeekly || weekly.Remain != 50 {
		t.Errorf("周窗口条目错误：%+v", weekly)
	}
	// ⚠️ 两个百分比相加**无意义**，故 Summarize 的可消耗小计必须为 0（不得显示 198 积分）。
	if usable, unusable := provider.Summarize(items); usable != 0 || unusable != 0 {
		t.Errorf("InfoOnly 条目不得进小计：usable=%d unusable=%d", usable, unusable)
	}
	if expiring := provider.ExpiringWithin(items, 24*time.Hour); expiring != 0 {
		t.Errorf("InfoOnly 条目不得进临期统计：%d", expiring)
	}

	// 请求体：字段名必须是 `project`（不是 cloudaicompanionProject）。
	var probe map[string]string
	if err := json.Unmarshal(gotBody, &probe); err != nil {
		t.Fatalf("配额请求体不是 JSON 对象：%s", gotBody)
	}
	if probe["project"] != "aicode-consumers" {
		t.Errorf("配额请求体 project=%q（body=%s）", probe["project"], gotBody)
	}
	if _, has := probe["cloudaicompanionProject"]; has {
		t.Errorf("字段名必须是 project：%s", gotBody)
	}
	// 身份头与推理**完全一致**。
	if gotHdr.Get("User-Agent") != IdentityUserAgent || gotHdr.Get("X-Machine-Id") != IdentityMachineID {
		t.Errorf("配额请求缺身份头：%v", gotHdr)
	}
	if quotaCalls != 1 {
		t.Fatalf("首次应恰好查一次配额，得 %d", quotaCalls)
	}
	// 第二次调用命中 60s 缓存 → **配额**不再打上游（档位探测仍会发，故只断言配额计数）。
	if _, _, err := c.UserResourceDetail(testAuth()); err != nil {
		t.Fatalf("第二次 UserResourceDetail 失败：%v", err)
	}
	if quotaCalls != 1 {
		t.Errorf("TTL 内应命中缓存，配额请求次数 %d", quotaCalls)
	}
}

// ---------------------------------------------------------------------------
// project 探测
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 分类与续期
// ---------------------------------------------------------------------------

// TestClassify 判据顺序：429 先于 quota 文案（不变量 15）；403/404 归 SERVER（协议 §2.5）。
func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   provider.ErrKind
	}{
		{"429 优先于 quota 文案", 429, `{"error":{"message":"quota exceeded"}}`, provider.ErrSoftRate},
		{"401 会话失效", 401, `{"error":{"message":"unauthenticated"}}`, provider.ErrSessionDead},
		{"403 归 SERVER", 403, `{"error":{"status":"PERMISSION_DENIED","details":[{"reason":"SUBSCRIPTION_REQUIRED"}]}}`, provider.ErrServer},
		{"404 归 SERVER", 404, `{"error":{"code":404,"status":"NOT_FOUND"}}`, provider.ErrServer},
		{"上下文超限", 400, `{"error":{"message":"The input token count exceeds the maximum number of tokens allowed 1048576"}}`, provider.ErrPromptTooLong},
		{"配额耗尽", 400, `{"error":{"message":"resource_exhausted: quota exceeded"}}`, provider.ErrHardCredit},
		{"5xx", 503, `{"error":{"message":"backend down"}}`, provider.ErrServer},
		{"本地 bad_params", 400, `{"error":{"code":"bad_params"}}`, provider.ErrBadParams},
		{"本地 prompt_too_long", 400, `{"error":{"code":"prompt_too_long"}}`, provider.ErrPromptTooLong},
		{"其它 4xx", 400, `{"error":{"message":"bad field"}}`, provider.ErrClient},
		{"成功", 200, `{}`, provider.ErrNone},
	}
	for _, tc := range cases {
		if got := Classify(tc.status, tc.body); got != tc.want {
			t.Errorf("%s：Classify(%d)=%s，期望 %s", tc.name, tc.status, got, tc.want)
		}
	}
}

// TestRefreshTokenTerminalIsSessionDead 终态错误必须是 *provider.Error{ErrSessionDead}
// （上层 errors.As 依赖，不变量 19/23）。
func TestRefreshTokenTerminalIsSessionDead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
	}))
	defer srv.Close()
	c := New()
	c.TokenURL = srv.URL
	err := c.RefreshToken(testAuth())
	var ue *provider.Error
	if !errors.As(err, &ue) || ue.Kind != provider.ErrSessionDead {
		t.Fatalf("期望 *provider.Error{ErrSessionDead}，得 %#v", err)
	}
}

// TestRefreshTokenKeepsOldRefreshTokenWhenAbsent 响应不带 refresh_token ⇒ 回退旧值
// （Google 只在轮换时才给新的，不给就必须沿用，否则下次续期必失败）。
func TestRefreshTokenKeepsOldRefreshTokenWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"ya29.NEW","expires_in":3600}`)
	}))
	defer srv.Close()
	c := New()
	c.TokenURL = srv.URL
	a := testAuth()
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("RefreshToken 失败：%v", err)
	}
	if a.RefreshToken != "1//FAKE_refresh_for_test" {
		t.Errorf("缺 refresh_token 时须沿用旧值，得 %q", a.RefreshToken)
	}
}

// ---------------------------------------------------------------------------
// 静态表与其它 provider.Upstream 空能力
// ---------------------------------------------------------------------------

// TestStaticModelsAndNoCheckin 静态表只 1 条 + 四档；签到必须返回明确错误
// （返回 nil 会把缺陷伪装成「今日已签到」）。
func TestStaticModelsAndNoCheckin(t *testing.T) {
	ms := StaticModels()
	if len(ms) != 1 {
		t.Fatalf("静态表应只有 1 条（不暴露带后缀的名字与 lite），得 %d 条", len(ms))
	}
	m := ms[0]
	if m.ID != ModelFlash || m.ContextWindow != ContextWindow || m.MaxTokens != MaxOutputTokens {
		t.Errorf("静态表条目错误：%+v", m)
	}
	if !m.SupportsImages || !m.SupportsReasoning || !m.SupportsTools {
		t.Errorf("能力声明错误：%+v", m)
	}
	if len(m.ReasoningEfforts) != 4 || m.DefaultReasoningEffort != TierMedium {
		t.Errorf("档位表错误：%v / %q", m.ReasoningEfforts, m.DefaultReasoningEffort)
	}
	if m.CanDisableThinking == nil || *m.CanDisableThinking {
		t.Errorf("「关闭思考」是假关 ⇒ 必须显式声明不可关闭：%v", m.CanDisableThinking)
	}
	if m.ContextFromAPI {
		t.Error("静态表不是上游目录：ContextFromAPI 必须为 false")
	}
	c := &Client{}
	if err := c.DailyCheckin(nil); err == nil || !strings.Contains(err.Error(), "没有签到机制") {
		t.Errorf("本渠道无签到，必须返回明确错误：%v", err)
	}
	if models, err := c.FetchModels(nil); err != nil || len(models) != 1 {
		t.Errorf("FetchModels 应返回静态表且不报错：%v %v", models, err)
	}
	if pricing, err := c.FetchModelPricing(nil); err != nil || len(pricing) != 0 {
		t.Errorf("无费率端点时应返回空切片（面板显示未知，而非免费）：%v %v", pricing, err)
	}
	// 接口实现自检：Client 必须满足 provider.Upstream（10 个方法）。
	var _ provider.Upstream = (*Client)(nil)
}

// ---------------------------------------------------------------------------
// 图片与工具配对
// ---------------------------------------------------------------------------
