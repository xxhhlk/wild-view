// client.go 华为云 CodeArts Agent 渠道客户端，实现 provider.Upstream。
//
// 渠道形态：**正常多账号渠道**（AK/SK + SecurityToken，由 PKCE OAuth 登录或
// refresh_token 静默续期得到），因此**不置 SingleAccount**：账号级错误按正常分类
// 走池内轮换（R24/R29），额度用尽由 ErrHardCredit 表达长冷却。
//
// 与其它渠道最不一样的三点（协议 §2.2/§2.3/§2.4）：
//   - 出站请求靠 **SDK-HMAC-SHA256 签名**（不是 Bearer），且 `maas_type` 必须参与签名、
//     `Agent-Type` 必须不参与——两者写反都是 401 APIG.0301；
//   - 出站体恒 stream:true，且 `max_tokens` 有实测上限（>65536 触发空流）；
//   - 错误既可能挂在非 2xx，也可能是 **HTTP 200 + 流内 `error_code`**，
//     两条路径共用 classify.go 的一套判据。
package codearts

import (
	"crypto/rand"
	"encoding/hex"

	"fmt"

	"net"
	"net/http"

	"strings"
	"sync"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// Client 上游 HTTP 客户端。Base / BenefitBase / STSURL 可覆盖以便测试。
type Client struct {
	// HTTP 带总超时，供控制面（目录 / 积分 / 活动 / 续期）。
	HTTP *http.Client

	// Base 对话与控制面基址（默认 APIBase）。
	Base string
	// BenefitBase benefit 模型目录基址（默认 BenefitBase 常量）。
	BenefitBase string
	// STSURL 凭据换取 / 续期端点（默认 STSTokenURL）。
	STSURL string

	catMu sync.RWMutex // 见 models.go（动态目录 + benefit 集合的进程内缓存）
	cat   *catalogCache
}

// 编译期断言：本包对外的能力契约（Lead 的接线依赖 provider.Upstream 全部 10 个方法；
// 签到另实现 CheckinReporter/CheckinGranter，调度器优先用它们拿到结构化状态）。
var (
	_ provider.Upstream        = (*Client)(nil)
	_ provider.CheckinReporter = (*Client)(nil)
	_ provider.CheckinGranter  = (*Client)(nil)
)

// New 生产默认客户端。
func New() *Client {
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// ResponseHeaderTimeout 兜底：上游迟迟不回响应头时不至于无限挂住
		// 就会无限挂住（与 minimax/kilo 同款理由）。
		ResponseHeaderTimeout: 120 * time.Second,
	}
	return &Client{
		HTTP: &http.Client{Transport: tr, Timeout: requestTimeout},
		// 共用 Transport，不设 Timeout
		Base:        APIBase,
		BenefitBase: BenefitBase,
		STSURL:      STSTokenURL,
	}
}

// NewWithBase 测试用：覆盖对话/控制面基址（长驻 URL 不变，需要时另设字段）。
func NewWithBase(base string) *Client {
	c := New()
	c.Base = base
	return c
}

// SetTransport 让 main 装配 devdump / 备用通道包壳时统一替换 Transport
// （两个 client 必须一起换，否则流式路径绕过包装）。
func (c *Client) SetTransport(tr http.RoundTripper) {
	c.HTTP.Transport = tr

}

// base 返回生效的对话/控制面基址。
func (c *Client) base() string {
	if s := trimSlash(c.Base); s != "" {
		return s
	}
	return APIBase
}

// benefitBase 返回生效的 benefit 目录基址。
func (c *Client) benefitBase() string {
	if s := trimSlash(c.BenefitBase); s != "" {
		return s
	}
	return BenefitBase
}

// stsURL 返回生效的 STS 端点。
func (c *Client) stsURL() string {
	if s := strings.TrimSpace(c.STSURL); s != "" {
		return s
	}
	return STSTokenURL
}

// httClient 返回非流式客户端（nil 安全回落）。
func (c *Client) httClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: requestTimeout}
}

// ---------------------------------------------------------------------------
// provider.Upstream：对话
// ---------------------------------------------------------------------------

// userAgent 出站 UA。上游不校验指纹，这里如实标识本工具，便于上游侧排查异常流量。
const userAgent = "WildWork/1.0 (codearts)"

// ---------------------------------------------------------------------------
// provider.Upstream：不需要上游交互的能力
// ---------------------------------------------------------------------------

// FetchModelPricing CodeArts 没有本地费率表：benefit 模型的额度是按日免费额度
// （不是倍率），积分账户的计量单位是 package credit，两者都不构成「模型倍率」。
// 返回 nil 而不是编一份 0 倍率表——面板显示「未知」好过显示「免费」（R46/R49 同向）。
func (c *Client) FetchModelPricing(_ *auth.Auth) ([]provider.ModelPricing, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// randomHex 生成 n 字节随机数的 hex（合 2n 个字符）。
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// 随机源失败时退到时间戳（仅影响 id/缓存键的唯一性，不影响安全性）。
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// UUID4 生成 UUIDv4（x-snap-traceid、授权页 ticket_id 等"只要求唯一"的场景）。
func UUID4() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return randomHex(16)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
