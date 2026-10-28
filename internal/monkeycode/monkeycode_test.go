package monkeycode

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// ---------------------------------------------------------------------------
// 签名（sign.go）
// ---------------------------------------------------------------------------

// TestSignFixedVector 固定向量：算法是
// `"v1=" + hex(HMAC-SHA256(secret, text))`。
// 期望值由独立实现（python hmac/hashlib）算出，用于捕获编码方式回归
// （漏 v1= 前缀、误用 base64、大小写 hex 等）。
func TestSignFixedVector(t *testing.T) {
	got := Sign("omas_test_secret", "You are a helpful assistant.")
	want := "v1=a62052ff54bed4f362590aeab65922c85ffefa88b455b71559895fd9c77b53ca"
	if got != want {
		t.Fatalf("签名不匹配：\n got=%s\nwant=%s", got, want)
	}
}

// TestSignPreservesCRLF 是本渠道最关键的回归点：官方提示词含 44 个 CRLF，
// 任何换行归一化都会让签名与内容不匹配 → 上游 403。
// 这里断言 `a\r\nb` 与 `a\nb` 得到**不同**签名，且 `a\r\nb` 的签名等于
// 独立实现按原字节算出的值。
func TestSignPreservesCRLF(t *testing.T) {
	crlf := Sign("omas_test_secret", "a\r\nb")
	lf := Sign("omas_test_secret", "a\nb")
	if crlf == lf {
		t.Fatal("CRLF 与 LF 得到相同签名：签名输入被归一化了（会让上游 403）")
	}
	if want := "v1=709a1e4a08a739619cd1b47e9cf7bf5b85f6c394259209e5ab7b93fa5bd4647b"; crlf != want {
		t.Fatalf("CRLF 输入签名不匹配：\n got=%s\nwant=%s", crlf, want)
	}
}

func TestSignatureForRequiresSecret(t *testing.T) {
	if _, err := signatureFor("   "); err == nil {
		t.Fatal("空 signing_secret 应当报错")
	}
	a, err := signatureFor("omas_test_secret")
	if err != nil {
		t.Fatalf("首次计算失败：%v", err)
	}
	b, err := signatureFor("omas_test_secret")
	if err != nil || a != b {
		t.Fatalf("缓存命中结果不一致：%v %q %q", err, a, b)
	}
}

// ---------------------------------------------------------------------------
// 请求投影（request.go）
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 本地拒绝（client.go）
// ---------------------------------------------------------------------------

