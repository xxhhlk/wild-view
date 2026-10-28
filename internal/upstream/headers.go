// Package headers 构造三类上游请求头（common / chat / billing / refresh）。
// 规则来自 本地 docs/upstream-reverse-engineering.md §1/§4。
package upstream

import (
	"net/http"

	"wild-view/internal/auth"
)

// 官方 WorkBuddy 桌面客户端身份（2026-10-21 对本机 /Applications/WorkBuddy.app 5.8.3
// 静态解包取证，不启动客户端、不请求上游）：
//
//	UA           = "WorkBuddy/<app> WorkBuddy/<app> CLI/<cli>"
//	X-IDE-Version= <app>（桌面版本）
//	X-Product    = product.json 的 deploymentType（SaaS）
//
// 取证链：main/sidecar-manager.js 的 getCachedBaseEnv 给内嵌 CLI 配
// CLIENT_INFO_PLATFORM/PRODUCT_NAME=WorkBuddy、PLATFORM_VERSION/PRODUCT_VERSION=appVersion、
// USER_AGENT_EXTENSION=CLI/<cliVersion>；cli.mjs 的 buildUserAgent 依次 unshift platform、
// productName 再把 extension push 到末尾；appVersion 来自 Info.plist（5.8.3），cliVersion 来自
// 内嵌 @tencent-ai/codebuddy-code 的版本（2.161.0）。
//
// 旧值 `CLI/2.63.2 CodeBuddy/2.63.2` + X-IDE-Version 5.5.4 是 CodeBuddy CLI 时代的形态，
// 与桌面宿主实际发出的一整串（UA + IDE 版本 + Product + Private-Data + Agent-Intent）
// 不一致——混合身份本身就是特征，见 R117。
const (
	clientUA            = "WorkBuddy/5.8.3 WorkBuddy/5.8.3 CLI/2.161.0"
	originRefererCN     = "https://www.codebuddy.cn"
	originRefererGlobal = "https://www.workbuddy.ai"
	defaultClientVer    = "5.8.3"
)

func originRefererFor(a *auth.Auth) string {
	if a != nil && a.Region() == "global" {
		return originRefererGlobal
	}
	return originRefererCN
}

// CommonHeaders 设置所有 API 共享的请求头。
func CommonHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-CodeBuddy-Request", "1")
	origin := originRefererFor(a)
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("User-Agent", clientUA)
	req.Header.Set("Accept-Language", acceptLanguageFor(a))
}

// BillingHeaders billing 接口请求头。
func BillingHeaders(req *http.Request, a *auth.Auth) {
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
	if a.Domain != "" {
		req.Header.Set("X-Domain", a.Domain)
	}
}

// RefreshHeaders refresh 端点专属头（X-Refresh-Token 只允许出现在这里）。
// **调用方必须持有 a.Lock()**（本函数直接读 a.RefreshToken，不加锁——
// 若调用方未持锁，与 keepalive 刷新写回构成数据竞争）。
func RefreshHeaders(req *http.Request, a *auth.Auth) {
	CommonHeaders(req, a)
	req.Header.Set("X-Refresh-Token", a.RefreshToken)
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
	}
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
}

func acceptLanguageFor(a *auth.Auth) string {
	if a != nil && a.Region() == "global" {
		return "en-US"
	}
	return "zh-CN"
}
