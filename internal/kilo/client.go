// client.go Kilo AI 免费池的只读模型目录客户端。
// 本渠道没有凭据文件、登录流程、签到或积分，只保留匿名虚拟账号。
package kilo

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// Client 只读 HTTP 客户端。Base 可覆盖以便测试注入假上游。
type Client struct {
	HTTP *http.Client
	Base string
}

// New 生产默认客户端。
func New() *Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	tr := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSNextProto:          make(map[string]func(string, *tls.Conn) http.RoundTripper),
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
	}
	return &Client{HTTP: &http.Client{Timeout: requestTimeout, Transport: tr}}
}

// NewWithBase 测试用：覆盖上游基址。
func NewWithBase(base string) *Client {
	c := New()
	c.Base = base
	return c
}

func (c *Client) base() string {
	if c.Base != "" {
		return strings.TrimRight(c.Base, "/")
	}
	return DefaultBase
}

// AnonymousAuth 构造由 main 装配时注入池中的匿名虚拟账号。
func AnonymousAuth() *auth.Auth {
	return &auth.Auth{
		Kind:        string(Kind),
		AccessToken: "public",
		ExpiresAt:   noExpiry,
		UID:         AnonymousUID,
		Nickname:    AnonymousName,
	}
}

// StaticModels 上游清单拉取失败时的静态兜底（仍只含免费模型）。
func StaticModels() []provider.ModelInfo { return staticFree() }

// FetchModels 拉取模型清单并筛出免费池，失败时保留静态目录。
func (c *Client) FetchModels(_ *auth.Auth) ([]provider.ModelInfo, error) {
	rows, err := c.fetchListing()
	if err != nil || len(rows) == 0 {
		return StaticModels(), nil
	}
	free := filterFree(rows)
	if len(free) == 0 {
		return StaticModels(), nil
	}
	return free, nil
}

func (c *Client) fetchListing() ([]listingRow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("kilo: models http %d", resp.StatusCode)
	}
	var out struct {
		Data []listingRow `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// FetchModelPricing 免费池由上游明确标注，倍率为 0。
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

// RefreshToken 免费池没有 token 可刷新。
func (c *Client) RefreshToken(_ *auth.Auth) error { return nil }

// UserResource 免费池无积分概念，面板显示「不适用」。
func (c *Client) UserResource(_ *auth.Auth) (int64, error) { return 0, nil }

func (c *Client) UserResourceDetail(_ *auth.Auth) (int64, []provider.ResourceItem, error) {
	return 0, nil, nil
}

// DailyCheckin 免费池无签到活动。
func (c *Client) DailyCheckin(_ *auth.Auth) error {
	return fmt.Errorf("kilo 免费池无签到活动")
}
