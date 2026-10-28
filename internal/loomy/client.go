package loomy

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// Client 讯飞 Loomy 上游客户端。
// 与既有渠道不同，本渠道不做登录编排（凭据由「从本机客户端导入」得到），
// 且**无 refresh 端点**：session 到期需重新导入。
type Client struct {
	HTTP *http.Client

	// liveMu 保护 liveIDs（动态目录缓存）——与 raccoon 同款缺陷的修复：
	// 本地校验模型名时若**只查静态表**，上游新增的模型会被误拒，
	// 而它正被 /v1/models 正常透出（后者读动态目录）⇒「面板列出却调不动」。
	// 用户需要它是因为：静态表基于 2026-09 实测，上游随时新增/下线模型。
	liveMu  sync.RWMutex
	liveIDs map[string]bool

	// Base 网关基址（默认 GatewayBase）；测试注入 httptest 假上游用，生产不设。
	Base string
}

// baseURL 生效的网关基址。
func (c *Client) baseURL() string {
	if b := strings.TrimRight(strings.TrimSpace(c.Base), "/"); b != "" {
		return b
	}
	return GatewayBase
}

// New 默认 180s 超时（查询请求统一使用）。
func New() *Client { return NewWithTimeout(180 * time.Second) }

// NewWithTimeout 指定非流式超时。
func NewWithTimeout(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	return &Client{
		HTTP: &http.Client{Timeout: timeout, Transport: newTransport()},
	}
}

// newTransport 出厂 Transport：禁 h2 + Dial/keepalive + TLS 握手 + ResponseHeaderTimeout。
//
// ⚠️ 此前本包 `&http.Client{Timeout: ...}` 未设 Transport，实际在共用 http.DefaultTransport
// （h2 开启、无 ResponseHeaderTimeout）。查询请求必须有
// ResponseHeaderTimeout 兜底，否则连响应头都等不到就会无限挂住。
func newTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	return &http.Transport{
		DialContext:           dialer.DialContext,
		TLSNextProto:          make(map[string]func(string, *tls.Conn) http.RoundTripper), // 强制 HTTP/1.1
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
}

// httpClient 返回非流式调用用的客户端（models/points/refresh 的 do()）。
// 仍有总超时：这些是短请求，总超时是恰当的兜底。
func (c *Client) httpClient() *http.Client {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 180 * time.Second, Transport: newTransport()}
	}
	return c.HTTP
}

// sessionOf 读取凭据里的 session（nil 安全）。
//
// nil Auth 必须安全：签到/余额这类路径可能在「账号刚被移除」时被调度器调用，
// 旧写法 `a.AccessTokenValue()` 会直接空指针 panic（2026-10-17 由 checkin 测试抓到）。
func sessionOf(a *auth.Auth) string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.AccessTokenValue())
}

func session(a *auth.Auth) string { return sessionOf(a) }

// traceparent 生成 W3C traceparent；官方源码注释实测：**缺这个头 chat/completions 会挂死到超时**。
func traceparent() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00-" + strings.Repeat("0", 32) + "-" + strings.Repeat("0", 16) + "-01"
	}
	return "00-" + hex.EncodeToString(b[:16]) + "-" + hex.EncodeToString(b[16:]) + "-01"
}

// applyAuth 写鉴权与追踪头（官方客户端 session 模式是 token 头 + Authorization **双写**）。
func applyAuth(req *http.Request, a *auth.Auth) {
	tok := session(a)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("token", tok)
	req.Header.Set("traceparent", traceparent())
	req.Header.Set("loomy-version", AppVersion)
	req.Header.Set("Accept", "application/json")
}

// do 发一次带鉴权的非流式请求。
func (c *Client) do(method, url string, a *auth.Auth, body []byte) (*http.Response, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	applyAuth(req, a)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp, raw, rerr
}

// ---------------------------------------------------------------------------
// 凭据
// ---------------------------------------------------------------------------

// RefreshToken 实现 provider.Upstream。
// 上游**没有 refresh 端点**（客户端登录时请求 14 天有效期的 session，见 docs 备忘）：
// `utils/remote-keepalive.js` 经查是电源保活，与登录态无关。因此这里返回明确错误，
// 让调用方走 session_dead 分支提示用户重新导入，而不是发出一个必然失败的请求。
func (c *Client) RefreshToken(a *auth.Auth) error {
	return &provider.Error{
		Kind: provider.ErrSessionDead, Status: 0,
		Msg: "loomy: 上游无 refresh 端点（session 有效期 14 天），请在面板重新「从本机客户端导入」",
	}
}

