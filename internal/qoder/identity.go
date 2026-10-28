package qoder

// clientVersion 桌面客户端版本。出现在签名 payload 的 cosyVersion 与
// cosy-version 请求头 —— 两处必须一致，否则上游看到自相矛盾的客户端身份。
const clientVersion = "1.1.57"
