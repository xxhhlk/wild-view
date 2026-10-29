package qodercn

import "testing"

// TestCosyPublicKeyInitialised 守门：公钥必须在包初始化时解析好。
//
// 2026-10-29 现场事故：删除对话转发实现时连带删掉了 body.go，
// 其中的公钥初始化一起消失，serverPubKey 恒为 nil →
// NewCosySession 内 rsa.EncryptPKCS1v15 空指针 panic，
// 目录/额度刷新整条链路挂掉（日志只有一行 catalog refresh panic，无堆栈）。
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
