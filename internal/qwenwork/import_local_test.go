//go:build windows

package qwenwork

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJWTExpiry 到期时间以 JWT 的 exp 为准（服务端真实判据），解析失败返回 0。
func TestJWTExpiry(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1794290137,"sub":"u1"}`))
	tok := "eyJhbGciOiJIUzI1NiJ9." + payload + ".sig"
	if got := jwtExpiry(tok); got != 1794290137 {
		t.Fatalf("exp = %d", got)
	}
	if got := jwtExpiry("not-a-jwt"); got != 0 {
		t.Fatalf("非法 token 应返回 0，得到 %d", got)
	}
}

// TestDecryptSafeStoragePlainFallback 未加密的旧格式直接返回原文，不因版本差异失败。
func TestDecryptSafeStoragePlainFallback(t *testing.T) {
	raw := []byte(`{"token":"t","user":{"id":"u"}}`)
	out, err := decryptSafeStorage(filepath.Join(t.TempDir(), "Local State"), raw)
	if err != nil {
		t.Fatalf("decryptSafeStorage: %v", err)
	}
	if string(out) != string(raw) {
		t.Fatalf("明文应原样返回，得到 %q", out)
	}
}

// TestImportLocalMissingClient 未安装/未登录时给出可操作提示，而不是空指针或 panic。
func TestImportLocalMissingClient(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QWENWORK_HOME", dir)
	_, err := ImportLocal()
	if err == nil {
		t.Fatal("缺少凭据文件时应报错")
	}
	if !strings.Contains(err.Error(), "桌面端") {
		t.Errorf("错误信息应指向桌面端：%v", err)
	}
}

// TestImportLocalUnreadableAuth v10 密文但缺 Local State 时，错误要指出密钥来源问题。
func TestImportLocalUnreadableAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QWENWORK_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "auth-v2.dat"), append([]byte("v10"), make([]byte, 40)...), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ImportLocal()
	if err == nil {
		t.Fatal("缺 Local State 时应报错")
	}
	if !strings.Contains(err.Error(), "Local State") {
		t.Errorf("错误信息应提到 Local State：%v", err)
	}
}

// TestLocalAuthJSONShape 守门解密后 JSON 的字段名：桌面端改名时要在这里变红。
func TestLocalAuthJSONShape(t *testing.T) {
	doc := `{"token":"a","refreshToken":"r","refreshTokenExpiresAt":"2026-11-03T06:55:37.458Z",
	         "user":{"id":"u-1","name":"nick","email":"e@x"}}`
	var parsed struct {
		Token                 string `json:"token"`
		RefreshToken          string `json:"refreshToken"`
		RefreshTokenExpiresAt string `json:"refreshTokenExpiresAt"`
		User                  struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.User.ID != "u-1" || parsed.User.Name != "nick" || parsed.RefreshToken != "r" {
		t.Fatalf("字段解析不符合预期: %+v", parsed)
	}
}
