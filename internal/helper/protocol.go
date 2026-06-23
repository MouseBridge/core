package helper

import (
	"encoding/json"
	"fmt"
)

const (
	TypeRegister   = "register"
	TypeConfigPush = "config_push"
	TypeHotkey     = "hotkey"
	TypeEdge       = "edge"
	TypeInput      = "input"
	TypeAck        = "ack"
	TypeError      = "error"
)

// Envelope is the JSON-lines message container for daemon <-> helper IPC.
type Envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// RegisterPayload is the first message a helper sends after connecting.
type RegisterPayload struct {
	PID     int    `json:"pid,omitempty"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// ConfigPushPayload is the daemon snapshot sent immediately after helper register
// and again whenever daemon-side state changes.
type ConfigPushPayload struct {
	Daemon       DaemonInfo        `json:"daemon"`
	Sessions     []SessionInfo     `json:"sessions"`
	Hotkeys      map[string]string `json:"hotkeys"`
	EdgeTargets  map[string]string `json:"edge_targets"`
	ActiveTarget string            `json:"active_target"`
	Paused       bool              `json:"paused"`
}

// DaemonInfo identifies the local daemon the helper is attached to.
type DaemonInfo struct {
	DeviceID  string `json:"device_id"`
	DisplayID string `json:"display_id"`
	Name      string `json:"name"`
}

// SessionInfo is the subset of session state the helper needs for routing.
type SessionInfo struct {
	ConnectionID string `json:"connection_id"`
	DeviceID     string `json:"device_id"`
	Name         string `json:"name"`
	Role         string `json:"role"`
}

// HotkeyPayload reports a helper-captured hotkey action back to the daemon.
type HotkeyPayload struct {
	Action string `json:"action"`
	Combo  string `json:"combo,omitempty"`
}

// EdgePayload reports that the local cursor hit a configured screen edge.
type EdgePayload struct {
	Edge string  `json:"edge"`
	Pct  float64 `json:"pct,omitempty"`
}

// InputPayload tells helpers to inject a local input event.
type InputPayload struct {
	Kind      string  `json:"kind"`
	DX        float64 `json:"dx,omitempty"`
	DY        float64 `json:"dy,omitempty"`
	KeyCode   int64   `json:"key_code,omitempty"`
	Modifiers int64   `json:"modifiers,omitempty"`
	Button    string  `json:"button,omitempty"`
	Pressed   bool    `json:"pressed,omitempty"`
}

// AckPayload confirms a successful register.
type AckPayload struct {
	Message string `json:"message,omitempty"`
}

// ErrorPayload reports a protocol error before the connection is closed.
type ErrorPayload struct {
	Message string `json:"message"`
}

// Encode builds a JSON line for the given payload.
func Encode(msgType string, payload interface{}) ([]byte, error) {
	env := Envelope{Type: msgType}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("helper: marshal payload: %w", err)
		}
		env.Payload = raw
	}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("helper: marshal envelope: %w", err)
	}
	return append(data, '\n'), nil
}

// DecodePayload unmarshals an envelope payload into dst.
func DecodePayload(env Envelope, dst interface{}) error {
	if len(env.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Payload, dst); err != nil {
		return fmt.Errorf("helper: decode payload: %w", err)
	}
	return nil
}
