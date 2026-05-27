package p2p

import (
	"sync"
	"time"

	"github.com/mousebridge/core/internal/transport"
)

// OutboundPairing tracks one outbound pairing waiting for PIN submission.
type OutboundPairing struct {
	PairingID       string
	ConnectionID    string
	RemoteName      string
	RemoteDisplayID string
	ExpiresAt       time.Time
	Conn            *transport.Conn
}

// OutboundTracker maps pairing_id → outbound pairing on the client side.
type OutboundTracker struct {
	mu    sync.Mutex
	pairs map[string]*OutboundPairing
}

// NewOutboundTracker creates an empty tracker.
func NewOutboundTracker() *OutboundTracker {
	return &OutboundTracker{pairs: make(map[string]*OutboundPairing)}
}

// Add registers an outbound pairing.
func (t *OutboundTracker) Add(p *OutboundPairing) {
	t.mu.Lock()
	t.pairs[p.PairingID] = p
	t.mu.Unlock()
}

// Get looks up a pairing by ID.
func (t *OutboundTracker) Get(pairingID string) (*OutboundPairing, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.pairs[pairingID]
	return p, ok
}

// Remove deletes a pairing.
func (t *OutboundTracker) Remove(pairingID string) {
	t.mu.Lock()
	delete(t.pairs, pairingID)
	t.mu.Unlock()
}
