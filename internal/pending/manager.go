package pending

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// VerifyResult is the outcome of a PIN verification attempt.
type VerifyResult int

const (
	VerifyOK      VerifyResult = iota
	VerifyRetry                // Wrong PIN, attempts remaining
	VerifyReject               // Attempts exhausted
	VerifyExpired              // TTL expired
)

// Entry holds state for one inbound pairing waiting on PIN confirmation.
type Entry struct {
	PairingID    string
	ConnectionID string

	ClaimedDeviceID string
	DisplayID       string
	Name            string
	RemoteIP        string

	pin         string
	Attempts    int
	MaxAttempts int

	CreatedAt time.Time
	ExpiresAt time.Time
}

// AttemptsRemaining returns how many PIN attempts are left.
func (e *Entry) AttemptsRemaining() int {
	r := e.MaxAttempts - e.Attempts
	if r < 0 {
		return 0
	}
	return r
}

// Manager manages all in-flight inbound pairings.
type Manager struct {
	mu          sync.Mutex
	entries     map[string]*Entry // keyed by pairing_id
	ttl         time.Duration
	maxAttempts int
}

// New creates a PendingManager.
func New(ttl time.Duration, maxAttempts int) *Manager {
	return &Manager{
		entries:     make(map[string]*Entry),
		ttl:         ttl,
		maxAttempts: maxAttempts,
	}
}

// Add creates and registers a new pending entry; returns it.
func (m *Manager) Add(connectionID, claimedDeviceID, displayID, name, remoteIP string) (*Entry, error) {
	pairingID, err := randomHex(8)
	if err != nil {
		return nil, err
	}
	pin, err := generatePIN()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	e := &Entry{
		PairingID:       pairingID,
		ConnectionID:    connectionID,
		ClaimedDeviceID: claimedDeviceID,
		DisplayID:       displayID,
		Name:            name,
		RemoteIP:        remoteIP,
		pin:             pin,
		MaxAttempts:     m.maxAttempts,
		CreatedAt:       now,
		ExpiresAt:       now.Add(m.ttl),
	}
	m.mu.Lock()
	m.entries[pairingID] = e
	m.mu.Unlock()
	return e, nil
}

// PIN returns the generated PIN for the entry (server-side only, never sent over wire).
func (m *Manager) PIN(pairingID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[pairingID]; ok {
		return e.pin
	}
	return ""
}

// Verify checks the submitted PIN. Returns VerifyResult and updates attempts.
func (m *Manager) Verify(pairingID, pin string) (VerifyResult, *Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.entries[pairingID]
	if !ok {
		return VerifyExpired, nil
	}
	if time.Now().After(e.ExpiresAt) {
		delete(m.entries, pairingID)
		return VerifyExpired, e
	}
	if pin != e.pin {
		e.Attempts++
		if e.Attempts >= e.MaxAttempts {
			delete(m.entries, pairingID)
			return VerifyReject, e
		}
		return VerifyRetry, e
	}
	delete(m.entries, pairingID)
	return VerifyOK, e
}

// Reject explicitly removes an entry (user hit reject).
func (m *Manager) Reject(pairingID string) *Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[pairingID]
	delete(m.entries, pairingID)
	return e
}

// Get looks up an entry without modifying it.
func (m *Manager) Get(pairingID string) (*Entry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[pairingID]
	return e, ok
}

// RemoveExpired GC's expired entries and returns them.
func (m *Manager) RemoveExpired(now time.Time) []*Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var expired []*Entry
	for id, e := range m.entries {
		if now.After(e.ExpiresAt) {
			expired = append(expired, e)
			delete(m.entries, id)
		}
	}
	return expired
}

// Snapshot returns all current entries for status reporting.
func (m *Manager) Snapshot() []*Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Entry, 0, len(m.entries))
	for _, e := range m.entries {
		cp := *e
		out = append(out, &cp)
	}
	return out
}

// RunGC runs the expiry GC goroutine until ctx is cancelled.
// onExpired is called for each expired entry.
func (m *Manager) RunGC(ctx context.Context, onExpired func(*Entry)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			for _, e := range m.RemoveExpired(now) {
				if onExpired != nil {
					onExpired(e)
				}
			}
		}
	}
}

func generatePIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}
