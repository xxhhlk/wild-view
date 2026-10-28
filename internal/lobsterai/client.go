// client.go LobsterAI（有道龙虾）的目录、额度、签到与凭据续期客户端。
package lobsterai

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"wild-view/internal/auth"
	"wild-view/internal/provider"
)

// Client 上游 HTTP 客户端。Base / Version 可覆盖以便测试注入假上游。
type Client struct {
	// HTTP 用于非流式请求（目录 / 积分 / 签到 / 续期 / 版本号），带总超时。
	HTTP *http.Client
	// Base 上游 API 基址；空 = APIBase。
	Base string
	// Version 版本号接口地址；空 = VersionURL。测试注入假上游用。
	Version string
}

// defaultClient 包级默认客户端：只给 ClientVersion() 这个包级入口用
// （登录包在 exchange 请求体里要带 version，而它没有自己的 Client）。
var defaultClient = New()

// 编译期自检：本包必须完整实现 provider.Upstream；签到结果结构化上报（CheckinReporter）
// 让调度器能区分「本次领到」与「活动还没上线」（不变式 23 的同款要求）。
var (
	_ provider.Upstream        = (*Client)(nil)
	_ provider.CheckinReporter = (*Client)(nil)
)

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
	return &Client{
		HTTP: &http.Client{Timeout: RequestTimeout, Transport: tr},
	}
}

// NewWithBase 测试用：覆盖上游 API 基址。
//
// ⚠️ 测试还必须把 Version 指到假上游：ClientVersion() 会去打版本号接口，
// 不覆盖它就会发**真实网络请求**（本仓纪律：测试零真实请求）。
func NewWithBase(base string) *Client {
	c := New()
	c.Base = base
	return c
}

// base 返回生效的 API 基址。
func (c *Client) base() string {
	if s := strings.TrimRight(strings.TrimSpace(c.Base), "/"); s != "" {
		return s
	}
	return APIBase
}

// ---------------------------------------------------------------------------
// 请求头
// ---------------------------------------------------------------------------

// authHeaders 补齐带 Bearer 的请求头（对话 / 目录 / 积分 / 签到，协议 §2.1）。
//
// withClientMeta 控制是否附上两个 X-LobsterAI-Client-* 头：
//   - 对话与模型列表**必需**（缺 kimi-k3-agentic-v1 会少返回模型，协议 §2.2）；
//   - 积分 / 签到**不要**（协议 §2.1：签到/余额只有 4 个基础头）。多发一个客户端
//     能力头不会让请求失败，但会让「这条请求像谁」变得不可解释，故按协议分开。
func (c *Client) authHeaders(req *http.Request, token, accept string, withClientMeta bool) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	if withClientMeta {
		req.Header.Set(HeaderCapabilities, Capabilities)
		req.Header.Set(HeaderClientVersion, c.ClientVersion())
	}
}

// credentialHeaders 补齐凭据类请求头（exchange / refresh，协议 §2.1）：
// **不带 Authorization**，只要 Accept / Content-Type / User-Agent。
func credentialHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
}

// ---------------------------------------------------------------------------
// provider.Upstream：续期（协议 §3.2）
// ---------------------------------------------------------------------------