func TestClassify(t *testing.T) {
	c := New()
	cases := []struct {
		status int
		body   string
		want   provider.ErrKind
	}{
		// 403 invalid ohmyagent request：签名/协议错误，绝不能当账号问题
		{http.StatusForbidden, `{"error":"invalid ohmyagent request"}`, provider.ErrPassthrough},
		{http.StatusUnauthorized, `{"error":"unauthorized"}`, provider.ErrSessionDead},
		{http.StatusTooManyRequests, `{}`, provider.ErrSoftRate},
		{http.StatusBadGateway, `{}`, provider.ErrServer},
		{http.StatusBadRequest, `{"error":{"code":"model_not_found"}}`, provider.ErrBadParams},
	}
	for _, tc := range cases {
		if got := c.Classify(tc.status, tc.body); got != tc.want {
			t.Errorf("Classify(%d, %s) = %v，期望 %v", tc.status, tc.body, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 响应投影（sse.go）
// ---------------------------------------------------------------------------

const anthropicFixture = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":12}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

// ---------------------------------------------------------------------------
// Responses 面（responses.go）
// ---------------------------------------------------------------------------

// TestUsesResponsesAPI 只有客户端 settings.json 里 type=openai-responses 的
// 4 个模型走 Responses 面；三种 ID 形态都要能识别。
func TestUsesResponsesAPI(t *testing.T) {
	yes := []string{
		"basic/qwen3.8-flash", "pro/gpt-5.6-terra",
		"ultra/gpt-5.6-sol", "ultra/gpt-6-astra",
		"monkeycode/basic/qwen3.8-flash",
		"monkeycode-ultra/gpt-6-astra",
	}
	for _, id := range yes {
		if !usesResponsesAPI(id) {
			t.Errorf("%s 应走 Responses 面", id)
		}
	}
	no := []string{"basic/deepseek-flash", "monkeycode/basic/deepseek-flash", "pro/glm-5", "ultra/glm-5.1"}
	for _, id := range no {
		if usesResponsesAPI(id) {
			t.Errorf("%s 不应走 Responses 面", id)
		}
	}
	// 25 个模型里恰好 4 个走 Responses 面
	n := 0
	for _, id := range staticModels {
		if usesResponsesAPI(id) {
			n++
		}
	}
	if n != 4 {
		t.Errorf("Responses 面模型数期望 4，实际 %d", n)
	}
}

// responsesFixture 取自 2026-09-24 对 monkeycode-basic/qwen3.8-flash 的真实抓包
// （事件顺序与字段均按原文，仅裁掉重复的 delta 与冗长元数据）。
const responsesFixture = `event: response.created
data: {"response":{"id":"cc628625-5f29-901f-bb3d-cc23317b88eb","created_at":1790191381,"model":"monkeycode-basic/qwen3.8-flash","status":"in_progress","usage":null},"sequence_number":0,"type":"response.created"}

event: response.in_progress
data: {"response":{"id":"cc628625-5f29-901f-bb3d-cc23317b88eb","status":"in_progress","usage":null},"sequence_number":1,"type":"response.in_progress"}

event: response.output_item.added
data: {"sequence_number":2,"type":"response.output_item.added","item":{"id":"rs_x","summary":[],"type":"reasoning","status":"in_progress"},"output_index":0}

event: response.reasoning_summary_text.delta
data: {"sequence_number":4,"type":"response.reasoning_summary_text.delta","delta":"The user said","item_id":"rs_x","output_index":0,"summary_index":0}

event: response.reasoning_summary_text.done
data: {"sequence_number":16,"type":"response.reasoning_summary_text.done","item_id":"rs_x","output_index":0,"summary_index":0,"text":"The user said"}

event: response.output_item.done
data: {"sequence_number":18,"type":"response.output_item.done","item":{"id":"rs_x","summary":[{"text":"The user said","type":"summary_text"}],"type":"reasoning","status":"completed"},"output_index":0}

event: response.output_item.added
data: {"sequence_number":19,"type":"response.output_item.added","item":{"content":[],"id":"msg_x","role":"assistant","type":"message","status":"in_progress"},"output_index":1}

event: response.content_part.added
data: {"sequence_number":20,"type":"response.content_part.added","content_index":0,"item_id":"msg_x","output_index":1,"part":{"type":"output_text","text":"","logprobs":[],"annotations":[]}}

event: response.output_text.delta
data: {"sequence_number":21,"type":"response.output_text.delta","content_index":0,"delta":"P","item_id":"msg_x","output_index":1,"logprobs":[]}

event: response.output_text.delta
data: {"sequence_number":22,"type":"response.output_text.delta","content_index":0,"delta":"ONG","item_id":"msg_x","output_index":1,"logprobs":[]}

event: response.output_text.done
data: {"sequence_number":23,"type":"response.output_text.done","content_index":0,"item_id":"msg_x","output_index":1,"text":"PONG","logprobs":[]}

event: response.output_item.done
data: {"sequence_number":25,"type":"response.output_item.done","item":{"content":[{"type":"output_text","text":"PONG","logprobs":[],"annotations":[]}],"id":"msg_x","role":"assistant","type":"message","status":"completed"},"output_index":1}

event: response.completed
data: {"response":{"id":"cc628625-5f29-901f-bb3d-cc23317b88eb","status":"completed","output":[],"usage":{"input_tokens":651,"input_tokens_details":{"cached_tokens":512},"output_tokens":131,"output_tokens_details":{"reasoning_tokens":129},"total_tokens":782}},"sequence_number":26,"type":"response.completed"}

`

// ---------------------------------------------------------------------------
// 端到端（假上游）
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 控制台钱包（积分 / 每日 Token 额度）
// ---------------------------------------------------------------------------

// TestMilliCredits 毫积分 → 积分的取整与 console 前端一致（Math.ceil）。
func TestMilliCredits(t *testing.T) {
	cases := []struct {
		in   int64
		want int64
	}{
		{0, 0}, {1, 1}, {999, 1}, {1000, 1}, {1001, 2},
		{54487, 55}, // 54.487 → 55
		{-5, 0},
	}
	for _, c := range cases {
		if got := milliCredits(c.in); got != c.want {
			t.Errorf("milliCredits(%d)=%d want %d", c.in, got, c.want)
		}
	}
}

// TestUserResourceDetailWallet 钱包接口 → 两行明细：积分计入合计、每日 Token 只展示。
func TestUserResourceDetailWallet(t *testing.T) {
	var gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != epWallet {
			t.Errorf("路径=%s want %s", r.URL.Path, epWallet)
		}
		gotCookie = r.Header.Get("Cookie")
		io.WriteString(w, `{"code":0,"message":"success","data":{"id":"x",`+
			`"balance":54487,"daily_token_balance":0,"daily_token_limit":10000000}}`)
	}))
	defer srv.Close()

	c := New()
	c.Console = srv.URL
	a := &auth.Auth{AccessToken: "oma_k", SigningSecret: "omas_s", ConsoleCookie: "sess-123"}

	remain, items, err := c.UserResourceDetail(a)
	if err != nil {
		t.Fatalf("UserResourceDetail: %v", err)
	}
	if gotCookie != "monkeycode_ai_session=sess-123" {
		t.Errorf("Cookie=%q", gotCookie)
	}
	if remain != 55 {
		t.Errorf("remain=%d want 55（54487 毫积分 ceil）", remain)
	}
	if len(items) != 2 {
		t.Fatalf("items=%d want 2", len(items))
	}
	if items[0].Name != "积分余额" || items[0].Remain != 55 || items[0].InfoOnly {
		t.Errorf("积分行异常：%+v", items[0])
	}
	tok := items[1]
	if tok.Name != "每日 Token 额度" || tok.Remain != 0 || tok.Total != 10000000 || !tok.InfoOnly {
		t.Errorf("Token 行异常：%+v", tok)
	}
	// 合计口径：Token 行不入算术（否则 1000 万会淹没积分池）。
	if u, un := provider.Summarize(items); u != 55 || un != 0 {
		t.Errorf("Summarize=%d/%d want 55/0", u, un)
	}
}

// TestUserResourceDetailNoCookie 无控制台 Cookie 时明确报错，不静默返回 0。
func TestUserResourceDetailNoCookie(t *testing.T) {
	c := New()
	if _, err := c.UserResource(&auth.Auth{AccessToken: "oma_k"}); err == nil {
		t.Fatal("无 Cookie 应报错")
	}
	if _, _, err := c.UserResourceDetail(nil); err == nil {
		t.Fatal("nil 账号应报错")
	}
}

// TestUserResourceDetailErrors 上游 5xx / 业务 code≠0 都应报错而非返回 0 余额。
func TestUserResourceDetailErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"HTTP500", http.StatusInternalServerError, `{}`},
		{"business", http.StatusOK, `{"code":1001,"message":"unauthorized"}`},
		{"badjson", http.StatusOK, `not-json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c := New()
			c.Console = srv.URL
			if _, err := c.UserResource(&auth.Auth{ConsoleCookie: "s"}); err == nil {
				t.Fatal("应报错")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 控制台会话派生（百智云 OAuth）
// ---------------------------------------------------------------------------

// fakeConsole 造一对假上游：baizhiSrv 出授权、consoleSrv 收回调并放会话。
//
//	baizhiHits / consoleHits 用于断言「哪些请求真的发生了」（如 500 时不该去派生）。
func fakeConsole(t *testing.T, staleCookie, freshCookie string) (c *Client, consoleHits, baizhiHits *int32, stop func()) {
	t.Helper()
	var ch, bh int32
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&ch, 1)
		switch r.URL.Path {
		case oauthCallbackPath:
			q := r.URL.Query()
			if q.Get("code") != "GOODCODE" || q.Get("state") == "" {
				w.WriteHeader(http.StatusFound)
				http.Redirect(w, r, "/login?error=invalid_token", http.StatusFound)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: CookieNameConsole, Value: freshCookie, Path: "/"})
			w.WriteHeader(http.StatusFound)
		case epWallet:
			ck := r.Header.Get("Cookie")
			// staleCookie 为空表示「账号本来就没会话」，此时只有 fresh 能过
			if staleCookie != "" && strings.Contains(ck, CookieNameConsole+"="+staleCookie) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if !strings.Contains(ck, CookieNameConsole+"="+freshCookie) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"code":0,"message":"success","data":{"id":"x",`+
				`"balance":54487,"daily_token_balance":0,"daily_token_limit":10000000}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	baizhi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bh, 1)
		if r.URL.Path != epOAuthAuthorize {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("client_id") != oauthClientID || q.Get("response_type") != "code" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.Contains(r.Header.Get("Cookie"), CookieNameBaizhi+"=") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		loc := q.Get("redirect_uri") + "?code=GOODCODE&state=" + q.Get("state")
		w.Header().Set("Location", loc)
		w.WriteHeader(http.StatusFound)
	}))
	c = New()
	c.Console = console.URL
	c.Baizhi = baizhi.URL
	return c, &ch, &bh, func() { console.Close(); baizhi.Close() }
}

// TestWalletDerivesConsoleSession 存量控制台会话失效（401）时，自动用百智云会话
// 现派生一份再重试，并把新会话说回账号（含落盘）。
func TestWalletDerivesConsoleSession(t *testing.T) {
	c, ch, bh, stop := fakeConsole(t, "stale-sess", "fresh-sess")
	defer stop()

	fp := filepath.Join(t.TempDir(), "monkeycode-mc_1.json")
	a := &auth.Auth{AccessToken: "oma_k", SigningSecret: "omas_s",
		ConsoleCookie: "stale-sess", BaizhiCookie: "baizhi-ok", UID: "mc_1", FilePath: fp}

	remain, items, err := c.UserResourceDetail(a)
	if err != nil {
		t.Fatalf("UserResourceDetail: %v", err)
	}
	if remain != 55 || len(items) != 2 {
		t.Fatalf("remain=%d items=%d want 55/2", remain, len(items))
	}
	if *ch != 3 { // 401 的钱包 + 回调 + 重试的钱包
		t.Errorf("console 命中 %d 次 want 3", *ch)
	}
	if *bh != 1 {
		t.Errorf("baizhi 命中 %d 次 want 1", *bh)
	}
	if a.ConsoleCookie != "fresh-sess" {
		t.Errorf("内存会话未更新：%q", a.ConsoleCookie)
	}
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("读回凭据：%v", err)
	}
	if !strings.Contains(string(raw), "fresh-sess") {
		t.Errorf("派生出的会话未落盘：%s", raw)
	}
}

// TestWalletDerivesWhenCookieMissing 首次导入后客户端会话可能已没了（字段为空），
// 也应走派生而不是直接报「缺少控制台 Cookie」。
func TestWalletDerivesWhenCookieMissing(t *testing.T) {
	c, ch, bh, stop := fakeConsole(t, "", "fresh-sess")
	defer stop()

	a := &auth.Auth{AccessToken: "oma_k", BaizhiCookie: "baizhi-ok"}
	remain, _, err := c.UserResourceDetail(a)
	if err != nil {
		t.Fatalf("UserResourceDetail: %v", err)
	}
	if remain != 55 {
		t.Errorf("remain=%d want 55", remain)
	}
	if *ch != 2 || *bh != 1 { // 没打第一次钱包，直接派生 + 重试
		t.Errorf("console=%d baizhi=%d want 2/1", *ch, *bh)
	}
}

// TestWalletNoDeriveOnServerError 上游 5xx 不是会话问题，不该白跑一次派生。
func TestWalletNoDeriveOnServerError(t *testing.T) {
	var ch, bh int32
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&ch, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer console.Close()
	baizhi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bh, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer baizhi.Close()

	c := New()
	c.Console, c.Baizhi = console.URL, baizhi.URL
	a := &auth.Auth{ConsoleCookie: "s", BaizhiCookie: "b"}
	if _, _, err := c.UserResourceDetail(a); err == nil {
		t.Fatal("5xx 应报错")
	}
	if bh != 0 {
		t.Errorf("5xx 时不该去派生，baizhi 命中 %d 次", bh)
	}
}

// TestDeriveConsoleSessionFailures 派生自身的失败路径。
func TestDeriveConsoleSessionFailures(t *testing.T) {
	t.Run("无百智云会话", func(t *testing.T) {
		c, _, bh, stop := fakeConsole(t, "s", "f")
		defer stop()
		if _, err := c.deriveConsoleSession(&auth.Auth{ConsoleCookie: "s"}); err == nil {
			t.Fatal("缺百智云 Cookie 应报错")
		}
		if *bh != 0 {
			t.Error("缺 Cookie 时不该发请求")
		}
	})

	t.Run("百智云会话失效", func(t *testing.T) {
		baizhi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer baizhi.Close()
		c := New()
		c.Baizhi = baizhi.URL
		_, err := c.deriveConsoleSession(&auth.Auth{BaizhiCookie: "expired"})
		if err == nil || !strings.Contains(err.Error(), "百智云会话已失效") {
			t.Fatalf("err=%v want 含「百智云会话已失效」", err)
		}
	})

	t.Run("state 不匹配", func(t *testing.T) {
		baizhi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 故意回一个与请求不同的 state（模拟回调被替换）
			w.Header().Set("Location", "https://x/cb?code=C&state=tampered")
			w.WriteHeader(http.StatusFound)
		}))
		defer baizhi.Close()
		c := New()
		c.Baizhi = baizhi.URL
		_, err := c.deriveConsoleSession(&auth.Auth{BaizhiCookie: "ok"})
		if err == nil || !strings.Contains(err.Error(), "state 不匹配") {
			t.Fatalf("err=%v want 含「state 不匹配」", err)
		}
	})

	t.Run("回调没下发会话", func(t *testing.T) {
		var baizhi *httptest.Server
		console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK) // 200 但不带 Set-Cookie
		}))
		defer console.Close()
		baizhi = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", console.URL+oauthCallbackPath+"?code=C&state="+r.URL.Query().Get("state"))
			w.WriteHeader(http.StatusFound)
		}))
		defer baizhi.Close()
		c := New()
		c.Console, c.Baizhi = console.URL, baizhi.URL
		_, err := c.deriveConsoleSession(&auth.Auth{BaizhiCookie: "ok"})
		if err == nil || !strings.Contains(err.Error(), "未下发控制台会话") {
			t.Fatalf("err=%v want 含「未下发控制台会话」", err)
		}
	})
}

// TestWalletErrorMentionsBothSides 401 且派生也失败时，错误必须同时说明
// 「会话失效」与「续期失败原因」—— 只报前者用户不知道该做什么，
// 只报后者又不知道原问题（本次 monkeycode 凭据缺 baizhiCookie 就是这种场景）。
func TestWalletErrorMentionsBothSides(t *testing.T) {
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // 会话已失效
	}))
	defer console.Close()

	c := New()
	c.Console = console.URL
	// 有（失效的）控制台会话，但**没有**百智云会话 → 派生必然失败
	a := &auth.Auth{ConsoleCookie: "expired"}
	_, _, err := c.UserResourceDetail(a)
	if err == nil {
		t.Fatal("应报错")
	}
	msg := err.Error()
	for _, want := range []string{"控制台会话已失效", "自动续期失败", "百智云会话"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息缺少 %q：%s", want, msg)
		}
	}
}
