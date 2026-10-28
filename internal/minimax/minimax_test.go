// minimax_test.go 假上游回归测试：**全程 httptest，不发任何真实网络请求**。
//
// 覆盖协议文档里标注为「坑」的每一条：
//   - 出站体形状（顶层 system、stream 恒 true、tools 形状、有值才带的字段）；
//   - thinking 决策表（M3.1 强制 adaptive、M2.7 不发 thinking、switchable 的 on/none）；
//   - 消息序列化（tool_use/tool_result 成对、孤儿两侧都剔除、不跨 assistant 边界累积、
//     reasoning 不回传、图片裸 base64）；
//   - 入站 SSE（signature_delta 忽略、error 帧抛错、截断尾行不丢 message_delta、
//     thinking→reasoning_content、空响应抛错）；
//   - 目录解析（长名/短名/最大档窗口/model_order/兜底）；
//   - 积分求和（字符串金额）、签到幂等（claim_result=2）、续期（invalid_grant）；
//   - Classify（402/429/5xx/401/403/2013）。
package minimax

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// ---------------------------------------------------------------------------
// 测试脚手架
// ---------------------------------------------------------------------------

// newTestClient 起一个假上游，Base 与 Account 都指向它（本包没有任何真实出站请求）。
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewWithBase(srv.URL)
	c.Account = srv.URL
	return c
}

func testAuth() *auth.Auth {
	return &auth.Auth{
		Kind:         "minimax",
		AccessToken:  "mmoat_FAKE_access_for_test",
		RefreshToken: "mmort_FAKE_refresh_for_test",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	}
}

// sse 把若干 JSON 事件拼成合法的 SSE 文本（每帧带空行分隔）。
func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: ")
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 出站体形状
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 消息序列化（协议 §4.2）
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 入站 SSE（协议 §5）
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 模型目录（协议 §6）
// ---------------------------------------------------------------------------

const catalogFixture = `{
  "providers": [
    {"providerId": "other", "config": {"models": {"Nope": {"name": "nope"}}}},
    {"providerId": "minimax", "config": {
      "models": {
        "MiniMax-M3.1-Flash-Preview": {
          "name": "M3.1-Flash-Preview",
          "limit": {"context": 512000, "output": 128000},
          "context_window_options": [512000, 1000000],
          "modalities": {"input": ["text", "image"]},
          "effort_options": ["default", "low", "low", "max"],
          "default_effort": "low",
          "thinking_config": {"mode": "forced_on"}
        },
        "MiniMax-M3": {
          "name": "M3",
          "limit": {"context": 1000000},
          "modalities": {"input": ["text"]},
          "thinking_config": {"mode": "switchable"}
        },
        "BrokenModel": {"limit": {"output": 0}, "default_effort": "nope"}
      },
      "model_order": ["MiniMax-M3", "MiniMax-M3.1-Flash-Preview"]
    }}
  ]
}`

func TestParseCatalog(t *testing.T) {
	entries, err := parseCatalog([]byte(catalogFixture))
	if err != nil {
		t.Fatalf("parseCatalog: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("条目数 = %d，期望 3", len(entries))
	}
	// model_order 说的两个优先，未列出的按插入序追加在后。
	if entries[0].ID != "MiniMax-M3" || entries[1].ID != "MiniMax-M3.1-Flash-Preview" || entries[2].ID != "BrokenModel" {
		t.Fatalf("排序错误：%v %v %v", entries[0].ID, entries[1].ID, entries[2].ID)
	}
	e := entries[1]
	// 长名是 id、短名是 name，两者都保留。
	if e.ID != "MiniMax-M3.1-Flash-Preview" || e.Name != "M3.1-Flash-Preview" {
		t.Errorf("长名/短名 = %q / %q", e.ID, e.Name)
	}
	// 窗口取 context_window_options 的**最大档**（1000000），而不是 limit.context（512000）。
	if e.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d，期望 1000000（最大档）", e.ContextWindow)
	}
	if e.MaxTokens != 128000 {
		t.Errorf("MaxTokens = %d", e.MaxTokens)
	}
	if !e.SupportsImage {
		t.Error("modalities.input 含 image，SupportsImage 应为 true")
	}
	if len(e.Efforts) != 3 || e.Efforts[0] != "default" || e.Efforts[2] != "max" {
		t.Errorf("effort_options 去重保序失败：%v", e.Efforts)
	}
	if e.DefaultEffort != "low" || e.ThinkingMode != "forced_on" {
		t.Errorf("default_effort / thinking_config = %q / %q", e.DefaultEffort, e.ThinkingMode)
	}
	// M3：limit.context 回退、无 limit.output → MaxTokens 为 0（未声明）。
	m3 := entries[0]
	if m3.ContextWindow != 1000000 || m3.MaxTokens != 0 || m3.SupportsImage {
		t.Errorf("M3 归一错误：%+v", m3)
	}
	// BrokenModel：name 缺失回退 id；default_effort 不在档位表内 → 丢弃。
	b := entries[2]
	if b.Name != "BrokenModel" || b.ContextWindow != 0 || b.DefaultEffort != "" {
		t.Errorf("坏条目容错错误：%+v", b)
	}
}

