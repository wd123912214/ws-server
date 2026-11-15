package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Ban 一条黑名单。Until=0 表示永久。
type Ban struct {
	Key    string `json:"key"`
	Kind   string `json:"kind"` // ip | uid
	Value  string `json:"value"`
	Reason string `json:"reason"`
	At     int64  `json:"at"`
	Until  int64  `json:"until"`
}

// Violation 一条违规记录，只保存在内存里供面板查看。
type Violation struct {
	At     int64  `json:"at"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	Banned bool   `json:"banned"`
}

const violationLogSize = 200

// Guard 负责违规计数和黑名单。
type Guard struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	banFor  time.Duration
	path    string
	bans    map[string]Ban
	hits    map[string][]time.Time
	log     []Violation
	onBan   func(kind, value string)
	nowFunc func() time.Time
}

func newGuard(cfg Config) (*Guard, error) {
	g := &Guard{
		limit:   cfg.ViolationLimit,
		window:  time.Duration(cfg.ViolationWindow) * time.Minute,
		banFor:  time.Duration(cfg.BanMinutes) * time.Minute,
		path:    filepath.Join(cfg.DataDir, "bans.json"),
		bans:    map[string]Ban{},
		hits:    map[string][]time.Time{},
		nowFunc: time.Now,
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(g.path)
	if err == nil {
		var list []Ban
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		for _, b := range list {
			g.bans[b.Key] = b
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return g, nil
}

func banKey(kind, value string) string { return kind + ":" + value }

// Banned 判断是否在黑名单（顺便清理已过期的）。
func (g *Guard) Banned(kind, value string) bool {
	if value == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	k := banKey(kind, value)
	b, ok := g.bans[k]
	if !ok {
		return false
	}
	if b.Until > 0 && g.nowFunc().Unix() >= b.Until {
		delete(g.bans, k)
		g.saveLocked()
		return false
	}
	return true
}

// Violate 记一次违规，达到上限自动拉黑，返回是否已被拉黑。
// 回环地址（127.0.0.1 等）只记录不拉黑，防止反代配置错误时把所有人一起拉黑。
func (g *Guard) Violate(kind, value, reason string) bool {
	if value == "" {
		return false
	}
	now := g.nowFunc()
	k := banKey(kind, value)

	g.mu.Lock()
	if _, ok := g.bans[k]; ok {
		g.mu.Unlock()
		return true
	}
	list := g.hits[k][:0]
	for _, t := range g.hits[k] {
		if now.Sub(t) < g.window {
			list = append(list, t)
		}
	}
	list = append(list, now)
	g.hits[k] = list
	count := len(list)
	banned := count >= g.limit && !(kind == "ip" && isLoopback(value))
	if banned {
		delete(g.hits, k)
		g.addBanLocked(kind, value, "自动: "+reason, g.banFor)
	}
	g.log = append(g.log, Violation{At: now.Unix(), Kind: kind, Value: value, Reason: reason, Count: count, Banned: banned})
	if len(g.log) > violationLogSize {
		g.log = g.log[len(g.log)-violationLogSize:]
	}
	onBan := g.onBan
	g.mu.Unlock()

	if banned && onBan != nil {
		onBan(kind, value)
	}
	return banned
}

// Ban 手动拉黑。minutes=0 永久。
func (g *Guard) Ban(kind, value, reason string, minutes int) {
	g.mu.Lock()
	g.addBanLocked(kind, value, reason, time.Duration(minutes)*time.Minute)
	onBan := g.onBan
	g.mu.Unlock()
	if onBan != nil {
		onBan(kind, value)
	}
}

func (g *Guard) addBanLocked(kind, value, reason string, d time.Duration) {
	now := g.nowFunc()
	b := Ban{Key: banKey(kind, value), Kind: kind, Value: value, Reason: reason, At: now.Unix()}
	if d > 0 {
		b.Until = now.Add(d).Unix()
	}
	g.bans[b.Key] = b
	g.saveLocked()
}

// Unban 解封，同时清掉该对象的违规计数。
func (g *Guard) Unban(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.bans[key]; !ok {
		return false
	}
	delete(g.bans, key)
	delete(g.hits, key)
	g.saveLocked()
	return true
}

func (g *Guard) Snapshot() ([]Ban, []Violation) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.nowFunc().Unix()
	bans := make([]Ban, 0, len(g.bans))
	for _, b := range g.bans {
		if b.Until == 0 || now < b.Until {
			bans = append(bans, b)
		}
	}
	sort.Slice(bans, func(i, j int) bool { return bans[i].At > bans[j].At })
	logs := make([]Violation, len(g.log))
	for i, v := range g.log {
		logs[len(g.log)-1-i] = v // 新的在前
	}
	return bans, logs
}

// saveLocked 原子写 bans.json。
func (g *Guard) saveLocked() {
	list := make([]Ban, 0, len(g.bans))
	for _, b := range g.bans {
		list = append(list, b)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	raw, _ := json.MarshalIndent(list, "", "  ")
	tmp := g.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		logger.Error("保存黑名单失败", "err", err)
		return
	}
	if err := os.Rename(tmp, g.path); err != nil {
		logger.Error("保存黑名单失败", "err", err)
	}
}

func isLoopback(ip string) bool {
	p := net.ParseIP(ip)
	return p != nil && p.IsLoopback()
}
