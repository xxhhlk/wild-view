// client.go WorkBuddy 国际版上游客户端：对话 / 刷新 / 目录 / 余额 / 签到，
// 实现 provider.Upstream 接口。
package workbuddyai

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// Client 国际版上游 HTTP 客户端。Base 可覆盖以便测试。
type Client struct {
	HTTP *http.Client

	Base string // 默认 https://www.workbuddy.ai
}

// New 生产默认。配置连接池减少 TLS 握手。
func New() *Client { return NewWithTimeout(120 * time.Second) }

// NewWithTimeout 指定非流式超时。Transport 与 CN 同构：禁 h2 + Dial/keepalive/TLS 握手 + ResponseHeaderTimeout。
func NewWithTimeout(timeout time.Duration) *Client {
	return &Client{
		HTTP: &http.Client{Timeout: timeout, Transport: newTransport()},

		Base: WBAIHost,
	}
}

// newTransport 出厂 Transport：禁 h2 + Dial/keepalive/TLS 握手 + ResponseHeaderTimeout。
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
		ResponseHeaderTimeout: 60 * time.Second,
	}
}

// NewWithBase 测试用：覆盖 base。
func NewWithBase(base string) *Client {
	c := New()
	c.Base = base
	return c
}

func (c *Client) base() string {
	if c.Base == "" {
		return WBAIHost
	}
	return c.Base
}

// ---------------------------------------------------------------------------
// 请求头
// ---------------------------------------------------------------------------

// commonHeaders 所有 API 共享的头（与国内版 internal/upstream/headers.go 同构）。
//
// ⚠️ 2026-10-21 随上游 R117 删掉本包自造的 X-Machine-ID / X-Session-ID：
// 官方桌面客户端**不发**这两个头（对本机 WorkBuddy.app 5.8.3 静态解包取证），
// 而「混合身份」（真的是官方那一串 + 自造的这一对）本身就是反作弊视角下的特征。
// 上一版曾用 A/B 探针（52 次直连）确认这两个头与 14003 限流无关，补与不补都不影响
// 成功率 —— 既然无收益又增加身份噪声，按上游口径删除。
func commonHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-CodeBuddy-Request", "1")
	req.Header.Set("Origin", originRef)
	req.Header.Set("Referer", originRef+"/")
	req.Header.Set("User-Agent", clientUA)
	req.Header.Set("Accept-Language", "en-US")
}

// billingHeaders 余额 / 签到接口头。
func billingHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	req.Header.Set("X-Domain", wbaiDomain)
}

// refreshHeaders refresh 端点专属头（X-Refresh-Token 只允许出现在这里）。
// refreshHeaders refresh 端点专属头。调用方必须持有 a.Lock()，本函数直接读 RefreshToken。
func refreshHeaders(req *http.Request, a *auth.Auth) {
	commonHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Refresh-Token", a.RefreshToken)
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
	}
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
}

// ---------------------------------------------------------------------------
// 信封与错误
// ---------------------------------------------------------------------------

// apiEnvelope 上游统一信封。
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// doJSON 发请求并解信封；HTTP 非 2xx 或业务 code != 0 时返回带分类的 *provider.Error。
func (c *Client) doJSON(req *http.Request) (json.RawMessage, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, &provider.Error{Kind: Classify(resp.StatusCode, string(raw)), Status: resp.StatusCode, Msg: truncate(string(raw), 200)}
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse failed: %w (body: %s)", err, truncate(string(raw), 120))
	}
	if env.Code != 0 {
		kind := Classify(resp.StatusCode, env.Msg)
		if kind == provider.ErrNone {
			kind = provider.ErrClient
		}
		return nil, &provider.Error{Kind: kind, Status: resp.StatusCode,
			Msg: fmt.Sprintf("code=%d msg=%s", env.Code, truncate(env.Msg, 160))}
	}
	return env.Data, nil
}

// ---------------------------------------------------------------------------
// provider.Upstream 实现
// ---------------------------------------------------------------------------

