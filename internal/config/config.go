// Package config 加载 JSON 配置 + 环境变量覆盖，并支持原子写回（GUI 面板修改）。
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Listen 监听地址：Host 为空表示全部接口。
type Listen struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// IsLoopback 是否是「仅本机可访问」的监听地址：环回 IP / localhost / 空主机名
// （空主机名在 Go 里等同 :port，即全部网卡，但旧配置里 ":7863" 曾被当作本机语义，
// 这里按「显式写环回才算安全」处理——空主机名一律视为对外暴露）。
func (l Listen) IsLoopback() bool {
	h := strings.TrimSpace(strings.ToLower(l.Host))
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Addr 返回 net.Listen 使用的地址串，如 "127.0.0.1:7863" / ":7863"。
func (l Listen) Addr() string {
	host := l.Host
	port := l.Port
	if port <= 0 {
		port = 7864
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// SameAddr 判断两个 net.Listen 地址串是否指向同一个监听地址。
// 空 host、0.0.0.0、:: 在 net.Listen 语义下都是「全部接口」，但字符串形式不同：
// 旧版配置写 ":7863"（或 WILDWORK_LISTEN=":7777"）时 Host 为空，面板会把监听主机
// 回显成 0.0.0.0，保存时回传 "0.0.0.0:7863"——字符串不等会被误判成「改了监听地址」，
// 于是在同一端口上重新 net.Listen，Windows 下撞自身报「端口已被占用」。
func SameAddr(a, b string) bool {
	ah, ap, errA := net.SplitHostPort(a)
	bh, bp, errB := net.SplitHostPort(b)
	if errA != nil || errB != nil {
		return a == b // 解析失败（裸端口等）退化为字符串比较
	}
	return ap == bp && normalizeListenHost(ah) == normalizeListenHost(bh)
}

// normalizeListenHost 把「全部接口」的各种写法归一化，其余 host 原样返回。
func normalizeListenHost(host string) string {
	switch strings.TrimSpace(host) {
	case "", "0.0.0.0", "::", "[::]", "*":
		return ""
	}
	return host
}

// UnmarshalJSON 兼容旧版字符串形式（":7863" / "127.0.0.1:9999" / "9999"）。
func (l *Listen) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		l2, err := ParseListen(s)
		if err != nil {
			return err
		}
		*l = l2
		return nil
	}
	var o struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := json.Unmarshal(data, &o); err != nil {
		return err
	}
	l.Host, l.Port = o.Host, o.Port
	return nil
}

// ParseListen 解析 "host:port" / ":port" / "port" 三种形式。
func ParseListen(s string) (Listen, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Listen{}, fmt.Errorf("empty listen address")
	}
	host, portStr := s, ""
	if i := strings.LastIndex(s, ":"); i >= 0 {
		host, portStr = s[:i], s[i+1:]
	} else if n, err := strconv.Atoi(host); err == nil {
		// 无冒号且整体是数字 → 仅端口（兼容旧配置 "7863"）
		return Listen{Port: n}, nil
	}
	port := 0
	if portStr != "" {
		n, err := strconv.Atoi(portStr)
		if err != nil || n <= 0 || n > 65535 {
			return Listen{}, fmt.Errorf("bad listen port %q", portStr)
		}
		port = n
	}
	return Listen{Host: host, Port: port}, nil
}