func TestModelInfosShape(t *testing.T) {
	infos := modelInfosOf(staticCatalog)
	if len(infos) != 4 {
		t.Fatalf("静态兜底应 4 条，实得 %d", len(infos))
	}
	byID := map[string]provider.ModelInfo{}
	for _, m := range infos {
		byID[m.ID] = m
	}
	m31 := byID["MiniMax-M3.1-Flash-Preview"]
	if m31.ContextWindow != 1_000_000 || m31.MaxTokens != 128_000 || !m31.SupportsImages || !m31.ContextFromAPI {
		t.Errorf("M3.1 字段错误：%+v", m31)
	}
	if m31.DefaultReasoningEffort != "default" {
		t.Errorf("默认档位 = %q", m31.DefaultReasoningEffort)
	}
	if m31.CanDisableThinking == nil || *m31.CanDisableThinking {
		t.Errorf("forced_on 的 CanDisableThinking 应为 false：%v", m31.CanDisableThinking)
	}
	// M3 是 switchable：追加 on / none（on 在前）。
	m3 := byID["MiniMax-M3"]
	if m3.CanDisableThinking == nil || !*m3.CanDisableThinking {
		t.Errorf("switchable 的 CanDisableThinking 应为 true")
	}
	if len(m3.ReasoningEfforts) != 2 || m3.ReasoningEfforts[0] != "on" || m3.ReasoningEfforts[1] != "none" {
		t.Errorf("switchable 档位 = %v，期望 [on none]", m3.ReasoningEfforts)
	}
	// M2.7 系无档位表 → 不编档（R49）。
	m27 := byID["MiniMax-M2.7"]
	if m27.ReasoningEfforts != nil || m27.DefaultReasoningEffort != "" {
		t.Errorf("M2.7 不该有档位：%v / %q", m27.ReasoningEfforts, m27.DefaultReasoningEffort)
	}
}