// RefreshToken 刷新 access token；成功时更新 a 的字段，调用方负责 SaveAtomic。
// 全程持 a 锁，防止并发读写半更新 token。refreshToken 会轮换，必须写回。
func (c *Client) RefreshToken(a *auth.Auth) error {
	a.Lock()
	defer a.Unlock()
	if strings.TrimSpace(a.RefreshToken) == "" {
		return fmt.Errorf("no refreshToken")
	}
	log.Printf("workbuddyai refresh start uid=%s", a.UID)
	req, err := http.NewRequest(http.MethodPost, c.base()+EpRefresh, nil)
	if err != nil {
		return err
	}
	refreshHeaders(req, a)
	data, err := c.doJSON(req)
	if err != nil {
		log.Printf("workbuddyai refresh failed uid=%s err=%v", a.UID, err)
		return err
	}
	var tok struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if err := json.Unmarshal(data, &tok); err != nil || tok.AccessToken == "" {
		err := fmt.Errorf("refresh_failed: no accessToken in response — re-login required")
		log.Printf("workbuddyai refresh failed uid=%s err=%v", a.UID, err)
		return err
	}
	oldRefresh := a.RefreshToken
	a.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		a.RefreshToken = tok.RefreshToken
	}
	if tok.Domain != "" {
		a.Domain = tok.Domain
	}
	// 响应缺 expiresIn 时保留旧过期时间，避免刷新风暴。
	if tok.ExpiresIn > 0 {
		a.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix()
	}
	log.Printf("workbuddyai refresh success uid=%s refresh_rotated=%t expires_at=%d",
		a.UID, a.RefreshToken != oldRefresh, a.ExpiresAt)
	return nil
}

// FetchModels 拉取国际版模型目录。
// 目录内模型剔除 brokenModels（实测 11102 不可用），再补入 extraModels
// （目录不返回但实测可用，含免费模型 deepseek-v4.1-flash）。
// FetchModels 返回当前可用模型清单（issue #39 起为 v2 ∪ v3 合并口径）。
//
// 目录来源两层：
//   - /v2/enterprises/personal/models（cli agent 面，权威顺序）；
//   - /v3/config（全量面，2026-09-28 实测可用，带能力与倍率字段）。
//
// v3 失败不拖累主路：退化为原 v2+extraModels 行为。
func (c *Client) FetchModels(a *auth.Auth) ([]provider.ModelInfo, error) {
	raws, err := c.fetchCatalog(a)
	if err != nil {
		return nil, err
	}
	// v3 增强：补 v2 缺失的模型、以 v3 的能力/容量字段修正重复项。
	// v3 里的模型也要过 brokenModels/disabled 过滤（同一套口径）。
	if v3, verr := c.fetchV3Catalog(a); verr == nil {
		raws, _ = mergeV3(raws, v3)
	}
	seen := make(map[string]bool, len(raws)+len(extraModels))
	out := make([]provider.ModelInfo, 0, len(raws)+len(extraModels))
	for _, m := range raws {
		if m.ID == "" || m.Disabled || brokenModels[m.ID] || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		def := m.Reasoning.DefaultEffort
		if def == "" {
			def = m.Reasoning.Effort
		}

		mi := provider.ModelInfo{
			ID:            m.ID,
			Name:          m.Name,
			ContextWindow: m.MaxInputTokens,
			MaxTokens:     m.MaxOutputTokens,
			// 目录接口真实返回的容量
			ContextFromAPI: true,
			// 能力：直接透传上游声明（不自行纠正上游与实际不符的情况）
			SupportsImages:    m.imageOK(),
			SupportsReasoning: m.SupportsReasoning,
			SupportsTools:     m.SupportsToolCall,
			// 档位能力（远端权威，缺失时由 internal/reasoning 静态表兜底）
			SupportedEfforts:       m.Reasoning.SupportedEfforts,
			ReasoningEfforts:       provider.CleanEfforts(m.Reasoning.SupportedEfforts),
			DefaultEffort:          def,
			ReasoningCanDisable:    m.Reasoning.CanDisableThinking,
			DefaultReasoningEffort: def,
		}
		out = append(out, mi)
	}
	// 硬编码补入目录外可用模型
	for _, m := range extraModels {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("models api returned empty list")
	}
	return out, nil
}

