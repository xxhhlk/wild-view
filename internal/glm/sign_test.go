package glm

import (
	"testing"
	"time"
)

// TestGenerateSignTimestampRaw 校验 X-Timestamp 用原始毫秒值（13 位纯数字）。
// 旧实现把倒数第二位替换成校验位——PROTOCOL §2 已证该算法对签名结果无影响，
// IDE 通道迁移时已删（chatglm-cli 实抓全部请求都是原始毫秒）。
func TestGenerateSignTimestampRaw(t *testing.T) {
	now := time.UnixMilli(1790384203102)
	s := generateSign(now)
	if s.Timestamp != "1790384203102" {
		t.Errorf("timestamp = %s, want 原始毫秒 1790384203102（不得改写任何位）", s.Timestamp)
	}
}

// TestGenerateSign 校验签名串形状与拼接顺序。
func TestGenerateSign(t *testing.T) {
	now := time.UnixMilli(1790384203102)
	s := generateSign(now)

	if len(s.Nonce) != 32 {
		t.Errorf("nonce 长度 = %d, want 32", len(s.Nonce))
	}
	if !isLowerHex(s.Nonce) {
		t.Errorf("nonce 非小写 hex: %s", s.Nonce)
	}
	if len(s.Sign) != 32 {
		t.Errorf("sign 长度 = %d, want 32", len(s.Sign))
	}
	if len(s.Timestamp) != 13 {
		t.Errorf("timestamp 长度 = %d, want 13", len(s.Timestamp))
	}
	// 签名必须等于 md5("<timestamp>-<nonce>-<secret>")
	want := md5Hex(s.Timestamp + "-" + s.Nonce + "-" + SIGN_SECRET)
	if s.Sign != want {
		t.Errorf("sign 拼接顺序不对: got %s, want %s", s.Sign, want)
	}
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
