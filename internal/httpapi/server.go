package httpapi

import (
	"context"
	"net"
	"net/http"
	"strconv"

	"github.com/mousebridge/core/internal/daemon"
	"github.com/mousebridge/core/internal/shortcuts"
)

// Server is an HTTP server that bridges the daemon to browser clients.
type Server struct {
	d         *daemon.Daemon
	addr      string
	srv       *http.Server
	hub       *sseHub
	shortcuts *shortcuts.Manager
}

// New creates a Server bound to host:port.
func New(d *daemon.Daemon, host string, port int) *Server {
	return &Server{
		d:         d,
		addr:      net.JoinHostPort(host, strconv.Itoa(port)),
		hub:       newSSEHub(),
		shortcuts: shortcuts.New(),
	}
}

// Start begins listening. Returns an error if the port is already in use.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	s.routes(mux)
	s.srv = &http.Server{Addr: s.addr, Handler: corsMiddleware(mux)}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	go s.fanOutEvents()
	go s.srv.Serve(ln)
	return nil
}

// Stop shuts down the HTTP server gracefully.
func (s *Server) Stop() {
	if s.srv != nil {
		_ = s.srv.Shutdown(context.Background())
	}
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("/api/serve", s.handleServe)
	mux.HandleFunc("/api/connect", s.handleConnect)
	mux.HandleFunc("/api/stop-serve", s.handleStopServe)
	mux.HandleFunc("/api/disconnect", s.handleDisconnect)
	mux.HandleFunc("/api/pair/accept", s.handlePairAccept)
	mux.HandleFunc("/api/pair/reject", s.handlePairReject)
	mux.HandleFunc("/api/pair/pin", s.handlePairPIN)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/shortcuts", s.handleShortcuts)
	mux.HandleFunc("/api/events", s.handleSSE)
}

// fanOutEvents subscribes to daemon events and fans them out to SSE clients.
func (s *Server) fanOutEvents() {
	ch := s.d.Subscribe()
	defer s.d.Unsubscribe(ch)
	for ev := range ch {
		s.hub.broadcast(ev)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
