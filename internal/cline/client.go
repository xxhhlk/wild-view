package cline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// Client 实现 provider.Upstream。
type Client struct {
	HTTP *http.Client

	// Base 网关基址（默认 APIBase）；测试注入 httptest 假上游用，生产不设。
	Base string

	knownMu sync.RWMutex
	// knownIDs 是已知模型集（ID → 展示名，来自 free[]），也是本地模型校验集。
	// 冷启动（未拉到目录）时校验放行 —— 见 models.go 的 knownModel 注释。
	knownIDs map[string]string
	capMu    sync.RWMutex
	capped   map[string]time.Time
}

// New 构造客户端（非流式 120s 总超时；一问一答需要兜底，故保留 Timeout）。
func New() *Client {
	tr := newTransport()
	return &Client{
		HTTP: &http.Client{Timeout: 120 * time.Second, Transport: tr},

		knownIDs: map[string]string{},
		capped:   map[string]time.Time{},
	}
}

// newTransport 出厂 Transport：禁 h2 + ResponseHeaderTimeout 兜底。
// ResponseHeaderTimeout 兜底：上游迟迟不回响应头时不至于无限挂住。
func newTransport() *http.Transport {
	return &http.Transport{
		DisableKeepAlives:     false,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 90 * time.Second,
		DisableCompression:    false,
	}
}

func (c *Client) base() string {
	if b := strings.TrimRight(strings.TrimSpace(c.Base), "/"); b != "" {
		return b
	}
	return defaultBaseURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 120 * time.Second, Transport: newTransport()}
	}
	return c.HTTP
}

// applyHeaders 统一出站头。
//
// ★ X-CLIENT-TYPE 必须带且必须非空：缺这个头，所有 `cline-free/*` 一律 403
//
//	API_REQUEST_ERROR_CODE，且**症状是「免费模型全部消失」而不报任何配置错误**。
//	值本身不参与判定（实测 bogus-value 同样放行），故固定写死，勿当优化点删除。
func applyHeaders(req *http.Request, key string) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set(HeaderClientType, HeaderClientTypeValue)
	if key != "" {
		req.Header.Set(HeaderAuth, "Bearer "+key)
	}
}

func keyOf(a *auth.Auth) string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.AccessTokenValue())
}

// ---------------------------------------------------------------------------
// 对话
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 凭据探针（面板添加 Key 用：零对话成本，只读自己的 Key 列表）
// ---------------------------------------------------------------------------

type apiKeyInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	KeyPrefix string `json:"key_prefix"`
}

type apiKeysResp struct {
	Data struct {
		Keys []apiKeyInfo `json:"keys"`
	} `json:"data"`
	Success bool `json:"success"`
}

// ProbeResult 是 ValidateKey 的产出。
type ProbeResult struct {
	UID         string   // 稳定的 apk- id；取不到时退化为 key 的 sha256 前缀
	KeyPrefix   string   // 本 Key 的官方回显前缀
	SiblingKeys []string // 同一账号下的其它 Key 前缀（用于「同账号不扩额度」告警）
	AccountName string   // Key 的 name 字段，仅展示
}

// ValidateKey 校验 API Key 并取回身份（面板添加账号入口，见 app.SubmitClineKey）。
//
// 只调 GET /api/v1/api-keys —— **零 token 消耗、零副作用**，避免「添加账号即烧额度」。
// uid 取该端点里 key_prefix 与本 Key 相符的那条 id（实测 key_prefix == key 前 10 字符）。
// 同账号判定：该端点返回**本账号全部 Key**，故能列出兄弟 Key 的前缀 ——
// 由于配额键名是 `…UsageCostUSDPerUser`（按账号计），同账号多 Key **不会**扩额度，调用方据此告警。
func ValidateKey(key string) (*ProbeResult, error) { return New().probeKey(key) }

