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
	if cfg.Hotkeys.SwitchRight != "ctrl+alt+right" {
		t.Fatalf("default hotkey: %q", cfg.Hotkeys.SwitchRight)
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
