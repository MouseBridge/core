package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Hotkeys holds configurable key bindings.
type Hotkeys struct {
	SwitchRight string `json:"switch_right"`
	SwitchLeft  string `json:"switch_left"`
}

// Config holds all user-configurable settings.
type Config struct {
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
	return &Config{
		Port:       39172,
		DeviceName: hostname(),
		Hotkeys: Hotkeys{
			SwitchRight: "ctrl+alt+right",
			SwitchLeft:  "ctrl+alt+left",
		},
		TrustedDevicesFile: filepath.Join(home, ".mousebridge", "trusted.json"),
		HTTPHost:           "127.0.0.1",
		HTTPPort:           39173,
	}
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