// FetchModelPricing 拉取模型倍率。费率面板「仅供参考」，
// 因此直接引用目录返回的 credits，不做增删（不含 extraModels、不剔除 brokenModels）。
//
// issue #39：v2 目录不含 deepseek-v4.1-flash/gpt-6-astra/kimi-k2.8-preview 等
// extraModels，它们此前永远显示 unknown。/v3/config 对这批模型返回了真实倍率
// （x0.00 / x6.67 / x0.77，2026-09-28 实测），故在 v2 基础上并入 v3 的倍率表。
// v3 也没有的模型（deepseek-v3/glm-5.1/glm-5v-turbo/hy4-preview-f/kimi-k2.7/
// minimax-m3）上游确实无费率数据，继续由面板显示 unknown——不编数据。
func (c *Client) FetchModelPricing(a *auth.Auth) ([]provider.ModelPricing, error) {
	raws, err := c.fetchCatalog(a)
	if err != nil {
		return nil, err
	}
	// v3 倍率并入（同 id 时 v3 非空优先，v3 独有模型直接新增）
	var v3Rates map[string]string
	if v3, verr := c.fetchV3Catalog(a); verr == nil {
		raws, v3Rates = mergeV3(raws, v3)
	}
	// 促销折扣（modelPromotions）优先：discount.factor==0 表示当前免费。
	promo := c.fetchPromotions(a)
	out := make([]provider.ModelPricing, 0, len(raws))
	for _, m := range raws {
		if m.ID == "" {
			continue
		}
		rate := parseCredits(m.Credits)
		note := badgeOf(m.ID)
		if r, ok := promo[m.ID]; ok {
			rate = r
			if note == "" {
				note = "限时免费"
			}
		}
		out = append(out, provider.ModelPricing{
			Model:   m.ID,
			Channel: ChannelName,
			Rate:    rate,
			Note:    note,
			Color:   colorOf(m.ID),
		})
	}
	// v3 独有但 mergeV3 后仍在 raws 里的条目已含 credits；
	// 这里补一层：v3 有倍率而 v2 条目 credits 为空的（如 v3 面更新的情况）。
	if v3Rates != nil {
		for i := range out {
			if r, ok := v3Rates[out[i].Model]; ok && parseCredits(r) > 0 && out[i].Rate == 0 {
				out[i].Rate = parseCredits(r)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("pricing api returned empty models")
	}
	return out, nil
}

// UserResource 查询账号可花费积分余额（所有套餐 CycleCapacity* 聚合，负值钳 0）。
func (c *Client) UserResource(a *auth.Auth) (int64, error) {
	total, _, err := c.userResource(a)
	return total, err
}

// UserResourceDetail 查询积分明细（所有套餐条目）。
func (c *Client) UserResourceDetail(a *auth.Auth) (int64, []provider.ResourceItem, error) {
	return c.userResource(a)
}

// softRateResetLoc 上游墙钟时间口径：固定按 UTC+8 解释。
// 上游下发的 CycleEndTime 等时间串均为国内时区墙钟，用 time.Local 解析会在
// 非 UTC+8 机器上把到期日算错一天。
var softRateResetLoc = time.FixedZone("UTC+8", 8*60*60)

// userResource 单次请求同时产出总额与明细。
func (c *Client) userResource(a *auth.Auth) (int64, []provider.ResourceItem, error) {
	now := time.Now()
	body, _ := json.Marshal(map[string]any{
		"PageNumber":               1,
		"PageSize":                 100,
		"ProductCode":              ProductCode,
		"Status":                   []int{0, 3},
		"PackageEndTimeRangeBegin": now.Format("2006-01-02 15:04:05"),
		"PackageEndTimeRangeEnd":   now.Add(365 * 101 * 24 * time.Hour).Format("2006-01-02 15:04:05"),
	})
	req, err := http.NewRequest(http.MethodPost, c.base()+EpUserResource, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	billingHeaders(req, a)
	data, err := c.doJSON(req)
	if err != nil {
		return 0, nil, err
	}
	var resp struct {
		Response struct {
			Data struct {
				Accounts []struct {
					PackageName         string `json:"PackageName"`
					CapacitySize        int64  `json:"CapacitySize"`
					CapacityRemain      int64  `json:"CapacityRemain"`
					CapacityUsed        int64  `json:"CapacityUsed"`
					CycleCapacitySize   int64  `json:"CycleCapacitySize"`
					CycleCapacityRemain int64  `json:"CycleCapacityRemain"`
					CycleCapacityUsed   int64  `json:"CycleCapacityUsed"`
					// CycleEndTime 到期时间（"2006-01-02 15:04:05"，UTC+8 墙钟）。
					CycleEndTime string `json:"CycleEndTime"`
				} `json:"Accounts"`
			} `json:"Data"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, nil, fmt.Errorf("resource parse: %w", err)
	}
	var total int64
	items := make([]provider.ResourceItem, 0, len(resp.Response.Data.Accounts))
	for _, acct := range resp.Response.Data.Accounts {
		// 周期额度 > 0 时以周期口径为准，否则回退容量口径（与国内版一致）。
		var tot, used, remain int64
		switch {
		case acct.CycleCapacitySize > 0:
			tot, used, remain = acct.CycleCapacitySize, acct.CycleCapacityUsed, acct.CycleCapacityRemain
		case acct.CycleCapacityRemain > 0 || acct.CycleCapacityUsed > 0:
			tot, used, remain = acct.CycleCapacityRemain+acct.CycleCapacityUsed, acct.CycleCapacityUsed, acct.CycleCapacityRemain
		default:
			tot, used, remain = acct.CapacitySize, acct.CapacityUsed, acct.CapacityRemain
		}
		if remain < 0 {
			remain = 0
		}
		total += remain
		items = append(items, provider.ResourceItem{
			Name:     acct.PackageName,
			Total:    tot,
			Used:     used,
			Remain:   remain,
			ExpireAt: expireDate(acct.CycleEndTime),
			// 套餐名+到期日组合伪键（同国内版口径）：键只取日期前缀，值带精确时刻。
			Key:    fmt.Sprintf("%s|%s", acct.PackageName, expireDay(acct.CycleEndTime)),
			Usable: true, // 国际版无端点分区，所有套餐均可被本工具消耗
		})
	}
	return total, items, nil
}

// expireDate 把上游墙钟时间串（UTC+8）转为 **RFC3339 精确时刻**；缺失/不可解析时返回空串。
//
// 历史：旧实现返回 "YYYY-MM-DD"，丢了时分秒。面板按「N 小时内到期」分桶需要精确时刻
// （2026-10-19 实测：按日期桶会把今晚失效的包报成「明日到期」）。
//
// ⚠ 伪键不要用本函数（会带时刻）：Key 必须用 expireDay() 取日期前缀，否则升级瞬间
// 每个包一对 expire+earn 假流水。
func expireDate(ts string) string {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return ""
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, softRateResetLoc); err == nil {
		return t.Format(time.RFC3339)
	}
	return ""
}

// expireDay 取到期日的**日期前缀**（"YYYY-MM-DD"），专供 ledger 伪键使用。
// 与 expireDate() 分开的原因见其注释：键要稳定（不含时刻），值要精确（含时刻）。
func expireDay(ts string) string {
	return provider.ExpireDate(expireDate(ts))
}

// DailyCheckin 本渠道签到依赖对话，不支持。
func (c *Client) DailyCheckin(_ *auth.Auth) error {
	return fmt.Errorf("workbuddyai：签到依赖对话，当前不支持")
}

// Classify 实现 provider.Upstream。
func (c *Client) Classify(status int, body string) provider.ErrKind { return Classify(status, body) }
