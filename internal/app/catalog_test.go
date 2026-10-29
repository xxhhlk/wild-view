package app

import (
	"path/filepath"
	"testing"
	"time"

	"wild-view/internal/provider"
)

// TestCatalogAccountIsolation 守门「按账号缓存」的核心语义：
// 同一渠道下两个账号的目录/倍率互不覆盖，一个账号失败不会清空另一个账号的结果。
func TestCatalogAccountIsolation(t *testing.T) {
	c := newCatalog(filepath.Join(t.TempDir(), "catalog-cache.json"))
	now := time.Now().Unix()

	c.put(CatalogAccount{
		Channel:       "workbuddy",
		UID:           "a",
		Models:        []provider.ModelInfo{{ID: "m1"}},
		Pricing:       []provider.ModelPricing{{Model: "m1", Channel: "workbuddy", Rate: 0.08}},
		ModelsStatus:  QueryStatus{LastSuccessAt: now, LastAttemptAt: now, Source: "api"},
		PricingStatus: QueryStatus{LastSuccessAt: now, LastAttemptAt: now, Source: "api"},
	})
	c.put(CatalogAccount{
		Channel:       "workbuddy",
		UID:           "b",
		Models:        []provider.ModelInfo{{ID: "m2"}},
		ModelsStatus:  QueryStatus{LastAttemptAt: now, LastError: "boom"},
		PricingStatus: QueryStatus{LastAttemptAt: now, LastError: "boom"},
	})

	snap := c.snapshot()
	if len(snap) != 2 {
		t.Fatalf("账号数 = %d，期望 2（同渠道不同账号必须分开保存）", len(snap))
	}
	a := snap[catalogKey("workbuddy", "a")]
	b := snap[catalogKey("workbuddy", "b")]
	if len(a.Models) != 1 || a.Models[0].ID != "m1" {
		t.Errorf("账号 a 模型 = %+v，期望 m1", a.Models)
	}
	if len(b.Models) != 1 || b.Models[0].ID != "m2" {
		t.Errorf("账号 b 模型 = %+v，期望 m2", b.Models)
	}
	if b.PricingStatus.LastError == "" {
		t.Error("账号 b 的失败原因丢失")
	}
	if a.PricingStatus.LastError != "" {
		t.Error("账号 a 的成功状态被账号 b 的失败污染")
	}
}

// TestCatalogPersistRoundtrip 缓存需跨重启保留，且损坏文件不能阻塞启动。
func TestCatalogPersistRoundtrip(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "catalog-cache.json")
	c := newCatalog(fp)
	now := time.Now().Unix()
	c.put(CatalogAccount{
		Channel:       "qodercn",
		UID:           "u1",
		Models:        []provider.ModelInfo{{ID: "m1"}},
		Pricing:       []provider.ModelPricing{{Model: "m1", Rate: 1.5}},
		ModelsStatus:  QueryStatus{LastSuccessAt: now, Source: "api"},
		PricingStatus: QueryStatus{LastSuccessAt: now, Source: "api"},
	})
	if err := c.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloaded := newCatalog(fp)
	got, ok := reloaded.get("qodercn", "u1")
	if !ok {
		t.Fatal("重新加载后账号丢失")
	}
	if len(got.Models) != 1 || len(got.Pricing) != 1 {
		t.Errorf("重新加载结果不完整：models=%d pricing=%d", len(got.Models), len(got.Pricing))
	}

	// 删除后不得复活。
	reloaded.remove("qodercn", "u1")
	if _, ok := reloaded.get("qodercn", "u1"); ok {
		t.Error("remove 后仍能读到该账号")
	}
}

func TestFeesInfoHidesRemovedChannelsButKeepsCache(t *testing.T) {
	c := newCatalog(filepath.Join(t.TempDir(), "catalog-cache.json"))
	now := time.Now().Unix()
	for _, channel := range []string{"cline", "oczen", "workbuddy"} {
		c.put(CatalogAccount{Channel: channel, UID: "u", Models: []provider.ModelInfo{{ID: "model"}}, ModelsStatus: QueryStatus{LastSuccessAt: now}})
	}
	a := &App{catalog: c}
	result := a.FeesInfo()
	channels := result["channels"].([]feesChannel)
	if len(channels) != 1 || channels[0].Channel != "workbuddy" {
		t.Fatalf("渠道应只包含 workbuddy：%+v", channels)
	}
	if result["accounts"] != 1 || len(c.snapshot()) != 3 {
		t.Fatalf("展示数应为 1 且保留原缓存：accounts=%v cache=%d", result["accounts"], len(c.snapshot()))
	}
}

