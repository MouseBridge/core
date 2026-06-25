package daemon

import (
	"github.com/mousebridge/core/internal/localhelper"
	"github.com/mousebridge/core/internal/remembered"
)

// SessionStatus is a snapshot of one active session.
type SessionStatus struct {
	ConnectionID string  `json:"connection_id"`
	DeviceID     string  `json:"device_id"`
	Name         string  `json:"name"`
	IP           string  `json:"ip"`
	Role         string  `json:"role"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// PendingPairStatus is a snapshot of one in-flight pairing.
type PendingPairStatus struct {
	ConnectionID      string `json:"connection_id"`
	PairingID         string `json:"pairing_id"`
	ClaimedDeviceID   string `json:"claimed_device_id"`
	DisplayID         string `json:"display_id"`
	Name              string `json:"name"`
	Role              string `json:"role"`
	ExpiresAt         int64  `json:"expires_at"`
	AttemptsRemaining int    `json:"attempts_remaining"`
	DisplayPIN        string `json:"display_pin,omitempty"`
}

// DaemonInfo carries the local device identity fields in status responses.
type DaemonInfo struct {
	DeviceID  string `json:"device_id"`
	DisplayID string `json:"display_id"`
	Name      string `json:"name"`
}

// StatusSnapshot is the full status returned by GET /api/status and the SSE status event.
type StatusSnapshot struct {
	Daemon            DaemonInfo          `json:"daemon"`
	Listening         bool                `json:"listening"`
	ListenAddr        string              `json:"listen_addr,omitempty"`
	Sessions          []SessionStatus     `json:"sessions"`
	PendingPairs      []PendingPairStatus `json:"pending_pairs"`
	RememberedDevices []remembered.DTO    `json:"remembered_devices"`
	HelperRuntime     localhelper.Status  `json:"helper_runtime"`
	ActiveTargetID    string              `json:"active_target_device_id"`
	ControllingRemote bool                `json:"controlling_remote"`
	Paused            bool                `json:"paused"`
	UnsafeHTTPLAN     bool                `json:"unsafe_http_lan"`
}

// State returns a consistent snapshot of the daemon's current state.
func (d *Daemon) State() StatusSnapshot {
	id := d.identity

	d.sessMu.RLock()
	sessions := make([]SessionStatus, 0, len(d.sessions))
	for _, s := range d.sessions {
		sessions = append(sessions, SessionStatus{
			ConnectionID: s.ConnectionID,
			DeviceID:     s.DeviceID,
			Name:         s.Name,
			IP:           s.IP,
			Role:         s.Role,
			AvgLatencyMs: s.AvgLatencyMs,
		})
	}
	d.sessMu.RUnlock()

	pendingEntries := d.pending.Snapshot()
	pendingStatuses := make([]PendingPairStatus, 0, len(pendingEntries))
	for _, e := range pendingEntries {
		ps := PendingPairStatus{
			ConnectionID:      e.ConnectionID,
			PairingID:         e.PairingID,
			ClaimedDeviceID:   e.ClaimedDeviceID,
			DisplayID:         e.DisplayID,
			Name:              e.Name,
			Role:              "server",
			ExpiresAt:         e.ExpiresAt.Unix(),
			AttemptsRemaining: e.AttemptsRemaining(),
			DisplayPIN:        d.pending.PIN(e.PairingID),
		}
		pendingStatuses = append(pendingStatuses, ps)
	}

	// Also include outbound (client-side) pairings from OutboundTracker.
	for _, op := range d.tracker.Snapshot() {
		pendingStatuses = append(pendingStatuses, PendingPairStatus{
			PairingID:    op.PairingID,
			ConnectionID: op.ConnectionID,
			Name:         op.RemoteName,
			DisplayID:    op.RemoteDisplayID,
			Role:         "client",
			ExpiresAt:    op.ExpiresAt.Unix(),
		})
	}

	addr := d.Addr()
	activeTarget := d.ctrl.ActiveTarget()
	paused := d.Paused()

	return StatusSnapshot{
		Daemon: DaemonInfo{
			DeviceID:  id.DeviceID,
			DisplayID: id.DisplayID,
			Name:      id.Name,
		},
		Listening:         addr != "",
		ListenAddr:        addr,
		Sessions:          sessions,
		PendingPairs:      pendingStatuses,
		RememberedDevices: d.rem.List(),
		HelperRuntime:     d.LocalHelperStatus(),
		ActiveTargetID:    activeTarget,
		ControllingRemote: activeTarget != "" && activeTarget != id.DeviceID,
		Paused:            paused,
		UnsafeHTTPLAN:     d.cfg.UnsafeHTTPLAN,
	}
}
