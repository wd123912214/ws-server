package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Config struct {
	ListenAddr        string
	Token             string
	SuperAdminToken   string
	UIDWhitelist      []string
	RequestTimeout    time.Duration
	HeartbeatInterval time.Duration
	MaxMessageBytes   int64
	LogDir            string
	Debug             bool
}

type fileConfig struct {
	ListenAddr                string   `json:"listen_addr"`
	Token                     string   `json:"token"`
	SuperAdminToken           string   `json:"super_admin_token"`
	UIDWhitelist              []string `json:"uid_whitelist"`
	RequestTimeoutMS          int      `json:"request_timeout_ms"`
	HeartbeatIntervalSeconds  *int     `json:"heartbeat_interval_seconds"`
	LegacyPingIntervalSeconds *int     `json:"ping_interval_seconds"`
	MaxMessageBytes           int64    `json:"max_message_bytes"`
	LogDir                    string   `json:"log_dir"`
	Debug                     bool     `json:"debug"`
}

func Load(path string) (Config, error) {
	cfg := Config{
		ListenAddr:        ":8080",
		RequestTimeout:    10 * time.Second,
		HeartbeatInterval: 30 * time.Second,
		MaxMessageBytes:   64 << 10,
		LogDir:            "logs",
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		var file fileConfig
		if err := json.Unmarshal(raw, &file); err != nil {
			return Config{}, fmt.Errorf("decode config: %w", err)
		}
		if file.ListenAddr != "" {
			cfg.ListenAddr = file.ListenAddr
		}
		cfg.Token = file.Token
		cfg.SuperAdminToken = file.SuperAdminToken
		cfg.UIDWhitelist = append([]string(nil), file.UIDWhitelist...)
		cfg.Debug = file.Debug
		if file.RequestTimeoutMS > 0 {
			cfg.RequestTimeout = time.Duration(file.RequestTimeoutMS) * time.Millisecond
		}
		if file.HeartbeatIntervalSeconds != nil {
			cfg.HeartbeatInterval = time.Duration(*file.HeartbeatIntervalSeconds) * time.Second
		} else if file.LegacyPingIntervalSeconds != nil {
			cfg.HeartbeatInterval = time.Duration(*file.LegacyPingIntervalSeconds) * time.Second
		}
		if file.MaxMessageBytes > 0 {
			cfg.MaxMessageBytes = file.MaxMessageBytes
		}
		if file.LogDir != "" {
			cfg.LogDir = file.LogDir
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	override(&cfg.ListenAddr, "WS_LISTEN_ADDR")
	override(&cfg.Token, "WS_TOKEN")
	override(&cfg.SuperAdminToken, "WS_SUPER_ADMIN_TOKEN")
	override(&cfg.LogDir, "WS_LOG_DIR")
	return cfg, nil
}

func (c Config) Validate() error {
	if c.ListenAddr == "" {
		return errors.New("listen_addr is required")
	}
	if len(c.Token) < 16 || c.Token == "CHANGE_ME_AT_LEAST_16_CHARS" {
		return errors.New("token must contain at least 16 characters and must be changed")
	}
	if len(c.SuperAdminToken) < 16 || c.SuperAdminToken == "CHANGE_ME_SUPER_TOKEN" || c.SuperAdminToken == c.Token {
		return errors.New("super_admin_token must contain at least 16 characters and must be changed")
	}
	if c.RequestTimeout <= 0 {
		return errors.New("request_timeout_ms must be positive")
	}
	if c.HeartbeatInterval <= 0 {
		return errors.New("heartbeat_interval_seconds must be positive")
	}
	if c.MaxMessageBytes <= 0 || c.MaxMessageBytes > 64<<10 {
		return errors.New("max_message_bytes must be between 1 and 65536")
	}
	return nil
}

func UpdateWhitelist(path string, uids []string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config for whitelist update: %w", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode config for whitelist update: %w", err)
	}
	encodedUIDs, err := json.Marshal(uids)
	if err != nil {
		return err
	}
	document["uid_whitelist"] = encodedUIDs
	updated, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	updated = append(updated, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(updated); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func override(target *string, name string) {
	if value := os.Getenv(name); value != "" {
		*target = value
	}
}
