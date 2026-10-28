//go:build windows

package officeace

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// 本文件锁死 DPAPI 链路的**调用契约**与文件层语义。
//
// 为什么必须存在：`cryptUnprotect` 曾因为少传一个参数（`ppszDataDescr`）而在本机
// 恒失败，而失败被 `loadOAuthProfile` 静默吞掉（`err != nil → continue`），最终
// 表现为「导入的 OfficeAce 账号永远没有临时凭据」——一条没有任何日志的静默失效链
// （R34/R41 家族）。syscall 的参数错位在编译期与 vet 期都查不出来，只能靠**真实
// 往返调用**锁死：用 CryptProtectData 加密一段假文本，再要求 cryptUnprotect 原样解回。

var procProtectData = syscall.NewLazyDLL("crypt32.dll").NewProc("CryptProtectData")

// TestReadProtectedTextPlainPassthrough 无魔数头的文件按明文返回（既有语义，不得回归）。
func TestReadProtectedTextPlainPassthrough(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(p, []byte("plain-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readProtectedText(p)
	if err != nil {
		t.Fatalf("明文直通失败：%v", err)
	}
	if got != "plain-secret" {
		t.Fatalf("明文直通内容不一致：got %q", got)
	}
}
