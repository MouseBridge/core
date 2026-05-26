package httpapi

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/mousebridge/core/internal/config"
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
	var req struct {
		DeviceID string `json:"device_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	s.d.HandleCommand(daemon.Command{Cmd: "pair_accept", DeviceID: req.DeviceID})
	writeJSON(w, map[string]string{"ok": "true"})
}

func (s *Server) handlePairReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		DeviceID string `json:"device_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	s.d.HandleCommand(daemon.Command{Cmd: "pair_reject", DeviceID: req.DeviceID})
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

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.d.State())
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

func (s *Server) handleTrust(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.d.TrustedList())
	case http.MethodPost:
		var req struct {
			DeviceID string `json:"device_id"`
			Name     string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DeviceID == "" {
			http.Error(w, "device_id required", http.StatusBadRequest)
			return
		}
		s.d.HandleCommand(daemon.Command{Cmd: "trust", DeviceID: req.DeviceID, Name: req.Name})
		writeJSON(w, map[string]string{"ok": "true"})
	case http.MethodDelete:
		var req struct {
			DeviceID string `json:"device_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DeviceID == "" {
			http.Error(w, "device_id required", http.StatusBadRequest)
			return
		}
		s.d.HandleCommand(daemon.Command{Cmd: "untrust", DeviceID: req.DeviceID})
		writeJSON(w, map[string]string{"ok": "true"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleShortcuts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.d.Config().Hotkeys)
	case http.MethodPut:
		var h config.Hotkeys
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if err := s.d.UpdateHotkeys(h); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = s.shortcuts.Push(h)
		writeJSON(w, map[string]string{"ok": "true"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
