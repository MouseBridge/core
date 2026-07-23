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
	if cfg.Hotkeys.SwitchNext != "ctrl+alt+right" {
		t.Fatalf("default switch_next: want ctrl+alt+right got %q", cfg.Hotkeys.SwitchNext)
	}
	if cfg.Hotkeys.SwitchPrev != "ctrl+alt+left" {
		t.Fatalf("default switch_prev: want ctrl+alt+left got %q", cfg.Hotkeys.SwitchPrev)
	}
	if cfg.Hotkeys.SwitchToHost != "ctrl+alt+escape" {
		t.Fatalf("default switch_to_host: want ctrl+alt+escape got %q", cfg.Hotkeys.SwitchToHost)
	}
	if cfg.EdgeTargets.Left != "" || cfg.EdgeTargets.Right != "" || cfg.EdgeTargets.Top != "" || cfg.EdgeTargets.Bottom != "" {
		t.Fatalf("default edge targets should be empty, got %+v", cfg.EdgeTargets)
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

func TestValidateAllowsRememberedAutoConnect(t *testing.T) {
	cfg := config.Default()
	cfg.RememberedAutoConnectEnabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("remembered_auto_connect_enabled should be valid now: %v", err)
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := config.Default()
	cfg.Port = 12345
	cfg.DeviceName = "TestMac"
	cfg.Hotkeys.TogglePause = "ctrl+alt+p"
	cfg.EdgeTargets.Right = "remote-a"

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
	if loaded.Hotkeys.TogglePause != "ctrl+alt+p" {
		t.Fatalf("toggle_pause: want ctrl+alt+p got %q", loaded.Hotkeys.TogglePause)
	}
	if loaded.EdgeTargets.Right != "remote-a" {
		t.Fatalf("edge_targets.right: want remote-a got %q", loaded.EdgeTargets.Right)
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