// Config 顶层配置。
type Config struct {
	Listen    Listen `json:"listen"`
	AdminPass string `json:"admin_password"` // 空 = 管理面板不鉴权（仅允许监听环回地址时为空）
	AuthDir   string `json:"auth_dir"`       // ./auths
	StateFile string `json:"state_file"`     // ./data/state.json
	Region    string `json:"region"`         // 只收 "cn"

	Schedule struct {
		CheckinHours []int    `json:"checkin_hours,omitempty"` // 旧格式：[9,21]
		CheckinTimes []string `json:"checkin_times,omitempty"` // 新格式：["09:00","21:30"]
		// ExpiringThresholdHours 临期展示窗口（小时，本仓独有；上游 R112 已删该开关）。
		//
		// ⚠️ 两侧语义不同、互不冲突：上游 R112 让**选号**改按「最早到期时刻升序」，
		// 不再需要「N 天内算临期」；本仓保留它是给**面板**用的——「临期额度」这列
		// 按用户选的天数（1/2/3）统计，见 ExpiringThresholdDays / POST /api/config/expiring_days。
		// 选号完全不读本字段，故保留不构成对 R112 的偏离。
		ExpiringThresholdHours int `json:"expiring_threshold_hours,omitempty"`
	} `json:"schedule"`

	Upstream struct {
		TimeoutSeconds int `json:"timeout_seconds"` // 默认 120
	} `json:"upstream"`

	// Login 登录流程的可调项。
	Login struct {
		// LobsterAICallbackPort 固定 LobsterAI 的 OAuth 回调端口（0 = 每次随机）。
		//
		// 为什么需要它：有道 portal 的回调派发器**只认**
		// `http://127.0.0.1:<port>/auth/callback`（hostname 严格等于 127.0.0.1，
		// 否则丢弃该地址、改发 `lobsterai://` deep-link —— 见 docs/决议项.md R97）。
		// 所以「浏览器与网关不同机」（局域网/远程访问面板）时要自动完成回调，
		// 只能让浏览器侧也能连到那个环回端口：在那台机器上建端口转发
		// （`ssh -L 127.0.0.1:<port>:127.0.0.1:<port> <网关>`）。
		// 随机端口无法预建隧道，故允许写死一个端口。留空/0 保持随机（更省心，
		// 但异地浏览器只能用面板的「粘贴回调地址」兜底）。
		LobsterAICallbackPort int `json:"lobsterai_callback_port,omitempty"`
	} `json:"login"`

	// Features 功能开关（面板「设置」可切换）。
	Features struct {
		// CheckinEnabled 纯签到总开关。查看器默认关闭；开启后仅在配置的签到时间
		// 执行不依赖对话的签到，任何聊天保活都不会运行。
		CheckinEnabled bool `json:"checkin_enabled"`
	} `json:"features"`

	// 解析后
	// ExpiringThresholdDur 临期阈值（时长形态，保留供既有调用方使用）。
	// 自 2026-10-19 起临期判定改用**自然日**口径（ExpiringThresholdDays），
	// 本字段仅作展示/兼容，不再参与判据。
	ExpiringThresholdDur time.Duration `json:"-"`
}

// ExpiringThresholdDays 临期展示窗口（自然日数，1/2/3）。
//
// 2026-10-19 起临期判定统一用**自然日**口径：days 个自然日内到期
// （次日 00:00 失效算今日，见 provider.ExpireWithinDays）。
// 由 ExpiringThresholdHours 换算；非整日时向上取整（保守，宁可提前预警）。
//
// ⚠️ 上游 R112 把选号改成「最早到期时刻升序」，这个天数**只影响面板「临期额度」列**；
// 本仓保留可配是因为面板有 1/2/3 下拉框（上游已删该开关、固定成 provider.ExpiringDays=1）。
func (c *Config) ExpiringThresholdDays() int {
	h := c.Schedule.ExpiringThresholdHours
	if h <= 0 {
		h = 24
	}
	days := h / 24
	if h%24 != 0 {
		days++
	}
	if days < 1 {
		days = 1
	}
	return days
}

// Default 默认配置。
func Default() *Config {
	c := &Config{
		Listen:    Listen{Host: "127.0.0.1", Port: 7864},
		AuthDir:   "./auths",
		StateFile: "./data/state.json",
		Region:    "cn",
	}
	c.Schedule.CheckinHours = []int{9, 21}
	c.Schedule.CheckinTimes = []string{"09:00", "21:00"}
	c.Schedule.ExpiringThresholdHours = 24
	c.Upstream.TimeoutSeconds = 120
	return c
}

// Load 从文件读，再用 WILDWORK_* env 覆盖。
func Load(path string) (*Config, error) {
	c := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := json.Unmarshal(raw, c); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
		// Default() 带有新字段默认值；老配置没有 checkin_times 时必须让旧的
		// checkin_hours 生效，而不能被 Default 的 [09:00,21:00] 覆盖。
		var shape struct {
			Schedule map[string]json.RawMessage `json:"schedule"`
		}
		if json.Unmarshal(raw, &shape) == nil {
			if _, ok := shape.Schedule["checkin_times"]; !ok {
				c.Schedule.CheckinTimes = nil
			}
		}
	}
	applyEnv(c)
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return c, nil
}

