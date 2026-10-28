// client.go OpenCodeZen 匿名免费通道客户端，实现 provider.Upstream。
//
// 与其它渠道最大的不同：本渠道「无账号」。匿名凭证是常量 "public"，
// 面板里只有一个虚拟账号（AnonymousUID），不可增删停用、无签到、无积分。
// 所有请求都按 OpenCode 官方 CLI 的形态构造（见 constants.go 的三道校验）。
package oczen

import (
	"bufio"

	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"math/big"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"wild-view/internal/auth"

	"wild-view/internal/provider"
)

// Client 上游 HTTP 客户端。Base 可覆盖以便测试。
type Client struct {
	HTTP *http.Client

	Base string

	// mu 保护 apikey 的并发读写（面板热更新与请求路径并发）。
	mu sync.RWMutex
	// apiKey 自定义 API key；空 = 匿名凭证（AnonymousKey）。
	// 由面板设置（config.Proxies 同屏的 oczen_api_key）热更新。
	apiKey string
}

// New 生产默认客户端。
//
// 刻意**不**设 Base：留空由 base() 按凭证车道选择（匿名 → AnonymousBase，
// 自备 key → DefaultBase）。设成 DefaultBase 会把匿名车道钉在 /zen 上，
// 正是 issue #117 的恒 403 形态（R69）。
func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: requestTimeout, Transport: newTransport()}} // 无总超时；空闲兜底见 internal/idle
	// 流中空闲监控兜底（未注入时用本仓默认 90s）

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

// base 返回当前出站基址：**按车道分流**。
//
// 匿名（无 key）走 AnonymousBase(/inference)，自备 sk-… key 走 DefaultBase(/zen)：
// 上游 2026-10-14 把 keyless 推理迁到了独立入口，两条车道基址不再相同。
// 显式 c.Base（测试注入 NewWithBase）仍优先，两种车道共用同一 httptest 假上游。
func (c *Client) base() string {
	if c.Base != "" {
		return c.Base
	}
	// apiKey 由面板热更新（SetAPIKey 持写锁），故取值也须持读锁，否则与保存操作并发即数据竞争。
	c.mu.RLock()
	keyed := c.apiKey != ""
	c.mu.RUnlock()
	if keyed {
		return DefaultBase
	}
	return AnonymousBase
}

// AnonymousAuth 构造匿名虚拟账号。唯一合法账号，由 main 装配时注入 pool。
// ExpiresAt 取远期值：使 NeedsRefresh 恒为假，避免走无意义的 token 刷新路径。
func AnonymousAuth() *auth.Auth {
	return &auth.Auth{
		Kind:        string(Kind),
		AccessToken: AnonymousKey,
		ExpiresAt:   noExpiry,
		UID:         AnonymousUID,
		Nickname:    AnonymousName,
	}
}

// StaticModels 上游不可达时的静态兜底清单（仍只含免费模型）。
func StaticModels() []provider.ModelInfo { return staticFree() }

// ---------------------------------------------------------------------------
// 请求构造
// ---------------------------------------------------------------------------

// 时间戳段不是任意 12 位 hex：免费档会按官方客户端 Identifier.descending()
// 的编码解析它——~(unix_ms<<12|counter) 取低 48 位，故小写 hex 段的末三位是
// 0xFFF-counter。实盘探测（2026-10-13）：免费档接受 counter 1–63（末三位
// 0xFC0–0xFFF），超出即判为非 OpenCode 客户端 → 403 FreeTierError，即便 ses_
// 外形正确；摘要段（旧实现）同属该拒收窗口。故仅当时间戳段落在该编码窗口内
// 才原样保留，否则确定性重编码——同时保住「同一会话稳定映射到同一 session」
// 的 prompt cache 亲和。
var descendingTimePartPattern = regexp.MustCompile(`^[0-9a-f]{9}f[c-f][0-9a-f]$`)

// canonicalSessionID 把任意会话种子映射成 OpenCode 官方格式
// ses_<12位小写hex><14位Base62>。已是官方客户端签发的 descending 编码则原样
// 返回（保住 prompt cache 亲和）；其余（含旧实现的摘要段与升序 ID）重编码成
// 该精确布局。上游自 2026-09-16 起对非该形状/编码的会话头一律回 403 FreeTierError。
func canonicalSessionID(signal string) string {
	// 官方形状：ses_ + 12 位小写 hex（时间戳）+ 14 位 Base62
	if len(signal) == 4+12+14 && strings.HasPrefix(signal, "ses_") {
		ok := true
		for i := 4; i < 16; i++ {
			c := signal[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				ok = false
				break
			}
		}
		if ok {
			for i := 16; i < len(signal); i++ {
				if !isBase62(signal[i]) {
					ok = false
					break
				}
			}
		}
		if ok && descendingTimePartPattern.MatchString(signal[4:16]) {
			return signal
		}
		// 形状对但编码外来（旧摘要段 / 升序 ID）：落到下面确定性重编码。
	}
	sum := sha256.Sum256([]byte("ses\x00" + signal))
	timePart := descendingTimePart(sum[:6])
	n := new(big.Int).SetBytes(sum[6:16])
	return "ses_" + timePart + base62Fixed(n, 14)
}

