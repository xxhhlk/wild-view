package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Listen.Addr() != "127.0.0.1:7864" {
		t.Errorf("listen=%s", c.Listen.Addr())
	}
	if err := c.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if c.Upstream.TimeoutSeconds != 120 {
		t.Errorf("timeout=%d", c.Upstream.TimeoutSeconds)
	}
}

func TestListenParsing(t *testing.T) {
	cases := []struct {
		in   string
		addr string
	}{
		{":9999", ":9999"},
		{"127.0.0.1:9999", "127.0.0.1:9999"},
		{"9999", ":9999"},
		{"0.0.0.0:7863", "0.0.0.0:7863"},
	}
	for _, c := range cases {
		l, err := ParseListen(c.in)
		if err != nil {
			t.Errorf("ParseListen(%q): %v", c.in, err)
			continue
		}
		if got := l.Addr(); got != c.addr {
			t.Errorf("ParseListen(%q).Addr()=%s want %s", c.in, got, c.addr)
		}
	}
	if _, err := ParseListen(":notaport"); err == nil {
		t.Error("want error for bad port")
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	os.WriteFile(fp, []byte(`{"listen":"127.0.0.1:9999","region":"cn"}`), 0o600)
	c, err := Load(fp)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen.Addr() != "127.0.0.1:9999" {
		t.Errorf("c=%+v", c)
	}
}

func TestLoadFileObjectListen(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	os.WriteFile(fp, []byte(`{"listen":{"host":"192.168.1.2","port":8000}}`), 0o600)
	c, err := Load(fp)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen.Addr() != "192.168.1.2:8000" {
		t.Errorf("addr=%s", c.Listen.Addr())
	}
}

func TestSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "sub", "c.json")
	c := Default()
	c.Listen.Host = "127.0.0.1"
	c.Schedule.CheckinHours = []int{8, 20}
	if err := Save(c, fp); err != nil {
		t.Fatal(err)
	}
	c2, err := Load(fp)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Listen.Addr() != "127.0.0.1:7864" {
		t.Errorf("addr=%s", c2.Listen.Addr())
	}
	if len(c2.Schedule.CheckinHours) != 2 || c2.Schedule.CheckinHours[0] != 8 {
		t.Errorf("hours=%v", c2.Schedule.CheckinHours)
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("WILDWORK_LISTEN", ":7777")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen.Addr() != ":7777" {
		t.Errorf("c=%+v", c)
	}
}

func TestClockTimes(t *testing.T) {
	got, err := ParseClockTimes([]string{"21:30", "09:05", "09:05"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 9*60+5 || got[1] != 21*60+30 {
		t.Fatalf("minutes=%v", got)
	}
	if _, err := ParseClockTimes([]string{"24:00"}); err == nil {
		t.Fatal("want invalid time error")
	}
	if got := FormatClockTimes(got); got[0] != "09:05" || got[1] != "21:30" {
		t.Fatalf("times=%v", got)
	}
}

// 空输入必须返回**非 nil** 空切片：scheduler.New 用 nil 表示「未配置」并补成默认
// 9:00/21:00，若这里退化成 nil，用户就无法通过在配置里写 "checkin_times": [] 关掉签到。
func TestClockTimesEmptyIsNonNil(t *testing.T) {
	for _, in := range [][]string{nil, {}} {
		got, err := ParseClockTimes(in)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatalf("ParseClockTimes(%v) 返回 nil，会让 scheduler.New 补成默认签到时段", in)
		}
		if len(got) != 0 {
			t.Fatalf("ParseClockTimes(%v) = %v，want 空", in, got)
		}
	}
}

func TestLoadLegacyCheckinHours(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	os.WriteFile(fp, []byte(`{"schedule":{"checkin_hours":[8,20]}}`), 0o600)
	c, err := Load(fp)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Schedule.CheckinTimes; len(got) != 2 || got[0] != "08:00" || got[1] != "20:00" {
		t.Fatalf("times=%v", got)
	}
}

func TestLoadMinuteCheckinTimes(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	os.WriteFile(fp, []byte(`{"schedule":{"checkin_times":["09:05","21:30"]}}`), 0o600)
	c, err := Load(fp)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Schedule.CheckinTimes; len(got) != 2 || got[0] != "09:05" || got[1] != "21:30" {
		t.Fatalf("times=%v", got)
	}
}

func TestAdminPasswordAndLoopback(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	// 旧配置（无 admin_password）必须能加载，且默认空 = 面板不鉴权
	os.WriteFile(fp, []byte(`{"listen":"127.0.0.1:9999"}`), 0o600)
	c, err := Load(fp)
	if err != nil {
		t.Fatal(err)
	}
	if c.AdminPass != "" {
		t.Errorf("缺省 admin_password 应为空，实际 %q", c.AdminPass)
	}
	// 新字段可读
	os.WriteFile(fp, []byte(`{"listen":{"host":"0.0.0.0","port":8000},"admin_password":"pw12345678"}`), 0o600)
	if c, err = Load(fp); err != nil {
		t.Fatal(err)
	} else if c.AdminPass != "pw12345678" {
		t.Errorf("admin_password=%q", c.AdminPass)
	}
	// env 覆盖
	t.Setenv("WILDWORK_ADMIN_PASSWORD", "env-pw")
	if c, err = Load(fp); err != nil || c.AdminPass != "env-pw" {
		t.Errorf("env override 失败：%v %q", err, c.AdminPass)
	}
	// IsLoopback
	for host, want := range map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true, "0.0.0.0": false, "": false, "192.168.1.9": false} {
		if got := (Listen{Host: host}).IsLoopback(); got != want {
			t.Errorf("IsLoopback(%q)=%v want %v", host, got, want)
		}
	}
}

