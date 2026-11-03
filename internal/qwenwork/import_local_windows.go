//go:build windows

package qwenwork

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// import_local_windows.go 从千问办公桌面端读取登录凭据。
//
// 桌面端（Electron）把登录态写在 %APPDATA%\QwenWorkCN\auth-v2.dat，
// 用 Chromium 的 safeStorage 加密：v10 前缀 + AES-256-GCM，
// 密钥由 Windows DPAPI 保护，存在同目录 Local State 的 os_crypt.encrypted_key 里。
// 只要以同一 Windows 用户运行即可解开——本工具与被导入的客户端天然同用户。
//
// 为什么不走网页 OAuth（2026-11 实测）：
//   - 官方 client_id 的 redirect_uri 白名单只认 gateway.qwenwork.cn，自建回环被拒；
//   - 该官方回调页会校验桌面端会话，粘回来的 code 被判 INVALID_OAUTH_CALLBACK；
//   - 动态注册（DCR）生成的 client_id 是 36 位 UUID，登录页把它当 X-OAuth-Source
//     发送，服务端限制 ≤32 字符 → bad-request。
// 因此导入本机凭据是当前唯一稳定路径。

type dataBlob struct {
	cbData uint32
	pbData *byte
}

var (
	modCrypt32    = syscall.NewLazyDLL("crypt32.dll")
	procUnprotect = modCrypt32.NewProc("CryptUnprotectData")
	modKernel32   = syscall.NewLazyDLL("kernel32.dll")
	procLocalFree = modKernel32.NewProc("LocalFree")
)

// dpapiUnprotect 解开当前用户 DPAPI 保护的二进制块。
func dpapiUnprotect(in []byte) ([]byte, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("DPAPI 密文为空")
	}
	blobIn := dataBlob{cbData: uint32(len(in)), pbData: &in[0]}
	var blobOut dataBlob
	r, _, err := procUnprotect.Call(
		uintptr(unsafe.Pointer(&blobIn)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&blobOut)),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptUnprotectData 失败: %v", err)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(blobOut.pbData)))
	out := make([]byte, blobOut.cbData)
	copy(out, unsafe.Slice(blobOut.pbData, blobOut.cbData))
	return out, nil
}

// ImportLocal 读取桌面端登录态。找不到客户端或未登录时返回可读错误。
func ImportLocal() (*LocalAuth, error) {
	base := strings.TrimSpace(os.Getenv("QWENWORK_HOME"))
	if base == "" {
		appData := strings.TrimSpace(os.Getenv("APPDATA"))
		if appData == "" {
			return nil, fmt.Errorf("找不到 APPDATA，无法定位千问办公桌面端数据目录")
		}
		base = filepath.Join(appData, "QwenWorkCN")
	}
	authPath := filepath.Join(base, "auth-v2.dat")
	statePath := filepath.Join(base, "Local State")

	authRaw, err := os.ReadFile(authPath)
	if err != nil {
		return nil, fmt.Errorf("未找到千问办公桌面端凭据（%s）：请先安装并登录桌面端", authPath)
	}

	plain, err := decryptSafeStorage(statePath, authRaw)
	if err != nil {
		return nil, err
	}

	var doc struct {
		Token                 string `json:"token"`
		RefreshToken          string `json:"refreshToken"`
		ExpiresAt             string `json:"expiresAt"`
		RefreshTokenExpiresAt string `json:"refreshTokenExpiresAt"`
		User                  struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Email    string `json:"email"`
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := json.Unmarshal(plain, &doc); err != nil {
		return nil, fmt.Errorf("桌面端凭据格式无法解析（%s）：%w", authPath, err)
	}
	if strings.TrimSpace(doc.Token) == "" || strings.TrimSpace(doc.User.ID) == "" {
		return nil, fmt.Errorf("桌面端未登录（%s 缺少 token/user.id）：请在桌面端完成登录后重试", authPath)
	}

	out := &LocalAuth{
		AccessToken:  doc.Token,
		RefreshToken: doc.RefreshToken,
		UID:          doc.User.ID,
		Nickname:     firstNonEmpty(doc.User.Name, doc.User.Username),
		Email:        doc.User.Email,
		SourcePath:   authPath,
	}
	// 到期时间优先取 JWT 的 exp（服务端真实判据），其次取文件里的 expiresAt。
	if exp := jwtExpiry(doc.Token); exp > 0 {
		out.ExpiresAt = exp
	} else if t, err := time.Parse(time.RFC3339, doc.ExpiresAt); err == nil {
		out.ExpiresAt = t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, doc.RefreshTokenExpiresAt); err == nil {
		out.RefreshExpiresAt = t
	}
	return out, nil
}

// decryptSafeStorage 按 Chromium safeStorage 规则解出明文：
// Local State 里的 encrypted_key = "DPAPI" + DPAPI(blob) → AES-256 key；
// 数据文件 = "v10" + nonce(12) + ciphertext + tag(16)。
// 未加密的旧格式（直接 JSON）原样返回，避免因客户端版本差异直接失败。
func decryptSafeStorage(statePath string, raw []byte) ([]byte, error) {
	if len(raw) < 3 || string(raw[:3]) != "v10" {
		return raw, nil
	}
	stateRaw, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败：%w", statePath, err)
	}
	var state struct {
		OSCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}
	if err := json.Unmarshal(stateRaw, &state); err != nil {
		return nil, fmt.Errorf("解析 Local State 失败：%w", err)
	}
	encKey, err := base64.StdEncoding.DecodeString(state.OSCrypt.EncryptedKey)
	if err != nil || len(encKey) <= 5 || string(encKey[:5]) != "DPAPI" {
		return nil, fmt.Errorf("Local State 里没有可用的 DPAPI 密钥（客户端可能未使用系统密钥加密）")
	}
	key, err := dpapiUnprotect(encKey[5:])
	if err != nil {
		return nil, fmt.Errorf("解不开桌面端密钥：%w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AES 初始化失败：%w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("GCM 初始化失败：%w", err)
	}
	if len(raw) < 3+gcm.NonceSize() {
		return nil, fmt.Errorf("桌面端凭据文件长度异常")
	}
	nonce := raw[3 : 3+gcm.NonceSize()]
	plain, err := gcm.Open(nil, nonce, raw[3+gcm.NonceSize():], nil)
	if err != nil {
		return nil, fmt.Errorf("桌面端凭据解密失败（密钥不匹配或文件损坏）：%w", err)
	}
	return plain, nil
}

// jwtExpiry 取 JWT payload 的 exp；解析失败返回 0。
func jwtExpiry(token string) int64 {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return 0
	}
	payload := strings.TrimRight(parts[1], "=")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return 0
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return 0
	}
	return claims.Exp
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