// descendingTimePart 把哈希前 6 字节渲染成官方客户端 Identifier.descending()
// 的布局：36 位时间戳移位进 48 位字的高位，OR 上 counter=1，再按位取反，
// 故末三位恒为 ffe。
func descendingTimePart(hash []byte) string {
	current := (uint64(hash[0])<<28 | uint64(hash[1])<<20 | uint64(hash[2])<<12 |
		uint64(hash[3])<<4 | uint64(hash[4])>>4) << 12
	current |= 1
	inverted := ^current & 0xFFFFFFFFFFFF
	return hex.EncodeToString([]byte{
		byte(inverted >> 40), byte(inverted >> 32), byte(inverted >> 24),
		byte(inverted >> 16), byte(inverted >> 8), byte(inverted),
	})
}

func isBase62(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func base62Fixed(n *big.Int, width int) string {
	base := big.NewInt(62)
	out := make([]byte, width)
	rem := new(big.Int)
	for i := width - 1; i >= 0; i-- {
		n.DivMod(n, base, rem)
		out[i] = base62Alphabet[rem.Int64()]
	}
	return string(out)
}

func randomID(prefix string, size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return prefix + "_" + hex.EncodeToString([]byte(time.Now().String()))
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

// authKey 返回当前出站凭证：自定义 key 优先，空则回匿名。
func (c *Client) authKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.apiKey != "" {
		return c.apiKey
	}
	return AnonymousKey
}

// SetAPIKey 热更新自定义 API key（空串 = 回匿名凭证）。
func (c *Client) SetAPIKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKey = strings.TrimSpace(key)
}

// headered 为上游请求注入 OpenCode CLI 伪装头（含规范会话头）。
//
// 头集与官方客户端镜像（2026-10-14 起上游收紧）：
//   - **不发 `x-opencode-request`**：该关联头一旦出现即判非官方客户端 ⇒ 403；
//     （参考实现 opencode2api 注释原文："The x-opencode-request correlation
//     header ... rejected with 403 since 2026-10-14, so neither is forwarded"）
//   - `x-opencode-project` 必须是**裸 40 位小写 hex**（官方 CLI 自身形态）；
//     `prj_` 前缀的规范 id 会被拒，值本身不校验只校验形状。
//   - 会话头四个都要带：`x-opencode-session` + **`x-opencode-session-id`**
//   - `x-session-affinity` + `X-Session-Id`，且同值（缺 `-id` 变体会被拒）。
func (c *Client) headered(req *http.Request, session string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+c.authKey())
	req.Header.Set("x-opencode-client", "cli")
	// 会话头族：上游要求「两个 session 头携带同一 descending id」，官方客户端
	// 同时发 -session 与 -session-id 两种拼写，故四个都发同一值。
	req.Header.Set("x-opencode-session", session)
	req.Header.Set("x-opencode-session-id", session)
	req.Header.Set("x-session-affinity", session)
	req.Header.Set("X-Session-Id", session)
	// project 由 session 确定性派生裸 40 位 hex：形状合规，且同一会话恒定映射
	// 到同一 project（保住上游 prompt cache 亲和）。
	req.Header.Set("x-opencode-project", clientProjectID(session))
}

// clientProjectID 把会话标识派生成官方 CLI 那种**裸 40 位小写 hex**的 project id。
// 上游只校验形状（非 `prj_` 前缀的 40 位 hex），值本身不校验；用 sha256 派生
// 保证同一会话稳定映射到同一 project（prompt cache 亲和）。
func clientProjectID(session string) string {
	sum := sha256.Sum256([]byte("opencode-project\x00" + session))
	return hex.EncodeToString(sum[:20])
}

// ---------------------------------------------------------------------------
// provider.Upstream 实现
// ---------------------------------------------------------------------------

