package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config holds all daemon settings matching v1.5.2-final-mvp spec.
type Config struct {
	ListenHost    string `json:"listen_host"`
	Port          int    `json:"port"`
	UnsafeHTTPLAN bool   `json:"unsafe_http_lan"`

	RememberedEnabled            bool `json:"remembered_enabled"`
	RememberedAutoConnectEnabled bool `json:"remembered_auto_connect_enabled"`
	KeepAwakeEnabled             bool `json:"keep_awake_enabled"`
	ClipboardSyncEnabled         bool `json:"clipboard_sync_enabled"`
	ClipboardAllowServerSync     bool `json:"clipboard_allow_server_sync"`
	ClipboardHistoryEnabled      bool `json:"clipboard_history_enabled"`
	ClipboardHistoryLimit        int  `json:"clipboard_history_limit"`

	PairingPINTTLSeconds  int `json:"pairing_pin_ttl_seconds"`
	PairingPINMaxAttempts int `json:"pairing_pin_max_attempts"`

	ConnectTimeoutSeconds int         `json:"connect_timeout_seconds"`
	JSONBodyLimitBytes    int64       `json:"json_body_limit_bytes"`
	MouseSpeed            float64     `json:"mouse_speed"`
	ScrollSpeed           float64     `json:"scroll_speed"`
	ShakeToLocateEnabled  bool        `json:"shake_to_locate_enabled"`
	ShakeSensitivity      string      `json:"shake_sensitivity"`
	Hotkeys               Hotkeys     `json:"hotkeys"`
	QuickTexts            []QuickText `json:"quick_texts,omitempty"`
	EdgeTargets           EdgeTargets `json:"edge_targets"`
	EdgeLayoutConfigured  bool        `json:"edge_layout_configured,omitempty"`

	// DeviceName is user-configurable; DeviceID is always derived, never stored.
	DeviceName string `json:"device_name,omitempty"`
}

// Hotkeys holds configurable key bindings.
type Hotkeys struct {
	SwitchNext       string `json:"switch_next"`
	SwitchPrev       string `json:"switch_prev"`
	SwitchToHost     string `json:"switch_to_host"`
	DisconnectAll    string `json:"disconnect_all"`
	TogglePause      string `json:"toggle_pause"`
	ClipboardHistory string `json:"clipboard_history"`
}

// QuickText stores a fixed text snippet and its optional global hotkey.
// Sensitive snippets keep their value in the macOS Keychain instead of config.json.
type QuickText struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Text        string `json:"text,omitempty"`
	Hotkey      string `json:"hotkey,omitempty"`
	AppendEnter bool   `json:"append_enter,omitempty"`
	Sensitive   bool   `json:"sensitive,omitempty"`
	HasSecret   bool   `json:"has_secret,omitempty"`
}

// EdgeTargets maps local screen edges to remembered/connected remote device IDs.
type EdgeTargets struct {
	Left   string `json:"left"`
	Right  string `json:"right"`
	Top    string `json:"top"`
	Bottom string `json:"bottom"`
}

// Default returns a Config with sensible defaults.
func Default() *Config {
	return &Config{
		ListenHost:                   "127.0.0.1",
		Port:                         39172,
		UnsafeHTTPLAN:                false,
		RememberedEnabled:            true,
		RememberedAutoConnectEnabled: true,
		ClipboardSyncEnabled:         true,
		ClipboardAllowServerSync:     true,
		ClipboardHistoryLimit:        50,
		PairingPINTTLSeconds:         120,
		PairingPINMaxAttempts:        3,
		ConnectTimeoutSeconds:        5,
		JSONBodyLimitBytes:           65536,
		MouseSpeed:                   1.5,
		ScrollSpeed:                  2.0,
		ShakeToLocateEnabled:         true,
		ShakeSensitivity:             "4",
		Hotkeys: Hotkeys{
			SwitchNext:       "ctrl+alt+right",
			SwitchPrev:       "ctrl+alt+left",
			SwitchToHost:     "ctrl+alt+escape",
			DisconnectAll:    "ctrl+alt+backspace",
			TogglePause:      "ctrl+alt+p",
			ClipboardHistory: "ctrl+alt+v",
		},
		DeviceName: hostname(),
	}
}

// DeriveDeviceID computes a stable 32-hex-char (128-bit) ID from hardware + port.
// Uses macOS IOPlatformSerialNumber + hostname + port so the ID is stable across
// restarts but differs across physical machines and ports.
func DeriveDeviceID(port int) string {
	serial := machineSerial()
	h, _ := os.Hostname()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", serial, h, port)))
	return hex.EncodeToString(sum[:16]) // 32 hex chars = 128-bit
}

// machineSerial returns the macOS hardware serial number, or "" if unavailable.
func machineSerial() string {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "IOPlatformSerialNumber") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				return strings.Trim(strings.TrimSpace(parts[1]), `"`)
			}
		}
	}
	return ""
}

// Validate checks config constraints and returns an error if invalid.
func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("config: port %d out of range", c.Port)
	}
	if !isLoopback(c.ListenHost) && !c.UnsafeHTTPLAN {
		return fmt.Errorf("config: listen_host=%q is not loopback but unsafe_http_lan=false; set unsafe_http_lan=true to allow LAN HTTP access", c.ListenHost)
	}
	return nil
}

func isLoopback(h string) bool {
	return h == "127.0.0.1" || h == "::1" || h == "localhost"
}

// Load reads Config from path. Returns Default() if the file does not exist.
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
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	// Migrate configurations created before disconnect/pause defaults existed.
	// Empty values here mean "not configured", so restore the safe defaults.
	if cfg.Hotkeys.SwitchNext == "" {
		cfg.Hotkeys.SwitchNext = Default().Hotkeys.SwitchNext
	}
	if cfg.Hotkeys.SwitchPrev == "" {
		cfg.Hotkeys.SwitchPrev = Default().Hotkeys.SwitchPrev
	}
	if cfg.Hotkeys.SwitchToHost == "" {
		cfg.Hotkeys.SwitchToHost = Default().Hotkeys.SwitchToHost
	}
	if cfg.Hotkeys.DisconnectAll == "" {
		cfg.Hotkeys.DisconnectAll = Default().Hotkeys.DisconnectAll
	}
	if cfg.Hotkeys.TogglePause == "" {
		cfg.Hotkeys.TogglePause = Default().Hotkeys.TogglePause
	}
	if cfg.Hotkeys.ClipboardHistory == "" {
		cfg.Hotkeys.ClipboardHistory = Default().Hotkeys.ClipboardHistory
	}
	if cfg.ClipboardHistoryLimit <= 0 {
		cfg.ClipboardHistoryLimit = Default().ClipboardHistoryLimit
	}
	if cfg.MouseSpeed <= 0 {
		cfg.MouseSpeed = Default().MouseSpeed
	}
	if cfg.ScrollSpeed <= 0 {
		cfg.ScrollSpeed = Default().ScrollSpeed
	}
	return cfg, nil
}

// Save writes Config to path, creating parent dirs as needed.
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
