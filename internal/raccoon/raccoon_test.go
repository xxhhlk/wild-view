package raccoon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"testing"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// TestClassify 覆盖阶段 C 实测到的全部错误形态（LiteLLM 信封）。
func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   provider.ErrKind
	}{
		{"401 空 token", 401, `{"code":200001,"message":"authorization_empty_error"}`, provider.ErrSessionDead},
		{"401 token 校验失败", 401, `{"code":200003,"message":"authorization_verify_error"}`, provider.ErrSessionDead},
		{"403 也按会话失效", 403, `{"code":200003}`, provider.ErrSessionDead},
		{"本地拒绝的未知模型", 400, `{"error":{"code":"model_not_found"}}`, provider.ErrBadParams},
		{"400 空消息/超长", 400, `{"error":{"code":"400","message":"litellm.BadRequestError: message is empty or message is bigger than 70MB"}}`, provider.ErrPromptTooLong},
		{"429 纯限流", 429, `{"error":{"message":"rate limit exceeded"}}`, provider.ErrSoftRate},
		{"429 带 quota 字样仍按限流（§6.15）", 429, `{"code":200001,"message":"quota exceeded"}`, provider.ErrSoftRate},
		{"402 余额不足", 402, `{"message":"insufficient credit"}`, provider.ErrHardCredit},
		{"404", 404, `404 page not found`, provider.ErrNotFound},
		{"500", 500, `internal error`, provider.ErrServer},
		{"成功", 200, `{"id":"x"}`, provider.ErrNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.status, tc.body); got != tc.want {
				t.Fatalf("Classify(%d, %s) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

func TestKnownModel(t *testing.T) {
	for _, id := range []string{DefaultModel, ClientAliasModel, "sn-kimi-k3", "raccoon-405a1c"} {
		if !KnownModel(id) {
			t.Fatalf("KnownModel(%q) = false, want true", id)
		}
	}
	if KnownModel("nope-xyz") {
		t.Fatal("KnownModel(未知) 应为 false")
	}
}

func TestStaticModelsNonEmpty(t *testing.T) {
	ms := StaticModels()
	if len(ms) != len(staticModels) {
		t.Fatalf("StaticModels 应返回副本，got %d", len(ms))
	}
	ms[0].ID = "mutated"
	if staticModels[0].ID == "mutated" {
		t.Fatal("StaticModels 必须返回副本，不能共享底层数组")
	}
}

// sse 构造一条 data 行。
func sseLine(v any) string {
	raw, _ := json.Marshal(v)
	return "data: " + string(raw) + "\n\n"
}

// TestJwtExp 覆盖 refresh 后从 JWT 取到期时间的路径。
func TestJwtExp(t *testing.T) {
	// {"exp":1790089841}
	tok := "h." + "eyJleHAiOjE3OTAwODk4NDF9" + ".s"
	if got := jwtExp(tok); got != 1790089841 {
		t.Fatalf("jwtExp = %d", got)
	}
	if got := jwtExp("not-a-jwt"); got != 0 {
		t.Fatalf("非 JWT 应返回 0，got %d", got)
	}
}

// TestParsePointsBalance 覆盖积分余额的四个池子与充值冻结标记（实测响应形状）。
func TestParsePointsBalance(t *testing.T) {
	raw := []byte(`{"code":0,"message":"success","data":{"available_points":6313,"daily_points":313,` +
		`"monthly_points":0,"reward_points":6000,"topup_points":0,"topup_frozen":false}}`)
	var resp pointsBalanceResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	remain, items := parsePointsBalance(resp.Data)
	if remain != 6313 {
		t.Fatalf("available = %d, want 6313", remain)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4（每日/奖励/充值/月度）", len(items))
	}
	byName := map[string]provider.ResourceItem{}
	for _, it := range items {
		byName[it.Name] = it
	}
	if byName["每日额度"].Remain != 313 || byName["奖励积分"].Remain != 6000 {
		t.Fatalf("池子拆分不对: %+v", items)
	}
	if !byName["充值积分"].Usable {
		t.Fatal("topup_frozen=false 时充值积分应可用")
	}

	// 冻结场景：充值池标记为不可用，Summarize 应把它算进 unusable（面板分开展示）。
	// 测试数据保持自洽：实测 available_points == 可用池之和（313+6000=6313），
	// 故冻结的 50 不计入 available。
	frozen := pointsBalanceData{AvailablePoints: 100, DailyPoints: 100, TopupPoints: 50, TopupFrozen: true}
	_, items2 := parsePointsBalance(frozen)
	for _, it := range items2 {
		if it.Name == "充值积分" && it.Usable {
			t.Fatal("topup_frozen=true 时充值积分必须 Usable=false")
		}
	}
	usable, unusable := provider.Summarize(items2)
	if usable != 100 || unusable != 50 {
		t.Fatalf("Summarize = (%d,%d), want (100,50)", usable, unusable)
	}
	if usable != frozen.AvailablePoints {
		t.Fatalf("available_points(%d) 应等于可用池之和(%d)", frozen.AvailablePoints, usable)
	}
}

// errAfterReader 先吐完 data，再返回指定错误（模拟「读到一半流断了」）。
type errAfterReader struct {
	data string
	err  error
	off  int
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

// TestRefreshWithTokenWebFirst 刷新必须**先试 web 前缀**（2026-10-10 实测 electron 旧前缀已 404），
// 并正确解析出轮换后的 access/refresh。
func TestRefreshWithTokenWebFirst(t *testing.T) {
	var hit []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = append(hit, r.URL.Path)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["refresh_token"] != "old-rt" {
			t.Errorf("提交的 refresh_token 不符：%v", body)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"new-at","refresh_token":"new-rt"}}`))
	}))
	defer srv.Close()

	c := New()
	c.AuthBase = srv.URL
	at, rt, err := c.RefreshWithToken("old-rt")
	if err != nil {
		t.Fatalf("RefreshWithToken: %v", err)
	}
	if at != "new-at" || rt != "new-rt" {
		t.Fatalf("轮换后的令牌不符：at=%q rt=%q", at, rt)
	}
	if len(hit) != 1 || hit[0] != EpAuthRefreshWeb {
		t.Fatalf("应先试且只试 web 前缀，实际命中 %v", hit)
	}
}

// TestRefreshFallsBackToElectronOn404 web 前缀 404 时回落 electron 旧前缀（兼容旧上游）。
func TestRefreshFallsBackToElectronOn404(t *testing.T) {
	var hit []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = append(hit, r.URL.Path)
		if r.URL.Path == EpAuthRefreshWeb {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"at2","refresh_token":"rt2"}}`))
	}))
	defer srv.Close()

	c := New()
	c.AuthBase = srv.URL
	at, rt, err := c.RefreshWithToken("old-rt")
	if err != nil {
		t.Fatalf("应回落成功：%v", err)
	}
	if at != "at2" || rt != "rt2" {
		t.Fatalf("回落结果不符：at=%q rt=%q", at, rt)
	}
	want := []string{EpAuthRefreshWeb, EpAuthRefresh}
	if len(hit) != 2 || hit[0] != want[0] || hit[1] != want[1] {
		t.Fatalf("回落路径不符：%v", hit)
	}
}