// TestFetchModelsDynamicThenFallback：动态目录优先；失败/0 条 → 静态兜底且不报错。
func TestFetchModelsDynamicThenFallback(t *testing.T) {
	fail := false
	var sawPath, sawQuery, sawAuth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		sawPath, sawQuery, sawAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"base_resp":{"status_code":1,"status_msg":"boom"}}`)
			return
		}
		_, _ = io.WriteString(w, catalogFixture)
	})
	infos, err := c.FetchModels(testAuth())
	if err != nil {
		t.Fatalf("目录成功路径不该报错：%v", err)
	}
	if sawPath != EpModels || !strings.Contains(sawQuery, "region=cn") || !strings.Contains(sawQuery, "buildEnv=prod") {
		t.Errorf("目录请求错误：%s?%s", sawPath, sawQuery)
	}
	if sawAuth != "Bearer mmoat_FAKE_access_for_test" {
		t.Errorf("目录请求缺 Bearer：%q", sawAuth)
	}
	if len(infos) != 3 || infos[0].ID != "MiniMax-M3" {
		t.Fatalf("动态目录未生效：%+v", infos)
	}
	// 失败 → 静态兜底，不报错。
	fail = true
	infos, err = c.FetchModels(testAuth())
	if err != nil {
		t.Fatalf("兜底路径不得报错：%v", err)
	}
	if len(infos) != 4 || infos[0].ID != "MiniMax-M3.1-Flash-Preview" {
		t.Fatalf("静态兜底未生效：%+v", infos)
	}
}

// ---------------------------------------------------------------------------
// 积分 / 签到 / 续期
// ---------------------------------------------------------------------------

func TestCreditDetailsSum(t *testing.T) {
	var path, authz string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path, authz = r.URL.Path, r.Header.Get("Authorization")
		// 平铺结构：base_resp / total_count / details 同级，**没有 data 键**。
		_, _ = io.WriteString(w, `{"base_resp":{"status_code":0,"status_msg":"success"},
		  "total_count":2,
		  "details":[{"remaining_amount":"800.00"},{"remaining_amount":200}]}`)
	})
	remain, items, err := c.UserResourceDetail(testAuth())
	if err != nil {
		t.Fatalf("UserResourceDetail: %v", err)
	}
	if path != EpCreditDetails || authz == "" {
		t.Errorf("请求错误：%s %q", path, authz)
	}
	// 余额 = Σ remaining_amount（字符串要兼容数字）；total_count 不是余额。
	if remain != 1000 {
		t.Errorf("余额 = %d，期望 1000（800 字符串 + 200 数字）", remain)
	}
	if len(items) != 1 || items[0].Remain != 1000 || !items[0].Usable {
		t.Errorf("明细条目错误：%+v", items)
	}
	// UserResource 与 UserResourceDetail 口径同源。
	if v, err := c.UserResource(testAuth()); err != nil || v != 1000 {
		t.Errorf("UserResource = (%d, %v)", v, err)
	}
}

// TestCreditDetailsEmptyIsZero：details 缺失/非数组 ⇒ 空数组 ⇒ 0，且**不是**查询失败。
func TestCreditDetailsEmptyIsZero(t *testing.T) {
	for _, resp := range []string{
		`{"base_resp":{"status_code":0},"total_count":0}`,
		`{"base_resp":{"status_code":0},"details":{"oops":"not-an-array"}}`,
	} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, resp)
		})
		remain, items, err := c.UserResourceDetail(testAuth())
		if err != nil {
			t.Fatalf("details 缺失/非数组不该报错：%v（resp=%s）", err, resp)
		}
		if remain != 0 || len(items) != 1 || items[0].Remain != 0 {
			t.Errorf("应为 0 余额：%d %+v", remain, items)
		}
	}
	// 业务码非 0 → 才算失败。
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"base_resp":{"status_code":1001,"status_msg":"bad"},"details":[]}`)
	})
	if _, _, err := c.UserResourceDetail(testAuth()); err == nil {
		t.Error("业务码非 0 必须报错")
	}
}

func TestCheckinIdempotentAndClaim(t *testing.T) {
	var gotBody, gotQuery, gotMethod, gotPath string
	mode := "already"
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if mode == "already" {
			// 重复领取同样 HTTP 200，判据是 claim_result。
			_, _ = io.WriteString(w, `{"base_resp":{"status_code":0,"status_msg":"ok"},"data":{"claim_result":2,"points":0}}`)
			return
		}
		_, _ = io.WriteString(w, `{"base_resp":{"status_code":0,"status_msg":"ok"},"data":{"claim_result":1,"points":800}}`)
	})

	rep, err := c.DailyCheckinReport(testAuth())
	if err != nil {
		t.Fatalf("DailyCheckinReport: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != EpSigninClaim {
		t.Errorf("签到请求错误：%s %s", gotMethod, gotPath)
	}
	if !strings.Contains(gotQuery, "timezone_id=") {
		t.Errorf("timezone_id 必须走 query 参数：%q", gotQuery)
	}
	if gotBody != "{}" {
		t.Errorf("签到请求体必须是空 JSON 对象，实得 %q", gotBody)
	}
	if rep.Status != provider.CheckinAlready {
		t.Fatalf("claim_result=2 应判 already，实得 %v", rep.Status)
	}
	// DailyCheckin 幂等：已领取返回 nil。
	if err := c.DailyCheckin(testAuth()); err != nil {
		t.Errorf("已领取时 DailyCheckin 应为 nil，实得 %v", err)
	}

	mode = "claimed"
	rep, err = c.DailyCheckinReport(testAuth())
	if err != nil || rep.Status != provider.CheckinClaimed || rep.Amount != 800 {
		t.Fatalf("claim_result=1 应判 claimed：%+v %v", rep, err)
	}
}

