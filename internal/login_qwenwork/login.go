// Package login_qwenwork 实现千问办公 OAuth 授权码登录：
// PKCE(S256) + Ory Hydra 授权码流，本机 HTTP 回调收 code 后本地兑换 token。
//
// 链路：
//  1. Start：起本机回调 server（随机端口）→ 用该回调地址向授权服务做**动态客户端注册**
//     （RFC 7591，`/oauth2/register` 开放）拿到 client_id → 生成 PKCE + state → 返回授权 URL。
//  2. 用户在系统浏览器打开授权 URL，登录后浏览器跳回本机回调，code 直接到达本工具。
//  3. 回调收到 code → 用 code_verifier + 同一 redirect_uri + 注册得到的 client_id 兑换 token。
//  4. Poll：轮询 pending 通道，成功即写凭据。
//
// 为什么不再用官方的 `qwenwork-desktop-app` 客户端：2026-11 起该客户端的 redirect_uri
// 白名单只保留 `https://gateway.qwenwork.cn/oauth/callback`，自建回环地址一律
// invalid_request；而官方回调页会校验桌面端会话，授权码粘回来时已被判 INVALID_OAUTH_CALLBACK。
// 授权服务同时开放动态客户端注册，故改为「每次登录注册一个专用 public client」，
// 回调地址与本机监听端口精确对应，无需依赖任何预注册值。
package login_qwenwork

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 上游常量（IDE 桌面版同款）。
const (
	OAuthBase   = "https://qwenwork.cn"
	TokenURL    = OAuthBase + "/oauth2/token"
	// RegisterURL 动态客户端注册端点（RFC 7591）。返回的 client_id 与本次登录的
	// 回环回调地址绑定，供授权与 token 兑换共用。
	RegisterURL = OAuthBase + "/oauth2/register"
	// clientName 注册时展示的客户端名（出现在用户授权页）。
	clientName = "wild-view"
	Scope       = "openid profile email offline_access qwen_work"
	callbackTTL = 5 * time.Minute // 本机回调 server 最长等待
)

// ErrPending 表示授权尚未完成。
var ErrPending = errors.New("login pending")

// Result 登录成功后拿到的凭证与账号信息。
type Result struct {
	AccessToken  string
	RefreshToken string // ory_rt_（轮换）
	ExpiresIn    int64  // 秒
	UID          string
	Nickname     string
	// IDToken 仅诊断用（不落盘到 auth，避免多余敏感面）
	IDToken string
}

// state 落盘的 PKCE 状态（含回调端口，供 Poll 与回调 server 共享）。
type state struct {
	ClientID     string `json:"client_id"`
	Verifier     string `json:"verifier"`
	RedirectURI  string `json:"redirect_uri"`
	CallbackPort int    `json:"callback_port"`
	AuthURL      string `json:"auth_url"`
	CreatedAt    int64  `json:"created_at"`
}

// pendingCode 回调 server 收到的授权码（内存通道 + 文件双保险）。
type pendingCode struct {
	mu       sync.Mutex
	code     string
	errMsg   string
	received bool
}

var (
	pending     = &pendingCode{}
	callbackSrv *http.Server
	callbackLn  net.Listener
)

// SubmitCallbackURL 手动提交浏览器跳转后的回调地址（远程/局域网面板的粘贴兜底入口）。
//
// 与 codearts/lobsterai/gemini 的同名函数同构：解析 URL query → 复用 HTTP 回调
// 同一条校验链（error 透传 → code 先到保留）。qwenwork 的 state 服务端只透传不校验
// （uuid4Hex 透传标识，见 Start），故这里不做 state 比对——与 HTTP 回调行为一致。
// 写入同一 pending 通道，Poll 兑换路径零分叉。
func SubmitCallbackURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("回调地址为空：请复制登录完成后浏览器地址栏里的完整 URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("回调地址无法解析: %w", err)
	}
	q := u.Query()
	if len(q) == 0 && strings.Contains(raw, "=") {
		// 只贴了查询串：容忍
		if qq, perr := url.ParseQuery(strings.TrimPrefix(raw, "?")); perr == nil && len(qq) > 0 {
			q = qq
		}
	}
	if e := q.Get("error"); e != "" {
		desc := q.Get("error_description")
		if desc == "" {
			desc = e
		} else {
			desc = desc + " (" + e + ")"
		}
		pending.fail(desc)
		return fmt.Errorf("登录失败: %s", desc)
	}
	code := q.Get("code")
	if code == "" {
		const m = "回调缺少 code 参数：请复制登录完成后浏览器地址栏里含 code=… 的整条 URL"
		pending.fail(m)
		return errors.New(m)
	}
	pending.set(code)
	return nil
}