// ---------------------------------------------------------------------------
// R107 每日额度逐笔到期（2026-10-20 生产实录驱动）
// ---------------------------------------------------------------------------

// billsFixture 生成一段实测形状的积分流水（2026-10-20 抓自 RaccoonAddison）。
// 三种 biz_type 都在：daily_grant 带 expire_at，daily_expire / desktop 不带。
// 到期时间相对当前时间生成，避免测试随日期过期。
func billsFixture() string {
	now := time.Now()
	format := func(t time.Time) string { return t.Format(time.RFC3339) }
	expire1 := now.Add(4 * time.Hour)
	expire2 := now.Add(28 * time.Hour)
	expire3 := now.Add(52 * time.Hour)
	expired := now.Add(-time.Hour)
	return fmt.Sprintf(`{"code":0,"message":"success","data":{"items":[
 {"bill_id":"b1","client_type":"web","event_name":"每日积分发放","biz_type":"daily_grant","points":300,
  "created_at":%q,"grant_at":%q,"expire_at":%q},
 {"bill_id":"b2","client_type":"web","event_name":"每日积分到期清零","biz_type":"daily_expire","points":-200,
  "created_at":%q,"grant_at":%q,"expire_at":%q},
 {"bill_id":"b3","client_type":"web","event_name":"每日积分发放","biz_type":"daily_grant","points":300,
  "created_at":%q,"grant_at":%q,"expire_at":%q},
 {"bill_id":"b4","client_type":"desktop","event_name":"桌面端 LLM 对话消耗","biz_type":"desktop","points":-2,
  "created_at":%q},
 {"bill_id":"b5","client_type":"web","event_name":"每日积分发放","biz_type":"daily_grant","points":300,
  "created_at":%q,"grant_at":%q,"expire_at":%q},
 {"bill_id":"b6","client_type":"web","event_name":"服务异常补偿积分","biz_type":"reward_grant","points":1500,
  "created_at":%q}
]}}`,
		format(now.Add(-3*time.Hour)), format(now.Add(-11*time.Hour)), format(expire3),
		format(expired), format(now.Add(-7*24*time.Hour)), format(expired),
		format(now.Add(-27*time.Hour)), format(now.Add(-35*time.Hour)), format(expire2),
		format(now.Add(-51*time.Hour)),
		format(now.Add(-75*time.Hour)), format(now.Add(-75*time.Hour)), format(expire1),
		format(now.Add(-4*24*time.Hour)),
	)
}

