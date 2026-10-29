package qodercom

import "testing"

// TestCosyPublicKeyInitialised 守门：公钥必须在包初始化时解析好，
// 否则 NewCosySession 内 rsa.EncryptPKCS1v15 会空指针 panic。
func TestCosyPublicKeyInitialised(t *testing.T) {
	if serverPubKey == nil {
		t.Fatal("serverPubKey 未初始化：NewCosySession 会空指针 panic")
	}
	s, err := NewCosySession("mid", "mtoken", "windows", "nick", "uid-1", "dt-1", "drt-1", "personal_standard")
	if err != nil {
		t.Fatalf("NewCosySession: %v", err)
	}
	if s == nil || s.CosyKey == "" || s.Info == "" {
		t.Fatalf("会话字段为空：%+v", s)
	}
}
