package httpapi

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/mousebridge/core/internal/daemon"
)

// handleSSE streams daemon events to the client as Server-Sent Events.
// The browser connects with: new EventSource("/api/events")
// Each event is written as:  data: <json>\n\n
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan daemon.Event, 64)
	s.hub.add(ch)
	defer s.hub.remove(ch)

	log.Printf("httpapi: SSE client connected from %s", r.RemoteAddr)
	defer log.Printf("httpapi: SSE client disconnected from %s", r.RemoteAddr)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
