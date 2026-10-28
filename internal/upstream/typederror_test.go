package upstream

// 本文件锁定 R79：目录/费率接口遇 HTTP ≥400 必须返回**类型化** *Error。
//
// 为什么必须锁死：上层自愈链（app.refreshIfSessionDead → handler/app 的
// 401 重试）全靠 errors.As 取分类。这两个接口原来返回裸 fmt.Errorf
// （`models api status 401: …`），errors.As 恒 false ⇒ 401 自愈在模型面与
// 费率面**永远不可能生效**：本地 expiresAt 仍在未来而上游已拒绝时
// （token 在别处被轮换 / 上游临时吊销），费率与模型列表只能一直陈旧，
// 直到进程重启或 token 自然过期。
//
// 与「上游是否真有 bug」无关——这是本仓自己的缺陷，2026-10-17 排查
// WorkBuddyCN 401 时顺带发现。
