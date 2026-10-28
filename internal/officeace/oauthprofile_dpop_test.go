package officeace

// 本文件锁死「导入路径必须复用桌面端档案里的 DPoP 私钥」这一契约（R-6，2026-10-17 定位）。
//
// 为什么关键：refresh.go 的 dpopKey() 是按 `map[string]string` 反序列化落盘 JWK 的。
// 桌面端档案里的 `dpop.privateJwk` 除 kty/crv/x/y/d 之外还带 `ext`（bool）与 `key_ops`（数组）
// —— 直接把整个对象 Marshal 落盘，反序列化会**失败**，dpopKey() 随即静默回落到
// NewDPoPKey() 新建一把密钥；而 DPoP 要求续期与签发使用同一把密钥，
// 结果就是「导入当天可用、临时凭据（~2h）到期后积分/签到又全坏」且没有任何日志。
// 因此 dpopJWKJSON 必须做窄化，且窄化结果必须能被 dpopKeyFromJWK 还原。
