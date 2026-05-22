// Package ipc defines the Handler interface that a future UI layer must implement.
// Nothing in this package is used by the CLI — it is reserved for Step 2.
package ipc

import "mousebridge/internal/session"

// Handler receives runtime events from the core and surfaces them to a UI.
// Step 2 will implement this over a Unix socket.
type Handler interface {
	OnDeviceDiscovered(id, name string)
	OnPairRequest(id, name, pin string)
	OnPairResult(id string, accepted bool)
	OnConnected(id, name string)
	OnDisconnected(id string)
	OnEventSent(msgType string, rttMs float64)
	OnEventReceived(msgType string, latencyMs float64)
	OnSwitchTarget(deviceID, trigger string)
	OnStatus(devices []session.DeviceInfo)
}
