package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// balance.go 余额采样：每次额度查询成功后记录一次真实余额，用于画余额变化曲线。
//
// 与 credit-*.jsonl 的区别：积分流水是「条目差分」（earn/spend/expire），
// 余额采样是「查询时刻的绝对值」。曲线只反映余额本身，不推断消耗原因——
// 套餐到期、赠送、人工校正都会让曲线下降，不能一律标成对话消耗。

// BalancePoint 一次余额采样。
type BalancePoint struct {
	Ts      int64  `json:"ts"`
	Channel string `json:"ch"`
	UID     string `json:"uid"`
	Credits int64  `json:"credits"`
}

// BalanceSeries 单个账号的余额时间序列。
type BalanceSeries struct {
	Channel  string         `json:"channel"`
	UID      string         `json:"uid"`
	Nickname string         `json:"nickname,omitempty"`
	Points   []BalancePoint `json:"points"`
}

// balanceMinGap 同一账号两次采样的最小间隔：避免面板频繁刷新把曲线写成锯齿。
const balanceMinGap = 10 * time.Minute

// balanceLast 内存里记录每个账号的最近一次采样，用于去抖（进程重启后从文件恢复）。
type balanceLast struct {
	ts      int64
	credits int64
}

var (
	balanceMu   sync.Mutex
	balanceSeen = map[string]balanceLast{}
)

// RecordBalance 记录一次余额采样。
// 同一账号 10 分钟内且余额未变化时跳过；余额变化则立即记录（变化点最有价值）。
func (l *Ledger) RecordBalance(channel, uid string, credits int64) {
	if l == nil || uid == "" {
		return
	}
	now := time.Now()
	key := channel + "/" + uid

	balanceMu.Lock()
	last, ok := balanceSeen[key]
	if ok && last.credits == credits && now.Unix()-last.ts < int64(balanceMinGap/time.Second) {
		balanceMu.Unlock()
		return
	}
	balanceSeen[key] = balanceLast{ts: now.Unix(), credits: credits}
	balanceMu.Unlock()

	line, err := json.Marshal(BalancePoint{Ts: now.Unix(), Channel: channel, UID: uid, Credits: credits})
	if err != nil {
		return
	}
	fp := filepath.Join(l.dir, fmt.Sprintf("balance-%s.jsonl", now.Format("200601")))
	f, err := os.OpenFile(fp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// BalanceSeries 读取最近 days 天的余额采样，按账号聚合（点按时间升序）。
// 昵称由调用方补全：ledger 只存 uid，避免账号改名后历史数据被写死。
func (l *Ledger) BalanceSeries(days int) []BalanceSeries {
	if l == nil {
		return nil
	}
	if days <= 0 {
		days = 7
	}
	cutoff := time.Now().AddDate(0, 0, -days).Unix()

	// 覆盖窗口的月分段（当前月 + 上一个月，days ≤ 31 足够）。
	// 上月标签走 monthTagShift：月末直接 AddDate 会溢出回本月，同一文件被读两遍、采样点翻倍。
	now := time.Now()
	files := []string{
		filepath.Join(l.dir, fmt.Sprintf("balance-%s.jsonl", now.Format("200601"))),
		filepath.Join(l.dir, fmt.Sprintf("balance-%s.jsonl", monthTagShift(now, 1))),
	}

	byAcct := map[string]*BalanceSeries{}
	for _, fp := range files {
		raw, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		for _, line := range balanceLines(raw) {
			var p BalancePoint
			if json.Unmarshal(line, &p) != nil || p.Ts < cutoff || p.UID == "" {
				continue
			}
			key := p.Channel + "/" + p.UID
			s := byAcct[key]
			if s == nil {
				s = &BalanceSeries{Channel: p.Channel, UID: p.UID}
				byAcct[key] = s
			}
			s.Points = append(s.Points, p)
		}
	}

	out := make([]BalanceSeries, 0, len(byAcct))
	for _, s := range byAcct {
		sort.Slice(s.Points, func(i, j int) bool { return s.Points[i].Ts < s.Points[j].Ts })
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Channel != out[j].Channel {
			return out[i].Channel < out[j].Channel
		}
		return out[i].UID < out[j].UID
	})
	return out
}

// balanceLines 按行切分，跳过空行。
func balanceLines(raw []byte) [][]byte {
	out := make([][]byte, 0, 64)
	start := 0
	for i, b := range raw {
		if b != '\n' {
			continue
		}
		if i > start {
			out = append(out, raw[start:i])
		}
		start = i + 1
	}
	if start < len(raw) {
		out = append(out, raw[start:])
	}
	return out
}
