# wild-view 项目提示词（AGENTS.md）

> 本文件面向 AI Agent 与开发者，只保留当前有效的纪律与不变量。
> 历史决议与转发时代的规则已随代码删除，不再适用。

## 0. 项目定位

wild-view 由 [wild-work](https://github.com/xxhhlk/wild-work) 派生（基线 `afda902`），
是**只读的账号额度与模型倍率查看器**：

- 不提供任何会话转发入口：`/v1/*` 与 `/status` 一律 404；
- 不产生对话：聊天保活已移除，依赖对话的签到（WorkBuddyAI、智谱清言）不参与；
- 纯签到可开关：`features.checkin_enabled`，默认 false，手动与定时签到同受门控。

## 1. 硬性纪律

1. **不得引入会话转发**：不要恢复 `/v1/*`、兼容层、生图或客户端接入配置。
2. **不得为查询发起对话**：目录、倍率、额度只能走各自的只读接口；任何"先聊一句再拿数据"的实现都禁止。
3. **凭据不落日志、不进面板明文**：日志与 API 响应不得包含 access/refresh token；面板只回显脱敏值。
4. **凭据文件格式不变**：`internal/auth` 的嵌套形 `{auth:{...},account:{...}}` 与 `SaveAtomic()` 必须保持一致；
   新增字段要同时进解析与写回。
5. **续期必须落盘**：refresh token 会轮换，刷新成功后必须 `SaveAtomic()`；保存失败要记录并可见。
6. **同账号续期单飞**：并发刷新一律走 `provider.RefreshOnce`，否则互相作废 refresh token。
7. **查询不得用路由选号**：不要用 `Pool.Pick` 决定查询账号；按 `Pool.List()` 逐账号查询，单账号失败隔离。
8. **缺失不等于免费**：倍率字段缺失、解析失败、上游未提供，都不得显示为 0/Free。
9. **不把不同口径相加**：积分、Token、窗口百分比、不同平台积分不得混算；Token 口径渠道不进积分曲线。
10. **数据目录独立**：默认 exe 旁 `wild-view-data/`，可用 `WILDVIEW_HOME` 覆盖；不要读写 wild-work 的目录。
11. **管理面板鉴权**：`admin_password` 为空时只允许监听环回地址；`/api/auth/*` 不加会话守卫，其余 `/api/*` 必须经过守卫。
12. **前端与后端路由同源**：新增/删除 `/api/*` 时必须同步 `cmd/wild-view/web/app.js`；
    `internal/app/frontend_routes_test.go` 与 `scripts/ui-static-check.mjs` 会守门。

## 2. 代码地图

```
cmd/wild-view/main.go          入口：账号加载 → 查询客户端 → 管理 HTTP + 托盘
cmd/wild-view/web/             纯静态 Web UI（index.html / app.js / style.css，go:embed）
internal/app/app.go            管理 API、登录编排、日志
internal/app/catalog.go        模型目录/倍率的账号作用域缓存（TTL、失败退避、原子持久化）
internal/app/session.go        面板鉴权：cookie 会话 + 管理员密码
internal/auth/                 凭据解析与原子写回
internal/pool/                 账号存储与状态持久化
internal/provider/             共享类型（ModelInfo / ModelPricing / ResourceItem）与续期单飞
internal/staticmodels/         渠道静态模型兜底表（纯数据）
internal/ledger/               积分流水 + 余额采样（balance-*.jsonl）
internal/scheduler/            定时纯签到与凭据续期（保活已停用）
internal/<channel>/            各渠道查询客户端（目录/倍率/额度）
```

## 3. 关键约定

- **目录/倍率缓存**：按 `channel/uid` 保存；新鲜期 1 小时，失败退避 5 分钟，批量并发 3；
  失败保留上次成功结果，不清空同渠道其他账号。
- **额度口径**：`credits_unit` 为 `token` 的渠道（ZCode）单独展示；`credits_na` 表示该渠道无积分概念。
- **余额曲线**：`ledger.RecordBalance` 在额度查询成功后采样；同一账号 10 分钟内余额未变则去抖。
  曲线是绝对值，下降可能来自消耗、到期或套餐回收，不得称为"对话消耗"。
- **纯签到能力**：`provider.SupportsPureCheckin` 列出不依赖对话的渠道；
  新增渠道若签到需要先发消息，必须排除。
- **构建**：`CGO_ENABLED=0 go build -tags devdump -ldflags "-H windowsgui" -o dist/wild-view.exe ./cmd/wild-view`。
- **验证**：`go build ./... && go vet ./... && go test ./... && node scripts/ui-static-check.mjs`。
  `internal/raccoon` 的两个日额度测试依赖固定日期，属存量失败，与改动无关。