// TestQueryStatusFreshness 新鲜期与失败退避必须区分「从未成功」与「成功过但已过期」。
func TestQueryStatusFreshness(t *testing.T) {
	now := time.Now()
	freshStatus := QueryStatus{LastSuccessAt: now.Unix(), Source: "api"}
	if !fresh(freshStatus, now, catalogTTL) {
		t.Error("刚成功的状态应视为新鲜")
	}
	if fresh(QueryStatus{}, now, catalogTTL) {
		t.Error("从未成功不应视为新鲜")
	}
	expired := QueryStatus{LastSuccessAt: now.Add(-2 * catalogTTL).Unix()}
	if fresh(expired, now, catalogTTL) {
		t.Error("超过 TTL 应视为过期")
	}
	if !retryBlocked(QueryStatus{LastAttemptAt: now.Unix(), LastError: "x"}, now) {
		t.Error("刚失败应处于退避窗口")
	}
	if retryBlocked(QueryStatus{LastAttemptAt: now.Add(-2 * catalogRetry).Unix(), LastError: "x"}, now) {
		t.Error("超过退避窗口应允许重试")
	}
	if retryBlocked(QueryStatus{LastSuccessAt: now.Unix()}, now) {
		t.Error("没有错误时不应被退避拦住")
	}
}

// TestAttachCatalogScopesPerAccountRates 守门面板契约：
// 同模型在不同账号倍率不同时必须逐账号下发，而不是只给一个渠道级数字。
func TestAttachCatalogScopesPerAccountRates(t *testing.T) {
	now := time.Now().Unix()
	scopes := map[string]CatalogAccount{
		catalogKey("workbuddy", "a"): {
			Channel: "workbuddy", UID: "a", Nickname: "账号A",
			Models:        []provider.ModelInfo{{ID: "m1"}},
			Pricing:       []provider.ModelPricing{{Model: "m1", Channel: "workbuddy", Rate: 0.08}},
			ModelsStatus:  QueryStatus{LastSuccessAt: now, Source: "api"},
			PricingStatus: QueryStatus{LastSuccessAt: now, Source: "api"},
		},
		catalogKey("workbuddy", "b"): {
			Channel: "workbuddy", UID: "b", Nickname: "账号B",
			Models:        []provider.ModelInfo{{ID: "m1"}},
			Pricing:       []provider.ModelPricing{{Model: "m1", Channel: "workbuddy", Rate: 0.16}},
			ModelsStatus:  QueryStatus{LastSuccessAt: now, Source: "api"},
			PricingStatus: QueryStatus{LastSuccessAt: now, Source: "api"},
		},
	}
	channels := []feesChannel{{
		Channel: "workbuddy",
		Models:  []feesModelRow{{Model: "m1", ID: "workbuddy/m1", Priced: true, Rate: 0.08}},
	}}

	out := attachCatalogScopes(channels, scopes)
	if len(out) != 1 {
		t.Fatalf("渠道数 = %d", len(out))
	}
	if len(out[0].Accounts) != 2 {
		t.Fatalf("账号数 = %d，期望 2", len(out[0].Accounts))
	}
	row := out[0].Models[0]
	if len(row.Rates) != 2 {
		t.Fatalf("行级账号倍率数 = %d，期望 2", len(row.Rates))
	}
	rates := map[string]float64{}
	for _, r := range row.Rates {
		rates[r.UID] = r.Rate
	}
	if rates["a"] != 0.08 || rates["b"] != 0.16 {
		t.Errorf("账号倍率 = %v，期望 a=0.08 b=0.16", rates)
	}
}

// TestAttachCatalogScopesMarksMissingRate 缺失倍率不得被当成免费。
func TestAttachCatalogScopesMarksMissingRate(t *testing.T) {
	now := time.Now().Unix()
	scopes := map[string]CatalogAccount{
		catalogKey("raccoon", "a"): {
			Channel: "raccoon", UID: "a",
			Models:        []provider.ModelInfo{{ID: "m1"}},
			Pricing:       []provider.ModelPricing{{Model: "other", Rate: 0}},
			ModelsStatus:  QueryStatus{LastSuccessAt: now, Source: "api"},
			PricingStatus: QueryStatus{LastSuccessAt: now, Source: "api"},
		},
		catalogKey("raccoon", "b"): {
			Channel: "raccoon", UID: "b",
			Models:        []provider.ModelInfo{{ID: "m1"}},
			Pricing:       []provider.ModelPricing{{Model: "m1", Rate: 0}},
			ModelsStatus:  QueryStatus{LastSuccessAt: now, Source: "api"},
			PricingStatus: QueryStatus{LastSuccessAt: now, Source: "api"},
		},
	}
	channels := []feesChannel{{Channel: "raccoon", Models: []feesModelRow{{Model: "m1"}}}}
	out := attachCatalogScopes(channels, scopes)
	for _, r := range out[0].Models[0].Rates {
		if r.UID == "a" && (r.Priced || r.Free) {
			t.Error("账号 a 没有该模型的费率记录，不得标为已定价/免费")
		}
	}
}
