package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Hotkeys holds configurable key bindings.
type Hotkeys struct {
	SwitchNext    string `json:"switch_next"`
	SwitchPrev    string `json:"switch_prev"`
	SwitchToHost  string `json:"switch_to_host"`
	DisconnectAll string `json:"disconnect_all"`
	TogglePause   string `json:"toggle_pause"`
}

// Config holds all user-configurable settings.
// DeviceID is derived from hostname+port and must not be modified by users.
type Config struct {
	DeviceID           string  `json:"device_id"`
	Port               int     `json:"port"`
	DeviceName         string  `json:"device_name"`
	Hotkeys            Hotkeys `json:"hotkeys"`
	TrustedDevicesFile string  `json:"trusted_devices_file"`
	HTTPHost           string  `json:"http_host"`
	HTTPPort           int     `json:"http_port"`
}

// Default returns a Config with sensible defaults.
func Default() *Config {
	home, _ := os.UserHomeDir()
	cfg := &Config{
		Port:       39172,
		DeviceName: hostname(),
		Hotkeys: Hotkeys{
			SwitchNext:   "ctrl+alt+right",
			SwitchPrev:   "ctrl+alt+left",
			SwitchToHost: "ctrl+alt+home",
		},
		TrustedDevicesFile: filepath.Join(home, ".mousebridge", "trusted.json"),
		HTTPHost:           "127.0.0.1",
		HTTPPort:           39173,
	}
	cfg.DeviceID = DeriveDeviceID(cfg.Port)
	return cfg
}

// DeriveDeviceID computes a stable 8-char ID from hostname + port.
// Same machine, different port → different ID. Deterministic across restarts.
func DeriveDeviceID(port int) string {
	h, _ := os.Hostname()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", h, port)))
	return hex.EncodeToString(sum[:4])
}

// Load reads a Config from path. Returns Default() if the file does not exist.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	// Always recompute — ignore any device_id written to disk.
	cfg.DeviceID = DeriveDeviceID(cfg.Port)
	return cfg, nil
}

// Save writes the Config to path, creating parent dirs as needed.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