// TestFetchDailyGrants 只收未到期的 daily_grant，且按到期时刻升序。
//
// 为什么必须升序：上游每日 300 分、7 天有效，消耗按先进先出（最老一笔先花），
// 升序 = 消耗顺序，withDailyExpiry 的余额拆分依赖它。
func TestFetchDailyGrants(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != EpPointsBills {
			t.Errorf("路径不符：%s（应走 %s）", r.URL.Path, EpPointsBills)
		}
		if got := r.URL.Query().Get("paging.limit"); got == "" {
			t.Error("缺少 paging.limit 分页参数（上游缺省 limit 很小）")
		}
		_, _ = w.Write([]byte(billsFixture()))
	}))
	defer srv.Close()

	c := New()
	c.AuthBase = srv.URL
	grants, err := c.fetchDailyGrants(&auth.Auth{AccessToken: "at-1", UID: "u1"})
	if err != nil {
		t.Fatalf("fetchDailyGrants: %v", err)
	}
	if len(grants) != 3 {
		t.Fatalf("在效发放 = %d, want 3（daily_expire/desktop/reward_grant 都不算）: %+v", len(grants), grants)
	}
	for i := 1; i < len(grants); i++ {
		if grants[i-1].ExpireAt >= grants[i].ExpireAt {
			t.Fatalf("未按到期升序: %+v", grants)
		}
	}
	if grants[0].Points != 300 {
		t.Fatalf("金额丢失: %+v", grants[0])
	}
}

