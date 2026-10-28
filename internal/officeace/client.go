package officeace

import (
	"net"
	"net/http"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// Client 是 OfficeAce（华为云果办模型网关）渠道客户端，实现 provider.Upstream。
//
// 上游本身就是 OpenAI 兼容协议，故 chat 走 {base}/chat/completions；但有三处改写：
//   - 恒以 stream:true 打上游（非流式由本地聚合）；
//   - 按模型补足 max_tokens 推理预算（推理模型 max_tokens 太小 → 正文空）；
//   - 剥离 glm-5.3 系不认的 thinking 参数。
//
// 控制面（积分/签到/续期）另见 quota.go / refresh.go。
type Client struct {
	HTTP *http.Client // 带总超时，供非流式/控制面
	// 无总超时，供长 SSE 流式（R35）

}

// DefaultChatThrottleInterval 对话请求默认最小间隔，对齐上游 1 req/s 的限制。
//
// 取 1.1s 而非 1.0s：上游按「秒」开桶，间隔恰好 1.0s 时两次请求可能落在同一秒
// 桶里（客户端时钟与上游判定点存在偏差），留 10% 余量更稳。
const DefaultChatThrottleInterval = 1100 * time.Millisecond

// New 构造 officeace 客户端。
func New() *Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 90 * time.Second,
	}
	return &Client{
		HTTP: &http.Client{Transport: tr, Timeout: 600 * time.Second},
	}
}

// SetTransport 让 main 装配 devdump 包壳时统一替换 Transport。
func (c *Client) SetTransport(tr http.RoundTripper) {
	c.HTTP.Transport = tr

}

// ---------------------------------------------------------------------------
// provider.Upstream 必需接口实现
// ---------------------------------------------------------------------------

// RefreshToken 在 refresh.go 实现（批 3）；此处保留接口位。
func (c *Client) RefreshToken(a *auth.Auth) error { return c.refreshToken(a) }

// FetchModelPricing 返回空（officeace 无本地费率表；实际倍率见 quota 的 model-token-rates）。
func (c *Client) FetchModelPricing(_ *auth.Auth) ([]provider.ModelPricing, error) {
	return nil, nil
}

// Classify 按华为网关错误码分类（见 classify.go）。
func (c *Client) Classify(status int, body string) provider.ErrKind {
	return classifyUpstream(status, body)
}

// FetchModels 拉取上游模型目录（{base}/models）；失败时回退各账号本地已知模型。
func (c *Client) FetchModels(a *auth.Auth) ([]provider.ModelInfo, error) {
	return c.fetchModels(a)
}
