// Command wild-work 系统托盘 daemon 入口。
// 单进程 = OpenAI 兼容 HTTP 服务 + 自动签到调度器 + 系统托盘 + 静态 Web UI。
// 双击 exe 启动常驻托盘；托盘菜单：打开主界面 / 刷新积分 / 查看日志 / 退出；
// 打开主界面或双击托盘 → 系统浏览器打开 Web UI。
//
// 另有一个**非守护**入口：`--raccoon-callback <深链>`（小浣熊**旧版**桌面协议登录的回调，
// 由 Windows 按 HKCU 的 office-raccoon 注册表唤起）。该分支只把深链解析后落盘再退出，
// **绝不**启动第二份服务/托盘——详见 handleRaccoonCallback。
//
// 注：小浣熊登录已于 R48 改为**网页版 CDP 捕获**（不再改写注册表），此入口保留
// 仅为了接住旧版本可能残留的注册表指向，属兼容死代码，不再有新流程使用。
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"wild-view/internal/app"
	"wild-view/internal/auth"
	"wild-view/internal/codearts"
	"wild-view/internal/config"
	"wild-view/internal/gemini"
	"wild-view/internal/glm"
	"wild-view/internal/kilo"
	"wild-view/internal/ledger"
	"wild-view/internal/lobsterai"
	"wild-view/internal/loomy"
	"wild-view/internal/minimax"
	"wild-view/internal/monkeycode"
	"wild-view/internal/officeace"
	"wild-view/internal/platform"
	"wild-view/internal/pool"
	"wild-view/internal/provider"
	"wild-view/internal/qoder"
	"wild-view/internal/qodercn"
	"wild-view/internal/qodercom"
	"wild-view/internal/qwenwork"
	"wild-view/internal/raccoon"
	"wild-view/internal/scheduler"
	"wild-view/internal/staticmodels"
	"wild-view/internal/systray"
	"wild-view/internal/traework"
	"wild-view/internal/upstream"
	"wild-view/internal/workbuddyai"
	"wild-view/internal/zcode"
)

//go:embed all:web
var webFS embed.FS

//go:embed build/trayicon.ico
var trayIconICO []byte

// protocolCallbackArg 从命令行取出小浣熊协议回调深链（无则返回空串）。
func protocolCallbackArg(args []string) string {
	for i, a := range args {
		if a == raccoon.CallbackFlag && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, raccoon.CallbackFlag+"="); ok {
			return v
		}
	}
	return ""
}

// handleProtocolCallback 协议处理器子进程的落地动作：把深链参数写进 data/ 后立即返回。
// 主实例的登录轮询（app.pollLogin）每 2 秒读一次该文件，读到即用授权码兑换 token。
// 这里刻意不做任何 UI/网络动作 —— 本进程的生命周期只有毫秒级。
func handleProtocolCallback(raw string) {
	stateDir := "data"
	if cfg, err := config.Load("config.json"); err == nil {
		stateDir = filepath.Dir(cfg.StateFile)
	}
	if err := raccoon.SaveCallback(stateDir, raw); err != nil {
		log.Printf("小浣熊协议回调落盘失败：%v", err)
	}
}

