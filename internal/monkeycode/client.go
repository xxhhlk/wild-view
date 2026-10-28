// client.go MonkeyCode 平台托管模型渠道客户端，实现 provider.Upstream。
//
// 渠道形态：**B（从本机官方客户端导入凭据）** —— 一个账号 = 一对
// `oma_` api_key + `omas_` signing_secret，无 uid、无 refresh 端点。
// 凭据由 internal/app 的 ImportLocal 从 ohmyagent 的 settings.json 读出，
// 落成 `auths/monkeycode-<派生标识>.json`。
//
// 上游是 **Anthropic Messages** 形状（`{base}/messages`），因此本渠道做两件
// 方言投影：请求 OpenAI Chat → Anthropic（request.go），响应 Anthropic SSE →
// OpenAI SSE（sse.go）。签名见 sign.go。
package monkeycode

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// Client 上游 HTTP 客户端。Base 可覆盖以便测试。
type Client struct {
	HTTP *http.Client

	Base string
	// Console 控制台站点（钱包等 Cookie 认证接口）；空则用 consoleBase。
	Console string
	// Baizhi 百智云站点（派生控制台会话的授权端点）；空则用 baizhiBase。
	Baizhi string
}

// New 生产默认客户端。
func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: requestTimeout, Transport: newTransport()}, Base: DefaultBase, Console: consoleBase}
}

// newTransport 出厂 Transport：禁 h2 + 连接池 + ResponseHeaderTimeout 兜底。
//
// ResponseHeaderTimeout 兜底：上游迟迟不回响应头时不至于无限挂住。
func newTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	return &http.Transport{
		DialContext:           dialer.DialContext,
		TLSNextProto:          make(map[string]func(string, *tls.Conn) http.RoundTripper),
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
	}
}

// NewWithBase 测试用：覆盖上游基址。
func NewWithBase(base string) *Client {
	c := New()
	c.Base = base
	return c
}

// console 返回控制台站点基址（测试可覆盖）。
func (c *Client) console() string {
	if c.Console != "" {
		return strings.TrimRight(c.Console, "/")
	}
	return consoleBase
}

// baizhi 返回百智云站点基址（测试可覆盖）。
func (c *Client) baizhi() string {
	if c.Baizhi != "" {
		return strings.TrimRight(c.Baizhi, "/")
	}
	return baizhiBase
}

// base 返回该账号的上游基址（账号自带 ApiHost+Domain 优先，否则回默认）。
func (c *Client) base(a *auth.Auth) string {
	if a != nil {
		host := strings.TrimSpace(a.ApiHost)
		if host != "" {
			path := strings.TrimSpace(a.Domain)
			if path == "" {
				path = "/v1"
			}
			return strings.TrimRight(host, "/") + "/" + strings.TrimLeft(path, "/")
		}
	}
	if c.Base != "" {
		return c.Base
	}
	return DefaultBase
}

// StaticModels 上游无 GET /models（405），模型表为静态清单。
func StaticModels() []provider.ModelInfo { return modelInfos() }

