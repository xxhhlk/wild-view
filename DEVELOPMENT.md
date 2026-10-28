# wild-view 开发者文档

> 面向代码维护者。普通用户请看 [README.md](README.md)，纪律与不变量见 [AGENTS.md](AGENTS.md)。

## 0. 项目速览

- **是什么**：账号额度与模型倍率查看器。单进程 = 管理 HTTP 服务 + 纯签到调度 + 托盘 + 静态 Web UI。
  不提供会话转发，`/v1/*` 与 `/status` 返回 404。
- **技术栈**：Go 1.25+ / energye/systray / 纯静态前端（go:embed，无构建链）。
- **模块名**：`wild-view`（与 `go.mod` 一致）。
- **平台**：Windows（完整）、macOS（cgo）、Linux（`--no-tray` 无头）。
- **数据文件**（默认在 `wild-view-data/`）：
  `config.json`、`auths/<kind>-*.json`（凭据，勿外泄）、`data/state-<kind>.json`、
  `data/catalog-cache.json`（账号作用域目录/倍率）、`data/ledger/`（流水与余额采样）、`data/app.log`。

## 1. 代码地图

```
cmd/wild-view/main.go        入口：加载账号 → 构造查询客户端 → 管理 HTTP + 托盘
cmd/wild-view/web/           静态 Web UI（index.html / app.js / style.css）
cmd/genicon/                 图标生成
internal/
├── app/app.go               管理 API、登录编排、日志、额度刷新
├── app/catalog.go           目录/倍率账号作用域缓存
├── app/session.go           面板鉴权（cookie + 管理员密码 + 登录限流）
├── auth/                    凭据解析（嵌套/扁平）与原子写回
├── pool/                    账号存储、状态持久化
├── provider/                共享类型 + 续期单飞
├── staticmodels/            渠道静态模型兜底表
├── ledger/                  积分流水 + 余额采样
├── scheduler/               定时纯签到（保活已停用）
├── config/                  配置加载/校验/写回
├── systray/                 托盘
└── <channel>/               各渠道查询客户端（目录/倍率/额度）
```

## 2. 关键流程

**启动**：`workDir()` 决定数据目录（`WILDVIEW_HOME` > exe 旁 `wild-view-data/`）→ 加载配置与账号 →
每个渠道一个 `app.Runtime{Kind, Pool, Upstream, Scheduler, StaticModels}` → `app.New` →
`mux` 挂静态面板、`/api/*`、`/v1/*` 404 → `StartServer` → 后台额度刷新（10 分钟）、
目录/倍率刷新（30 分钟检查，1 小时新鲜期）、纯签到（开启时）。

**查询**：`RefreshCatalog(force)` 遍历 `Pool.List()` 的每个账号 → 续期（单飞）→
`FetchModels` / `FetchModelPricing` → 写 `catalog`（按 `channel/uid`）→ `FeesInfo` 聚合输出。
失败只更新该账号的错误与尝试时间，保留上次成功数据。

**额度**：`creditTotals` → `UserResourceDetail` → 写 pool 快照 + 积分流水差分 + 余额采样。

## 3. 关键不变量

1. **凭据格式**：嵌套形 `{auth:{...},account:{...}}`；`auth.Parse` 与各登录 `SaveAuth` 必须一致。
2. **日志/面板零 token**：任何输出不得含 access/refresh token。
3. **续期落盘**：refresh token 会轮换，刷新后必须 `SaveAtomic()`；失败要记录。
4. **同账号单飞**：`provider.RefreshOnce`，键为凭据文件路径。
5. **查询不选号**：不使用 `Pool.Pick`；逐账号查询、失败隔离。
6. **倍率口径**：缺失/非法 → `unknown`；只有上游显式 0 才是 Free。
7. **额度口径**：积分 / Token / 百分比不混算；`credits_unit="token"` 的渠道不进积分合计与曲线。
8. **纯签到**：`features.checkin_enabled` 控制；依赖对话的渠道不参与，禁止聊天保活。
9. **面板鉴权**：非环回监听必须设置 `admin_password`；`/api/auth/*` 不加守卫，其余 `/api/*` 必须经过。
10. **前端同步**：删除/新增端点必须同步 `web/app.js`；`frontend_routes_test.go` 与
    `scripts/ui-static-check.mjs` 会守门。

## 4. 构建与验证

```bash
go build ./... && go vet ./... && go test ./...
node scripts/ui-static-check.mjs
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags devdump \
  -ldflags "-H windowsgui" -o dist/wild-view.exe ./cmd/wild-view
```

日额度测试夹具以当前时间生成到期日期，不应随日历推进失败。所有验证失败都需要排查。

`.github/workflows/verify.yml` 在 push、PR 与手动触发时使用 Go 1.25 和 Node.js 24 执行上述四项检查。
手动发布工作流先复用该验证，再对通过验证的同一提交进行多平台构建和发布；发布触发方式仍为手动。

## 5. 渠道接入要点

- 新渠道需实现查询方法：`RefreshToken` / `FetchModels` / `FetchModelPricing` / `UserResourceDetail`。
- 只读接口不需要对话能力；若某数据只能通过对话获得，应显示"上游未提供"，不要发对话。
- 倍率解析必须区分"字段缺失"与"显式 0"，并把来源写进 `ModelPricing.Explicit`。
- 签到若依赖对话（如先发一句再领），不要加入 `SupportsPureCheckin`。