func main() {
	// 工作目录：便携/CLI 形态固定为 exe 所在目录，保证相对路径配置（./auths ./data）稳定。
	// macOS .app bundle 内该目录只读、且随 app 替换被清空，故改用系统数据目录（见 workDir）。
	_ = os.Chdir(workDir())
	wd, _ := os.Getwd()

	// 小浣熊协议回调分支（必须最先处理）：Windows 会以
	//   "<exe>" --raccoon-callback "office-raccoon://auth/callback?code=…"
	// 唤起本进程投递深链。这条路径只落盘后立即退出，绝不能继续启动 HTTP 服务与托盘
	// —— 那是主实例的职责，重复启动会撞端口。
	if raw := protocolCallbackArg(os.Args[1:]); raw != "" {
		handleProtocolCallback(raw)
		return
	}

	cfgPath := "config.json"
	cfg, err := config.Load(cfgPath)
	// --autostart 由开机自启项附带：开机启动不弹提示
	// --no-tray 无头模式（无桌面 Linux/服务器）
	autostart := false
	noTray := false
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--autostart":
			autostart = true
		case "--no-tray":
			noTray = true
		}
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			log.Printf("config.json 不存在，使用默认配置并生成")
			cfg = config.Default()
			if err := config.Save(cfg, cfgPath); err != nil {
				log.Printf("write default config: %v", err)
			}
		} else {
			fatal("加载配置失败：%v\n\n请检查 config.json 后重新启动", err)
		}
	}
	// 管理面板鉴权：监听非环回地址（0.0.0.0 / 网卡 IP）时必须设置管理员密码，
	// 否则局域网任何设备都能无凭据打开面板、退出程序。启动层与面板保存层双重把关。
	if !cfg.Listen.IsLoopback() && strings.TrimSpace(cfg.AdminPass) == "" {
		fatal("监听 %s 已对外暴露，但 config.json 中未设置 admin_password。\n\n"+
			"请设置 admin_password（或在面板设置中修改监听地址）后重新启动；\n"+
			"若只想本机使用，把 listen.host 改回 127.0.0.1。", cfg.Listen.Addr())
	}
	if cfg.AdminPass != "" && len(cfg.AdminPass) < 8 {
		log.Printf("警告：管理员密码不足 8 位，建议使用更长口令（面板鉴权已生效）")
	}
	_ = os.MkdirAll(cfg.AuthDir, 0o755)
	_ = os.MkdirAll(filepath.Dir(cfg.StateFile), 0o755)

	// 多渠道运行时：workbuddy / traework / qoder 各自独立 pool + state + scheduler。
	stateDir := filepath.Dir(cfg.StateFile)
	// 旧版兼容：迁移 state.json → 分渠道 state-{kind}.json
	migrateStateFiles(cfg.StateFile, stateDir)

	wbAuths, err := auth.LoadWorkBuddyDir(cfg.AuthDir, cfg.Region)
	if err != nil {
		fatal("读取 WorkBuddy 账号目录失败：%v", err)
	}
	trAuths, err := auth.LoadTraeDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 TraeWork 账号目录失败：%v", err)
	}
	qdAuths, err := auth.LoadQoderDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 Qoder 账号目录失败：%v", err)
	}
	wbaAuths, err := auth.LoadWorkBuddyAiDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 WorkBuddy 国际版账号目录失败：%v", err)
	}
	qwAuths, err := auth.LoadQwenWorkDir(cfg.AuthDir)
	if err != nil {
		fatal("读取千问办公账号目录失败：%v", err)
	}
	qcnAuths, err := auth.LoadQoderCNDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 QoderCN 账号目录失败：%v", err)
	}
	qcmAuths, err := auth.LoadQoderCOMDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 QoderCOM 账号目录失败：%v", err)
	}
	// 小浣熊 / Loomy：凭据不由本工具登录产生，而由面板「从本机客户端导入」写入 auths/。
	rcAuths, err := auth.LoadRaccoonDir(cfg.AuthDir)
	if err != nil {
		fatal("读取小浣熊账号目录失败：%v", err)
	}
	lmAuths, err := auth.LoadLoomyDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 Loomy 账号目录失败：%v", err)
	}
	// MonkeyCode：同为「导入型」渠道，凭据由面板「从本机客户端导入」写入 auths/。
	mcAuths, err := auth.LoadMonkeyCodeDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 MonkeyCode 账号目录失败：%v", err)
	}
	// 智谱清言：凭据是浏览器 Cookie 里的 chatglm_refresh_token（glm-*.json）。
	glmAuths, err := auth.LoadGLMDir(cfg.AuthDir)
	if err != nil {
		fatal("读取智谱清言账号目录失败：%v", err)
	}
	// OfficeAce：导入型渠道，凭据由面板从本机 OfficeAce 客户端文件发现或自助 OAuth 登录写入 auths/。
	oaAuths, err := auth.LoadOfficeAceDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 OfficeAce 账号目录失败：%v", err)
	}
	// Kilo 免费池同样是免密渠道：没有凭证文件、没有登录流程，只有一个虚拟账号。
	kiAuths := []*auth.Auth{kilo.AnonymousAuth()}
	// MiniMax Code：正常多账号渠道，凭据由设备码登录写入 auths/minimax-*.json。
	mimAuths, err := auth.LoadMinimaxDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 MiniMax 账号目录失败：%v", err)
	}
	// LobsterAI：正常多账号渠道，凭据由本机回调登录写入 auths/lobsterai-*.json。
	lobAuths, err := auth.LoadLobsterAIDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 LobsterAI 账号目录失败：%v", err)
	}
	// ZCode：正常多账号渠道，凭据由服务端中介设备授权流登录写入 auths/zcode-*.json。
	zcdAuths, err := auth.LoadZCodeDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 ZCode 账号目录失败：%v", err)
	}
	// Gemini（Google Code Assist）：正常多账号渠道，凭据由 loopback OAuth 登录写入 auths/gemini-*.json。
	gemAuths, err := auth.LoadGeminiDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 Gemini 账号目录失败：%v", err)
	}
	// CodeArts（华为云 CodeArts Agent）：正常多账号渠道，凭据为 AK/SK + SecurityToken
	// （由 PKCE OAuth 登录写入 auths/codearts-*.json）。
	caAuths, err := auth.LoadCodeArtsDir(cfg.AuthDir)
	if err != nil {
		fatal("读取 CodeArts 账号目录失败：%v", err)
	}
	log.Printf("loaded accounts: workbuddy=%d %s, traework=%d, qoder=%d, qodercn=%d, qodercom=%d, workbuddyai=%d, qwenwork=%d, glm=%d, monkeycode=%d, raccoon=%d, loomy=%d, officeace=%d, kilo=%d(匿名), minimax=%d, lobsterai=%d, zcode=%d, gemini=%d, codearts=%d from %s",
		len(wbAuths), cfg.Region, len(trAuths), len(qdAuths), len(qcnAuths), len(qcmAuths), len(wbaAuths), len(qwAuths), len(glmAuths), len(mcAuths), len(rcAuths), len(lmAuths), len(oaAuths), len(kiAuths), len(mimAuths), len(lobAuths), len(zcdAuths), len(gemAuths), len(caAuths), cfg.AuthDir)

	wbPool := pool.New(filepath.Join(stateDir, "state-workbuddy.json"))
	for _, a := range wbAuths {
		wbPool.Add(a)
	}
	trPool := pool.New(filepath.Join(stateDir, "state-traework.json"))
	for _, a := range trAuths {
		trPool.Add(a)
	}
	qdPool := pool.New(filepath.Join(stateDir, "state-qoder.json"))
	for _, a := range qdAuths {
		qoder.EnsureFingerprint(a) // 老凭证补机器指纹
		qdPool.Add(a)
	}
	wbaPool := pool.New(filepath.Join(stateDir, "state-workbuddyai.json"))
	for _, a := range wbaAuths {
		wbaPool.Add(a)
	}
	qwPool := pool.New(filepath.Join(stateDir, "state-qwenwork.json"))
	for _, a := range qwAuths {
		qwPool.Add(a)
	}
	qcnPool := pool.New(filepath.Join(stateDir, "state-qodercn.json"))
	for _, a := range qcnAuths {
		qodercn.EnsureFingerprint(a) // 老凭证补机器指纹
		qcnPool.Add(a)
	}
	qcmPool := pool.New(filepath.Join(stateDir, "state-qodercom.json"))
	for _, a := range qcmAuths {
		qodercom.EnsureFingerprint(a) // 老凭证补机器指纹
		qcmPool.Add(a)
	}
	rcPool := pool.New(filepath.Join(stateDir, "state-raccoon.json"))
	for _, a := range rcAuths {
		rcPool.Add(a)
	}
	lmPool := pool.New(filepath.Join(stateDir, "state-loomy.json"))
	for _, a := range lmAuths {
		lmPool.Add(a)
	}
	mcPool := pool.New(filepath.Join(stateDir, "state-monkeycode.json"))
	for _, a := range mcAuths {
		mcPool.Add(a)
	}
	// 智谱清言：与其它渠道同为多账号池（可加多个 refresh_token），
	// 但每个账号不可人工恢复（refresh_token 轮换后旧值即失效），
	// 故 Runtime 置 SingleAccount —— 见 runtimes 装配处的说明。
	glmPool := pool.New(filepath.Join(stateDir, "state-glm.json"))
	for _, a := range glmAuths {
		glmPool.Add(a)
	}
	// OfficeAce：真多账号池（每个华为账号一份凭据），不置 SingleAccount。
	oaPool := pool.New(filepath.Join(stateDir, "state-officeace.json"))
	for _, a := range oaAuths {
		oaPool.Add(a)
	}
	// kilo：整池只有一个虚拟账号，state 仅用于记冷却（无积分、无签到）。
	// 同样要在启动时自愈：任何历史脏数据（冷却/禁用）都会让这条没有第二个账号的渠道直接下线。
	kiPool := pool.New(filepath.Join(stateDir, "state-kilo.json"))
	for _, a := range kiAuths {
		kiPool.Add(a)
	}
	kiPool.SetDisabled(kilo.AnonymousUID, false)
	kiPool.ClearPenalty(kilo.AnonymousUID)
	// MiniMax：真多账号池（每个 MiniMax 账号一份 OAuth 凭据），不置 SingleAccount。
	mimPool := pool.New(filepath.Join(stateDir, "state-minimax.json"))
	for _, a := range mimAuths {
		mimPool.Add(a)
	}
	// LobsterAI：真多账号池（每个有道账号一份凭据，含 keyfrom 四元组），不置 SingleAccount。
	lobPool := pool.New(filepath.Join(stateDir, "state-lobsterai.json"))
	for _, a := range lobAuths {
		lobPool.Add(a)
	}
	// ZCode：真多账号池（每个智谱账号一份凭据 + device_mid），不置 SingleAccount。
	zcdPool := pool.New(filepath.Join(stateDir, "state-zcode.json"))
	for _, a := range zcdAuths {
		zcdPool.Add(a)
	}
	// Gemini：真多账号池（每个 Google 账号一份 OAuth 凭据 + project），不置 SingleAccount。
	gemPool := pool.New(filepath.Join(stateDir, "state-gemini.json"))
	for _, a := range gemAuths {
		gemPool.Add(a)
	}
	// CodeArts：真多账号池（每个华为账号一份 AK/SK + SecurityToken），不置 SingleAccount。
	caPool := pool.New(filepath.Join(stateDir, "state-codearts.json"))
	for _, a := range caAuths {
		caPool.Add(a)
	}

	// 查询客户端统一使用配置的网络超时。
	wbUp := upstream.New()
	wbUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	trUp := traework.New()
	trUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	// TraeCode（代码版）：与 TraeWork 同一上游、共用账号，仅 function 不同（solo_agent）。
	trCodeUp := traework.NewTraeCode()
	trCodeUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	qdUp := qoder.New()
	qdUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	wbaUp := workbuddyai.New()
	wbaUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	qwUp := qwenwork.New()
	qwUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	qcnUp := qodercn.New()
	qcnUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	qcmUp := qodercom.New()
	qcmUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	// 等响应头上限由 NewWithTimeout 统一算（≥180s，R118）；总超时仍取 upstream.timeout_seconds。
	rcUp := raccoon.NewWithTimeout(time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second)
	lmUp := loomy.New()
	lmUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	mcUp := monkeycode.New()
	mcUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	oaUp := officeace.New()
	oaUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	kiUp := kilo.New()
	kiUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	mimUp := minimax.New()
	mimUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	lobUp := lobsterai.New()
	lobUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	zcdUp := zcode.New()
	zcdUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	gemUp := gemini.New()
	gemUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	caUp := codearts.New()
	caUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	glmUp := glm.New()
	glmUp.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second

	checkinMinutes, err := config.ParseClockTimes(cfg.Schedule.CheckinTimes)
	if err != nil {
		fatal("解析签到时间失败：%v", err)
	}
	// 查看器默认不执行任何签到；仅在面板显式开启后按配置时间运行纯签到。
	if !cfg.Features.CheckinEnabled {
		checkinMinutes = []int{}
	}
	// 聊天保活已移除：查看器不得为维持会话向上游发起对话。
	keepaliveHours := []int{}

	// Qoder 双区签到窗口（对齐上游 qoder2api 的调度机制）：
	// 每日 10:00（UTC+8）开放，活动可能在整点之后才创建，故 10:00–12:00 每分钟重试。
	const (
		qoderCheckinMinute     = 10 * 60 // 10:00
		qoderCheckinRetryUntil = 12 * 60 // 12:00（上游同款兜底截止）
	)

	// 积分流水记账器：在 scheduler 之前创建（签到后差分记账用）；
	// App 内部不重建，main 装配时统一注入。失败仅降级为无统计。
	lg, lgerr := ledger.New(filepath.Join(stateDir, "ledger"))
	if lgerr != nil {
		log.Printf("ledger init failed（统计功能不可用）: %v", lgerr)
	}

	wbSch := scheduler.New(scheduler.Config{Pool: wbPool, Upstream: wbUp, Name: "workbuddy", CheckinMinutes: checkinMinutes, KeepaliveHours: keepaliveHours, Ledger: lg})
	trSch := scheduler.New(scheduler.Config{Pool: trPool, Upstream: trUp, Name: "traework", CheckinMinutes: checkinMinutes, KeepaliveHours: keepaliveHours, Ledger: lg})
	// Qoder 无签到活动：调度器只做 token keepalive（每日 refresh 保活）。
	// CheckinMinutes 用显式空切片关闭（传 nil 会被 scheduler.New 补成默认 9:00/21:00）。
	qdSch := scheduler.New(scheduler.Config{Pool: qdPool, Upstream: qdUp, Name: "qoder", CheckinMinutes: []int{}, KeepaliveHours: keepaliveHours, Ledger: lg})
	// WorkBuddy 国际版：无显式签到（ActivitiesOnly 模式）。
	// 定时仍对每个账号调用 DailyCheckin，其实现为「免费模型对话保活 + 签到探测」；
	// 不记录/上报签到状态，保持对用户透明。
	// Keepalive 关闭（token 有效期 365 天，无需每日刷新），用显式空切片关闭
	// （传 nil 会被 scheduler.New 补成默认 22:00）。
	wbaSch := scheduler.New(scheduler.Config{Pool: wbaPool, Upstream: wbaUp, Name: "workbuddyai",
		// 该渠道的「签到」实为免费模型对话保活，查看器不产生会话 ⇒ 显式关闭。
		CheckinMinutes: []int{}, KeepaliveHours: []int{}, ActivitiesOnly: true, Ledger: lg})
	// 千问办公：无签到活动（每日积分服务端被动发放，无需保活/领取）；
	// CheckinMinutes + KeepaliveHours 均为显式空切片，本渠道没有任何定时任务
	// （token 由 deviceToken/refresh 按需轮换，定时保活反而会与千问办公 App
	// 互踩 —— 见备忘 §7.5 风险 1）。
	// 余额/费率靠 StartCreditAutoRefresh 循环拉取。
	qwSch := scheduler.New(scheduler.Config{Pool: qwPool, Upstream: qwUp, Name: "qwenwork",
		CheckinMinutes: []int{}, KeepaliveHours: []int{}, Ledger: lg})
	// QoderCN：仅 campaigns 活动路径（legacy daily-check-in 已 DISABLED，其 claim 恒返回
	// 409 会造成「假成功」，故不再使用）。签到窗口对齐上游 qoder2api：10:00 开放，
	// 活动可能在整点后才创建，故 10:00–12:00 之间每分钟重试，直到真正领到或超时。
	qcnSch := scheduler.New(scheduler.Config{Pool: qcnPool, Upstream: qcnUp, Name: "qodercn", CheckinMinutes: []int{qoderCheckinMinute}, CheckinRetryUntil: qoderCheckinRetryUntil, KeepaliveHours: keepaliveHours, Ledger: lg})
	// QoderCOM：仅 campaigns 活动路径（无 daily-check-in）；其余同 QoderCN。
	qcmSch := scheduler.New(scheduler.Config{Pool: qcmPool, Upstream: qcmUp, Name: "qodercom", CheckinMinutes: []int{qoderCheckinMinute}, CheckinRetryUntil: qoderCheckinRetryUntil, KeepaliveHours: keepaliveHours, Ledger: lg})
	// MonkeyCode：已实现签到（控制台 Cookie 走 POST {console}/api/v1/users/wallet/checkin，
	// 见 internal/monkeycode/checkin.go）。签到时刻用全局配置；**不设 CheckinRetryUntil**
	// ——它是「直接领一次」的接口，没有 Qoder 那种「活动整点后才创建」的延迟，
	// 每个签到时刻单次尝试即可（失败状态会在下一个时刻重试），避免窗口内每分钟空跑。
	// KeepaliveHours 仍为显式空切片：上游无 refresh 端点，保活没有可刷的东西。
	mcSch := scheduler.New(scheduler.Config{Pool: mcPool, Upstream: mcUp, Name: "monkeycode",
		CheckinMinutes: checkinMinutes, KeepaliveHours: []int{}, Ledger: lg})
	// 小浣熊：签到走 setting_info（每日积分惰性落账的触发点，见 internal/raccoon/checkin.go）。
	// access_token 实测仅 ≈2h，而 refresh_token ≈30 天，
	// 故保活比其它渠道更密（每 4 小时一次）以减少「请求先 401 再刷新」的额外往返；
	// 请求路径上的 refreshIfSessionDead 仍会兜底自愈。
	//
	// 签到时间用全局配置（checkinMinutes）：上游按天去重、无活动上线延迟问题，
	// 故**不设 CheckinRetryUntil**（窗口重试是为「活动尚未创建」设计的，本渠道无此语义）。
	rcSch := scheduler.New(scheduler.Config{Pool: rcPool, Upstream: rcUp, Name: "raccoon",
		CheckinMinutes: checkinMinutes, KeepaliveHours: []int{1, 5, 9, 13, 17, 21}, Ledger: lg})
	// Loomy：已实现签到（宠物每日打工奖励，POST /api/v1/pet-work/rewards，
	// 见 internal/loomy/checkin.go）——判据是该账号当日**有效对话轮次**（上游算），
	// 故未在官方客户端用过该账号时结果是「未达档位」（如实可重试状态）。
	// 签到时刻用全局配置、**不设 CheckinRetryUntil**：档位在一天里是渐进的，
	// 窗口内每分钟重试既无意义又违反 §0.1；每个签到时刻单次尝试即可。
	// KeepaliveHours 仍为显式空切片：**上游无 refresh 端点**（session 14 天，到期需重新登录/导入），
	// 保活没有可刷的东西（refreshToken 为空，调度器会跳过并只记一条日志）。
	lmSch := scheduler.New(scheduler.Config{Pool: lmPool, Upstream: lmUp, Name: "loomy",
		CheckinMinutes: checkinMinutes, KeepaliveHours: []int{}, Ledger: lg})
	// OfficeAce：有每日签到（bonus/claim）但要先有临时凭据（V11 签名），保活用于续期。
	// CheckinMinutes 用全局签到时间；KeepaliveHours 用配置保活时段（临时凭据约 2h 到期）。
	oaSch := scheduler.New(scheduler.Config{Pool: oaPool, Upstream: oaUp, Name: "officeace",
		CheckinMinutes: checkinMinutes, KeepaliveHours: keepaliveHours, Ledger: lg})
	// Kilo 免费池：无账号、无签到、无 token 可保活 ⇒ 定时任务全关。
	// 必须传**显式空切片**（R39）：传 nil 会被 scheduler.New 当作「未配置」补成
	// 默认 9:00/21:00 签到与 22:00 保活，每天空跑两次必然失败的调用。
	kiSch := scheduler.New(scheduler.Config{Pool: kiPool, Upstream: kiUp, Name: "kilo",
		CheckinMinutes: []int{}, KeepaliveHours: []int{}})
	// MiniMax：有每日签到（7 天面板，claim 幂等判据是响应里的 claim_result），
	// 也需要 token 保活（refresh_token 会轮换，刷新后必须落盘）。
	mimSch := scheduler.New(scheduler.Config{Pool: mimPool, Upstream: mimUp, Name: "minimax",
		CheckinMinutes: checkinMinutes, KeepaliveHours: keepaliveHours, Ledger: lg})
	// LobsterAI：有每日签到（三步：slot → context → check_in，客户端幂等键），
	// 也需要 token 保活（refresh 会轮换，刷新后必须落盘）。
	lobSch := scheduler.New(scheduler.Config{Pool: lobPool, Upstream: lobUp, Name: "lobsterai",
		CheckinMinutes: checkinMinutes, KeepaliveHours: keepaliveHours, Ledger: lg})
	// ZCode：**无签到**（积分领取依赖阿里云验证码 + headful 浏览器，本仓不实现；
	// DailyCheckin 返回明确的「不支持」错误而不是伪造成功），且凭据**不可续期**
	// （JWT 无 exp）⇒ 定时任务全关。必须传显式空切片（R39）：传 nil 会被补成默认
	// 9:00/21:00 签到与 22:00 保活，每天空跑两次必然失败的调用并污染面板。
	zcdSch := scheduler.New(scheduler.Config{Pool: zcdPool, Upstream: zcdUp, Name: "zcode",
		CheckinMinutes: []int{}, KeepaliveHours: []int{}})
	// Gemini：**无签到**（DailyCheckin 返回明确的「不支持」错误），但有 refresh_token 可续期
	// ⇒ 只开保活；CheckinMinutes 用显式空切片关闭（R39：传 nil 会被补成默认 9:00/21:00，
	// 每天空跑两次必然失败的签到并污染面板）。
	gemSch := scheduler.New(scheduler.Config{Pool: gemPool, Upstream: gemUp, Name: "gemini",
		CheckinMinutes: []int{}, KeepaliveHours: keepaliveHours, Ledger: lg})
	// CodeArts：有每日签到（三步，走 HMAC 签名；本包实现了 CheckinReporter/CheckinGranter，
	// 签到结果按结构化状态上报），也需要 token 保活（续期后必须落盘，不变量 20）。
	caSch := scheduler.New(scheduler.Config{Pool: caPool, Upstream: caUp, Name: "codearts",
		CheckinMinutes: checkinMinutes, KeepaliveHours: keepaliveHours, Ledger: lg})
	// 智谱清言：签到与对话联动（当日需先对话才能领打卡进度），故保活与签到
	// 合成一个流程（glm.Client.Keepalive 先发最小对话再签到）。
	// 用 DailyCheckin 通道驱动：CheckinMinutes 用全局签到时间，
	// KeepaliveHours 也用同一时段（保活=签到，不必额外一次）。
	glmSch := scheduler.New(scheduler.Config{Pool: glmPool, Upstream: glmUp, Name: "glm",
		// 智谱清言的签到依赖先发一轮最小对话（keepaliveChat），查看器不产生会话 ⇒ 显式关闭。
		CheckinMinutes: []int{}, KeepaliveHours: keepaliveHours, Ledger: lg})

	// 查看器运行时：只有账号池、查询客户端与静态兜底表，没有转发 Handler。
	appRuntimes := map[provider.Kind]*app.Runtime{
		provider.WorkBuddy:   {Kind: provider.WorkBuddy, Pool: wbPool, Upstream: wbUp, Scheduler: wbSch, StaticModels: staticmodels.WorkBuddy()},
		provider.WorkBuddyAI: {Kind: provider.WorkBuddyAI, Pool: wbaPool, Upstream: wbaUp, Scheduler: wbaSch, StaticModels: workbuddyai.StaticModels()},
		provider.TraeWork:    {Kind: provider.TraeWork, Pool: trPool, Upstream: trUp, Scheduler: trSch, StaticModels: staticmodels.TraeWork()},
		// TraeCode 与 TraeWork 共享账号池（同一账号体系，避免 refresh token 轮换冲突），
		// 仅上游客户端不同（function=solo_agent）。
		provider.TraeCode:   {Kind: provider.TraeCode, Pool: trPool, Upstream: trCodeUp, Alias: true, StaticModels: staticmodels.TraeCode()},
		provider.Qoder:      {Kind: provider.Qoder, Pool: qdPool, Upstream: qdUp, Scheduler: qdSch, StaticModels: qoder.StaticModels()},
		provider.QoderCN:    {Kind: provider.QoderCN, Pool: qcnPool, Upstream: qcnUp, Scheduler: qcnSch, StaticModels: qodercn.StaticModels()},
		provider.QoderCOM:   {Kind: provider.QoderCOM, Pool: qcmPool, Upstream: qcmUp, Scheduler: qcmSch, StaticModels: qodercom.StaticModels()},
		provider.QwenWork:   {Kind: provider.QwenWork, Pool: qwPool, Upstream: qwUp, Scheduler: qwSch, StaticModels: qwenwork.StaticModels()},
		provider.MonkeyCode: {Kind: provider.MonkeyCode, Pool: mcPool, Upstream: mcUp, Scheduler: mcSch, StaticModels: monkeycode.StaticModels()},
		provider.Raccoon:    {Kind: provider.Raccoon, Pool: rcPool, Upstream: rcUp, Scheduler: rcSch, StaticModels: raccoon.StaticModels()},
		provider.Loomy:      {Kind: provider.Loomy, Pool: lmPool, Upstream: lmUp, Scheduler: lmSch, StaticModels: loomy.StaticModels()},
		provider.GLM:        {Kind: provider.GLM, Pool: glmPool, Upstream: glmUp, Scheduler: glmSch, StaticModels: glm.StaticModels()},
		provider.OfficeAce:  {Kind: provider.OfficeAce, Pool: oaPool, Upstream: oaUp, Scheduler: oaSch, StaticModels: officeace.StaticModels()},
		provider.Kilo:       {Kind: provider.Kilo, Pool: kiPool, Upstream: kiUp, Scheduler: kiSch, StaticModels: kilo.StaticModels()},
		provider.Minimax:    {Kind: provider.Minimax, Pool: mimPool, Upstream: mimUp, Scheduler: mimSch, StaticModels: minimax.StaticModels()},
		provider.LobsterAI:  {Kind: provider.LobsterAI, Pool: lobPool, Upstream: lobUp, Scheduler: lobSch, StaticModels: lobsterai.StaticModels()},
		provider.ZCode:      {Kind: provider.ZCode, Pool: zcdPool, Upstream: zcdUp, Scheduler: zcdSch, StaticModels: zcode.StaticModels()},
		provider.Gemini:     {Kind: provider.Gemini, Pool: gemPool, Upstream: gemUp, Scheduler: gemSch, StaticModels: gemini.StaticModels()},
		provider.CodeArts:   {Kind: provider.CodeArts, Pool: caPool, Upstream: caUp, Scheduler: caSch, StaticModels: codearts.StaticModels()},
	}

	appInst, err := app.New(app.Options{
		ConfigPath: cfgPath,
		Config:     cfg,
		Runtimes:   appRuntimes,
		Ledger:     lg,
	})
	if err != nil {
		fatal("初始化失败：%v", err)
	}
	defer appInst.Close()

	// 调度器结果写日志（无 GUI 推送，日志即面板数据源）
	wbSch.SetCheckinObserver(func(r scheduler.CheckinResult) { appInst.NotifyCheckin("workbuddy", r) })
	trSch.SetCheckinObserver(func(r scheduler.CheckinResult) { appInst.NotifyCheckin("traework", r) })
	qdSch.SetCheckinObserver(func(r scheduler.CheckinResult) { appInst.NotifyCheckin("qoder", r) })
	wbSch.SetRefreshObserver(func(uid string, ok bool, msg string) { appInst.NotifyRefresh("workbuddy", uid, ok, msg) })
	trSch.SetRefreshObserver(func(uid string, ok bool, msg string) { appInst.NotifyRefresh("traework", uid, ok, msg) })
	qdSch.SetRefreshObserver(func(uid string, ok bool, msg string) { appInst.NotifyRefresh("qoder", uid, ok, msg) })
	wbaSch.SetCheckinObserver(func(r scheduler.CheckinResult) { appInst.NotifyCheckin("workbuddyai", r) })
	wbaSch.SetRefreshObserver(func(uid string, ok bool, msg string) { appInst.NotifyRefresh("workbuddyai", uid, ok, msg) })
	qwSch.SetRefreshObserver(func(uid string, ok bool, msg string) { appInst.NotifyRefresh("qwenwork", uid, ok, msg) })
	glmSch.SetCheckinObserver(func(r scheduler.CheckinResult) { appInst.NotifyCheckin("glm", r) })
	glmSch.SetRefreshObserver(func(uid string, ok bool, msg string) { appInst.NotifyRefresh("glm", uid, ok, msg) })
	// 小浣熊签到已接入（setting_info 触发每日积分落账，见 internal/raccoon/checkin.go）：
	// 结果必须写日志——面板的「签到成功/失败」标签与 stats 的签到收入都读日志。
	rcSch.SetCheckinObserver(func(r scheduler.CheckinResult) { appInst.NotifyCheckin("raccoon", r) })
	rcSch.SetRefreshObserver(func(uid string, ok bool, msg string) { appInst.NotifyRefresh("raccoon", uid, ok, msg) })
	oaSch.SetCheckinObserver(func(r scheduler.CheckinResult) { appInst.NotifyCheckin("officeace", r) })
	oaSch.SetRefreshObserver(func(uid string, ok bool, msg string) { appInst.NotifyRefresh("officeace", uid, ok, msg) })

	// 查看器 HTTP 入口：静态面板 + 管理 API。不构造、也不挂载任何转发 Handler。
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		fatal("embed web: %v", err)
	}
	mux := http.NewServeMux()
	// /v1/* 与旧网关状态一律 404，且必须注册在 "/" 之前（ServeMux 取最长前缀，
	// 否则会被静态站点兜底成 HTML 200）。
	mux.HandleFunc("/v1/", notFound)
	mux.HandleFunc("/status", notFound)
	mux.Handle("/", http.FileServer(http.FS(sub)))
	appInst.HandleAPI(mux)
	appInst.SetRootHandler(mux)
	log.Printf("wild-view revision=%s 数据目录=%s", buildRevision(), wd)

	if err := appInst.StartServer(); err != nil {
		// 监听失败必须终止：只记日志继续跑会让调度器与托盘照常起来，
		// 表现为「面板看着一切正常、其实是别的实例在服务本端口」，
		// 而两个进程共享同一数据目录，会重复签到并互相覆盖刷新出的 token。
		fatal("监听 %s 失败：%v\n\n该端口可能已被占用（例如已有一个 wild-view 实例在运行）。\n"+
			"请先退出已有实例，或改用 config.json 的 listen.port（也可用环境变量 WILDWORK_LISTEN）。",
			cfg.Listen.Addr(), err)
	}
	// 地址必须落日志：启动弹窗可能被忽略或关掉，日志里始终留一份。
	log.Printf("已启动：账号额度与模型倍率查看器（Web UI http://%s:%d/）",
		displayHost(cfg), cfg.Listen.Port)

	// 调度器后台运行
	sctx, stop := context.WithCancel(context.Background())
	defer stop()
	go wbSch.Run(sctx)
	go trSch.Run(sctx)
	go qdSch.Run(sctx)
	go wbaSch.Run(sctx)
	go qwSch.Run(sctx)
	go qcnSch.Run(sctx)
	go qcmSch.Run(sctx)
	go rcSch.Run(sctx)
	go lmSch.Run(sctx)
	go mcSch.Run(sctx)
	// ⚠️ 新增渠道时必须在这里补 Run()。
	// 漏了**不会报错**——只表现为「该渠道的自动签到/保活从不运行」，
	// 而手工触发（面板按钮 / RunCheckinNow）仍然可用，极难发现。
	go glmSch.Run(sctx)
	// OfficeAce 的 Run 不可漏（R34）：本渠道有签到与保活。
	go oaSch.Run(sctx)
	// kilo 的 Run 同样不可漏（R34）：本渠道当前无任何定时任务，Run 只会阻塞等
	// ctx/配置变更；但漏了它，将来给它加任务就会静默不跑。
	go kiSch.Run(sctx)
	// MiniMax 的 Run 不可漏（R34）：本渠道有签到与保活两条定时任务。
	go mimSch.Run(sctx)
	// LobsterAI 的 Run 不可漏（R34）：本渠道有签到与保活两条定时任务。
	go lobSch.Run(sctx)
	// ZCode 的 Run 同样不可漏（R34）：本渠道当前无定时任务，但漏了将来加任务即静默失效。
	go zcdSch.Run(sctx)
	// Gemini 的 Run 不可漏（R34）：本渠道有 token 保活任务。
	go gemSch.Run(sctx)
	// CodeArts 的 Run 不可漏（R34）：本渠道有签到与保活两条定时任务。
	go caSch.Run(sctx)

	// 积分自动刷新覆盖全部渠道：
	// - workbuddyai / qoder 无签到活动，不自动刷就会一直显示旧值或 0；
	// - traework / workbuddy(CN) 虽有签到顺带拉余额，但一天只有两次，
	//   其间 token 若在别处被轮换（401）也无法自愈；统一纳入循环才能
	//   启动即出真实拆分数字，并靠 401 自愈（refreshIfSessionDead）及时恢复。
	// glm 纳入：其凭据本体是 refresh_token（accessToken 可为空），
	// 积分靠 member_info 拉取，不刷就永远是 0。
	appInst.StartCreditAutoRefresh(sctx, []provider.Kind{
		// 旧 Qoder 渠道已下线：不自动刷积分/token（避免周期性 token refresh failed 噪音）
		provider.WorkBuddy, provider.WorkBuddyAI, provider.TraeWork, provider.QoderCN, provider.QoderCOM, provider.QwenWork, provider.GLM,
		provider.Raccoon, provider.Loomy, provider.MonkeyCode,
		// MiniMax 有 credit/details 额度端点：不纳入就永远显示 0（面板看不到真实余额）。
		provider.Minimax,
		// LobsterAI 有 profile-summary 额度端点，同理必须纳入。
		provider.LobsterAI,
		// ZCode 的额度是 **token 口径**（credits_unit="token"）：纳入刷新是为了让面板能显示
		// Token 用量（用户 2026-10-17 的明确要求）；它的数字不进积分小计与积分流水
		// —— 由 app.creditsUnit 分流保证（积分流水跳过 token 渠道，条目一律 InfoOnly）。
		provider.ZCode,
		// Gemini **不**纳入：它的 UserResource 恒 0——真实信息是「配额窗口剩余百分比」，
		// 已按 InfoOnly 呈现在明细里、不参与积分算术，纳入只会刷出一个无意义的 0。
		// CodeArts 有 benefit 额度（按 UTC+8 自然日重置）与积分账户形态，必须纳入。
		provider.CodeArts,
	}, app.CreditRefreshInterval)

	// 启动即刷新「模型列表 + 费率」，之后每 30 分钟。
	// 否则刚启动时费率缓存为空，面板下方全是 unknown（需手动点刷新才正常）。
	appInst.StartPricingAutoRefresh(sctx, app.PricingRefreshInterval)

	if noTray {
		// 无头模式：打印信息，阻塞等待信号
		addr := cfg.Listen.Addr()
		fmt.Printf("wild-view headless mode started\n")
		fmt.Printf("  Web UI (本机): http://%s:%d/\n", openHost(cfg), cfg.Listen.Port)
		fmt.Printf("  Listen:        %s\n", addr)
		if appInst.PanelAuthEnabled() {
			fmt.Printf("  Panel auth:    admin_password enabled (cookie session, 7d)\n")
		}
		fmt.Printf("  Accounts:      workbuddy=%d traework=%d qoder=%d kilo=%d(匿名) minimax=%d lobsterai=%d zcode=%d gemini=%d codearts=%d\n", len(wbAuths), len(trAuths), len(qdAuths), len(kiAuths), len(mimAuths), len(lobAuths), len(zcdAuths), len(gemAuths), len(caAuths))
		fmt.Printf("\nPress Ctrl+C to exit\n")
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		fmt.Printf("\nShutting down...\n")
		stop()
		appInst.Stop()
		return
	}

	// 系统托盘（阻塞）。无桌面环境（如 SSH/服务会话）下托盘初始化失败时
	// 捕获 panic，提示用户使用 --no-tray 参数启动。
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "Tray init failed: %v\n", r)
				fmt.Fprintf(os.Stderr, "Use --no-tray to run in headless mode\n")
				stop()
				appInst.Stop()
				os.Exit(1)
			}
		}()
		// 托盘提示带上监听地址：启动弹窗可能被忽略或关掉，悬停也能看到地址。
		trayTip := fmt.Sprintf("wild-view — 账号额度与模型倍率查看器\nhttp://%s:%d", displayHost(cfg), cfg.Listen.Port)
		systray.Run(trayIconICO, trayTip, systray.Actions{
			OpenUI: func() {
				// 本机打开面板：通配监听时浏览器访问不了 0.0.0.0，用局域网 IP 兜底
				_ = platform.OpenURL(fmt.Sprintf("http://%s:%d/", openHost(cfg), cfg.Listen.Port))
			},
			OpenLog: func() {
				if err := appInst.OpenLogFile(); err != nil {
					log.Printf("open log: %v", err)
				}
			},
			Quit: func() {
				if platform.AskYesNo("wild-view", "确定退出 wild-view 吗？") {
					// 先摘托盘图标：NIM_DELETE 由托盘消息循环执行，返回即已回收。
					// 直接 os.Exit 会跳过这一步，Windows 任务栏留下幽灵图标。
					systray.Quit(3 * time.Second)
				}
			},
			// 启动提示（非 --autostart）：托盘就绪后再弹。
			//
			// 必须晚于托盘创建，且必须跑在独立 goroutine 里（Ready 即如此）：
			// 这是个**模态**框（Windows 上 platform.Notify 就是 MessageBox），
			// 放在托盘之前会把托盘创建整个卡住，双击 exe 表现为「点了没反应」。
			Ready: func() {
				if autostart {
					return
				}
				platform.Notify("wild-view 已启动",
					fmt.Sprintf("OpenAI 兼容 API 地址：http://%s:%d\n点击托盘图标或菜单打开主界面。",
						displayHost(cfg), cfg.Listen.Port))
			},
		})
	}()

	// Run 返回 = 托盘图标已回收，停机清理后进程自然退出。
	stop()
	appInst.Stop()
}

