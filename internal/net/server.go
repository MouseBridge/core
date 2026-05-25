package net

import (
	"fmt"
	"log"
	"net"
	"sync"
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
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
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
