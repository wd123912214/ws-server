package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestLoadDefaultsAndJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{
  "listen_addr": ":9090",
  "token": "1234567890abcdef",
  "super_admin_token": "fedcba0987654321",
  "uid_whitelist": ["client2", "client1"],
  "request_timeout_ms": 2500,
  "heartbeat_interval_seconds": 20,
  "max_message_bytes": 4096,
  "log_dir": "custom-logs",
  "debug": true
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":9090" || cfg.Token != "1234567890abcdef" || cfg.SuperAdminToken != "fedcba0987654321" || !slices.Equal(cfg.UIDWhitelist, []string{"client2", "client1"}) || cfg.RequestTimeout != 2500*time.Millisecond || cfg.HeartbeatInterval != 20*time.Second || cfg.MaxMessageBytes != 4096 || cfg.LogDir != "custom-logs" || !cfg.Debug {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestLoadMissingFileUsesSafeDefaultsAndEnvironment(t *testing.T) {
	t.Setenv("WS_LISTEN_ADDR", "127.0.0.1:7070")
	t.Setenv("WS_TOKEN", "abcdefghijklmnop")
	t.Setenv("WS_SUPER_ADMIN_TOKEN", "ponmlkjihgfedcba")
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:7070" || cfg.Token != "abcdefghijklmnop" || cfg.SuperAdminToken != "ponmlkjihgfedcba" {
		t.Fatalf("environment not applied: %#v", cfg)
	}
	if cfg.RequestTimeout != 10*time.Second || cfg.HeartbeatInterval != 30*time.Second || cfg.MaxMessageBytes != 64<<10 || cfg.LogDir != "logs" || cfg.Debug {
		t.Fatalf("defaults not applied: %#v", cfg)
	}
}

func TestValidateRejectsWeakConfiguration(t *testing.T) {
	tests := []Config{
		{},
		{ListenAddr: ":8080", Token: "short", SuperAdminToken: "fedcba0987654321", RequestTimeout: time.Second, HeartbeatInterval: time.Second, MaxMessageBytes: 10},
		{ListenAddr: ":8080", Token: "1234567890abcdef", SuperAdminToken: "short", RequestTimeout: time.Second, HeartbeatInterval: time.Second, MaxMessageBytes: 10},
		{ListenAddr: ":8080", Token: "1234567890abcdef", SuperAdminToken: "1234567890abcdef", RequestTimeout: time.Second, HeartbeatInterval: time.Second, MaxMessageBytes: 10},
		{ListenAddr: ":8080", Token: "1234567890abcdef", SuperAdminToken: "fedcba0987654321", RequestTimeout: 0, HeartbeatInterval: time.Second, MaxMessageBytes: 10},
		{ListenAddr: ":8080", Token: "1234567890abcdef", SuperAdminToken: "fedcba0987654321", RequestTimeout: time.Second, HeartbeatInterval: 0, MaxMessageBytes: 10},
		{ListenAddr: ":8080", Token: "1234567890abcdef", SuperAdminToken: "fedcba0987654321", RequestTimeout: time.Second, HeartbeatInterval: time.Second, MaxMessageBytes: 0},
	}
	for i, cfg := range tests {
		if err := cfg.Validate(); err == nil {
			t.Errorf("case %d unexpectedly valid", i)
		}
	}
	valid := Config{ListenAddr: ":8080", Token: "1234567890abcdef", SuperAdminToken: "fedcba0987654321", RequestTimeout: time.Second, HeartbeatInterval: time.Second, MaxMessageBytes: 1024}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestUpdateWhitelistPersistsWithoutLosingOtherConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := `{"listen_addr":":8080","token":"1234567890abcdef","super_admin_token":"fedcba0987654321","uid_whitelist":["old"],"request_timeout_ms":1000,"heartbeat_interval_seconds":30,"max_message_bytes":1024,"log_dir":"logs","debug":true}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateWhitelist(path, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.UIDWhitelist, []string{"a", "b"}) || cfg.Token != "1234567890abcdef" || cfg.ListenAddr != ":8080" || !cfg.Debug {
		t.Fatalf("persisted config = %#v", cfg)
	}
}

func TestLoadLegacyPingInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ping_interval_seconds":45}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HeartbeatInterval != 45*time.Second {
		t.Fatalf("legacy interval = %v", cfg.HeartbeatInterval)
	}
}