// workDir 解析数据目录（config.json / auths/ / data/ 的落点）。
//
//   - WILDWORK_HOME 显式指定时优先（测试/自定义布局用）；
//   - 运行在 macOS .app bundle 内（.../Xxx.app/Contents/MacOS）时改用
//     ~/Library/Application Support/WildWork：bundle 内容对普通用户只读，
//     且写到包内会在替换/升级 app 时连同账号一起丢失；
//   - 其余形态（CLI 二进制、Windows 便携包、Linux）沿用 exe 所在目录。
func workDir() string {
	// WILDVIEW_HOME 优先；保留 WILDWORK_HOME 仅用于从旧版本平滑迁移数据目录。
	for _, env := range []string{"WILDVIEW_HOME", "WILDWORK_HOME"} {
		if v := os.Getenv(env); v != "" {
			if err := os.MkdirAll(v, 0o755); err == nil {
				return v
			}
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	dir := filepath.Dir(exe)
	if strings.Contains(dir, ".app/Contents/MacOS") {
		if home, err := os.UserHomeDir(); err == nil {
			p := filepath.Join(home, "Library", "Application Support", "WildView")
			if err := os.MkdirAll(p, 0o755); err == nil {
				return p
			}
		}
	}
	// 默认落在可执行文件旁的独立子目录：与 wild-work 的 config.json / auths / data
	// 不重名，两个程序可并存且不会互相覆盖凭据。
	p := filepath.Join(dir, "wild-view-data")
	if err := os.MkdirAll(p, 0o755); err == nil {
		return p
	}
	return dir
}

// buildRevision 构建时嵌入的 VCS 提交号（未启用 VCS stamping 的构建返回 unknown）。
// 落日志的用途：现场排障时一眼看出跑的是哪次构建，避免拿旧二进制对已修的 bug 复现。
func buildRevision() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			if len(s.Value) > 12 {
				return s.Value[:12]
			}
			return s.Value
		}
	}
	return "unknown"
}

