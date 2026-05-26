package api

import (
	"sync"

	"github.com/mousebridge/core/internal/daemon"
)

type sseHub struct {
	mu      sync.RWMutex
	clients map[chan<- daemon.Event]struct{}
}

func newSSEHub() *sseHub {
	return &sseHub{clients: make(map[chan<- daemon.Event]struct{})}
}

func (h *sseHub) add(ch chan<- daemon.Event) {
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
}

func (h *sseHub) remove(ch chan<- daemon.Event) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

func (h *sseHub) broadcast(ev daemon.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- ev:
		default:
		}
	}
}
