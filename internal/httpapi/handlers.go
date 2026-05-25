package httpapi

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/mousebridge/core/internal/daemon"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleServe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Port int `json:"port"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	s.d.HandleCommand(daemon.Command{Cmd: "serve", Port: req.Port})
	writeJSON(w, map[string]string{"ok": "true"})
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		IP   string `json:"ip"`
		Port int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IP == "" {
		http.Error(w, "ip required", http.StatusBadRequest)
		return
	}
	s.d.HandleCommand(daemon.Command{Cmd: "connect", IP: req.IP, Port: req.Port})
	writeJSON(w, map[string]string{"ok": "true"})
}

func (s *Server) handleStopServe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.d.HandleCommand(daemon.Command{Cmd: "stop_serve"})
	writeJSON(w, map[string]string{"ok": "true"})
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DeviceID == "" {
		http.Error(w, "device_id required", http.StatusBadRequest)
		return
	}
	s.d.HandleCommand(daemon.Command{Cmd: "disconnect", DeviceID: req.DeviceID})
	writeJSON(w, map[string]string{"ok": "true"})
}

func (s *Server) handlePairAccept(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.d.HandleCommand(daemon.Command{Cmd: "pair_accept"})
	writeJSON(w, map[string]string{"ok": "true"})
}

func (s *Server) handlePairReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.d.HandleCommand(daemon.Command{Cmd: "pair_reject"})
	writeJSON(w, map[string]string{"ok": "true"})
}

func (s *Server) handlePairPIN(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PIN == "" {
		http.Error(w, "pin required", http.StatusBadRequest)
		return
	}
	s.d.HandleCommand(daemon.Command{Cmd: "pair_pin", PIN: req.PIN})
	writeJSON(w, map[string]string{"ok": "true"})
}

// handleStatus subscribes to the daemon, sends a "status" command, waits for
// the status event (max 2s), and returns the device list as JSON.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ch := s.d.Subscribe()
	defer s.d.Unsubscribe(ch)

	s.d.HandleCommand(daemon.Command{Cmd: "status"})

	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Event == "status" {
				devices := ev.Devices
				if devices == nil {
					devices = []daemon.DeviceStatus{}
				}
				writeJSON(w, map[string]any{"devices": devices})
				return
			}
		case <-timeout:
			writeJSON(w, map[string]any{"devices": []daemon.DeviceStatus{}})
			return
		}
	}
}

// sseHub tracks SSE connections.
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
