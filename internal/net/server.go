package net

import (
	"fmt"
	"net"
	"sync"

	log "github.com/sirupsen/logrus"
)

// ConnHandler is called for each accepted connection.
type ConnHandler func(c *Conn)

// Server listens for inbound TCP connections.
type Server struct {
	mu       sync.Mutex
	listener net.Listener
	handler  ConnHandler
}

// NewServer creates a Server that calls handler for each connection.
func NewServer(handler ConnHandler) *Server {
	return &Server{handler: handler}
}

// Listen starts accepting on the given port. Closes any existing listener first.
func (s *Server) Listen(port int) error {
	addr := fmt.Sprintf("0.0.0.0:%d", port)
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return fmt.Errorf("net: listen %s: %w", addr, err)
	}
	s.mu.Lock()
	old := s.listener
	s.listener = ln
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	go s.acceptLoop(ln)
	return nil
}

// IsListening reports whether the server is currently listening.
func (s *Server) IsListening() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listener != nil
}

// Port returns the local port the server is listening on, or 0 if not listening.
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return 0
	}
	addr, ok := s.listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0
	}
	return addr.Port
}

// Stop closes the listener.
func (s *Server) Stop() {
	s.mu.Lock()
	ln := s.listener
	s.listener = nil
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		raw, err := ln.Accept()
		if err != nil {
			log.Printf("net: accept: %v", err)
			return
		}
		go s.handler(NewConn(raw))
	}
}