// ---------------------------------------------------------------------------
// 推理
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 模型与费率
// ---------------------------------------------------------------------------

type loomyModel struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	ContextLength          int64    `json:"context_length"`
	MaxOutputTokens        int64    `json:"max_output_tokens"`
	ReasoningEfforts       []string `json:"reasoning_efforts"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort"`
	Capabilities           struct {
		Reasoning       bool     `json:"reasoning"`
		Vision          bool     `json:"vision"`
		FunctionCalling bool     `json:"function_calling"`
		InputModalities []string `json:"input_modalities"`
	} `json:"capabilities"`
}

// modelsResp /models 信封：只保留被消费的字段。
type modelsResp struct {
	Data []loomyModel `json:"data"`
}

// fetchModels 拉取上游模型目录。
func (c *Client) fetchModels(a *auth.Auth) (*modelsResp, error) {
	resp, raw, err := c.do(http.MethodGet, c.baseURL()+EpModels, a, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.Error{
			Kind: Classify(resp.StatusCode, string(raw)), Status: resp.StatusCode, Msg: truncate(string(raw), 300),
		}
	}
	var out modelsResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("loomy 模型目录解析失败: %w (%s)", err, truncate(string(raw), 200))
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("loomy 模型目录为空")
	}
	// 记下目录里的模型名，供本地模型校验取并集（同 raccoon）。
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			ids = append(ids, id)
		}
	}
	c.rememberLiveModels(ids...)
	return &out, nil
}

// knownModel 模型名是否可接受（本地模型校验入口）。
//
// 判据 = KnownModel（静态表） ∪ 已拉取到的动态目录。
// 动态部分**必须**参与：/v1/models 透出的是动态目录（FetchModels 的结果），
// 只认静态表会导致「面板列出却调不动」。
func (c *Client) knownModel(id string) bool {
	if KnownModel(id) {
		return true
	}
	c.liveMu.RLock()
	ok := c.liveIDs[id]
	c.liveMu.RUnlock()
	return ok
}

// rememberLiveModels 记下动态目录里的模型名（单调扩大，只增不减）。
func (c *Client) rememberLiveModels(ids ...string) {
	c.liveMu.Lock()
	defer c.liveMu.Unlock()
	if c.liveIDs == nil {
		c.liveIDs = make(map[string]bool, len(ids))
	}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			c.liveIDs[id] = true
		}
	}
}

// FetchModels 实现 provider.Upstream（档位直接取上游 reasoning_efforts —— 权威值）。
func (c *Client) FetchModels(a *auth.Auth) ([]provider.ModelInfo, error) {
	resp, err := c.fetchModels(a)
	if err != nil {
		return nil, err
	}
	out := make([]provider.ModelInfo, 0, len(resp.Data))
	for _, m := range resp.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		name := cleanName(m.Name)
		if name == "" {
			name = id
		}
		info := provider.ModelInfo{
			ID:                     id,
			Name:                   name,
			ContextWindow:          m.ContextLength,
			MaxTokens:              m.MaxOutputTokens,
			ContextFromAPI:         m.ContextLength > 0,
			SupportsImages:         m.Capabilities.Vision,
			SupportsReasoning:      m.Capabilities.Reasoning,
			SupportsTools:          m.Capabilities.FunctionCalling,
			SupportedEfforts:       append([]string{}, m.ReasoningEfforts...),
			ReasoningEfforts:       provider.CleanEfforts(m.ReasoningEfforts),
			DefaultEffort:          strings.TrimSpace(m.DefaultReasoningEffort),
			DefaultReasoningEffort: strings.TrimSpace(m.DefaultReasoningEffort),
			ReasoningCanDisable:    false, // 实测该系列"强制思考"，最低档仍会产生思考
		}
		out = append(out, info)
	}
	return out, nil
}