// modelInfos 构造静态模型表。
//
// 来源标注（issue 报告 #6）：ContextWindow/MaxTokens（200000 / 32000）抄自官方
// 客户端 settings.json 的托管条目，属上游声明容量，但并非模型目录接口字段——
// 严格按 provider.go 口径，ContextFromAPI（上游接口返回）与 HandMeasured（本工具
// 实测）都不完全匹配。仍取 ContextFromAPI=true 的取舍：值确系上游声明而非估算，
// 两个标志全关会让费用面板按「未知」处理、丢失这批模型的容量展示（/v1/models
// 不受此标志门控，照常透出）；而 HandMeasured 会给从未实测的值冒充「实测」标注，
// 更不实。若上游未来下发模型目录接口，应改为真实解析并更新本说明。
func modelInfos() []provider.ModelInfo {
	out := make([]provider.ModelInfo, 0, len(staticModels))
	for _, id := range staticModels {
		name := modelDisplayNames[id]
		if name == "" {
			name = id
		}
		out = append(out, provider.ModelInfo{
			ID:             id,
			Name:           name,
			ContextWindow:  200000,
			MaxTokens:      32000,
			ContextFromAPI: true,
			SupportsTools:  true,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// provider.Upstream 实现
// ---------------------------------------------------------------------------

// FetchModels 上游无目录接口，恒回静态清单。
func (c *Client) FetchModels(_ *auth.Auth) ([]provider.ModelInfo, error) {
	return modelInfos(), nil
}

// FetchModelPricing 上游无费率接口，返回空（面板显示未知，而非「免费」）。
func (c *Client) FetchModelPricing(_ *auth.Auth) ([]provider.ModelPricing, error) {
	return nil, nil
}

// RefreshToken 空实现：本渠道无 refresh 端点。
// 账号 ExpiresAt 由导入器置为远期值，正常路径不会走到这里；
// 即便走到也不报错，避免把「没有刷新机制」误判成会话失效。
func (c *Client) RefreshToken(_ *auth.Auth) error { return nil }

// UserResource 查询账号积分余额（面板/pool 路由口径）。
//
// 数据来自**控制台**钱包接口（console 域，Cookie 认证），不是 agent 域——
// 本渠道是「导入型」，agent 的 oma_ key 在 console 域一律 401。
// 账号没带控制台 Cookie（老凭据或客户端未登录）时返回错误，调用方按
// 「无额度信息」处理，不罚号。
func (c *Client) UserResource(a *auth.Auth) (int64, error) {
	remain, _, err := c.wallet(a)
	return remain, err
}

// UserResourceDetail 返回两行：**积分余额**（计入合计）与**每日 Token 额度**
// （InfoOnly，只展示、不入任何算术——单位是 token，与积分相加无意义）。
func (c *Client) UserResourceDetail(a *auth.Auth) (int64, []provider.ResourceItem, error) {
	return c.wallet(a)
}

// walletResponse 控制台钱包接口响应（2026-09-25 实测）：
//
//	{"code":0,"message":"success","data":{"id":"…","balance":54487,
//	 "daily_token_balance":0,"daily_token_limit":10000000}}
type walletResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		// Balance 单位是**毫积分**（console 前端除以 1e3 后再 ceil 展示）。
		Balance int64 `json:"balance"`
		// DailyTokenBalance 是**当日剩余 token**（console 的进度条分子，
		// 百分比 = (limit-balance)/limit），DailyTokenLimit 是当日上限。
		DailyTokenBalance int64 `json:"daily_token_balance"`
		DailyTokenLimit   int64 `json:"daily_token_limit"`
	} `json:"data"`
}

// wallet 单次请求同时产出总额与明细条目。
//
// 控制台会话（monkeycode_ai_session）只活约 6 天，而它的**上游**百智云会话约 29 天：
// 因此会话失效时（401，或压根没导入过）先尝试用百智云会话现派生一份再重试，
// 免得用户每 6 天就要重导一次凭据。派生失败才把原错误抛出去。
func (c *Client) wallet(a *auth.Auth) (int64, []provider.ResourceItem, error) {
	cookie := consoleCookieOf(a)
	w, err := c.fetchWallet(cookie)
	if err != nil && (errors.Is(err, errConsoleUnauthorized) || cookie == "") {
		fresh, derr := c.deriveConsoleSession(a)
		if derr != nil {
			// 派生不可用（账号没带百智云 Cookie / 百智云会话也过期了）：
			// 两边的信息都带上，用户才知道下一步该做什么。
			return 0, nil, fmt.Errorf("%w；自动续期失败：%v", err, derr)
		}
		adoptConsoleCookie(a, fresh)
		w, err = c.fetchWallet(fresh)
	}
	if err != nil {
		return 0, nil, err
	}

	credits := milliCredits(w.Data.Balance)
	items := []provider.ResourceItem{{
		Name: "积分余额", Total: credits, Remain: credits, Usable: true, Key: "credits",
	}}
	// 每日 Token 额度：单位与积分不同，只展示不参与合计（InfoOnly）。
	if w.Data.DailyTokenLimit > 0 || w.Data.DailyTokenBalance > 0 {
		used := w.Data.DailyTokenLimit - w.Data.DailyTokenBalance
		if used < 0 {
			used = 0
		}
		items = append(items, provider.ResourceItem{
			Name:     "每日 Token 额度",
			Total:    w.Data.DailyTokenLimit,
			Used:     used,
			Remain:   w.Data.DailyTokenBalance,
			Usable:   true,
			InfoOnly: true,
			Key:      "daily_token",
		})
	}
	return credits, items, nil
}

// errConsoleUnauthorized 控制台会话失效（上游 401）。触发一次派生重试。
var errConsoleUnauthorized = errors.New("monkeycode：控制台会话已失效")

// fetchWallet 用给定会话打钱包接口。
func (c *Client) fetchWallet(cookie string) (*walletResponse, error) {
	if cookie == "" {
		return nil, errors.New("monkeycode：账号缺少控制台 Cookie，无法查询积分（客户端重新登录后再次导入即可）")
	}
	req, err := http.NewRequest(http.MethodGet, c.console()+epWallet, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", CookieNameConsole+"="+cookie)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errConsoleUnauthorized
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("monkeycode：钱包接口 HTTP %d", resp.StatusCode)
	}
	var w walletResponse
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("monkeycode：钱包响应解析失败：%w", err)
	}
	if w.Code != 0 {
		return nil, fmt.Errorf("monkeycode：钱包接口 code=%d message=%s", w.Code, w.Message)
	}
	return &w, nil
}

// deriveConsoleSession 用百智云会话现换一份控制台会话，返回 `monkeycode_ai_session` 的值。
//
// 背景：MonkeyCode 不是自建登录，而是长亭百智云（baizhi.cloud）SSO 的 OAuth 客户端；
// 控制台会话（≈6 天）由百智云会话（≈29 天）派生。动力与实测见评估文档 §3.14 ④。
//
// 链路（与官方前端「同意授权」按钮跳转的完全同形，纯 HTTP 两步）：
//
//	GET  {baizhiBase}/api/v1/oauth/authorize?client_id=monkeycode-ai
//	       &redirect_uri=<回调>&scope=user phone&state=<随机>&response_type=code
//	     Cookie: baizhi_session=…
//	  → 302 Location: {consoleBase}/api/v1/users/baizhi/callback?code=…&state=…
//	GET  <Location>（不带 Cookie）
//	  → 302 + Set-Cookie: monkeycode_ai_session=…
//
// redirect_uri 由 c.console() 拼出，故测试里把 Console 指到假上游即可整条闭环。
// 注意**不能自动跟随重定向**：回调那步的 302 里才带 Set-Cookie，跟随会把会话
// 落到 CookieJar 里而非拿到手里（本客户端没有 Jar）。
func (c *Client) deriveConsoleSession(a *auth.Auth) (string, error) {
	baizhi := baizhiCookieOf(a)
	if baizhi == "" {
		return "", errors.New("monkeycode：账号没有百智云会话 Cookie，无法续期控制台会话")
	}
	state, err := randomState()
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("client_id", oauthClientID)
	q.Set("redirect_uri", c.console()+oauthCallbackPath)
	q.Set("scope", oauthScope)
	q.Set("state", state)
	q.Set("response_type", "code")

	req, err := http.NewRequest(http.MethodGet, c.baizhi()+epOAuthAuthorize+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Cookie", CookieNameBaizhi+"="+baizhi)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	noRedirect := *c.HTTP
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode == http.StatusUnauthorized {
		return "", errors.New("monkeycode：百智云会话已失效（需在客户端重新登录后再次导入）")
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		u, perr := url.Parse(loc)
		if perr != nil {
			return "", fmt.Errorf("monkeycode：授权回调地址无法解析：%w", perr)
		}
		if got := u.Query().Get("state"); got != state {
			return "", errors.New("monkeycode：授权回调 state 不匹配（疑似被劫持），已放弃")
		}
		if ec := u.Query().Get("error"); ec != "" {
			return "", fmt.Errorf("monkeycode：授权失败 %s %s", ec, u.Query().Get("error_description"))
		}
		if code := u.Query().Get("code"); code != "" {
			return c.exchangeCallback(loc)
		}
	}
	return "", fmt.Errorf("monkeycode：授权未返回 code（HTTP %d，%s）", resp.StatusCode, snippet(raw))
}

// exchangeCallback 走回调端点把 code 换成控制台会话。
func (c *Client) exchangeCallback(callbackURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, callbackURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	noRedirect := *c.HTTP
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	for _, ck := range resp.Cookies() {
		if ck.Name == CookieNameConsole && ck.Value != "" {
			return ck.Value, nil
		}
	}
	return "", fmt.Errorf("monkeycode：回调未下发控制台会话（HTTP %d，%s）", resp.StatusCode, snippet(raw))
}

// adoptConsoleCookie 把派生出的控制台会话写回账号：内存立即生效 + 尝试落盘。
//
// 落盘的意义是**下次启动不必再派生一次** —— 每次派生都会在上游新建一个会话。
// 落盘失败只影响下次启动（会再派生一次），故只记日志不影响本次查询。
func adoptConsoleCookie(a *auth.Auth, cookie string) {
	if a == nil {
		return
	}
	a.Lock()
	a.ConsoleCookie = cookie
	a.Unlock()
	if err := a.SaveAtomic(); err != nil {
		log.Printf("monkeycode：控制台会话落盘失败（仅影响下次启动）：%v", err)
	}
}

// randomState 生成 OAuth state（防回调被替换）。
func randomState() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// snippet 截一段响应体用于错误信息（不含凭据，只是排障线索）。
func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 120 {
		s = s[:120]
	}
	if s == "" {
		return "空响应"
	}
	return s
}

// milliCredits 毫积分 → 积分。取整方式与 console 前端一致（`Math.ceil(balance/1e3)`），
// 保证面板数字与用户在自己控制台看到的相同。
func milliCredits(milli int64) int64 {
	if milli <= 0 {
		return 0
	}
	return (milli + 999) / 1000
}

// DailyCheckin 见 checkin.go（2026-10-17 起本渠道有签到：POST {console}/api/v1/users/wallet/checkin）。
// 历史：此前这里返回「本渠道无签到活动」的桩，渠道也因此在前端名单里被列为无签到；
// 桌面端现身含该端点，故移入 checkin.go 实现。

// Classify 上游错误分类。
//
// 关键：`403 {"error":"invalid ohmyagent request"}` 是**签名/协议错误**，
// 不是账号问题 → 必须归 ErrPassthrough（原文透传、不罚号）。
// 若按「403 = 账号不可用」处理会把健康账号喂到冷却，整条渠道随签名细节波动下线。
func (c *Client) Classify(status int, body string) provider.ErrKind {
	if provider.CodeMarker(strings.ToLower(body), "model_not_found") {
		return provider.ErrBadParams // 本地拒绝的未知模型：请求级，不罚号
	}
	switch {
	case status == http.StatusUnauthorized:
		return provider.ErrSessionDead // key 失效：需重新导入客户端凭据
	case status == http.StatusTooManyRequests:
		return provider.ErrSoftRate
	case status >= 500:
		return provider.ErrServer
	case status >= 400:
		// 403（签名不匹配）/400（body 不被接受）等一律请求级透传
		return provider.ErrPassthrough
	default:
		return provider.ErrNone
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// apiKeyOf 取账号的 api_key（oma_…）。兼容手填 auth 文件时写在 refreshToken 的情况。
func apiKeyOf(a *auth.Auth) string {
	if a == nil {
		return ""
	}
	if k := strings.TrimSpace(a.AccessToken); k != "" {
		return k
	}
	return strings.TrimSpace(a.RefreshToken)
}

// secretOf 取账号的 signing_secret（omas_…）。
func secretOf(a *auth.Auth) string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.SigningSecret)
}

// consoleCookieOf 取账号的控制台会话 Cookie（monkeycode_ai_session）。
// 仅控制台接口（钱包等）用得到；老凭据里为空。
func consoleCookieOf(a *auth.Auth) string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.ConsoleCookie)
}

// baizhiCookieOf 取账号的百智云会话 Cookie（控制台会话的上游来源）。
func baizhiCookieOf(a *auth.Auth) string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.BaizhiCookie)
}
