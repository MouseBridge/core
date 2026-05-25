package session

import "sync"

// DeviceInfo holds runtime state for a connected peer.
type DeviceInfo struct {
	ID           string
	Name         string
	IP           string
	AvgLatencyMs float64
	latencies    []float64
}

// Manager tracks connected devices and their statistics.
type Manager struct {
	mu      sync.RWMutex
	devices map[string]*DeviceInfo
}

func NewManager() *Manager {
	return &Manager{devices: make(map[string]*DeviceInfo)}
}

// Add registers a device as connected.
func (m *Manager) Add(id, name, ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices[id] = &DeviceInfo{ID: id, Name: name, IP: ip}
}

// Remove deregisters a device.
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.devices, id)
}

// RecordLatency records a latency sample and updates the rolling average (last 20).
func (m *Manager) RecordLatency(id string, ms float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return
	}
	d.latencies = append(d.latencies, ms)
	if len(d.latencies) > 20 {
		d.latencies = d.latencies[len(d.latencies)-20:]
	}
	var sum float64
	for _, v := range d.latencies {
		sum += v
	}
	d.AvgLatencyMs = sum / float64(len(d.latencies))
}

// Devices returns a snapshot of all connected devices.
func (m *Manager) Devices() []DeviceInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]DeviceInfo, 0, len(m.devices))
	for _, d := range m.devices {
		out = append(out, *d)
	}
	return out
}
