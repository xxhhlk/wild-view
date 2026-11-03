//go:build !windows

package qwenwork

import "fmt"

// ImportLocal 非 Windows 平台不支持：桌面端凭据由系统密钥环加密，
// 当前只实现了 Windows 的 DPAPI 解密路径。
func ImportLocal() (*LocalAuth, error) {
	return nil, fmt.Errorf("从千问办公桌面端导入仅支持 Windows")
}