// NewClient 简单 HTTP 客户端（无 cookie 需求；token 兑换用）。
func NewClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// Start 发起登录：起本机回调、动态注册客户端、返回授权 URL。
func Start(client *http.Client, statePath string) (string, error) {
	if client == nil {
		client = NewClient()
	}
	verifier, challenge := makePKCE()

	// 1) 本机回调：随机空闲端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen callback: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	// 2) 动态注册：client_id 与本次回调地址绑定（官方客户端白名单不再接受自建回环）
	clientID, err := registerClient(client, RegisterURL, redirectURI)
	if err != nil {
		_ = ln.Close()
		return "", err
	}

	authURL := buildAuthURL(clientID, redirectURI, challenge, uuid4Hex())

	st := state{ClientID: clientID, Verifier: verifier, RedirectURI: redirectURI, CallbackPort: port, AuthURL: authURL, CreatedAt: time.Now().Unix()}
	if err := writeState(statePath, st); err != nil {
		_ = ln.Close()
		return "", err
	}

	// 3) 起回调 server（异步等 code）
	pending.reset()
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			desc := q.Get("error_description")
			if desc == "" {
				desc = e
			} else {
				desc = desc + " (" + e + ")"
			}
			pending.fail(desc)
			_, _ = io.WriteString(w, callbackPage(false, desc))
			return
		}
		code := q.Get("code")
		if code == "" {
			pending.fail("回调缺少 code 参数")
			_, _ = io.WriteString(w, callbackPage(false, "回调缺少 code 参数"))
			return
		}
		pending.set(code)
		// 此时尚未完成兑换（Poll 轮询兑换），文案用「已收到授权」而非「登录成功」。
		_, _ = io.WriteString(w, callbackPage(true, ""))
	})
	callbackLn = ln
	callbackSrv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = callbackSrv.Serve(ln) }()

	return authURL, nil
}

// buildAuthURL 组装授权 URL。抽成函数便于用例直接断言参数，不必起网络。
func buildAuthURL(clientID, redirectURI, challenge, stateVal string) string {
	return fmt.Sprintf("%s/oauth2/auth?client_id=%s&code_challenge=%s&code_challenge_method=S256&redirect_uri=%s&response_type=code&scope=%s&state=%s",
		OAuthBase,
		url.QueryEscape(clientID),
		url.QueryEscape(challenge),
		url.QueryEscape(redirectURI),
		url.QueryEscape(Scope),
		url.QueryEscape(stateVal),
	)
}

// registerClient 动态注册一个 public client（RFC 7591），回调地址绑定到本次监听端口。
// 返回的 client_id 用于授权与 token 兑换；注册失败时给出可读原因。
func registerClient(client *http.Client, endpoint, redirectURI string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"client_name":                clientName,
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"scope":                      Scope,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("动态注册客户端失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("动态注册客户端失败 http %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("注册响应解析失败: %w", err)
	}
	if out.ClientID == "" {
		return "", fmt.Errorf("注册响应缺少 client_id: %s", truncate(string(raw), 200))
	}
	return out.ClientID, nil
}

// Poll 单次检查登录结果；未完成返回 ErrPending；成功兑换 token 并清理。
func Poll(client *http.Client, statePath string) (Result, error) {
	st, err := readState(statePath)
	if err != nil {
		return Result{}, fmt.Errorf("read state: %w", err)
	}
	// 超时清理
	if time.Since(time.Unix(st.CreatedAt, 0)) > callbackTTL {
		Shutdown()
		_ = os.Remove(statePath)
		return Result{}, fmt.Errorf("登录超时（5 分钟），请重试")
	}

	code, errMsg, ok := pending.snapshot()
	if !ok {
		if errMsg != "" {
			Shutdown()
			_ = os.Remove(statePath)
			return Result{}, fmt.Errorf("登录失败: %s", errMsg)
		}
		return Result{}, ErrPending
	}
	defer func() { Shutdown(); _ = os.Remove(statePath) }()

	// 兑换 token（Hydra 标准 token endpoint，public client + PKCE）
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {st.RedirectURI},
		"client_id":     {st.ClientID},
		"code_verifier": {st.Verifier},
	}
	req, err := http.NewRequest(http.MethodPost, TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("token exchange http %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return Result{}, fmt.Errorf("token parse: %w (body: %s)", err, truncate(string(raw), 200))
	}
	if tok.AccessToken == "" {
		return Result{}, fmt.Errorf("token exchange: missing access_token (body: %s)", truncate(string(raw), 200))
	}

	res := Result{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresIn:    tok.ExpiresIn,
		IDToken:      tok.IDToken,
	}
	// 从 JWT 解 uid/nickname（token endpoint 不返回 userinfo）
	res.UID, res.Nickname = parseJWTIdentity(tok.AccessToken)
	if res.UID == "" {
		res.UID, _ = parseJWTIdentity(tok.IDToken)
	}
	return res, nil
}

