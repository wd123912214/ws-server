package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Config 对应 config.json。首次运行自动生成。
type Config struct {
	ListenAddr      string `json:"listen_addr"`       // 监听地址，宝塔反代时用 127.0.0.1:8080
	Token           string `json:"token"`             // 共享Token，留空表示不校验
	PanelPassword   string `json:"panel_password"`    // 面板口令，首次运行随机生成
	TrustProxy      bool   `json:"trust_proxy"`       // 从 X-Real-IP / X-Forwarded-For 取真实IP（必须在 nginx 后面才开）
	TaskTimeout     int    `json:"task_timeout"`      // HTTP 等待回复默认秒数
	MaxTaskTimeout  int    `json:"max_task_timeout"`  // 调用方可指定的最大秒数
	Heartbeat       int    `json:"heartbeat"`         // 服务器发 ping 的间隔秒数
	MaxMessageBytes int64  `json:"max_message_bytes"` // 单条消息上限
	ViolationLimit  int    `json:"violation_limit"`   // 窗口内违规几次拉黑
	ViolationWindow int    `json:"violation_window"`  // 违规计数窗口（分钟）
	BanMinutes      int    `json:"ban_minutes"`       // 自动拉黑时长（分钟），0=永久
	DataDir         string `json:"data_dir"`          // 黑名单等数据目录
}

func defaultConfig() Config {
	return Config{
		ListenAddr:      "127.0.0.1:8080",
		PanelPassword:   randHex(8),
		TrustProxy:      true,
		TaskTimeout:     30,
		MaxTaskTimeout:  120,
		Heartbeat:       30,
		MaxMessageBytes: 64 << 10,
		ViolationLimit:  3,
		ViolationWindow: 10,
		BanMinutes:      0,
		DataDir:         "data",
	}
}

// loadConfig 读取配置；文件不存在时生成默认配置并返回 created=true。
func loadConfig(path string) (cfg Config, created bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg = defaultConfig()
		out, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.WriteFile(path, out, 0o600); err != nil {
			return cfg, false, err
		}
		cfg.DataDir = filepath.Join(filepath.Dir(path), cfg.DataDir)
		return cfg, true, nil
	}
	if err != nil {
		return cfg, false, err
	}
	cfg = defaultConfig()
	cfg.PanelPassword = ""
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, false, err
	}
	if cfg.PanelPassword == "" {
		return cfg, false, errors.New("panel_password 不能为空")
	}
	cfg.fill()
	if !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir = filepath.Join(filepath.Dir(path), cfg.DataDir)
	}
	return cfg, false, nil
}

func (c *Config) fill() {
	d := defaultConfig()
	if c.ListenAddr == "" {
		c.ListenAddr = d.ListenAddr
	}
	if c.TaskTimeout <= 0 {
		c.TaskTimeout = d.TaskTimeout
	}
	if c.MaxTaskTimeout < c.TaskTimeout {
		c.MaxTaskTimeout = c.TaskTimeout
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = d.Heartbeat
	}
	if c.MaxMessageBytes <= 0 {
		c.MaxMessageBytes = d.MaxMessageBytes
	}
	if c.ViolationLimit <= 0 {
		c.ViolationLimit = d.ViolationLimit
	}
	if c.ViolationWindow <= 0 {
		c.ViolationWindow = d.ViolationWindow
	}
	if c.DataDir == "" {
		c.DataDir = d.DataDir
	}
}

func (c Config) heartbeat() time.Duration { return time.Duration(c.Heartbeat) * time.Second }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
