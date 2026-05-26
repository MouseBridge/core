package daemon

// Command is sent by a CLI client to the daemon over Unix Socket.
type Command struct {
	Cmd      string `json:"cmd"`                 // "serve"|"connect"|"pair_accept"|"pair_reject"|"pair_pin"|"status"|"disconnect"|"stop_serve"|"trust"|"untrust"|"trusted_list"
	IP       string `json:"ip,omitempty"`        // for "connect"
	Port     int    `json:"port,omitempty"`      // for "connect"
	PIN      string `json:"pin,omitempty"`       // for "pair_pin"
	DeviceID string `json:"device_id,omitempty"` // for "disconnect"|"trust"|"untrust"
	Name     string `json:"name,omitempty"`      // for "trust"
}

// Event is pushed from the daemon to all connected CLI clients.
type Event struct {
	Event    string         `json:"event"`               // "listening"|"pair_request"|"paired"|"connected"|"disconnected"|"log"|"status"|"error"
	Port     int            `json:"port,omitempty"`      // for "listening" and "status"
	DeviceID string         `json:"device_id,omitempty"` // for device events
	Name     string         `json:"name,omitempty"`      // for device events
	PIN      string         `json:"pin,omitempty"`       // for "pair_request"
	Role     string         `json:"role,omitempty"`      // for "pair_request": "host"|"slave"
	IP       string         `json:"ip,omitempty"`        // for "connected"
	Msg      string         `json:"msg,omitempty"`       // for "log" and "error"
	Devices  []DeviceStatus `json:"devices"`             // for "status"
	Serving  bool           `json:"serving,omitempty"`   // for "status"
	LocalID  string         `json:"local_id,omitempty"`  // for "status"
	LocalName string        `json:"local_name,omitempty"` // for "status"
}

// DeviceStatus is included in "status" events.
type DeviceStatus struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	IP           string  `json:"ip"`
	Role         string  `json:"role"` // "host" | "slave"
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}
