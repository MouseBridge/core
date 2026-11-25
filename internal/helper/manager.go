package helper

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Manager owns the daemon-side helper socket and connected helper processes.
type Manager struct {
	socketPath  string
	snapshotFn  func() ConfigPushPayload
	onHotkey    func(HotkeyPayload)
	onEdge      func(EdgePayload)
	onInput     func(InputPayload)
	onClipboard func(ClipboardPayload)

	mu       sync.RWMutex
	listener net.Listener
	clients  map[*client]struct{}
	secureMu sync.Mutex
	secure   map[string]chan SecureResponse
}

type client struct {
	conn   net.Conn
	sendMu sync.Mutex
}

// NewManager creates a helper socket manager.
func NewManager(socketPath string, snapshotFn func() ConfigPushPayload, onHotkey func(HotkeyPayload), onEdge func(EdgePayload), onInput func(InputPayload)) *Manager {
	return &Manager{
		socketPath: socketPath,
		snapshotFn: snapshotFn,
		onHotkey:   onHotkey,
		onEdge:     onEdge,
		onInput:    onInput,
		clients:    make(map[*client]struct{}),
		secure:     make(map[string]chan SecureResponse),
	}
}

// SetClipboardHandler installs the handler for native clipboard changes reported by the helper.
func (m *Manager) SetClipboardHandler(handler func(ClipboardPayload)) {
	m.mu.Lock()
	m.onClipboard = handler
	m.mu.Unlock()
}

// Start begins listening for helper connections.
func (m *Manager) Start() error {
	if err := os.MkdirAll(filepath.Dir(m.socketPath), 0700); err != nil {
		return fmt.Errorf("helper: mkdir %s: %w", filepath.Dir(m.socketPath), err)
	}
	_ = os.Remove(m.socketPath)

	ln, err := net.Listen("unix", m.socketPath)
	if err != nil {
		return fmt.Errorf("helper: listen %s: %w", m.socketPath, err)
	}

	m.mu.Lock()
	m.listener = ln
	m.mu.Unlock()

	go m.acceptLoop(ln)
	return nil
}

// Stop closes the socket and all active helper connections.
func (m *Manager) Stop() {
	m.mu.Lock()
	ln := m.listener
	m.listener = nil
	clients := make([]*client, 0, len(m.clients))
	for c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = make(map[*client]struct{})
	m.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range clients {
		_ = c.conn.Close()
	}
	_ = os.Remove(m.socketPath)
}

// SocketPath returns the Unix socket path helpers should dial.
func (m *Manager) SocketPath() string { return m.socketPath }

// ClientCount returns the number of currently connected helpers.
func (m *Manager) ClientCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.clients)
}

// BroadcastConfig pushes the latest snapshot to all connected helpers.
func (m *Manager) BroadcastConfig() {
	snap := m.snapshotFn()

	m.mu.RLock()
	clients := make([]*client, 0, len(m.clients))
	for c := range m.clients {
		clients = append(clients, c)
	}
	m.mu.RUnlock()

	for _, c := range clients {
		if err := c.send(TypeConfigPush, snap); err != nil {
			log.Printf("[helper] config push failed: %v", err)
			m.removeClient(c)
		}
	}
}

// BroadcastInput sends an input injection event to all connected helpers.
func (m *Manager) BroadcastInput(input InputPayload) {
	m.mu.RLock()
	clients := make([]*client, 0, len(m.clients))
	for c := range m.clients {
		clients = append(clients, c)
	}
	m.mu.RUnlock()

	for _, c := range clients {
		if err := c.send(TypeInput, input); err != nil {
			log.Printf("[helper] input push failed: %v", err)
			m.removeClient(c)
		}
	}
}

// BroadcastClipboard sends a native clipboard entry to the local helper.
func (m *Manager) BroadcastClipboard(payload ClipboardPayload) {
	m.mu.RLock()
	clients := make([]*client, 0, len(m.clients))
	for c := range m.clients {
		clients = append(clients, c)
	}
	m.mu.RUnlock()
	for _, c := range clients {
		if err := c.send(TypeClipboardSet, payload); err != nil {
			log.Printf("[helper] clipboard update failed: %v", err)
			m.removeClient(c)
		}
	}
}