// Save 以 0600 原子写回配置。
func Save(c *Config, path string) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ParseClockTimes 将 HH:MM 列表转换为当天分钟数（0..1439）。
func ParseClockTimes(values []string) ([]int, error) {
	seen := map[int]bool{}
	out := make([]int, 0, len(values))
	for _, v := range values {
		parts := strings.Split(strings.TrimSpace(v), ":")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid time %q", v)
		}
		h, errH := strconv.Atoi(parts[0])
		m, errM := strconv.Atoi(parts[1])
		if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return nil, fmt.Errorf("invalid time %q", v)
		}
		minute := h*60 + m
		if !seen[minute] {
			seen[minute] = true
			out = append(out, minute)
		}
	}
	sort.Ints(out)
	return out, nil
}

// parseClockHM 解析 "HH:MM" 为当天分钟偏移（0 ~ 1439）。
func parseClockHM(v string) (time.Duration, error) {
	mins, err := ParseClockTimes([]string{v})
	if err != nil || len(mins) != 1 {
		return 0, fmt.Errorf("invalid HH:MM %q", v)
	}
	return time.Duration(mins[0]) * time.Minute, nil
}

// FormatClockTimes 将当天分钟数格式化为排序后的 HH:MM 列表。
func FormatClockTimes(minutes []int) []string {
	out := make([]int, 0, len(minutes))
	seen := map[int]bool{}
	for _, m := range minutes {
		if m >= 0 && m < 24*60 && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Ints(out)
	formatted := make([]string, 0, len(out))
	for _, m := range out {
		formatted = append(formatted, fmt.Sprintf("%02d:%02d", m/60, m%60))
	}
	return formatted
}

func applyEnv(c *Config) {
	// 同时接受 WILDVIEW_*（新）与 WILDWORK_*（旧）前缀，便于从原程序平滑迁移。
	env := func(name string) string {
		if v := os.Getenv("WILDVIEW_" + name); v != "" {
			return v
		}
		return os.Getenv("WILDWORK_" + name)
	}
	if v := env("LISTEN"); v != "" {
		if l, err := ParseListen(v); err == nil {
			c.Listen = l
		}
	}
	if v := env("ADMIN_PASSWORD"); v != "" {
		c.AdminPass = v
	}
	if v := env("AUTH_DIR"); v != "" {
		c.AuthDir = v
	}
	if v := env("STATE_FILE"); v != "" {
		c.StateFile = v
	}
	if v := env("REGION"); v != "" {
		c.Region = v
	}
	if v := env("TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Upstream.TimeoutSeconds = n
		}
	}
}

func (c *Config) normalize() error {
	if c.Upstream.TimeoutSeconds <= 0 {
		c.Upstream.TimeoutSeconds = 120
	}
	// 临期阈值：默认 24h，下限 24h（日期粒度的到期判定低于一天没有意义）。
	if c.Schedule.ExpiringThresholdHours <= 0 {
		c.Schedule.ExpiringThresholdHours = 24
	}
	if c.Schedule.ExpiringThresholdHours < 24 {
		c.Schedule.ExpiringThresholdHours = 24
	}
	c.ExpiringThresholdDur = time.Duration(c.Schedule.ExpiringThresholdHours) * time.Hour
	if c.Listen.Port <= 0 {
		c.Listen.Port = 7864
	}
	c.AdminPass = strings.TrimSpace(c.AdminPass)
	if c.Region == "" {
		c.Region = "cn"
	}
	c.Region = strings.ToLower(c.Region)
	if c.Region != "cn" && c.Region != "global" {
		return fmt.Errorf("region must be cn or global, got %q", c.Region)
	}
	// 兼容旧版 checkin_hours；新版本统一规范化为 HH:MM。
	if len(c.Schedule.CheckinTimes) == 0 {
		c.Schedule.CheckinTimes = make([]string, 0, len(c.Schedule.CheckinHours))
		for _, h := range c.Schedule.CheckinHours {
			if h < 0 || h > 23 {
				return fmt.Errorf("schedule.checkin_hours: hour out of range: %d", h)
			}
			c.Schedule.CheckinTimes = append(c.Schedule.CheckinTimes, fmt.Sprintf("%02d:00", h))
		}
	}
	mins, err := ParseClockTimes(c.Schedule.CheckinTimes)
	if err != nil {
		return fmt.Errorf("schedule.checkin_times: %w", err)
	}
	c.Schedule.CheckinTimes = FormatClockTimes(mins)
	if len(c.Schedule.CheckinHours) == 0 {
		for _, m := range mins {
			if m%60 == 0 {
				c.Schedule.CheckinHours = append(c.Schedule.CheckinHours, m/60)
			}
		}
	}
	return nil
}