// regionBlockedMarkers 是「换出口 IP 有可能救回来」的拒绝文案（触发备用通道重放）。
//
// 实测形态：
//   - 403 + `{"error":{"type":"RegionError",...}}`：地域受限的免费模型
//     （如 muse-spark-*-contributor-free）。
//   - 403 + `{"error":{"type":"FreeTierError","message":"...aren't available from
//     your current network because we've detected abusive traffic from this IP
//     address..."}}`：**出口 IP 级**封禁。2026-10-19 实盘：同一条请求在武汉家宽、
//     日本/香港/新加坡/英国/德国/韩国/土耳其/加拿大/法国节点全部命中此文案，
//     而换到干净的美国出口后同一实现立刻 200 —— 判据必须是「换 IP 能救」，此文案正合。
//
// 判据**宁窄勿宽**：命中会多发一发经代理的重放请求，误判的代价是把流量发到第三方出口，
// 故只认明确的地区/国家/出口 IP 语义文案；`access forbidden` 这类宽文案仍不得入选
// （回归测试 TestOczenIsRegionBlocked 有对应用例）。
var regionBlockedMarkers = []string{
	"regionerror", // 实测的 error.type 字面量
	"not available in your region",
	"not available in your country",
	"region-restricted",
	"geo-block",
	"geoblock",
	"abusive traffic",                     // FreeTierError：出口 IP 被判定滥用（换 IP 可解）
	"available from your current network", // FreeTierError：网络/出口级不可用（换 IP 可解）
}

// FetchModels 拉取上游模型列表并只保留免费集合。
// 上游不可达时不报错、回静态兜底清单：匿名渠道无账号可轮换，
// 报错只会让面板显示空白，不如给出已知可用清单。
func (c *Client) FetchModels(_ *auth.Auth) ([]provider.ModelInfo, error) {
	ids, err := c.fetchModelIDs()
	if err != nil || len(ids) == 0 {
		return StaticModels(), nil
	}
	free := filterFree(ids)
	if len(free) == 0 {
		return StaticModels(), nil
	}
	return free, nil
}

// fetchModelIDs 请求 GET /models（同样需要伪装头）。
func (c *Client) fetchModelIDs() ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, c.base()+"/models", nil)
	if err != nil {
		return nil, err
	}
	c.headered(req, canonicalSessionID(randomID("catalog", 8)))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("oczen: models http %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		ids = append(ids, d.ID)
	}
	return ids, nil
}

// FetchModelPricing 免费模型一律 0 倍率（Explicit=true 让面板显示「免费」而非 unknown）。
func (c *Client) FetchModelPricing(_ *auth.Auth) ([]provider.ModelPricing, error) {
	models, _ := c.FetchModels(nil)
	yes := true
	out := make([]provider.ModelPricing, 0, len(models))
	for _, m := range models {
		out = append(out, provider.ModelPricing{
			Model:    m.ID,
			Channel:  string(Kind),
			Rate:     0,
			Explicit: &yes,
		})
	}
	return out, nil
}

// RefreshToken 空实现：匿名通道无 token 可刷。
// 不返回错误以免上层把它当作会话失效；虚拟账号 ExpiresAt 为远期值，
// 正常路径不会走到这里。
func (c *Client) RefreshToken(_ *auth.Auth) error { return nil }

// UserResource 匿名通道无积分概念，恒为 0 且不报错（面板显示「不适用」）。
func (c *Client) UserResource(_ *auth.Auth) (int64, error) { return 0, nil }

// UserResourceDetail 同上：无明细条目。
func (c *Client) UserResourceDetail(_ *auth.Auth) (int64, []provider.ResourceItem, error) {
	return 0, nil, nil
}

// DailyCheckin 匿名通道无签到活动（渠道声明为 noExplicitCheckin，调度器不会调用）。
func (c *Client) DailyCheckin(_ *auth.Auth) error {
	return fmt.Errorf("opencodezen 匿名通道无签到活动")
}

// Classify 上游错误分类。本渠道是单账号（且不可人工恢复），分类原则是
// 「绝不因请求级问题惩罚账号」，否则唯一账号一冷却就等于整条渠道下线：
//   - 429 → ErrSoftRate：真限流，短冷却 —— 单账号渠道唯一需要的背压；
//   - 其它 4xx（含 401/402/403 的 FreeTierError/RegionError）→ ErrPassthrough：
//     原文透传、不计错不冷却 —— 这些是请求形态/地域限制，换账号无从规避；
//     尤其不得归 ErrSessionDead：那会 pool.Disable 永久禁用，而匿名账号不可重登；
//   - 5xx → ErrServer（server.Runtime 已置 NoCooldownOnServerError，同样不罚账号）。
func (c *Client) Classify(status int, body string) provider.ErrKind {
	switch {
	case status == http.StatusTooManyRequests:
		return provider.ErrSoftRate
	case status >= 500:
		return provider.ErrServer
	case status >= 400:
		return provider.ErrPassthrough
	default:
		return provider.ErrNone
	}
}

// ---------------------------------------------------------------------------
// model 字段回填
// ---------------------------------------------------------------------------

// rewriteModelReader 逐行把 SSE 帧里的 "model":"<上游裸名>" 替换成客户端模型名。
// 上游帧经规范化白名单重建，model 字段格式固定为 `"model":"xxx"`（紧凑无空格），
// 故按字节替换即可；按行处理保证流式不被缓冲（每行一到即吐，不攒整包）。
type modelRewriter struct {
	src     *bufio.Reader
	pending []byte // 已读入但尚未吐出的当前行缓冲
	model   string
}