// TestFetchDailyGrantsDropsExpired 已过期的发放必须剔除（否则「今日到期」恒为 0）。
func TestFetchDailyGrantsDropsExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"items":[
		 {"biz_type":"daily_grant","points":300,"expire_at":"2020-01-01T23:59:59+08:00"},
		 {"biz_type":"daily_grant","points":300,"expire_at":"2099-01-01T23:59:59+08:00"}
		]}}`))
	}))
	defer srv.Close()

	c := New()
	c.AuthBase = srv.URL
	grants, err := c.fetchDailyGrants(&auth.Auth{AccessToken: "at-1"})
	if err != nil {
		t.Fatalf("fetchDailyGrants: %v", err)
	}
	if len(grants) != 1 || grants[0].ExpireAt != "2099-01-01T23:59:59+08:00" {
		t.Fatalf("已过期发放未被剔除: %+v", grants)
	}
}

// TestWithDailyExpiryFIFO 余额按到期升序逐笔扣减（先进先出），余量挂最后一笔。
//
// 这是本改动的**口径核心**：拆开后 Remain 之和必须恒等于 daily_points，
// 否则 pool 快照与面板余额对不上（Summarize 直接求和）。
func TestWithDailyExpiryFIFO(t *testing.T) {
	items := parsePointsBalanceOnly(pointsBalanceData{
		AvailablePoints: 1000, DailyPoints: 500, RewardPoints: 500,
	})
	grants := []dailyGrant{
		{Points: 300, ExpireAt: "2026-10-21T23:59:59+08:00"},
		{Points: 300, ExpireAt: "2026-10-22T23:59:59+08:00"},
		{Points: 300, ExpireAt: "2026-10-23T23:59:59+08:00"},
	}
	got := withDailyExpiry(items, grants)

	var daily []provider.ResourceItem
	var sum int64
	for _, it := range got {
		if it.Name == dailyItemName {
			daily = append(daily, it)
			sum += it.Remain
		}
	}
	if sum != 500 {
		t.Fatalf("拆分后每日额度合计 = %d, want 500（必须等于 daily_points）", sum)
	}
	// 500 分池子 + 三笔 300：前两笔满额 300，最后一笔只剩 0 → 不产生空条目。
	if len(daily) != 2 {
		t.Fatalf("拆分条目 = %d, want 2（第三笔余额为 0 应省略）: %+v", len(daily), daily)
	}
	if daily[0].Remain != 300 || daily[0].ExpireAt != grants[0].ExpireAt {
		t.Fatalf("第一笔应按最早到期满额: %+v", daily[0])
	}
	if daily[1].Remain != 200 {
		t.Fatalf("第二笔应承接余量 200: %+v", daily[1])
	}

	// Summarize 必须仍等于可用余额（拆分对 pool 快照零影响）。
	usable, _ := provider.Summarize(got)
	if usable != 1000 {
		t.Fatalf("Summarize 可用 = %d, want 1000", usable)
	}
}

// TestWithDailyExpiryPoolExceedsGrants 上游口径漂移（池子比 bills 在效之和还大）时
// 余量挂最后一笔，绝不丢余额。
func TestWithDailyExpiryPoolExceedsGrants(t *testing.T) {
	items := parsePointsBalanceOnly(pointsBalanceData{DailyPoints: 900})
	grants := []dailyGrant{
		{Points: 300, ExpireAt: "2026-10-21T23:59:59+08:00"},
		{Points: 300, ExpireAt: "2026-10-22T23:59:59+08:00"},
	}
	got := withDailyExpiry(items, grants)
	var sum int64
	var n int
	for _, it := range got {
		if it.Name == dailyItemName {
			sum += it.Remain
			n++
		}
	}
	if sum != 900 {
		t.Fatalf("余量丢失：合计 %d, want 900", sum)
	}
	if n != 2 {
		t.Fatalf("条目数 = %d, want 2", n)
	}
}

// TestWithDailyExpirySharesOneLedgerKey 拆分条目必须共用同一个 ledger 差分键。
//
// 差分键 = Key（非空优先）else Name。所有拆分条目必须 Key 为空、Name 相同，
// ledger 的按 key 聚合才会把它们合回「每日额度」一条 —— 与拆分前完全同键同值，
// 升级零假流水；反之若给每条挂上不同的 Key（如拿到期时刻当 Key），一次刷新就会
// 产出 N 条独立流水、快照留 N 条，升级瞬间还会凭空产生一批 earn。
func TestWithDailyExpirySharesOneLedgerKey(t *testing.T) {
	items := parsePointsBalanceOnly(pointsBalanceData{DailyPoints: 900})
	grants := []dailyGrant{
		{Points: 300, ExpireAt: "2026-10-21T23:59:59+08:00"},
		{Points: 300, ExpireAt: "2026-10-22T23:59:59+08:00"},
		{Points: 300, ExpireAt: "2026-10-23T23:59:59+08:00"},
	}
	got := withDailyExpiry(items, grants)
	keys := map[string]bool{}
	for _, it := range got {
		if it.Name != dailyItemName {
			continue
		}
		if it.Key != "" {
			t.Fatalf("拆分条目不得设 Key（会拆散 ledger 差分键）: %+v", it)
		}
		keys[it.Name] = true
	}
	if len(keys) != 1 {
		t.Fatalf("拆分条目应共用 1 个差分键，实际 %d 个: %v", len(keys), keys)
	}
}

// TestWithDailyExpiryNoGrantsUnchanged bills 拿不到发放时原样返回（降级路径）。
func TestWithDailyExpiryNoGrantsUnchanged(t *testing.T) {
	items := parsePointsBalanceOnly(pointsBalanceData{DailyPoints: 300})
	got := withDailyExpiry(items, nil)
	if len(got) != len(items) {
		t.Fatalf("无发放数据时不得改动条目: %+v", got)
	}
	for i := range got {
		if got[i] != items[i] {
			t.Fatalf("条目被改动: %+v", got[i])
		}
	}
}

// TestUserResourceDetailAttachesDailyExpiry 端到端：balance + bills 两次请求，
// 每日额度条目带上逐笔到期时刻，其余池子不受影响。
func TestUserResourceDetailAttachesDailyExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case EpPointsBalance:
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"available_points":8100,` +
				`"daily_points":2100,"monthly_points":0,"reward_points":6000,"topup_points":0,"topup_frozen":false}}`))
		case EpPointsBills:
			_, _ = w.Write([]byte(billsFixture()))
		default:
			t.Errorf("意外路径: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New()
	c.AuthBase = srv.URL
	remain, items, err := c.UserResourceDetail(&auth.Auth{AccessToken: "at-1", UID: "u1"})
	if err != nil {
		t.Fatalf("UserResourceDetail: %v", err)
	}
	if remain != 8100 {
		t.Fatalf("remain = %d, want 8100", remain)
	}
	var dailyCount int
	var dailySum int64
	for _, it := range items {
		if it.Name != dailyItemName {
			continue
		}
		dailyCount++
		dailySum += it.Remain
		if it.ExpireAt == "" {
			t.Fatalf("每日额度条目必须带到期时刻: %+v", it)
		}
	}
	if dailySum != 2100 {
		t.Fatalf("每日额度合计 = %d, want 2100", dailySum)
	}
	if dailyCount != 3 {
		t.Fatalf("每日额度条目数 = %d, want 3（bills 里三笔在效发放）", dailyCount)
	}
	// 奖励积分不受影响（仍是一条聚合条目、无到期时刻）。
	for _, it := range items {
		if it.Name == "奖励积分" && (it.Remain != 6000 || it.ExpireAt != "") {
			t.Fatalf("奖励积分不应被改动: %+v", it)
		}
	}
}