// RequestSecureValue performs a Keychain operation through the signed macOS helper.
func (m *Manager) RequestSecureValue(operation, key, value string) (string, error) {
	var requestIDBytes [12]byte
	if _, err := rand.Read(requestIDBytes[:]); err != nil {
		return "", err
	}
	requestID := hex.EncodeToString(requestIDBytes[:])
	responseCh := make(chan SecureResponse, 1)
	m.secureMu.Lock()
	m.secure[requestID] = responseCh
	m.secureMu.Unlock()

	m.mu.RLock()
	var target *client
	for c := range m.clients {
		target = c
		break
	}
	m.mu.RUnlock()
	if target == nil {
		m.secureMu.Lock()
		delete(m.secure, requestID)
		m.secureMu.Unlock()
		return "", fmt.Errorf("macOS helper is not connected")
	}
	if err := target.send(TypeSecureRequest, SecureRequest{
		RequestID: requestID,
		Operation: operation,
		Key:       key,
		Value:     value,
	}); err != nil {
		m.secureMu.Lock()
		delete(m.secure, requestID)
		m.secureMu.Unlock()
		return "", err
	}
	select {
	case response := <-responseCh:
		if response.Error != "" {
			return "", fmt.Errorf("Keychain %s: %s", operation, response.Error)
		}
		return response.Value, nil
	case <-time.After(5 * time.Second):
		m.secureMu.Lock()
		delete(m.secure, requestID)
		m.secureMu.Unlock()
		return "", fmt.Errorf("Keychain %s timed out", operation)
	}
}

func (m *Manager) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go m.handleConn(conn)
	}
}

func (m *Manager) handleConn(conn net.Conn) {
	c := &client{conn: conn}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	if !scanner.Scan() {
		_ = c.send(TypeError, ErrorPayload{Message: "failed to read register"})
		_ = conn.Close()
		return
	}
	var env Envelope
	if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
		_ = c.send(TypeError, ErrorPayload{Message: "failed to read register"})
		_ = conn.Close()
		return
	}
	if env.Type != TypeRegister {
		_ = c.send(TypeError, ErrorPayload{Message: "expected register as first message"})
		_ = conn.Close()
		return
	}

	var reg RegisterPayload
	if err := DecodePayload(env, &reg); err != nil {
		_ = c.send(TypeError, ErrorPayload{Message: "invalid register payload"})
		_ = conn.Close()
		return
	}

	m.mu.Lock()
	m.clients[c] = struct{}{}
	m.mu.Unlock()
	defer m.removeClient(c)

	log.Printf("[helper] connected name=%s pid=%d version=%s", reg.Name, reg.PID, reg.Version)

	if err := c.send(TypeAck, AckPayload{Message: "registered"}); err != nil {
		return
	}
	if err := c.send(TypeConfigPush, m.snapshotFn()); err != nil {
		return
	}

	for scanner.Scan() {
		var next Envelope
		if err := json.Unmarshal(scanner.Bytes(), &next); err != nil {
			continue
		}
		switch next.Type {
		case TypeHotkey:
			var hotkey HotkeyPayload
			if err := DecodePayload(next, &hotkey); err != nil {
				continue
			}
			if hotkey.Action != "" && m.onHotkey != nil {
				m.onHotkey(hotkey)
			}
		case TypeEdge:
			var edge EdgePayload
			if err := DecodePayload(next, &edge); err != nil {
				continue
			}
			if edge.Edge != "" && m.onEdge != nil {
				m.onEdge(edge)
			}
		case TypeInput:
			var input InputPayload
			if err := DecodePayload(next, &input); err != nil {
				continue
			}
			if input.Kind != "" && m.onInput != nil {
				m.onInput(input)
			}
		case TypeClipboard:
			var payload ClipboardPayload
			if err := DecodePayload(next, &payload); err != nil {
				continue
			}
			m.mu.RLock()
			handler := m.onClipboard
			m.mu.RUnlock()
			if handler != nil {
				handler(payload)
			}
		case TypeSecureResponse:
			var response SecureResponse
			if err := DecodePayload(next, &response); err != nil || response.RequestID == "" {
				continue
			}
			m.secureMu.Lock()
			ch := m.secure[response.RequestID]
			delete(m.secure, response.RequestID)
			m.secureMu.Unlock()
			if ch != nil {
				ch <- response
			}
		}
	}
}

func (m *Manager) removeClient(c *client) {
	_ = c.conn.Close()
	m.mu.Lock()
	delete(m.clients, c)
	m.mu.Unlock()
}

func (c *client) send(msgType string, payload interface{}) error {
	data, err := Encode(msgType, payload)
	if err != nil {
		return err
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_, err = c.conn.Write(data)
	return err
}
