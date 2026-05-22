package daemon

import (
	"fmt"
	"log"
	"net"
	"os"
	"sync"
)

// IPCServer listens on a Unix Socket and broadcasts events to all connected clients.
type IPCServer struct {
	socketPath string
	listener   net.Listener

	mu      sync.RWMutex
	clients map[*ipcClient]struct{}

	onCommand func(Command)
}

// NewIPCServer creates an IPCServer for the given socket path.
func NewIPCServer(socketPath string, onCommand func(Command)) *IPCServer {
	return &IPCServer{
		socketPath: socketPath,
		clients:    make(map[*ipcClient]struct{}),
		onCommand:  onCommand,
	}
}

// Start removes any stale socket file and begins listening.
func (s *IPCServer) Start() error {
	_ = os.Remove(s.socketPath)
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("ipc: listen %s: %w", s.socketPath, err)
	}
	s.listener = ln
	go s.acceptLoop(ln)
	return nil
}

// Stop closes the listener and all clients, removes the socket file.
func (s *IPCServer) Stop() {
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.mu.Lock()
	for c := range s.clients {
		c.close()
	}
	s.clients = make(map[*ipcClient]struct{})
	s.mu.Unlock()
	_ = os.Remove(s.socketPath)
}

// Broadcast sends an Event to all connected clients.
func (s *IPCServer) Broadcast(ev Event) {
	s.mu.RLock()
	clients := make([]*ipcClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()

	for _, c := range clients {
		if err := c.send(ev); err != nil {
			s.removeClient(c)
		}
	}
}

func (s *IPCServer) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		client := newIPCClient(conn)
		s.mu.Lock()
		s.clients[client] = struct{}{}
		s.mu.Unlock()

		go func() {
			client.readCommands(s.onCommand)
			s.removeClient(client)
		}()
	}
}

func (s *IPCServer) removeClient(c *ipcClient) {
	c.close()
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	log.Printf("ipc: client disconnected (%d remaining)", len(s.clients))
}
