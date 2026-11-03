package login_qwenwork

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// TestStartUsesOfficialRedirect 守门 OAuth 回调白名单。
//
// 2026-11 起 qwenwork 的授权服务只接受官方注册的
// https://gateway.qwenwork.cn/oauth/callback，自建回环地址
// （http://127.0.0.1:<port>/callback）会直接返回
// invalid_request: redirect_uri does not match any of the OAuth 2.0 Client's
// pre-registered redirect urls。
//
// 因此授权 URL 与 token 兑换里的 redirect_uri 必须是同一个官方值；
// 一旦有人「优化」回本机监听，本用例必须变红。
func TestStartUsesOfficialRedirect(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "login-state.json")
	authURL, err := Start(nil, fp)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("auth url 解析失败: %v", err)
	}
	q := u.Query()
	if got := q.Get("redirect_uri"); got != OfficialRedirect {
		t.Fatalf("redirect_uri = %q，期望官方值 %q", got, OfficialRedirect)
	}
	if strings.Contains(authURL, "127.0.0.1") || strings.Contains(authURL, "localhost") {
		t.Fatalf("授权 URL 不得再出现本机回调：%s", authURL)
	}
	if q.Get("client_id") != ClientID {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Error("缺少 PKCE 参数")
	}

	// 兑换阶段用的 redirect_uri 必须与授权时一致，否则 token 端点会拒。
	st, err := readState(fp)
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if st.RedirectURI != OfficialRedirect {
		t.Fatalf("state.RedirectURI = %q，期望 %q", st.RedirectURI, OfficialRedirect)
	}
	if st.Verifier == "" {
		t.Error("state 缺少 code_verifier")
	}
}

// TestSubmitCallbackURLParsesCode 粘贴兜底：能从官方回调地址里取出 code。
func TestSubmitCallbackURLParsesCode(t *testing.T) {
	pending.reset()
	raw := "https://gateway.qwenwork.cn/oauth/callback?code=abc123&state=xyz"
	if err := SubmitCallbackURL(raw); err != nil {
		t.Fatalf("SubmitCallbackURL: %v", err)
	}
	code, errMsg, ok := pending.snapshot()
	if !ok || code != "abc123" {
		t.Fatalf("code=%q ok=%v err=%q", code, ok, errMsg)
	}
}

// TestSubmitCallbackURLReportsOAuthError 授权页带回 error 时要如实透传，不能当成成功。
func TestSubmitCallbackURLReportsOAuthError(t *testing.T) {
	pending.reset()
	err := SubmitCallbackURL("https://gateway.qwenwork.cn/oauth/callback?error=access_denied&error_description=user+canceled")
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !strings.Contains(err.Error(), "user canceled") {
		t.Errorf("错误信息未透传: %v", err)
	}
}
