package config_test

import (
	"path/filepath"
	"testing"

	"github.com/mousebridge/core/internal/config"
)

func TestDefaultConfig(t *testing.T) {
	cfg := config.Default()
	if cfg.Port != 39172 {
		t.Fatalf("default port: want 39172 got %d", cfg.Port)
	}
	if cfg.ListenHost != "127.0.0.1" {
		t.Fatalf("default listen_host: want 127.0.0.1 got %q", cfg.ListenHost)
	}
	if cfg.PairingPINTTLSeconds != 120 {
		t.Fatalf("default pin ttl: want 120 got %d", cfg.PairingPINTTLSeconds)
	}
}

func TestValidateLoopback(t *testing.T) {
	cfg := config.Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("loopback default should be valid: %v", err)
	}
}

func TestValidateLANWithoutUnsafe(t *testing.T) {
	cfg := config.Default()
	cfg.ListenHost = "0.0.0.0"
	cfg.UnsafeHTTPLAN = false
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when LAN host without unsafe flag")
	}
}

func TestValidateLANWithUnsafe(t *testing.T) {
	cfg := config.Default()
	cfg.ListenHost = "0.0.0.0"
	cfg.UnsafeHTTPLAN = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("LAN with unsafe flag should be valid: %v", err)
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := config.Default()
	cfg.Port = 12345
	cfg.DeviceName = "TestMac"

	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Port != 12345 {
		t.Fatalf("port: want 12345 got %d", loaded.Port)
	}
	if loaded.DeviceName != "TestMac" {
		t.Fatalf("name: want TestMac got %q", loaded.DeviceName)
	}
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	cfg, err := config.Load("/nonexistent/path/config.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 39172 {
		t.Fatalf("port: want 39172 got %d", cfg.Port)
	}
}