// FetchModelPricing 实现 provider.Upstream：倍率写在模型显示名里（`（x3.0）`），需解析。
func (c *Client) FetchModelPricing(a *auth.Auth) ([]provider.ModelPricing, error) {
	resp, err := c.fetchModels(a)
	if err != nil {
		return nil, err
	}
	out := make([]provider.ModelPricing, 0, len(resp.Data))
	for _, m := range resp.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		rate := priceFromName(m.Name)
		// ⚠️ 每轮必须取**新的**变量地址（2026-10-17 修复）：旧实现把 `explicit` 声明在循环外、
		// 每轮 `&explicit` 后又在末尾复位为 true ⇒ 所有条目共享同一个 bool 且末值恒 true，
		// 于是「解析不到倍率 → 面板显示未知而不是免费」（R5 约定）**静默失效**。
		// 现在按本条自己的解析结果取值：解析不到倍率即为 false。
		explicit := rate > 0
		out = append(out, provider.ModelPricing{
			Model:    id,
			Channel:  ChannelName,
			Rate:     rate,
			Explicit: &explicit,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 额度 / 签到
// ---------------------------------------------------------------------------

type pointsRecord struct {
	Balance *int64 `json:"balance"`
	Points  *int64 `json:"points"`
	Data    struct {
		Balance *int64 `json:"balance"`
		// DailyBalance 每日积分池余量（按 dailyCycleDate 每日循环，扣分优先消耗该池）。
		DailyBalance *int64 `json:"dailyBalance"`
		// AvailableBalance 上游给出的可用总额 = Balance + DailyBalance（**仅 v1 面下发**）。
		AvailableBalance *int64 `json:"availableBalance"`

		CurrentBalance *int64 `json:"currentBalance"`
		RemainPoints   *int64 `json:"remainPoints"`
		TotalPoints    *int64 `json:"totalPoints"`
	} `json:"data"`
}

// UserResource 实现 provider.Upstream：读积分记录接口的余额字段。
//
// 上游积分以「积分」为单位（推理响应里的 `usage.points_consumed` 即扣减量）。
// 端点在阶段 A 静态取证确认、字段名按宽松多候选解析；解析不到一律返回 0（宁缺勿错）。
func (c *Client) UserResource(a *auth.Auth) (int64, error) {
	remain, _, err := c.UserResourceDetail(a)
	return remain, err
}

// UserResourceDetail 实现 provider.Upstream。
//
// 余额口径 = 上游 `availableBalance`（常规池 + 每日池），**不能只取 `balance`** ——
// 2026-09-23 实测该账号 `balance=15000 / dailyBalance=4800 / availableBalance=19800`，
// 只读 balance 会把每日积分整块漏掉（面板少显示、pool 路由口径偏低）。
func (c *Client) UserResourceDetail(a *auth.Auth) (int64, []provider.ResourceItem, error) {
	resp, raw, err := c.do(http.MethodGet, c.baseURL()+EpPointsV1, a, nil)
	if err != nil {
		return 0, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, nil, &provider.Error{
			Kind: Classify(resp.StatusCode, string(raw)), Status: resp.StatusCode, Msg: truncate(string(raw), 300),
		}
	}
	// 上游可能返回 HTTP 200 + 业务错误码（同 100002 的形态）。
	if k := businessErrKind(string(raw)); k != provider.ErrNone {
		return 0, nil, &provider.Error{Kind: k, Status: resp.StatusCode, Msg: truncate(string(raw), 300)}
	}
	var rec pointsRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return 0, nil, nil // 结构未知：不报错、不留余额（避免把账号误判为异常）
	}
	remain, items := parsePoints(rec)
	return remain, items, nil
}

// parsePoints 把上游积分响应拆成 (可消耗余额, 明细条目)。
//
// 上游把积分分成两个池：
//
//	balance       常规池（注册奖励 / 新手任务 / 充值，长期有效）
//	dailyBalance  每日池（每日循环发放，扣分优先消耗；ledger 里的 consumeSource=daily）
//
// 可用总额优先取上游的 `availableBalance`；该字段缺失（v2 面）时按两池相加兜底。
// 抽成纯函数便于单测（Client 的 baseURL 是常量，不便在测试里换 host）。
func parsePoints(rec pointsRecord) (int64, []provider.ResourceItem) {
	base := firstInt(rec.Data.Balance, rec.Balance, rec.Points,
		rec.Data.CurrentBalance, rec.Data.RemainPoints, rec.Data.TotalPoints)
	daily := rec.Data.DailyBalance
	if base == nil && daily == nil {
		return 0, nil
	}
	items := make([]provider.ResourceItem, 0, 2)
	if base != nil {
		items = append(items, provider.ResourceItem{Name: "积分", Total: *base, Remain: *base, Usable: true})
	}
	if daily != nil {
		// 每日池「当日有效」：上游**只下发数值、不下发到期时间**。
		//
		// 历史：旧实现用 provider.ExpireDateToday() 静态补今天，等于编造一个到期时刻
		// （用户 2026-10-19 明确要求：上游没给就不显示，不要造）。现在留空串，
		// 前端「有效期」列自动隐藏该行，且不进「N 小时内到期」分桶（ExpireInstant
		// 对空串返回 ok=false，调用方自然跳过）。
		//
		// 影响面：ledger 差分对 ExpireAt="" 的条目下降时恒归 spend（poolShrankExpiry
		// 门槛 1 要求 ExpireAt 非空），这正是历史行为（ExpireDateToday() 当日不会
		// 触发 expired()），故口径不变。
		items = append(items, provider.ResourceItem{
			Name:   "每日积分",
			Total:  *daily,
			Remain: *daily,
			Usable: true,
		})
	}
	if v := rec.Data.AvailableBalance; v != nil {
		return *v, items
	}
	var sum int64
	if base != nil {
		sum += *base
	}
	if daily != nil {
		sum += *daily
	}
	return sum, items
}

func firstInt(vals ...*int64) *int64 {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// DailyCheckin 实现 provider.Upstream（**无业务日**的窄接口）。
//
// 本渠道的真实领取是 PET-WORK 领奖，请求体**必须**带服务端业务日 + 配置版本 + 档位
// （见 checkin.go）。本方法拿不到业务日，故**永远返回错误**，绝不假成功：
// 返回 nil 会让调度器记一次「已签到」，而实际一分钱没领到。
//
// 正确的入口是 DailyCheckinReport（带业务日），调度器在拿到快照后用业务日调用它。
func (c *Client) DailyCheckin(a *auth.Auth) error {
	return &provider.Error{
		Kind: provider.ErrClient,
		Msg: "loomy: 宠物打工领奖需要服务端业务日（/api/v1/pet-work 快照里的 businessDate），" +
			"不能走无参签到入口；请改用 DailyCheckinReport 或面板的「领取每日积分」",
	}
}

// ---------------------------------------------------------------------------
// 错误分类
// ---------------------------------------------------------------------------

// Classify 实现 provider.Upstream。
func (c *Client) Classify(status int, body string) provider.ErrKind { return Classify(status, body) }

// businessErrKind 识别"HTTP 200 + 业务码"形态的失败（Loomy 的鉴权失效就是这样返回的）。
// 返回 ErrNone 表示不是已知业务错误。
func businessErrKind(body string) provider.ErrKind {
	lower := strings.ToLower(body)
	// 100002 登录态失效；020002 会话校验失败（源码 AUTH_ERROR_CODES）。
	// CodeMarker 的第一个参数要求是**已小写化**的 body。
	if provider.CodeMarker(lower, "100002") || provider.CodeMarker(lower, "020002") {
		return provider.ErrSessionDead
	}
	if strings.Contains(lower, "model_not_found") {
		return provider.ErrBadParams
	}
	return provider.ErrNone
}

// Classify 包级分类。
//
// ⚠️ 顺序关键：上游把**鉴权失效塞在 HTTP 200** 里（{"code":"100002"}），
// 所以业务码判定必须排在状态码判定之前（对齐 AGENTS §6.25）。
func Classify(status int, body string) provider.ErrKind {
	if k := businessErrKind(body); k != provider.ErrNone {
		return k
	}
	lower := strings.ToLower(body)

	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return provider.ErrSessionDead
	}
	if status == http.StatusPaymentRequired {
		return provider.ErrHardCredit
	}
	// 429 优先于 hardMarkers（§6.15）。
	if status == http.StatusTooManyRequests {
		return provider.ErrSoftRate
	}
	for _, m := range hardMarkers {
		if strings.Contains(lower, m) {
			return provider.ErrHardCredit
		}
	}
	if strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many requests") || strings.Contains(lower, "请求过于频繁") {
		return provider.ErrSoftRate
	}
	if status == http.StatusNotFound {
		return provider.ErrNotFound
	}
	if status >= 500 {
		return provider.ErrServer
	}
	if status >= 400 {
		switch {
		case strings.Contains(body, "messages 不能为空"):
			return provider.ErrBadParams
		case strings.Contains(lower, "context length"), strings.Contains(lower, "too long"), strings.Contains(lower, "tokens exceed"):
			return provider.ErrPromptTooLong
		case strings.Contains(lower, "invalid image"), strings.Contains(lower, "image_url"):
			return provider.ErrImageInvalid
		case strings.Contains(lower, "content policy"), strings.Contains(lower, "sensitive"):
			return provider.ErrContentBlocked
		}
		return provider.ErrBadParams
	}
	return provider.ErrNone
}

// hardMarkers 余额/积分不足关键词。
var hardMarkers = []string{
	"积分不足", "额度不足", "余额不足", "积分用完", "额度用尽",
	"insufficient credit", "no credit", "credit exhausted", "out of credit",
	"quota exceeded", "quota exhaust", "payment required",
}

// truncate rune 安全截断。
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}
