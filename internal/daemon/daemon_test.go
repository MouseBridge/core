package daemon_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/daemon"
)

func TestDaemonStartStop(t *testing.T) {
	d, err := daemon.New(daemon.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := d.Addr()
	if addr == "" {
		t.Fatal("expected non-empty listen addr after Start")
	}
	d.Stop()
	if d.Addr() != "" {
		t.Fatal("expected empty addr after Stop")
	}
}

func TestDaemonSubscribeBroadcast(t *testing.T) {
	d, err := daemon.New(daemon.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch := d.Subscribe()
	defer d.Unsubscribe(ch)

	// State() should not panic.
	snap := d.State()
	if snap.Daemon.DeviceID == "" {
		t.Fatal("expected non-empty device_id")
	}
}

func TestUpdateHotkeysPersistsConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	cfg := config.Default()
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("Save: %v", err)
	}

	d, err := daemon.New(daemon.Options{DataDir: dir, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = d.UpdateHotkeys(config.Hotkeys{
		SwitchNext:    "ctrl+1",
		SwitchPrev:    "ctrl+2",
		SwitchToHost:  "ctrl+3",
		DisconnectAll: "ctrl+4",
		TogglePause:   "ctrl+5",
	})
	if err != nil {
		t.Fatalf("UpdateHotkeys: %v", err)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Hotkeys.TogglePause != "ctrl+5" {
		t.Fatalf("toggle_pause=%q want ctrl+5", loaded.Hotkeys.TogglePause)
	}
}

func TestDaemonNewPersistsDefaultConfigWhenMissing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	d, err := daemon.New(daemon.Options{DataDir: dir, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config.json should exist after New: %v", err)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Port != d.Config().Port {
		t.Fatalf("port=%d want %d", loaded.Port, d.Config().Port)
	}
	if loaded.DeviceName != d.Config().DeviceName {
		t.Fatalf("device_name=%q want %q", loaded.DeviceName, d.Config().DeviceName)
	}
}