// probeKey 是 ValidateKey 的可注入实现（Base 指向 httptest 假上游即可单测）。
func (c *Client) probeKey(key string) (*ProbeResult, error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "sk_") {
		return nil, fmt.Errorf("Cline API Key 应以 sk_ 开头（在 app.cline.bot → Account / API Keys 创建）")
	}
	req, err := http.NewRequest(http.MethodGet, c.base()+EpAPIKeys, nil)
	if err != nil {
		return nil, err
	}
	applyHeaders(req, key)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("Key 无效或被拒（HTTP %d）：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		return nil, fmt.Errorf("校验 Key 失败 HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var r apiKeysResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("api-keys 响应无法解析：%v", err)
	}
	prefix := key
	if len(prefix) > KeyPrefixLen {
		prefix = prefix[:KeyPrefixLen]
	}
	res := &ProbeResult{KeyPrefix: prefix}
	for _, k := range r.Data.Keys {
		kp := strings.TrimSpace(k.KeyPrefix)
		if kp == "" {
			continue
		}
		if kp == prefix {
			res.UID = strings.TrimSpace(k.ID)
			res.AccountName = strings.TrimSpace(k.Name)
			continue
		}
		res.SiblingKeys = append(res.SiblingKeys, kp)
	}
	if res.UID == "" {
		// 列表里没有自己（上游形态变化）⇒ 用 hash 兜底，仍保证稳定且不含明文。
		sum := sha256.Sum256([]byte(key))
		res.UID = "k" + hex.EncodeToString(sum[:])[:16]
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// Upstream 其余方法
// ---------------------------------------------------------------------------

// RefreshToken 空实现：官方 API Key **不轮换**。
// 因此不变量 20（refresh 后必须 SaveAtomic）在本渠道不适用，
// 且 R26/R44 那种「同凭据多处使用互踢」的问题天然不存在。
func (c *Client) RefreshToken(a *auth.Auth) error { return nil }

// UserResource 恒 0 且**不得返回 error**：上游确无额度/余额端点
// （/credits /usage /billing /account /quota /me 等 12 个候选实测全 404）。
// 返回 error 会让面板与池逻辑报错，而"0"才是这里的正确事实。
func (c *Client) UserResource(a *auth.Auth) (int64, error) { return 0, nil }

// UserResourceDetail 同上：可消耗余额 0、无明细。
func (c *Client) UserResourceDetail(a *auth.Auth) (int64, []provider.ResourceItem, error) {
	return 0, nil, nil
}

// DailyCheckin 无签到活动。调度器侧 CheckinHours 传 []int{}（R39），
// 且 noExplicitCheckin 会挡掉面板手工签到，故本方法正常情况下不会被调用。
func (c *Client) DailyCheckin(a *auth.Auth) error { return fmt.Errorf("cline 无签到活动") }

// FetchModelPricing 上游无费率端点；单价只在逐请求 usage 里（cost/cost_details），
// 由 ledger 记账，不进费率表。返回空不报错（面板据此显示「不适用」）。
func (c *Client) FetchModelPricing(a *auth.Auth) ([]provider.ModelPricing, error) {
	return nil, nil
}

// Classify 实现 provider.Upstream。
func (c *Client) Classify(status int, body string) provider.ErrKind { return Classify(status, body) }

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

var modelRE = regexp.MustCompile(`"model"\s*:\s*"([^"]+)"`)

// getJSON 发一个 GET 并回原始 body（带 Bearer，虽然两个目录端点实测无需鉴权）。
func (c *Client) getJSON(a *auth.Auth, path string) (json.RawMessage, error) {
	req, err := http.NewRequest(http.MethodGet, c.base()+path, nil)
	if err != nil {
		return nil, err
	}
	applyHeaders(req, keyOf(a))
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &provider.Error{Kind: Classify(resp.StatusCode, string(raw)),
			Status: resp.StatusCode, Msg: truncate(string(raw), 240)}
	}
	return json.RawMessage(raw), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