// TestCheckinInvalidTimezoneIsBusinessError：invalid timezone_id 也是 200，必须查 base_resp。
func TestCheckinInvalidTimezoneIsBusinessError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"base_resp":{"status_code":2013,"status_msg":"invalid timezone_id"},"data":{}}`)
	})
	rep, err := c.DailyCheckinReport(testAuth())
	if err != nil {
		t.Fatalf("业务码失败是「未达成」而非传输错误：%v", err)
	}
	if rep.Status != provider.CheckinError || !strings.Contains(rep.Msg, "invalid timezone_id") {
		t.Fatalf("应报业务码失败：%+v", rep)
	}
}

func TestRefreshToken(t *testing.T) {
	var form url.Values
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("OAuth 必须走 form-urlencoded，实得 %q", ct)
		}
		raw, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(raw))
		if r.URL.Path != EpToken {
			t.Errorf("续期路径 = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"access_token":"mmoat_new","refresh_token":"mmort_new","token_type":"bearer","expires_in":3600,"scope":"agent.default"}`)
	})
	a := testAuth()
	before := time.Now()
	if err := c.RefreshToken(a); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "mmort_FAKE_refresh_for_test" ||
		form.Get("client_id") != "mcode-public" || form.Get("scope") != "agent.default" || form.Get("audience") != "agent-backend" {
		t.Errorf("续期表单字段错误：%v", form)
	}
	if a.AccessToken != "mmoat_new" || a.RefreshToken != "mmort_new" {
		t.Errorf("token 未回写：%q / %q", a.AccessToken, a.RefreshToken)
	}
	// expires_in 单位是**秒**（协议 §3.6：access_token 不是 JWT，只能由此写入）。
	if a.ExpiresAt < before.Add(3500*time.Second).Unix() || a.ExpiresAt > before.Add(3700*time.Second).Unix() {
		t.Errorf("ExpiresAt = %d，期望 ≈ now+3600s", a.ExpiresAt)
	}
}

// TestRefreshTokenTerminal：错误文本命中 invalid_grant/expired/revoked → ErrSessionDead 语义。
func TestRefreshTokenTerminal(t *testing.T) {
	for _, body := range []string{
		`{"error":"invalid_grant","error_description":"refresh token revoked"}`,
		`{"status":"expired"}`,
	} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, body)
		})
		err := c.RefreshToken(testAuth())
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Kind != provider.ErrSessionDead {
			t.Fatalf("期望 *provider.Error{ErrSessionDead}，实得 %#v（body=%s）", err, body)
		}
	}
}

// TestRefreshTokenMissingExpiresInIsError：没有 expires_in 时宁可报错，也不留假到期时刻。
func TestRefreshTokenMissingExpiresInIsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"mmoat_new","refresh_token":"mmort_new","token_type":"bearer"}`)
	})
	if err := c.RefreshToken(testAuth()); err == nil {
		t.Fatal("缺 expires_in 必须报错（access_token 不是 JWT，没有第二个到期来源）")
	}
}

// ---------------------------------------------------------------------------
// 错误分类
// ---------------------------------------------------------------------------

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   provider.ErrKind
	}{
		{http.StatusPaymentRequired, `{"error":"insufficient balance"}`, provider.ErrHardCredit},
		{http.StatusTooManyRequests, `{"error":"quota exceeded, please retry"}`, provider.ErrSoftRate},
		{http.StatusUnauthorized, ``, provider.ErrSessionDead},
		{http.StatusForbidden, ``, provider.ErrSessionDead},
		{http.StatusInternalServerError, ``, provider.ErrServer},
		{http.StatusBadGateway, ``, provider.ErrServer},
		{http.StatusNotFound, ``, provider.ErrNotFound},
		{http.StatusBadRequest, `{"code":2013,"message":"requires adaptive thinking"}`, provider.ErrBadParams},
		{http.StatusBadRequest, `{"code":2013,"message":"tool call result does not follow tool call"}`, provider.ErrBadParams},
		{http.StatusBadRequest, `{"code":2013,"message":"unsupported content type 'image_url'"}`, provider.ErrBadParams},
		{http.StatusBadRequest, `{"code":"bad_params","message":"本地拒绝"}`, provider.ErrBadParams},
		{http.StatusBadRequest, `{"error":"some other problem"}`, provider.ErrClient},
		{http.StatusOK, ``, provider.ErrNone},
	}
	for _, tc := range cases {
		if got := Classify(tc.status, tc.body); got != tc.want {
			t.Errorf("Classify(%d, %q) = %v，期望 %v", tc.status, tc.body, got, tc.want)
		}
	}
	// 429 必须优先于「余额」文案判定（否则限流会被误判成欠费 → 长冷却）。
	if got := Classify(http.StatusTooManyRequests, `{"message":"insufficient balance / quota exceeded"}`); got != provider.ErrSoftRate {
		t.Errorf("429 应优先判软限流，实得 %v", got)
	}
}