// RefreshToken 用 refreshToken 续期。
//
// 协议要点（每条都有坑）：
//   - `POST {APIBase}/api/auth/refresh`，**不带 Authorization**（凭据在请求体里）；
//   - 请求体 = keyfrom 身份载荷 + `refreshToken`；
//   - ⚠️ `uuid` / `userId` 缺省时**删键**而不是发空串（keyfromPayload 只在非空时 Set）；
//   - ⚠️ `latestKeyfrom` **刻意不更新为当前时刻**：严格对齐外部参考实现
//     （`lobsterai.ts:311-315`、`:456-461`；applyLobsteraiRefresh 也只改 token 与过期时间）。
//     这也意味着本方法**绝不回写 Extra 里的 keyfrom 四元组**；
//   - 终态：HTTP 401/403 或响应体命中 session-dead 标记 → `*provider.Error{ErrSessionDead}`；
//     其余（网络抖动 / 5xx / 429）→ 普通分类错误，走可重试路径；
//   - 成功后**必须落盘**（不变式 20）：refresh_token 会轮换，不落盘 = 下次启动用旧的。
func (c *Client) RefreshToken(a *auth.Auth) error {
	if a == nil {
		return &provider.Error{Kind: provider.ErrSessionDead, Msg: "lobsterai：没有账号"}
	}
	rt := strings.TrimSpace(a.RefreshTokenValue())
	if rt == "" {
		return &provider.Error{Kind: provider.ErrSessionDead, Msg: "lobsterai：账号没有 refresh_token"}
	}
	payload := c.keyfromPayload(a)
	payload["refreshToken"] = rt
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("lobsterai：续期请求体编码失败：%w", err)
	}
	status, respRaw, err := c.postJSON(c.base()+EpAuthRefresh, "", raw, false)
	if err != nil {
		return err
	}
	if e := c.businessErr(status, respRaw); e != nil {
		return e
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken  string          `json:"accessToken"`
			RefreshToken string          `json:"refreshToken"`
			ExpiresIn    json.RawMessage `json:"expiresIn"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respRaw, &env); err != nil {
		return fmt.Errorf("lobsterai：续期响应解析失败：%w", err)
	}
	if env.Code != 0 {
		return c.envelopeError(status, env.Code, env.Msg, respRaw)
	}
	access := strings.TrimSpace(env.Data.AccessToken)
	if access == "" {
		// 缺 accessToken 即报错，不落半成品凭据（协议 §3.1 的同款纪律，续期同理）。
		return errors.New("lobsterai：续期响应缺 accessToken")
	}
	newRT := strings.TrimSpace(env.Data.RefreshToken)
	a.Lock()
	a.AccessToken = access
	if newRT != "" {
		// 上游没回新 refresh_token 时保留旧的（不回退成空串）。
		a.RefreshToken = newRT
	}
	if exp := intOfRaw(env.Data.ExpiresIn); exp > 0 {
		// expiresAt 优先用 expiresIn（**单位秒**，协议 §3.1）。
		a.ExpiresAt = time.Now().Add(time.Duration(exp) * time.Second).Unix()
	}
	a.Unlock()
	// expiresIn 缺失时回退 access token 的 JWT exp（协议 §3.1 的兜底口径）。
	// AdoptJWTExpiry 只在 exp 更晚时覆盖，故不会把正常值改坏；两者都拿不到时
	// 保留原 expiresAt —— 那会让 NeedsRefresh 可能恒真（反复续期），
	// ⚠️ 这是**已知限制**：上游不给任何到期信息时无法自行判定，只能靠下一次 401 纠正。
	a.AdoptJWTExpiry()

	// 不变式 20：refresh 轮换必须落盘。handler 之后还会再 SaveAtomic 一次，幂等无害。
	// 日志零 token（AGENTS.md §0.2）：只记 err，绝不打印任何 token 值。
	if err := a.SaveAtomic(); err != nil {
		log.Printf("lobsterai：续期落盘失败（仅影响下次启动）：%v", err)
	}
	return nil
}

// keyfromPayload 构造 keyfrom 身份载荷（请求体形态，协议 §3.2）。
//
// 与 keyfromQuery 同源但用途不同：query 给模型列表，body 给续期。
// 两处都遵守「空值键不发送」——这正是协议 §3.2 ⚠️ 要求的「uuid/userId 缺省时删键」。
//
// 注意 `latestKeyfrom` 取的是凭据里存的**原值**（Extra），不是当前时刻。
func (c *Client) keyfromPayload(a *auth.Auth) map[string]any {
	p := map[string]any{"version": c.ClientVersion()}
	if v := extraOf(a, ExtraFirstKeyfrom); v != "" {
		p["firstKeyfrom"] = v
	}
	if v := extraOf(a, ExtraLatestKeyfrom); v != "" {
		p["latestKeyfrom"] = v
	}
	if v := extraOf(a, ExtraUUID); v != "" {
		p["uuid"] = v
	}
	if v := extraOf(a, ExtraUserID); v != "" {
		p["userId"] = v
	}
	return p
}

// ---------------------------------------------------------------------------
// provider.Upstream：积分（协议 §7.2）
// ---------------------------------------------------------------------------

// UserResource 查询可消耗积分余额（pool 路由口径）。
func (c *Client) UserResource(a *auth.Auth) (int64, error) {
	remain, _, err := c.creditSummary(a)
	return remain, err
}

// UserResourceDetail 返回余额与明细条目。
//
// 明细直接映射上游 creditItems[]：包名优先 label（实测「每日登录奖励」）、回退 type、
// 再回退「积分包」；到期时间取 expiresAt（ISO8601）并归一到 UTC+8 墙钟的日期粒度
// （不变式 18：ResourceItem.ExpireAt 是 YYYY-MM-DD；上游没给就留空串，前端隐藏整列）。
func (c *Client) UserResourceDetail(a *auth.Auth) (int64, []provider.ResourceItem, error) {
	return c.creditSummary(a)
}

// creditSummary 单次请求同时产出总额与明细（口径同源，避免两处各算一遍）。
func (c *Client) creditSummary(a *auth.Auth) (int64, []provider.ResourceItem, error) {
	tok := tokenOf(a)
	if tok == "" {
		return 0, nil, &provider.Error{Kind: provider.ErrSessionDead, Msg: "lobsterai：账号没有 access_token"}
	}
	// 只发 4 个基础头（协议 §2.1：签到/余额不带客户端能力头）。
	status, raw, err := c.getJSON(c.base()+EpProfileSummary, tok)
	if err != nil {
		return 0, nil, err
	}
	if e := c.businessErr(status, raw); e != nil {
		return 0, nil, e
	}
	top, perr := decodeObject(raw)
	if perr != nil {
		return 0, nil, fmt.Errorf("lobsterai：积分响应解析失败：%w", perr)
	}
	if code, msg, ok := envelopeCode(top); ok && code != 0 {
		return 0, nil, c.envelopeError(status, code, msg, raw)
	}
	data := businessObject(top)
	// 总额：data.totalCreditsRemaining，**负数 clamp 到 0**（协议 §7.2）。
	total := intOfRaw(data["totalCreditsRemaining"])
	if total < 0 {
		total = 0
	}
	items := creditItems(data["creditItems"])
	if total == 0 && len(items) == 0 {
		// 协议 §7.2 的空判据：**「总额 0 且无明细」必须与「余额真的为 0」区分**。
		// wild-work 的通道是 error（面板据此显示「查不到」而不是 0；creditSnapshot
		// 也不会把 0 写进 pool 快照与 ledger 差分）。
		//
		// 为什么用 ErrPassthrough：它在本仓的语义是「请求级、不罚号、不轮转」，
		// 恰好是这里需要的（这不是会话失效，也不是账号故障）。**未确认**：
		// 参考实现同样无法区分这两种情形，只是它选择返回 null；本仓没有 null 通道。
		return 0, nil, &provider.Error{Kind: provider.ErrPassthrough, Status: status,
			Msg: "lobsterai：积分接口未给出可解释的数据（总额 0 且无明细）"}
	}
	return total, items, nil
}

// creditPackage 明细条目的中间形态（面值要按组二次聚合，故先收集再产出）。
type creditPackage struct {
	name   string
	remain int64
}

// creditItems 解析 data.creditItems[]（协议 §7.2）。
//
// 面值推断：服务端**没有**面值字段，协议的做法是取「同组（同 label/type）有效包剩余量
// 的最大值」当面值 —— 于是 Used = 面值 − 剩余量。这是**展示口径**，不是真实历史消耗
// （协议未确认上游有 per-package 的发放总量）。
func creditItems(raw json.RawMessage) []provider.ResourceItem {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		// 非数组 ⇒ 视作「无明细」而不是失败（与 minimax 同口径：缺字段 ≠ 查询失败）。
		return nil
	}
	pkgs := make([]creditPackage, 0, len(list))
	expires := make([]string, 0, len(list))
	face := make(map[string]int64, len(list))
	for _, it := range list {
		obj, err := decodeObject(it)
		if err != nil {
			continue // 坏条目只损失自己
		}
		name := strings.TrimSpace(strOfRaw(obj["label"]))
		if name == "" {
			name = strings.TrimSpace(strOfRaw(obj["type"]))
		}
		if name == "" {
			name = "积分包"
		}
		// creditsRemaining 实测可能给字符串（"100.00"），故用宽松取数（协议 §7.2）。
		remain := intOfRaw(obj["creditsRemaining"])
		if remain < 0 {
			remain = 0
		}
		pkgs = append(pkgs, creditPackage{name: name, remain: remain})
		expires = append(expires, dateOfISO(strOfRaw(obj["expiresAt"])))
		if remain > face[name] {
			face[name] = remain
		}
	}
	out := make([]provider.ResourceItem, 0, len(pkgs))
	for i, p := range pkgs {
		total := face[p.name]
		used := total - p.remain
		if used < 0 {
			used = 0
		}
		out = append(out, provider.ResourceItem{
			Name:   p.name,
			Total:  total,
			Used:   used,
			Remain: p.remain,
			// ExpireAt 为空串 = 上游未下发（前端隐藏「有效期」列，不得用零值冒充）。
			ExpireAt: expires[i],
			// Usable 恒 true：这些包就是本工具消耗的额度池（协议没有专用池概念）。
			Usable: true,
			// Key 留空：上游条目没有稳定 ID 字段（协议 §7.2 的字段表只有
			// creditsRemaining/type/label/expiresAt），按 ResourceItem 约定退回
			// Name 作伪键。已知限制：同名不同到期日的条目在 ledger 差分里会并成一条。
		})
	}
	return out
}

// dateOfISO 把上游 ISO8601 的到期时间归一到 UTC+8 墙钟的 **RFC3339 精确时刻**。
// 解析不出（缺失 / 非 ISO）→ 空串：**不得**用零值时间冒充「永不过期」。
//
// 历史：旧实现截到日期粒度（不变式 18 时代的写法），丢了时分秒。面板按
// 「N 小时内到期」分桶需要精确时刻（2026-10-19 实测）。
//
// 注：lobsterai 的 ledger 伪键用条目名（Key 留空 ⇒ itemKey 退回 Name），
// 不含日期，故本改动零抖动。
func dateOfISO(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// 带时区偏移的形态：按偏移解析后归一到 UTC+8 输出
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(expireLoc).Format(time.RFC3339)
	}
	// 无时区后缀的形态：按 **UTC+8 墙钟**解析（上游是国内服务，下发的是本地墙钟）。
	// 旧实现用 time.Parse 会按 UTC 解析，"2026-10-31 12:00:00" 变成 20:00+08:00，
	// 整整偏 8 小时——精确到时刻后这个偏差会直接算错「剩几小时」（2026-10-19 修）。
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, expireLoc); err == nil {
			return t.Format(time.RFC3339)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// provider.Upstream：签到（协议 §7.1 三步流程）
// ---------------------------------------------------------------------------

// DailyCheckin 实现 provider.Upstream：每日签到（协议 §7.1 的三步流程）。
//
// 「今日已领取」返回 nil（幂等语义：已领取不是错误）。需要区分
// 「本次领到 / 今天已领过 / 活动还没上线」的调度器请用 DailyCheckinReport。
func (c *Client) DailyCheckin(a *auth.Auth) error {
	_, err := c.DailyCheckinReport(a)
	return err
}

// DailyCheckinReport 实现 provider.CheckinReporter：额外返回结构化状态。
//
// 为什么必须结构化（不变式 23 的同款理由）：error 通道分不清「活动还没上线」与
// 「上游故障」——两者都不是「已签到」，却都需要在当日窗口内继续重试，
// 而 Msg 是面板上唯一能说清差别的字段。
//
// 三步流程（每一步的判据都照抄协议 §7.1）：
//
//	① GET /api/client-activities/slot?placement=…&clientVersion=…&containerApiVersion=2&platform=win32
//	   只有 slotState=="available" 且 activityCode 非空才继续；
//	② GET /api/client-activities/{code}/context?configRevision=…
//	   claimedToday==true → 已领取；actions 不含 check_in → inactive；
//	③ POST /api/client-activities/{code}/actions/check_in
//	   body = {configRevision, idempotencyKey: uuid4, payload: {}}
func (c *Client) DailyCheckinReport(a *auth.Auth) (provider.CheckinReport, error) {
	tok := tokenOf(a)
	if tok == "" {
		return provider.CheckinReport{Status: provider.CheckinNoToken, Msg: "无 access_token"}, nil
	}

	// ── 第一步：活动槽位 ────────────────────────────────────────────────────
	status, slotRaw, err := c.getJSON(c.activitySlotURL(), tok)
	if err != nil {
		return provider.CheckinReport{Status: provider.CheckinError, Msg: err.Error()}, err
	}
	if e := c.businessErr(status, slotRaw); e != nil {
		return provider.CheckinReport{Status: provider.CheckinError, Msg: e.Error()}, e
	}
	top, perr := decodeObject(slotRaw)
	if perr != nil {
		return provider.CheckinReport{Status: provider.CheckinError, Msg: "槽位响应解析失败"}, nil
	}
	if code, msg, ok := envelopeCode(top); ok && code != 0 {
		e := c.envelopeError(status, code, msg, slotRaw)
		return provider.CheckinReport{Status: provider.CheckinError, Msg: e.Error()}, e
	}
	slotData := businessObject(top)
	slotState := strings.TrimSpace(strOfRaw(slotData["slotState"]))
	activityCode, revision := activityOf(slotData["activity"])
	if slotState != "available" || activityCode == "" {
		// 「活动还没上线／已下线」是可重试状态（协议 §7.1 的 inactive 家族），
		// 不是故障：返回 NoCampaign 并**把 slotState 透出**，面板才看得出区别。
		return provider.CheckinReport{Status: provider.CheckinNoCampaign,
			Msg: fmt.Sprintf("活动槽位不可用（slotState=%q）", slotState)}, nil
	}

	// ── 第二步：活动上下文 ──────────────────────────────────────────────────
	ctxURL := c.base() + ActivityContextPath(activityCode)
	if rev := revisionQuery(revision); rev != "" {
		ctxURL += "?configRevision=" + url.QueryEscape(rev)
	}
	status, ctxRaw, err := c.getJSON(ctxURL, tok)
	if err != nil {
		return provider.CheckinReport{Status: provider.CheckinError, Msg: err.Error()}, err
	}
	if e := c.businessErr(status, ctxRaw); e != nil {
		return provider.CheckinReport{Status: provider.CheckinError, Msg: e.Error()}, e
	}
	ctxTop, perr := decodeObject(ctxRaw)
	if perr != nil {
		return provider.CheckinReport{Status: provider.CheckinError, Msg: "活动上下文解析失败"}, nil
	}
	if code, msg, ok := envelopeCode(ctxTop); ok && code != 0 {
		e := c.envelopeError(status, code, msg, ctxRaw)
		return provider.CheckinReport{Status: provider.CheckinError, Msg: e.Error()}, e
	}
	ctxData := businessObject(ctxTop)
	if state, err := decodeObject(ctxData["state"]); err == nil {
		if claimed, ok := boolOfRaw(state["claimedToday"]); ok && claimed {
			// claimedToday===true → 已领取（幂等：不算错误，也不再发起签到）。
			return provider.CheckinReport{Status: provider.CheckinAlready, Msg: "今日已领取"}, nil
		}
	}
	actions := stringList(ctxData["actions"])
	if !contains(actions, ActivityCheckInAction) {
		// 活动存在但**不提供签到动作**（inactive）：可重试（活动可能刚创建，动作表未就绪）。
		return provider.CheckinReport{Status: provider.CheckinNoCampaign,
			Msg: fmt.Sprintf("活动不含 %s 动作（actions=%v）", ActivityCheckInAction, actions)}, nil
	}

	// ── 第三步：签到 ────────────────────────────────────────────────────────
	idem, err := newUUID4()
	if err != nil {
		return provider.CheckinReport{Status: provider.CheckinError, Msg: "幂等键生成失败"}, err
	}
	reqBody := map[string]any{
		// configRevision **原样**回传（数字/字符串都可能，不转换类型）。
		"idempotencyKey": idem,
		"payload":        map[string]any{},
	}
	if len(revision) > 0 {
		reqBody["configRevision"] = revision
	}
	bodyRaw, err := json.Marshal(reqBody)
	if err != nil {
		return provider.CheckinReport{Status: provider.CheckinError, Msg: "签到请求体编码失败"}, err
	}
	status, claimRaw, err := c.postJSON(c.base()+ActivityCheckInPath(activityCode), tok, bodyRaw, false)
	if err != nil {
		return provider.CheckinReport{Status: provider.CheckinError, Msg: err.Error()}, err
	}
	if e := c.businessErr(status, claimRaw); e != nil {
		return provider.CheckinReport{Status: provider.CheckinError, Msg: e.Error()}, e
	}
	claimTop, perr := decodeObject(claimRaw)
	if perr != nil {
		return provider.CheckinReport{Status: provider.CheckinError, Msg: "签到响应解析失败"}, nil
	}
	if code, msg, ok := envelopeCode(claimTop); ok && code != 0 {
		e := c.envelopeError(status, code, msg, claimRaw)
		return provider.CheckinReport{Status: provider.CheckinError, Msg: e.Error()}, e
	}
	result, err := decodeObject(businessObject(claimTop)["result"])
	if err != nil {
		result = nil
	}
	// 积分三级回退：creditsGranted → rewardCredits → credits；都没有则为 0。
	// 判据是**键是否存在**（不是值是否 > 0）：给了 0 就是 0，不该继续往下回退。
	granted := firstPresent(result, "creditsGranted", "rewardCredits", "credits")
	msg := "签到成功"
	if granted > 0 {
		msg = fmt.Sprintf("签到成功 +%d", granted)
	}
	// ⚠️ LobsterAI 签到响应**不含连续天数概念**，故 provider.CheckinReport 里
	// 没有 streak 字段可填（参考实现同样恒置 streakDays=0 / isStreakDay=false）。
	return provider.CheckinReport{Status: provider.CheckinClaimed, Amount: granted, Msg: msg}, nil
}

// activitySlotURL 槽位查询 URL（协议 §1.3 的固定四参数）。
func (c *Client) activitySlotURL() string {
	q := url.Values{}
	q.Set("placement", ActivityPlacement)
	q.Set("clientVersion", c.ClientVersion())
	q.Set("containerApiVersion", ActivityContainerAPIVersion)
	q.Set("platform", ActivityPlatform) // 伪装客户端形态，与运行环境无关
	return c.base() + EpActivitySlot + "?" + q.Encode()
}

// activityOf 取 slot 的 data.activity.{activityCode,configRevision}。
// configRevision 用 RawMessage 原样保留（数字/字符串都可能，转类型就会发错形状）。
func activityOf(raw json.RawMessage) (activityCode string, revision json.RawMessage) {
	obj, err := decodeObject(raw)
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(strOfRaw(obj["activityCode"])), obj["configRevision"]
}

// revisionQuery 把 configRevision 渲染成 query 值：JSON 字符串取其值，数字/布尔取原文。
func revisionQuery(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if s := strOfRaw(raw); s != "" {
		return s
	}
	s := strings.TrimSpace(string(raw))
	if s == "null" {
		return ""
	}
	return s
}

// firstPresent 按给定键序取第一个**存在**的整数值（宽松兼容字符串数字）。
func firstPresent(obj map[string]json.RawMessage, keys ...string) int64 {
	for _, k := range keys {
		raw, ok := obj[k]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		return intOfRaw(raw)
	}
	return 0
}

// stringList 把 JSON 数组解析成字符串切片（形状不符时给 nil）。
func stringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	return list
}

// contains 判定字符串切片是否含某值（大小写敏感：动作名是协议里的字面量）。
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// newUUID4 生成 RFC 4122 v4 UUID 字符串（签到幂等键，协议 §7.1）。
//
// 幂等键由**客户端**生成：上游用它去重，重试时必须换新键才代表「新的一次尝试」，
// 而同一次尝试内重试应复用同一键（本实现了没有重试，故每次调用生成一个）。
func newUUID4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ---------------------------------------------------------------------------
// provider.Upstream：错误分类（协议 §5.1）
// ---------------------------------------------------------------------------

// Classify 实现 provider.Upstream：按状态码 + body 语义分类。
//
// 顺序**严格按协议 §5.1**（判定顺序即优先级）：
//
//  1. HTTP 402                    → ErrHardCredit
//  2. body 命中「余额不足」关键词 → ErrHardCredit
//  3. body 命中 40100 / 40101 / token rejected / refresh token was rejected → ErrSessionDead
//  4. HTTP 429                    → ErrSoftRate
//  5. HTTP 404                    → ErrNotFound
//  6. HTTP ≥ 500                  → ErrServer
//  7. HTTP ≥ 400                  → ErrClient
//  8. 否则                        → ErrNone
//
// ⚠️ **唯一一处与字面顺序的偏离，且是仓库不变式要求的**：429 提前到「余额关键词」
// 之前（AGENTS.md 不变式 15）。限流 body 高频带 `quota exceeded` 这类字样，先判余额
// 关键词会把限流误归「余额耗尽」→ 12h 硬冷却，等于用分类器把整条渠道下线。
// 该偏离只影响「429 且同时命中余额关键词」这一种组合，其余组合与 §5.1 逐条一致。
//
// 另一处**分支内**细化：≥400 分支里先看本地自造的 `code=bad_params`
// （出站体不可构造时本包自己产的 400）。它只在没有额外信息可损失时才生效，
// 目的是别让网关本地拒绝去惩罚健康账号（同 minimax / monkeycode 的口径）。
func Classify(status int, body string) provider.ErrKind {
	lower := strings.ToLower(body)
	switch {
	case status == http.StatusPaymentRequired:
		return provider.ErrHardCredit
	case status == http.StatusTooManyRequests:
		// 见上方注释：429 必须先于余额关键词（不变式 15）。
		return provider.ErrSoftRate
	case hitsInsufficientCredits(lower):
		return provider.ErrHardCredit
	case hitsSessionDead(lower):
		return provider.ErrSessionDead
	case status == http.StatusNotFound:
		return provider.ErrNotFound
	case status >= 500:
		return provider.ErrServer
	case status >= 400:
		if provider.CodeMarker(lower, "bad_params") {
			return provider.ErrBadParams // 本包本地拒绝：请求级，不罚号不轮转
		}
		// 其余 4xx：按协议 §5.1 归 ErrClient（可轮换）。
		// 注意 401/403 若**不带** session-dead 标记也会落到这里（协议没有 401 分支），
		// 这与参考实现一致（"除成功外每一类都换号"）。积分/签到/续期路径另有显式
		// 401/403 判定（见 businessErr），不变式 19 的自愈不受影响。
		return provider.ErrClient
	default:
		return provider.ErrNone
	}
}

// Classify 实现 provider.Upstream（实例方法，便于将来注入渠道级配置）。
func (c *Client) Classify(status int, body string) provider.ErrKind { return Classify(status, body) }

// insufficientMarkers 余额/权益不足关键词（中英双表）。
//
// ⚠️ **未确认**：协议 §5.1 只说「body 命中『余额不足』关键词（中英双表）」，
// 关键词表本体在参考实现 lobsterai-errors.ts:54-66，**未收录进本地文档**；
// 文档只额外提到 2026-09 补充的「免费额度已用完，请升级套餐」类文案。
// 下表是按该描述整理的**推断**表：宁可多列几条已知说法，也不要漏判——
// 漏判会把余额耗尽的账号当健康账号反复重试（而它是本渠道最主要的失败模式）。
var insufficientMarkers = []string{
	// 中文
	"余额不足", "额度不足", "积分不足", "余额已用完", "额度已用完",
	"免费额度已用完", "免费额度用完", "请升级套餐", "升级套餐", "欠费",
	// 英文
	"insufficient", "not enough credits", "not enough balance", "no credits",
	"out of credits", "credit limit reached", "quota exhausted", "quota exceeded",
	"exceeded your current quota", "please upgrade", "upgrade your plan",
}

// sessionDeadMarkers 会话失效关键词（协议 §5.1 第 3 条逐字照抄）。
var sessionDeadTexts = []string{"token rejected", "refresh token was rejected"}

// sessionDeadCodes 会话失效业务码（协议 §5.1 第 3 条）。
var sessionDeadCodes = []string{"40100", "40101"}

// hitsInsufficientCredits 报告（已小写化的）文本是否命中余额不足关键词。
func hitsInsufficientCredits(lower string) bool {
	for _, m := range insufficientMarkers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// hitsSessionDead 报告（已小写化的）文本是否命中会话失效判据。
//
// 数字码用 provider.CodeMarker 而不是裸 Contains：裸 Contains 会被 request_id /
// 时间戳里的「40100」误命中（见 internal/provider/codemarker.go 的口径说明）。
func hitsSessionDead(lower string) bool {
	for _, c := range sessionDeadCodes {
		if provider.CodeMarker(lower, c) {
			return true
		}
	}
	for _, m := range sessionDeadTexts {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// businessErr 把业务端点（积分 / 签到 / 续期）的 HTTP 层失败转成类型化错误。
//
// 与 Classify 的差别：这里对 **HTTP 401/403 额外加一条显式判定**，归 ErrSessionDead
// ——协议 §5.1 的状态码表里没有 401 分支，但不变式 19/23 要求「登录态失效必须能被
// errors.As 识别」，否则 401 自愈（refresh + 落盘 + 重试一次）永远不触发。
// 另：HTTP 200 + 业务码非 0 由调用方经 envelopeError 处理，本函数不管。
func (c *Client) businessErr(status int, raw []byte) error {
	if status == 0 {
		return nil
	}
	lower := strings.ToLower(string(raw))
	if status == http.StatusUnauthorized || status == http.StatusForbidden || hitsSessionDead(lower) {
		return &provider.Error{Kind: provider.ErrSessionDead, Status: status, Msg: snippet(raw)}
	}
	if status >= 400 {
		return &provider.Error{Kind: Classify(status, string(raw)), Status: status, Msg: snippet(raw)}
	}
	return nil
}

// envelopeError 把「HTTP 200 但统一信封 code 非 0」的响应转成类型化错误。
//
// 先判 session-dead 标记（同一份 body 里可能带 40100/40101），其余归 ErrClient：
// 可轮转、不致命，且不会把「业务拒绝」误当成会话失效去禁用账号。
func (c *Client) envelopeError(status, code int, msg string, raw []byte) error {
	kind := provider.ErrClient
	if hitsSessionDead(strings.ToLower(string(raw))) {
		kind = provider.ErrSessionDead
	}
	return &provider.Error{Kind: kind, Status: status,
		Msg: fmt.Sprintf("lobsterai：业务码 %d %s", code, strings.TrimSpace(msg))}
}

// envelopeCode 取统一信封的 code（`{code,msg,data}`）。ok=false 表示响应里没有 code 键
// （部分端点可能直接给裸对象，此时不按业务码判失败）。
func envelopeCode(top map[string]json.RawMessage) (code int, msg string, ok bool) {
	raw, has := top["code"]
	if !has {
		return 0, "", false
	}
	return int(intOfRaw(raw)), strings.TrimSpace(strOfRaw(top["msg"])), true
}

// ---------------------------------------------------------------------------
// HTTP 小工具与 JSON 取值
// ---------------------------------------------------------------------------

// getJSON 发一个带 Bearer 的 GET（业务端点：积分 / 签到），只带 4 个基础头。
func (c *Client) getJSON(url, token string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	c.authHeaders(req, token, "application/json", false)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, raw, nil
}

// postJSON 发一个 JSON POST。
//
// token 为空时不带 Authorization：exchange / refresh 都是这种形态（协议 §2.1）——
// 凭据在请求体里，带上 Authorization 反而是上游没见过的形态。
func (c *Client) postJSON(url, token string, body []byte, withClientMeta bool) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if token == "" {
		credentialHeaders(req)
	} else {
		c.authHeaders(req, token, "application/json", withClientMeta)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, raw, nil
}

// tokenOf 取账号的 access_token 快照（锁内读，防与续期写回竞争）。
func tokenOf(a *auth.Auth) string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.AccessTokenValue())
}

// extraOf 取 auth.Extra 里的扩展凭据（nil 安全）。
func extraOf(a *auth.Auth, key string) string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.Extra[key])
}

// decodeObject 把 JSON 解成「键 → 原始值」。
func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, errors.New("字段缺失")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("不是 JSON 对象")
	}
	return m, nil
}

// businessObject 取业务字段所在的对象：有 `data` 对象就用它，否则用顶层
// （与 minimax 同款策略：信封与平铺两种形态都要能吃下）。
func businessObject(top map[string]json.RawMessage) map[string]json.RawMessage {
	if raw, ok := top["data"]; ok {
		if obj, err := decodeObject(raw); err == nil && len(obj) > 0 {
			return obj
		}
	}
	return top
}

// strOfRaw 取 JSON 字符串（非字符串给空串）。
func strOfRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// intOfRaw 宽松取整数：数字与数字字符串都认（上游的积分数实测给字符串）。
// 小数按截断取整（面板是整数口径）。
func intOfRaw(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int64(f)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if g, perr := strconv.ParseFloat(strings.TrimSpace(s), 64); perr == nil {
			return int64(g)
		}
	}
	return 0
}

// boolOfRaw 宽松取布尔：JSON 布尔与 "true"/"false" 字符串都认。
func boolOfRaw(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// snippet 截一段响应体用于错误信息（排障线索，不含凭据）。
func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	if s == "" {
		return "空响应"
	}
	return s
}

// expireLoc 本渠道的墙钟口径（与 provider 内部同值，固定 UTC+8）。
var expireLoc = time.FixedZone("UTC+8", 8*60*60)