// Shutdown 关闭回调 server 并清理残留状态（登录结束/超时/取消时调用）。
func Shutdown() {
	pending.reset()
	if callbackSrv != nil {
		_ = callbackSrv.Close()
		callbackSrv = nil
		callbackLn = nil
	}
}

// ---------------------------------------------------------------------------
// 保存凭证（对齐 qoder 渠道的 SaveAuth 契约）
// ---------------------------------------------------------------------------

// SaveAuth 以嵌套形原子写 auth 文件（与 internal/auth.Parse 读取格式一致）。
// 文件名 qwenwork-<uid>.json。
func SaveAuth(authDir string, r Result) (string, error) {
	if r.UID == "" {
		return "", fmt.Errorf("missing uid in result")
	}
	expiresAt := int64(0)
	if r.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second).Unix()
	} else {
		expiresAt = time.Now().Add(48 * time.Hour).Unix() // 观测 JWT 寿命 48h
	}
	doc := map[string]any{
		"auth": map[string]any{
			"accessToken":  r.AccessToken,
			"refreshToken": r.RefreshToken,
			"expiresAt":    expiresAt,
			"domain":       "qwenwork.cn",
			"apiHost":      "https://gateway.qwenwork.cn",
		},
		"account": map[string]any{
			"uid":      r.UID,
			"nickname": r.Nickname,
		},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	_ = os.MkdirAll(authDir, 0o755)
	fp := filepath.Join(authDir, "qwenwork-"+r.UID+".json")
	tmp := fp + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, fp); err != nil {
		return "", err
	}
	return fp, nil
}

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

// pendingCode 操作
func (p *pendingCode) reset()              { p.mu.Lock(); p.code, p.errMsg, p.received = "", "", false; p.mu.Unlock() }
func (p *pendingCode) set(code string)     { p.mu.Lock(); p.code, p.received = code, true; p.mu.Unlock() }
func (p *pendingCode) fail(msg string)     { p.mu.Lock(); p.errMsg, p.received = msg, true; p.mu.Unlock() }
func (p *pendingCode) snapshot() (code, errMsg string, ok bool) {
	p.mu.Lock(); defer p.mu.Unlock()
	return p.code, p.errMsg, p.received
}

// makePKCE 生成 verifier + S256 challenge（43 字符 base64url，RFC 7636）。
func makePKCE() (verifier, challenge string) {
	buf := make([]byte, 48)
	_, _ = rand.Read(buf)
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

// uuid4Hex 简单 state（服务端透传，不承载内容）。
func uuid4Hex() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// parseJWTIdentity 从 access/id token 解 uid 与 username。
func parseJWTIdentity(token string) (uid, name string) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", ""
	}
	p := parts[1]
	// base64url 无填充 → 标准解码需补齐到 4 的倍数。
	// 注意：Go 的 % 保留被除数符号，-len(p)%4 在 len%4!=0 时为负，
	// strings.Repeat 负数会 panic（实测踩中：login poll panic: strings: negative Repeat count）。
	if r := len(p) % 4; r != 0 {
		p += strings.Repeat("=", 4-r)
	}
	raw, err := base64.URLEncoding.DecodeString(p)
	if err != nil {
		return "", ""
	}
	var payload struct {
		Sub      string `json:"sub"`
		UserID   string `json:"user_id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return "", ""
	}
	uid = payload.UserID
	if uid == "" {
		uid = payload.Sub
	}
	return uid, payload.Username
}

func readState(path string) (state, error) {
	var st state
	raw, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(raw, &st)
	return st, err
}

func writeState(path string, st state) error {
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	raw, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(path, raw, 0o600)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}
