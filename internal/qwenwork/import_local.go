package qwenwork

import "time"

// LocalAuth 桌面端本地登录态。
// 类型放在无构建约束的文件里：Windows 实现负责解密填充，其它平台仅返回不支持。
type LocalAuth struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        int64 // access token 到期（Unix 秒）
	RefreshExpiresAt time.Time
	UID              string
	Nickname         string
	Email            string
	SourcePath       string
}
