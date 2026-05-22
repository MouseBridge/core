package pairing

import (
	"fmt"
	"sync"
)

type State string

const (
	StateIdle     State = "idle"
	StatePending  State = "pending"
	StatePaired   State = "paired"
	StateRejected State = "rejected"
)

// Manager runs the pairing state machine for one pairing attempt.
type Manager struct {
	mu       sync.Mutex
	state    State
	pin      string
	peerID   string
	peerName string
}

func NewManager() *Manager {
	return &Manager{state: StateIdle}
}

func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// StartAsSlave is called on the slave when a pair_request arrives.
// Returns the generated PIN to display.
func (m *Manager) StartAsSlave(peerID, peerName string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pin, err := GeneratePIN()
	if err != nil {
		return "", err
	}
	m.pin = pin
	m.peerID = peerID
	m.peerName = peerName
	m.state = StatePending
	return pin, nil
}

// Accept transitions to Paired (slave pressed the Accept button).
func (m *Manager) Accept() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != StatePending {
		return fmt.Errorf("pairing: Accept called in state %s", m.state)
	}
	m.state = StatePaired
	return nil
}

// ConfirmPIN checks the PIN submitted by the host.
func (m *Manager) ConfirmPIN(pin string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != StatePending {
		return fmt.Errorf("pairing: ConfirmPIN called in state %s", m.state)
	}
	if pin != m.pin {
		m.state = StateRejected
		return fmt.Errorf("pairing: wrong PIN")
	}
	m.state = StatePaired
	return nil
}

// Reject transitions to Rejected.
func (m *Manager) Reject() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != StatePending {
		return fmt.Errorf("pairing: Reject called in state %s", m.state)
	}
	m.state = StateRejected
	return nil
}

// PeerID returns the peer's device ID.
func (m *Manager) PeerID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peerID
}

// PeerName returns the peer's display name.
func (m *Manager) PeerName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peerName
}
