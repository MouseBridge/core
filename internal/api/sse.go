package api

import (
	"io"
	"sync"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/daemon"
)

// sseHub fans daemon events out to connected SSE clients.
type sseHub struct {
	mu      sync.RWMutex
	clients map[chan daemon.BusEvent]struct{}
}

func newSSEHub() *sseHub {
	return &sseHub{clients: make(map[chan daemon.BusEvent]struct{})}
}

func (h *sseHub) add(ch chan daemon.BusEvent) {
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
}

func (h *sseHub) remove(ch chan daemon.BusEvent) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

func (h *sseHub) broadcast(ev daemon.BusEvent) {
	h.mu.RLock()
	clients := make([]chan daemon.BusEvent, 0, len(h.clients))
	for ch := range h.clients {
		clients = append(clients, ch)
	}
	h.mu.RUnlock()
	for _, ch := range clients {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Server) handleSSE(c *gin.Context) {
	s.handleSSEView(c, false)
}

func (s *Server) handleLocalSSE(c *gin.Context) {
	s.handleSSEView(c, true)
}

func (s *Server) handleSSEView(c *gin.Context, includeSensitive bool) {
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	ch := make(chan daemon.BusEvent, 64)
	s.hub.add(ch)
	defer s.hub.remove(ch)

	// Push full status immediately on connect.
	var snap any
	if includeSensitive {
		snap = s.d.LocalState()
	} else {
		snap = s.d.State()
	}
	log.Printf("api: SSE client connected from %s", c.Request.RemoteAddr)

	firstSent := false
	c.Stream(func(w io.Writer) bool {
		if !firstSent {
			firstSent = true
			c.SSEvent("message", snap)
			return true
		}
		select {
		case ev, ok := <-ch:
			if !ok {
				return false
			}
			if !includeSensitive {
				ev = redactBusEvent(ev)
			}
			c.SSEvent("message", ev)
			return true
		case <-c.Request.Context().Done():
			return false
		}
	})

	log.Printf("api: SSE client disconnected from %s", c.Request.RemoteAddr)
}

func redactBusEvent(ev daemon.BusEvent) daemon.BusEvent {
	if ev.Kind != "pair_request" || ev.DisplayPIN == "" {
		return ev
	}
	ev.DisplayPIN = ""
	return ev
}