// TestUserResourceDetailBillsFailureDegrades bills 失败不得影响余额主流程。
func TestUserResourceDetailBillsFailureDegrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case EpPointsBalance:
			_, _ = w.Write([]byte(`{"code":0,"data":{"available_points":300,"daily_points":300}}`))
		case EpPointsBills:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":500}`))
		}
	}))
	defer srv.Close()

	c := New()
	c.AuthBase = srv.URL
	remain, items, err := c.UserResourceDetail(&auth.Auth{AccessToken: "at-1", UID: "u1"})
	if err != nil {
		t.Fatalf("bills 失败不得让余额查询失败: %v", err)
	}
	if remain != 300 {
		t.Fatalf("remain = %d, want 300", remain)
	}
	var found bool
	for _, it := range items {
		if it.Name == dailyItemName {
			found = true
			if it.Remain != 300 || it.ExpireAt != "" {
				t.Fatalf("降级后应为原聚合条目（无到期时刻）: %+v", it)
			}
		}
	}
	if !found {
		t.Fatal("每日额度条目丢失")
	}
}

// TestDailyItemNameStable 条目名是 ledger 差分键（Key 为空时退回 Name），不得改动。
//
// 改名 ⇒ 升级瞬间旧键「消失」记 expire、新键「出现」记 earn，凭空一对假流水（R105）。
func TestDailyItemNameStable(t *testing.T) {
	if dailyItemName != "每日额度" {
		t.Fatalf("dailyItemName = %q，改了会换 ledger 差分键并产出假流水", dailyItemName)
	}
	items := parsePointsBalanceOnly(pointsBalanceData{DailyPoints: 1})
	if items[0].Name != dailyItemName || items[0].Key != "" {
		t.Fatalf("每日额度条目必须用稳定 Name 且不设 Key（否则拆分条目会被账本当多条）: %+v", items[0])
	}
}

// parsePointsBalanceOnly 只取条目（忽略余额），测试可读性辅助。
func parsePointsBalanceOnly(d pointsBalanceData) []provider.ResourceItem {
	_, items := parsePointsBalance(d)
	return items
}
