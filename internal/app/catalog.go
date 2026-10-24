package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"wild-view/internal/provider"
)

// catalog.go 模型目录与倍率的账号作用域缓存。
//
// 为什么不复用转发 Handler 的缓存：那份缓存是「渠道级、由路由选号填充」的，
// 查看器既没有请求路径去预热它，也不该让路由冷却/禁用影响一次只读查询。
// 这里按「渠道 + 账号」独立保存，并各自记录最近成功/失败时间与来源，
// 单账号失败不会清空同渠道其他账号的数据。
const (
	// catalogTTL 目录/倍率的成功结果新鲜期；过期才重新拉取。
	catalogTTL = time.Hour
	// catalogRetry 失败后的最短重试间隔，避免面板轮询打爆上游。
	catalogRetry = 5 * time.Minute
	// catalogWorkers 批量刷新时的并发上限。
	catalogWorkers = 3
)

// QueryStatus 一次上游查询的时效与来源。时间为 Unix 秒，0 表示从未发生。
type QueryStatus struct {
	LastSuccessAt int64  `json:"last_success_at,omitempty"`
	LastAttemptAt int64  `json:"last_attempt_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	// Source: api=本次/最近一次成功来自上游接口，static=渠道静态兜底。
	Source string `json:"source,omitempty"`
}

// CatalogAccount 单个账号的目录与倍率快照。
type CatalogAccount struct {
	Channel       string                  `json:"channel"`
	UID           string                  `json:"uid"`
	Nickname      string                  `json:"nickname,omitempty"`
	Models        []provider.ModelInfo    `json:"models,omitempty"`
	Pricing       []provider.ModelPricing `json:"pricing,omitempty"`
	ModelsStatus  QueryStatus             `json:"models_status"`
	PricingStatus QueryStatus             `json:"pricing_status"`
}

// catalog 账号作用域缓存，JSON 原子持久化（不含任何 token）。
type catalog struct {
	mu       sync.Mutex
	fp       string
	accounts map[string]CatalogAccount
}

func newCatalog(fp string) *catalog {
	c := &catalog{fp: fp, accounts: map[string]CatalogAccount{}}
	c.load()
	return c
}

func catalogKey(channel, uid string) string { return channel + "/" + uid }

func (c *catalog) load() {
	raw, err := os.ReadFile(c.fp)
	if err != nil {
		return
	}
	var file struct {
		Accounts map[string]CatalogAccount `json:"accounts"`
	}
	if json.Unmarshal(raw, &file) != nil || file.Accounts == nil {
		return
	}
	c.mu.Lock()
	c.accounts = file.Accounts
	c.mu.Unlock()
}

// snapshot 返回深拷贝，调用方可安全读取。
func (c *catalog) snapshot() map[string]CatalogAccount {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]CatalogAccount, len(c.accounts))
	for k, v := range c.accounts {
		v.Models = append([]provider.ModelInfo(nil), v.Models...)
		v.Pricing = append([]provider.ModelPricing(nil), v.Pricing...)
		out[k] = v
	}
	return out
}

func (c *catalog) get(channel, uid string) (CatalogAccount, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.accounts[catalogKey(channel, uid)]
	return v, ok
}

func (c *catalog) put(a CatalogAccount) {
	c.mu.Lock()
	c.accounts[catalogKey(a.Channel, a.UID)] = a
	c.mu.Unlock()
}

func (c *catalog) remove(channel, uid string) {
	c.mu.Lock()
	delete(c.accounts, catalogKey(channel, uid))
	c.mu.Unlock()
}

// save 原子写回；失败只记日志，不影响展示（缓存属锦上添花）。
func (c *catalog) save() error {
	c.mu.Lock()
	raw, err := json.MarshalIndent(struct {
		Accounts map[string]CatalogAccount `json:"accounts"`
	}{Accounts: c.accounts}, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.fp), 0o755); err != nil {
		return err
	}
	tmp := c.fp + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.fp)
}

// fresh 报告该状态在 now 时刻是否仍可视为新鲜结果。
func fresh(s QueryStatus, now time.Time, ttl time.Duration) bool {
	if s.LastSuccessAt <= 0 {
		return false
	}
	return now.Sub(time.Unix(s.LastSuccessAt, 0)) < ttl
}

// retryBlocked 报告失败后是否仍在退避窗口内。
func retryBlocked(s QueryStatus, now time.Time) bool {
	if s.LastError == "" || s.LastAttemptAt <= 0 {
		return false
	}
	return now.Sub(time.Unix(s.LastAttemptAt, 0)) < catalogRetry
}
