package daemon

import "github.com/mousebridge/core/internal/p2p"

// BusEvent is the daemon-level event type broadcast to all subscribers (SSE, etc.).
// It is a direct alias of p2p.BusEvent so the two layers share one type.
type BusEvent = p2p.BusEvent

// Command is a legacy IPC command sent from CLI tools over Unix socket.
// New code should use the HTTP API instead.
type Command struct {
	Cmd      string `json:"cmd"`
	IP       string `json:"ip,omitempty"`
	Port     int    `json:"port,omitempty"`
	PIN      string `json:"pin,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
	Name     string `json:"name,omitempty"`
	PairingID string `json:"pairing_id,omitempty"`
}

// Event is a legacy IPC event pushed from daemon to CLI tools over Unix socket.
// New code should use SSE (GET /api/events) instead.
type Event struct {
	Event    string `json:"event"`
	Port     int    `json:"port,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
	Name     string `json:"name,omitempty"`
	PIN      string `json:"pin,omitempty"`
	Role     string `json:"role,omitempty"`
	IP       string `json:"ip,omitempty"`
	Msg      string `json:"msg,omitempty"`
}
