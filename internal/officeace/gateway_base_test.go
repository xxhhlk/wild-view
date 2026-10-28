package officeace

import "testing"

// 本文件锁死 OfficeAce 网关 base 的**两种命名空间**（R70 附件，2026-10-17 离线取证定位）：
//
//	数据面（对话/模型目录） = 网关根 + "/v2" + path
//	控制面（/v1/subscription、/v1/claw/*） = 网关根 + path（**不得**带 /v2）
//
// 历史缺陷：两个 base 来源形态不同（导入的 api_base 自带 `/v2`；自助登录的
// model_api_url_base 不带），而旧实现把同一个 BaseURL 直接拼到两种端点上 ⇒
// OAuth 产物的 `/models` 落在 `{host}/models` 被上游 404 APIG.0101；导入产物的
// 控制面落在 `{host}/v2/v1/subscription` 同样 404。修法是读时归一（本文件的判据）。

const (
	testHostNoV2 = "https://modelgw-0009.officeace.cn-southwest-2.huaweicloud-agentarts.com"
	testHostV2   = testHostNoV2 + "/v2"
)

// TestDefaultGatewayIsRoot 兜底常量本身就应是根（无 /v2），否则控制面会多一层前缀。
func TestDefaultGatewayIsRoot(t *testing.T) {
	if bareGateway(defaultGateway) != defaultGateway {
		t.Errorf("defaultGateway 不是网关根：%q ⇒ %q", defaultGateway, bareGateway(defaultGateway))
	}
}