// migrateStateFiles 旧版 state.json 兼容：旧版 WorkBuddy 状态文件名为 state.json，
// 新版改为 state-workbuddy.json。若旧文件存在且新文件不存在，则重命名。
func migrateStateFiles(oldStateFile, stateDir string) {
	newFp := filepath.Join(stateDir, "state-workbuddy.json")
	if _, err := os.Stat(newFp); err == nil {
		return // 新文件已存在
	}
	if _, err := os.Stat(oldStateFile); err != nil {
		return // 旧文件不存在
	}
	raw, err := os.ReadFile(oldStateFile)
	if err != nil {
		return
	}
	var sf struct {
		Accounts map[string]json.RawMessage `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &sf); err != nil {
		log.Printf("state.json 解析失败，跳过迁移: %v", err)
		return
	}
	if err := os.WriteFile(newFp, raw, 0o600); err != nil {
		log.Printf("迁移 state.json 失败: %v", err)
		return
	}
	_ = os.Rename(oldStateFile, oldStateFile+".old")
	log.Printf("已迁移 state.json → state-workbuddy.json")
}

// displayHost 展示用途的主机名。
// 通配监听（0.0.0.0/::/空）是用户主动配置的意图，如实显示 0.0.0.0；
// localhost 归一为 127.0.0.1。不做任何「善意」替换——面板顶栏同理（见 app.js renderTopbar）。
// 注意：浏览器/托盘打开面板时用此值，0.0.0.0 在浏览器不可直接访问，
// 故 OpenUI 场景改用 lanIPOrLocalhost() 兜底（见 main.go 调用处）。
func displayHost(cfg *config.Config) string {
	if cfg.Listen.Host == "" || cfg.Listen.Host == "::" {
		return "0.0.0.0"
	}
	if cfg.Listen.Host == "localhost" {
		return "127.0.0.1"
	}
	return cfg.Listen.Host
}

// lanIPOrLocalhost 返回局域网出口 IP，取不到时兜底 127.0.0.1。
// 用于「本机打开面板」场景：监听 0.0.0.0 时浏览器不能访问 0.0.0.0，
// 用局域网 IP（或 127.0.0.1）代替，用户可从本机或局域网其他机器访问。
func lanIPOrLocalhost() string {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil || addr.IP.IsLoopback() {
		return "127.0.0.1"
	}
	return addr.IP.String()
}

// openHost 返回适合「本机浏览器打开」的主机：通配监听时用局域网 IP 兜底。
func openHost(cfg *config.Config) string {
	switch cfg.Listen.Host {
	case "", "0.0.0.0", "::":
		return lanIPOrLocalhost()
	case "localhost":
		return "127.0.0.1"
	}
	return cfg.Listen.Host
}

// notFound 统一返回 404 JSON：已下线的转发入口不得回落为静态页面 200。
func notFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"message":"not found: this build is a read-only viewer"}}`))
}

// fatal 记录日志并弹出系统提示后退出。
func fatal(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("%s", msg)
	platform.InfoBox("wild-view", msg)
	os.Exit(1)
}

// migrateStateFiles 旧版 state.json 兼容：旧版 WorkBuddy 状态文件名为 state.json，
// 新版改为 state-workbuddy.json。若旧文件存在且新文件不存在，则重命名。
// TraeWork 状态文件始终是 state-traework.json，无需迁移。
