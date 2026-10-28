package officeace

import (
	"strings"

	"testing"
	"time"
)

// 锁定 V4/V11 签名的结构（不联网）：授权头应含算法标识、Credential、SignedHeaders、Signature。
func TestSignV4Shape(t *testing.T) {
	hdrs, err := SignV4("AKTEST", "SKTEST", "STS", "PROJ",
		"GET", "https://agentarts.cn-southwest-2.myhuaweicloud.com/v1/claw/client-permission-validate",
		"", "application/json;charset=utf8", nil, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	auth := hdrs["Authorization"]
	if !strings.HasPrefix(auth, "SDK-HMAC-SHA256 Access=AKTEST, SignedHeaders=") {
		t.Fatalf("unexpected V4 auth header: %s", auth)
	}
	if !strings.Contains(auth, ", Signature=") {
		t.Fatalf("missing signature: %s", auth)
	}
	if hdrs["X-Sdk-Date"] != "20231114T221320Z" {
		t.Fatalf("unexpected sdk date: %s", hdrs["X-Sdk-Date"])
	}
	if hdrs["X-Security-Token"] != "STS" || hdrs["X-Project-ID"] != "PROJ" {
		t.Fatalf("missing security/project headers: %v", hdrs)
	}
}

func TestSignV11Shape(t *testing.T) {
	hdrs, err := SignV11("AKTEST", "SKTEST", "", "",
		"POST", "https://modelgw-0004.officeace.cn-southwest-2.huaweicloud-agentarts.com/v2/v1/subscription/bonus/claim",
		"{}", "application/json;charset=utf8",
		map[string]string{"x-subscription-type": "v2"}, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	auth := hdrs["Authorization"]
	if !strings.HasPrefix(auth, "V11-HMAC-SHA256 Credential=AKTEST/") {
		t.Fatalf("unexpected V11 auth header: %s", auth)
	}
	if !strings.Contains(auth, "/cn-southwest-2/apic") {
		t.Fatalf("missing V11 scope region: %s", auth)
	}
	if hdrs["x-subscription-type"] != "v2" {
		t.Fatalf("missing x-subscription-type: %v", hdrs)
	}
}

// DPoP proof 应形如三段 base64url、头部带 jwk 无 d、算法 ES256。
func TestDPoPProof(t *testing.T) {
	k, err := NewDPoPKey()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := k.DPoPProof("POST", "https://sts.cn-north-4.myhuaweicloud.com/v1/oauth2/tokens", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		t.Fatalf("DPoP proof not 3 segments: %d", len(parts))
	}
}

// ---- 批2/批3 新增能力的回归 ----

func TestRotationCSVHelpers(t *testing.T) {
	s := ""
	s = appendCSV(s, "t1")
	s = appendCSV(s, "t2")
	s = appendCSV(s, "t1") // 去重
	if s != "t1,t2" {
		t.Fatalf("appendCSV got %q", s)
	}
	if !containsCSV(splitCSV(s), "t2") || containsCSV(splitCSV(s), "t3") {
		t.Fatalf("containsCSV wrong for %q", s)
	}
}

func TestAnyToUnixSec(t *testing.T) {
	if got := anyToUnixSec("2026-11-08T16:43:17.412Z"); got <= 0 {
		t.Fatalf("iso string not parsed: %d", got)
	}
	if got := anyToUnixSec(float64(1700000000)); got != 1700000000 {
		t.Fatalf("seconds wrong: %d", got)
	}
	if got := anyToUnixSec(float64(1700000000000)); got != 1700000000 {
		t.Fatalf("milliseconds not normalized: %d", got)
	}
}

// ---- 合并期审查修出的 4 个缺陷的回归锁 ----

// 缺陷4（合并期实证）：Export() 原缺 RefreshToken 字段，而「从客户端导入」路径用它落盘
// ⇒ 导入的账号只有 ~2h 临时凭据，之后积分/签到永久无法续期（对话不受影响，故极隐蔽）。
func TestExportCarriesRefreshToken(t *testing.T) {
	ac := &Account{ID: "acc:usr", RefreshToken: "RT-SECRET", AccessKeyID: "AK"}
	ex := ac.Export()
	if ex.RefreshToken != "RT-SECRET" {
		t.Fatalf("Export 丢了 refresh_token：%q（导入路径将无法续期）", ex.RefreshToken)
	}
}
