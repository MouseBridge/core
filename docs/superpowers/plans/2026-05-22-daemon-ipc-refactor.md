# Daemon + IPC Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor the CLI into a long-running daemon (one per machine) that owns all TCP/pairing/session state, controlled by thin CLI clients over a configurable Unix Socket.

**Architecture:** Each machine runs `mousebridge daemon --socket <path>`. CLI commands (`serve`, `connect`, `pair`, `status`) connect to the daemon socket, send one JSON command, then stream back events until done. Multiple CLI clients (and later the UI) can connect simultaneously. Two daemons communicate with each other over TCP exactly as before.

**Tech Stack:** Go 1.22, cobra, standard library (net, encoding/json, os/signal). No new dependencies.

---

## File Map

```
# New files
internal/daemon/events.go          # IPC command + event struct definitions (JSON)
internal/daemon/ipc_server.go      # Unix Socket listener, fan-out event broadcaster
internal/daemon/ipc_client.go      # one connected IPC client handler (read cmd, write events)
internal/daemon/daemon.go          # Daemon struct: owns TCP server + IPC server + core state
internal/daemon/daemon_test.go     # integration test: daemon + two IPC clients

# New CLI commands
internal/cli/daemon.go             # `mousebridge daemon [--socket path] [--port N]`
internal/cli/pair.go               # `mousebridge pair accept|reject|pin <PIN>`

# Modified CLI commands (become thin IPC clients)
internal/cli/serve.go              # sends {"cmd":"serve"} → streams events
internal/cli/connect.go            # sends {"cmd":"connect","ip":...} → streams events
internal/cli/status.go             # sends {"cmd":"status"} → prints result + exits

# Modified entry point
cmd/mousebridge/main.go            # add PairCmd(), DaemonCmd()

# Unchanged packages (zero modifications)
internal/net/          # TCP conn/server/client
internal/pairing/      # PIN state machine
internal/session/      # latency tracking
internal/switch/       # edge + controller
internal/event/        # wire message types
internal/config/       # load/save
internal/ipc/          # Handler interface (keep as-is)
```

---

## Task 1: IPC event/command types

**Files:**
- Create: `internal/daemon/events.go`
- Create: `internal/daemon/events_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/daemon/events_test.go
package daemon_test

import (
	"encoding/json"
	"testing"

	"mousebridge/internal/daemon"
)

func TestEncodeCommand(t *testing.T) {
	cmd := daemon.Command{Cmd: "connect", IP: "192.168.1.5", Port: 39172}
	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got daemon.Command
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Cmd != "connect" || got.IP != "192.168.1.5" || got.Port != 39172 {
		t.Fatalf("round-trip failed: %+v", got)
	}
}

func TestEncodeEvent(t *testing.T) {
	ev := daemon.Event{Event: "pair_request", Name: "MacBook-Pro", PIN: "847291"}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got daemon.Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Event != "pair_request" || got.PIN != "847291" {
		t.Fatalf("round-trip failed: %+v", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/daemon/... 2>&1 | head -5
```
Expected: `cannot find package`

- [ ] **Step 3: Implement events.go**

```go
// internal/daemon/events.go
package daemon

// Command is sent by a CLI client to the daemon over Unix Socket.
type Command struct {
	Cmd      string `json:"cmd"`                 // "serve"|"connect"|"pair_accept"|"pair_reject"|"pair_pin"|"status"|"disconnect"
	IP       string `json:"ip,omitempty"`        // for "connect"
	Port     int    `json:"port,omitempty"`      // for "connect"
	PIN      string `json:"pin,omitempty"`       // for "pair_pin"
	DeviceID string `json:"device_id,omitempty"` // for "disconnect"
}

// Event is pushed from the daemon to all connected CLI clients.
type Event struct {
	Event      string  `json:"event"`                  // "listening"|"pair_request"|"paired"|"connected"|"disconnected"|"log"|"status"|"error"
	Port       int     `json:"port,omitempty"`         // for "listening"
	DeviceID   string  `json:"device_id,omitempty"`    // for device events
	Name       string  `json:"name,omitempty"`         // for device events
	PIN        string  `json:"pin,omitempty"`          // for "pair_request"
	IP         string  `json:"ip,omitempty"`           // for "connected"
	Msg        string  `json:"msg,omitempty"`          // for "log" and "error"
	Devices    []DeviceStatus `json:"devices,omitempty"` // for "status"
}

// DeviceStatus is included in "status" events.
type DeviceStatus struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	IP           string  `json:"ip"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/daemon/... -v -run TestEncode
```
Expected: `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/
git commit -m "feat: IPC command and event types"
```

---

## Task 2: IPC server (Unix Socket broadcaster)

**Files:**
- Create: `internal/daemon/ipc_server.go`
- Create: `internal/daemon/ipc_client.go`

- [ ] **Step 1: Implement ipc_client.go**

```go
// internal/daemon/ipc_client.go
package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
)

// ipcClient represents one connected CLI/UI client on the Unix Socket.
type ipcClient struct {
	conn   net.Conn
	writer *bufio.Writer
	mu     sync.Mutex
}

func newIPCClient(conn net.Conn) *ipcClient {
	return &ipcClient{conn: conn, writer: bufio.NewWriter(conn)}
}

// send writes an Event as a JSON Line to the client.
func (c *ipcClient) send(ev Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("ipc: marshal event: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.writer.Write(append(data, '\n')); err != nil {
		return err
	}
	return c.writer.Flush()
}

// readCommands reads JSON Lines from the client and calls handler for each.
func (c *ipcClient) readCommands(handler func(Command)) {
	scanner := bufio.NewScanner(c.conn)
	for scanner.Scan() {
		var cmd Command
		if err := json.Unmarshal(scanner.Bytes(), &cmd); err != nil {
			log.Printf("ipc: decode command: %v", err)
			continue
		}
		handler(cmd)
	}
}

func (c *ipcClient) close() {
	_ = c.conn.Close()
}
```

- [ ] **Step 2: Implement ipc_server.go**

```go
// internal/daemon/ipc_server.go
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

	// onCommand is called when any client sends a command.
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
```

- [ ] **Step 3: Build to verify**

```bash
go build ./internal/daemon/
```
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add internal/daemon/
git commit -m "feat: IPC Unix Socket server with fan-out broadcast"
```

---

## Task 3: Daemon core

**Files:**
- Create: `internal/daemon/daemon.go`
- Create: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Write failing test**

```go
// internal/daemon/daemon_test.go
package daemon_test

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mousebridge/internal/daemon"
)

func TestDaemonStartStop(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "mb.sock")
	d := daemon.New(daemon.Options{
		SocketPath: sockPath,
		TCPPort:    0, // random port
	})
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	// Socket file should exist
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket not created: %v", err)
	}
}

func TestDaemonBroadcastsStatusEvent(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "mb.sock")
	d := daemon.New(daemon.Options{
		SocketPath: sockPath,
		TCPPort:    0,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	// Connect a client
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send status command
	cmd := daemon.Command{Cmd: "status"}
	data, _ := json.Marshal(cmd)
	if _, err := conn.Write(append(data, '\n')); err != nil {
		t.Fatalf("write cmd: %v", err)
	}

	// Read one event back
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatalf("no event received: %v", scanner.Err())
	}
	var ev daemon.Event
	if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if ev.Event != "status" {
		t.Fatalf("want status event got %q", ev.Event)
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/daemon/... -run TestDaemon 2>&1 | head -10
```
Expected: compile error — `daemon.New` undefined

- [ ] **Step 3: Implement daemon.go**

```go
// internal/daemon/daemon.go
package daemon

import (
	"fmt"
	"log"
	"net"
	"time"

	"mousebridge/internal/config"
	"mousebridge/internal/event"
	mnet "mousebridge/internal/net"
	"mousebridge/internal/pairing"
	"mousebridge/internal/session"
	sw "mousebridge/internal/switch"
)

// Options configures the daemon.
type Options struct {
	SocketPath string
	TCPPort    int
	DeviceName string
}

// Daemon owns all long-running state: TCP listener, IPC server, sessions.
type Daemon struct {
	opts    Options
	cfg     *config.Config
	ipc     *IPCServer
	tcpSrv  *mnet.Server
	sess    *session.Manager
	ctrl    *sw.Controller
	stopCh  chan struct{}

	// pendingPair holds the pairing manager for the current in-progress pairing.
	pendingPair   *pairing.Manager
	pendingPairID string
	pendingConn   *mnet.Conn
}

// New creates a Daemon with the given options.
func New(opts Options) *Daemon {
	cfg := config.Default()
	if opts.DeviceName != "" {
		cfg.DeviceName = opts.DeviceName
	}
	if opts.TCPPort != 0 {
		cfg.Port = opts.TCPPort
	}
	if opts.SocketPath != "" {
		// stored in opts, not cfg
	}
	d := &Daemon{
		opts:   opts,
		cfg:    cfg,
		sess:   session.NewManager(),
		ctrl:   sw.NewController("local"),
		stopCh: make(chan struct{}),
	}
	d.ipc = NewIPCServer(opts.SocketPath, d.handleCommand)
	d.tcpSrv = mnet.NewServer(d.handleInbound)
	return d
}

// Start begins listening on the Unix Socket. TCP listening starts on demand via "serve" command.
func (d *Daemon) Start() error {
	if err := d.ipc.Start(); err != nil {
		return fmt.Errorf("daemon: ipc: %w", err)
	}
	log.Printf("[daemon] socket: %s", d.opts.SocketPath)

	// If TCPPort==0, find a free port and listen immediately for tests.
	if d.opts.TCPPort == 0 {
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			return err
		}
		d.opts.TCPPort = ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
	}
	return nil
}

// Stop shuts down all listeners cleanly.
func (d *Daemon) Stop() {
	close(d.stopCh)
	d.tcpSrv.Stop()
	d.ipc.Stop()
}

// handleCommand processes one command from a CLI/UI client.
func (d *Daemon) handleCommand(cmd Command) {
	switch cmd.Cmd {
	case "serve":
		d.cmdServe()
	case "connect":
		go d.cmdConnect(cmd.IP, cmd.Port)
	case "pair_accept":
		d.cmdPairAccept()
	case "pair_reject":
		d.cmdPairReject()
	case "pair_pin":
		go d.cmdPairPIN(cmd.PIN)
	case "status":
		d.cmdStatus()
	case "disconnect":
		d.cmdDisconnect(cmd.DeviceID)
	default:
		d.broadcast(Event{Event: "error", Msg: fmt.Sprintf("unknown command: %s", cmd.Cmd)})
	}
}

func (d *Daemon) cmdServe() {
	port := d.cfg.Port
	if d.opts.TCPPort != 0 {
		port = d.opts.TCPPort
	}
	if err := d.tcpSrv.Listen(port); err != nil {
		d.broadcast(Event{Event: "error", Msg: err.Error()})
		return
	}
	d.broadcast(Event{Event: "listening", Port: port})
	log.Printf("[daemon] TCP listening on :%d", port)
}

func (d *Daemon) cmdConnect(ip string, port int) {
	if port == 0 {
		port = d.cfg.Port
	}
	d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("connecting to %s:%d ...", ip, port)})

	c, err := mnet.Dial(ip, port)
	if err != nil {
		d.broadcast(Event{Event: "error", Msg: err.Error()})
		return
	}
	go d.runHostSession(c)
}

func (d *Daemon) cmdPairAccept() {
	if d.pendingPair == nil {
		d.broadcast(Event{Event: "error", Msg: "no pending pair request"})
		return
	}
	if err := d.pendingPair.Accept(); err != nil {
		d.broadcast(Event{Event: "error", Msg: err.Error()})
		return
	}
	_ = d.pendingConn.Send(event.Message{
		V: 1, Seq: 3, Type: event.TypePairAccept, Ts: nowMs(), Payload: struct{}{},
	})
	d.broadcast(Event{Event: "paired", DeviceID: d.pendingPairID, Name: d.pendingPair.PeerName()})
	log.Printf("[daemon] paired with %s (accept)", d.pendingPair.PeerName())
	conn := d.pendingConn
	peerID := d.pendingPairID
	peerName := d.pendingPair.PeerName()
	d.pendingPair = nil
	d.pendingConn = nil
	d.pendingPairID = ""
	go d.runSlaveSession(conn, peerID, peerName)
}

func (d *Daemon) cmdPairReject() {
	if d.pendingPair == nil {
		d.broadcast(Event{Event: "error", Msg: "no pending pair request"})
		return
	}
	_ = d.pendingPair.Reject()
	_ = d.pendingConn.Send(event.Message{
		V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{},
	})
	d.broadcast(Event{Event: "log", Msg: "pairing rejected"})
	d.pendingConn.Close()
	d.pendingPair = nil
	d.pendingConn = nil
	d.pendingPairID = ""
}

func (d *Daemon) cmdPairPIN(pin string) {
	// PIN path is handled on the host side in runHostSession.
	// This command is a no-op here; host sends pair_confirm directly.
	d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("PIN %s submitted (not applicable on slave side)", pin)})
}

func (d *Daemon) cmdStatus() {
	devs := d.sess.Devices()
	statuses := make([]DeviceStatus, 0, len(devs))
	for _, dv := range devs {
		statuses = append(statuses, DeviceStatus{
			ID:           dv.ID,
			Name:         dv.Name,
			AvgLatencyMs: dv.AvgLatencyMs,
		})
	}
	d.broadcast(Event{Event: "status", Devices: statuses})
}

func (d *Daemon) cmdDisconnect(deviceID string) {
	d.sess.Remove(deviceID)
	d.broadcast(Event{Event: "disconnected", DeviceID: deviceID})
}

// handleInbound is called by the TCP server for each incoming connection (slave role).
func (d *Daemon) handleInbound(c *mnet.Conn) {
	defer func() {
		if d.pendingConn == c {
			d.pendingConn = nil
			d.pendingPair = nil
			d.pendingPairID = ""
		}
		c.Close()
	}()
	remote := c.RemoteAddr().String()

	// Handshake
	hs, err := c.Recv()
	if err != nil {
		log.Printf("[daemon] handshake from %s: %v", remote, err)
		return
	}
	var hsPay event.HandshakePayload
	if err := event.DecodePayload(hs, &hsPay); err != nil || hs.Type != event.TypeHandshake {
		return
	}
	_ = c.Send(event.Message{
		V: 1, Seq: 1, Type: event.TypeHandshake, Ts: nowMs(),
		Payload: event.HandshakePayload{DeviceID: "local", Name: d.cfg.DeviceName, Platform: "macos"},
	})

	// Pair request
	pairMsg, err := c.Recv()
	if err != nil || pairMsg.Type != event.TypePairRequest {
		return
	}
	var prPay event.PairRequestPayload
	_ = event.DecodePayload(pairMsg, &prPay)

	mgr := pairing.NewManager()
	pin, err := mgr.StartAsSlave(prPay.DeviceID, prPay.Name)
	if err != nil {
		return
	}

	d.pendingPair = mgr
	d.pendingPairID = prPay.DeviceID
	d.pendingConn = c

	_ = c.Send(event.Message{
		V: 1, Seq: 2, Type: event.TypePairPin, Ts: nowMs(),
		Payload: event.PairPinPayload{PIN: pin},
	})
	d.broadcast(Event{
		Event: "pair_request", DeviceID: prPay.DeviceID, Name: prPay.Name, PIN: pin,
	})
	log.Printf("[daemon] pair_request from %s, PIN=%s", prPay.Name, pin)

	// Wait for pair_confirm from host (PIN path), or cmdPairAccept/Reject from CLI.
	// Poll the pairing manager state.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		// Check if CLI accepted/rejected already.
		state := mgr.State()
		if state == pairing.StatePaired || state == pairing.StateRejected {
			return
		}
		// Check for incoming pair_confirm message.
		_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		msg, err := c.Recv()
		_ = c.SetReadDeadline(time.Time{})
		if err != nil {
			continue
		}
		if msg.Type == event.TypePairConfirm {
			var pay event.PairConfirmPayload
			_ = event.DecodePayload(msg, &pay)
			if err := mgr.ConfirmPIN(pay.PIN); err != nil {
				_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{}})
				d.broadcast(Event{Event: "error", Msg: "wrong PIN"})
				return
			}
			_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairAccept, Ts: nowMs(), Payload: struct{}{}})
			d.broadcast(Event{Event: "paired", DeviceID: prPay.DeviceID, Name: prPay.Name})
			d.pendingPair = nil
			d.pendingConn = nil
			d.pendingPairID = ""
			go d.runSlaveSession(c, prPay.DeviceID, prPay.Name)
			return
		}
	}
	// Timeout
	d.broadcast(Event{Event: "error", Msg: "pairing timed out"})
	_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{}})
	d.pendingPair = nil
	d.pendingConn = nil
	d.pendingPairID = ""
}

// runHostSession runs after successful pairing on the host side.
func (d *Daemon) runHostSession(c *mnet.Conn) {
	defer c.Close()

	// Handshake
	if err := c.Send(event.Message{
		V: 1, Seq: 1, Type: event.TypeHandshake, Ts: nowMs(),
		Payload: event.HandshakePayload{DeviceID: "local", Name: d.cfg.DeviceName, Platform: "macos"},
	}); err != nil {
		d.broadcast(Event{Event: "error", Msg: "handshake: " + err.Error()})
		return
	}
	hsReply, err := c.Recv()
	if err != nil || hsReply.Type != event.TypeHandshake {
		d.broadcast(Event{Event: "error", Msg: "handshake reply failed"})
		return
	}
	var hsPay event.HandshakePayload
	_ = event.DecodePayload(hsReply, &hsPay)
	remoteName := hsPay.Name
	remoteID := hsPay.DeviceID

	// Send pair request
	if err := c.Send(event.Message{
		V: 1, Seq: 2, Type: event.TypePairRequest, Ts: nowMs(),
		Payload: event.PairRequestPayload{DeviceID: "local", Name: d.cfg.DeviceName},
	}); err != nil {
		d.broadcast(Event{Event: "error", Msg: err.Error()})
		return
	}

	// Receive PIN
	pinMsg, err := c.Recv()
	if err != nil || pinMsg.Type != event.TypePairPin {
		d.broadcast(Event{Event: "error", Msg: "expected pair_pin"})
		return
	}
	var pinPay event.PairPinPayload
	_ = event.DecodePayload(pinMsg, &pinPay)
	d.broadcast(Event{Event: "pair_request", DeviceID: remoteID, Name: remoteName, PIN: pinPay.PIN})
	log.Printf("[daemon] remote PIN: %s (waiting for remote accept or local pair_pin cmd)", pinPay.PIN)

	// Wait for pair_accept/reject from remote
	pairResult, err := c.Recv()
	if err != nil {
		d.broadcast(Event{Event: "error", Msg: "pairing connection lost"})
		return
	}
	if pairResult.Type == event.TypePairReject {
		d.broadcast(Event{Event: "error", Msg: "pairing rejected by remote"})
		return
	}
	if pairResult.Type != event.TypePairAccept {
		d.broadcast(Event{Event: "error", Msg: "unexpected pairing message: " + pairResult.Type})
		return
	}

	d.broadcast(Event{Event: "paired", DeviceID: remoteID, Name: remoteName})
	d.sess.Add(remoteID, remoteName)
	d.broadcast(Event{Event: "connected", DeviceID: remoteID, Name: remoteName, IP: c.RemoteAddr().String()})
	log.Printf("[daemon] connected to %s (%s)", remoteName, c.RemoteAddr())

	var seq int64 = 3
	for {
		msg, err := c.Recv()
		if err != nil {
			d.broadcast(Event{Event: "disconnected", DeviceID: remoteID, Name: remoteName})
			d.sess.Remove(remoteID)
			return
		}
		latency := float64(nowMs() - msg.Ts)
		d.sess.RecordLatency(remoteID, latency)
		switch msg.Type {
		case event.TypePong:
			var pay event.PongPayload
			_ = event.DecodePayload(msg, &pay)
			rtt := float64(nowMs() - pay.EchoTs)
			d.sess.RecordLatency(remoteID, rtt)
			d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("pong rtt=%.1fms", rtt)})
		case event.TypeSwitchAck:
			d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("switch_ack from %s", remoteName)})
		default:
			d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("recv %s latency=%.1fms", msg.Type, latency)})
		}
		seq++
	}
}

// runSlaveSession runs the event loop after pairing on the slave side.
func (d *Daemon) runSlaveSession(c *mnet.Conn, peerID, peerName string) {
	d.sess.Add(peerID, peerName)
	d.broadcast(Event{Event: "connected", DeviceID: peerID, Name: peerName, IP: c.RemoteAddr().String()})
	log.Printf("[daemon] slave session started with %s", peerName)

	var seq int64 = 4
	for {
		msg, err := c.Recv()
		if err != nil {
			d.broadcast(Event{Event: "disconnected", DeviceID: peerID, Name: peerName})
			d.sess.Remove(peerID)
			return
		}
		latency := float64(nowMs() - msg.Ts)
		d.sess.RecordLatency(peerID, latency)
		switch msg.Type {
		case event.TypePing:
			_ = c.Send(event.Message{
				V: 1, Seq: seq, Type: event.TypePong, Ts: nowMs(),
				Payload: event.PongPayload{EchoTs: msg.Ts},
			})
			d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("recv ping latency=%.1fms", latency)})
			seq++
		case event.TypeSwitchRequest:
			var pay event.SwitchRequestPayload
			_ = event.DecodePayload(msg, &pay)
			d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("recv switch_request trigger=%s entry=%s@%.0f%% latency=%.1fms",
				pay.Trigger, pay.Edge, pay.EntryPct*100, latency)})
			d.ctrl.SwitchTo(peerID)
			_ = c.Send(event.Message{V: 1, Seq: seq, Type: event.TypeSwitchAck, Ts: nowMs(), Payload: struct{}{}})
			seq++
		default:
			d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("recv %s latency=%.1fms", msg.Type, latency)})
		}
	}
}

func (d *Daemon) broadcast(ev Event) {
	d.ipc.Broadcast(ev)
}

// SetReadDeadline exposes deadline setting on mnet.Conn (needed for polling).
func (c *mnet.Conn) SetReadDeadline(t time.Time) error {
	return c.Raw().SetReadDeadline(t)
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}
```

- [ ] **Step 4: Add Raw() to mnet.Conn**

`internal/net/conn.go` — add one method after `RemoteAddr()`:

```go
// Raw returns the underlying net.Conn (needed for deadline control).
func (c *Conn) Raw() net.Conn {
	return c.raw
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/daemon/... -v
```
Expected: `PASS`

- [ ] **Step 6: Build**

```bash
go build ./...
```
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/ internal/net/conn.go
git commit -m "feat: daemon core with IPC server and TCP session management"
```

---

## Task 4: IPC client helper + thin CLI wrapper

**Files:**
- Create: `internal/daemon/connect_client.go`  (IPC client Go helper used by CLI commands)
- Modify: `internal/cli/serve.go`
- Modify: `internal/cli/connect.go`
- Modify: `internal/cli/status.go`
- Create: `internal/cli/daemon.go`
- Create: `internal/cli/pair.go`
- Modify: `cmd/mousebridge/main.go`

- [ ] **Step 1: Create IPC client helper**

```go
// internal/daemon/connect_client.go
package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
)

// DialSocket connects to a daemon Unix Socket and returns the connection.
func DialSocket(socketPath string) (net.Conn, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to daemon at %s — is it running? (mousebridge daemon --socket %s)", socketPath, socketPath)
	}
	return conn, nil
}

// SendCommand sends one Command to the daemon and returns the connection
// (caller should read events and close when done).
func SendCommand(conn net.Conn, cmd Command) error {
	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

// ReadEvents reads Event lines from conn and calls handler for each.
// Returns when conn closes or handler returns false.
func ReadEvents(conn net.Conn, handler func(Event) bool) error {
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if !handler(ev) {
			return nil
		}
	}
	return scanner.Err()
}
```

- [ ] **Step 2: Rewrite serve.go as thin client**

```go
// internal/cli/serve.go
package cli

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"mousebridge/internal/daemon"
)

func ServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Tell daemon to listen for incoming TCP connections",
		RunE:  runServe,
	}
	cmd.Flags().IntP("port", "p", 0, "TCP port (overrides config)")
	return cmd
}

func runServe(cmd *cobra.Command, args []string) error {
	socketPath := socketFlag(cmd)
	port, _ := cmd.Flags().GetInt("port")

	conn, err := daemon.DialSocket(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := daemon.SendCommand(conn, daemon.Command{Cmd: "serve", Port: port}); err != nil {
		return err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	doneCh := make(chan struct{})
	go func() {
		daemon.ReadEvents(conn, func(ev daemon.Event) bool {
			printEvent(ev)
			return true
		})
		close(doneCh)
	}()

	select {
	case <-sigCh:
		fmt.Println()
	case <-doneCh:
	}
	return nil
}
```

- [ ] **Step 3: Rewrite connect.go as thin client**

```go
// internal/cli/connect.go
package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"mousebridge/internal/daemon"
)

func ConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect <ip>",
		Short: "Tell daemon to connect to a remote device",
		Args:  cobra.ExactArgs(1),
		RunE:  runConnect,
	}
	cmd.Flags().IntP("port", "p", 39172, "remote TCP port")
	return cmd
}

func runConnect(cmd *cobra.Command, args []string) error {
	socketPath := socketFlag(cmd)
	ip := args[0]
	port, _ := cmd.Flags().GetInt("port")

	conn, err := daemon.DialSocket(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := daemon.SendCommand(conn, daemon.Command{Cmd: "connect", IP: ip, Port: port}); err != nil {
		return err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	doneCh := make(chan struct{})
	go func() {
		daemon.ReadEvents(conn, func(ev daemon.Event) bool {
			printEvent(ev)
			return true
		})
		close(doneCh)
	}()

	select {
	case <-sigCh:
		fmt.Println()
	case <-doneCh:
	}
	return nil
}
```

- [ ] **Step 4: Rewrite status.go as thin client**

```go
// internal/cli/status.go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"mousebridge/internal/daemon"
)

func StatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon connection status",
		RunE: func(cmd *cobra.Command, args []string) error {
			socketPath := socketFlag(cmd)
			conn, err := daemon.DialSocket(socketPath)
			if err != nil {
				return err
			}
			defer conn.Close()

			if err := daemon.SendCommand(conn, daemon.Command{Cmd: "status"}); err != nil {
				return err
			}

			return daemon.ReadEvents(conn, func(ev daemon.Event) bool {
				if ev.Event == "status" {
					if len(ev.Devices) == 0 {
						fmt.Println("No connected devices.")
					}
					for _, d := range ev.Devices {
						fmt.Printf("  %s (%s)  avg_latency=%.1fms\n", d.Name, d.ID[:8], d.AvgLatencyMs)
					}
					return false // stop after first status event
				}
				return true
			})
		},
	}
}
```

- [ ] **Step 5: Create daemon.go CLI command**

```go
// internal/cli/daemon.go
package cli

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"mousebridge/internal/config"
	"mousebridge/internal/daemon"
)

func DaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the background daemon",
		RunE:  runDaemon,
	}
	cmd.Flags().IntP("port", "p", 0, "TCP port (default: from config)")
	return cmd
}

func runDaemon(cmd *cobra.Command, args []string) error {
	socketPath := socketFlag(cmd)
	port, _ := cmd.Flags().GetInt("port")

	cfg, err := loadConfig()
	if err != nil {
		cfg = config.Default()
	}
	if port == 0 {
		port = cfg.Port
	}

	d := daemon.New(daemon.Options{
		SocketPath: socketPath,
		TCPPort:    port,
		DeviceName: cfg.DeviceName,
	})

	if err := d.Start(); err != nil {
		return err
	}
	log.Printf("[daemon] started — socket=%s port=%d device=%s", socketPath, port, cfg.DeviceName)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[daemon] shutting down...")
	d.Stop()
	return nil
}
```

- [ ] **Step 6: Create pair.go CLI command**

```go
// internal/cli/pair.go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"mousebridge/internal/daemon"
)

func PairCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Manage pairing with a remote device",
	}
	cmd.AddCommand(pairAcceptCmd())
	cmd.AddCommand(pairRejectCmd())
	cmd.AddCommand(pairPINCmd())
	return cmd
}

func pairAcceptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "accept",
		Short: "Accept the pending pair request",
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendOneCommand(socketFlag(cmd), daemon.Command{Cmd: "pair_accept"})
		},
	}
}

func pairRejectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reject",
		Short: "Reject the pending pair request",
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendOneCommand(socketFlag(cmd), daemon.Command{Cmd: "pair_reject"})
		},
	}
}

func pairPINCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pin <PIN>",
		Short: "Submit a PIN to the remote device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendOneCommand(socketFlag(cmd), daemon.Command{Cmd: "pair_pin", PIN: args[0]})
		},
	}
}

// sendOneCommand sends a command and reads until the daemon closes or sends an error/log.
func sendOneCommand(socketPath string, cmd daemon.Command) error {
	conn, err := daemon.DialSocket(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := daemon.SendCommand(conn, cmd); err != nil {
		return err
	}

	return daemon.ReadEvents(conn, func(ev daemon.Event) bool {
		printEvent(ev)
		// stop after first event (ack or error)
		return false
	})
}
```

- [ ] **Step 7: Create shared helpers file**

```go
// internal/cli/helpers.go
package cli

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"mousebridge/internal/config"
	"mousebridge/internal/daemon"
)

const defaultSocketPath = "~/.mousebridge/mb.sock"

// socketFlag returns the --socket flag value, expanding ~ if needed.
func socketFlag(cmd *cobra.Command) string {
	root := cmd.Root()
	s, _ := root.PersistentFlags().GetString("socket")
	if s == "" {
		s = defaultSocketPath
	}
	if len(s) >= 2 && s[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err == nil {
			s = filepath.Join(home, s[2:])
		}
	}
	return s
}

// printEvent formats and prints a daemon event to stdout.
func printEvent(ev daemon.Event) {
	switch ev.Event {
	case "log":
		log.Printf("%s", ev.Msg)
	case "error":
		log.Printf("[error] %s", ev.Msg)
	case "pair_request":
		log.Printf("[pair] request from %s — PIN: %s", ev.Name, ev.PIN)
		log.Printf("[pair] run: mousebridge pair accept  OR  mousebridge pair reject")
	case "paired":
		log.Printf("[pair] paired with %s", ev.Name)
	case "connected":
		log.Printf("[conn] connected to %s (%s)", ev.Name, ev.IP)
	case "disconnected":
		log.Printf("[conn] disconnected from %s", ev.Name)
	case "listening":
		log.Printf("[serve] listening on :%d", ev.Port)
	case "status":
		if len(ev.Devices) == 0 {
			fmt.Println("No connected devices.")
			return
		}
		for _, d := range ev.Devices {
			fmt.Printf("  %-20s %s  avg=%.1fms\n", d.Name, d.IP, d.AvgLatencyMs)
		}
	}
}

func loadConfig() (*config.Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return config.Default(), nil
	}
	return config.Load(filepath.Join(home, ".mousebridge", "config.json"))
}
```

- [ ] **Step 8: Update main.go**

```go
// cmd/mousebridge/main.go
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"mousebridge/internal/cli"
)

func main() {
	root := &cobra.Command{
		Use:   "mousebridge",
		Short: "LAN mouse/keyboard sharing",
	}

	// Global --socket flag available to all subcommands
	root.PersistentFlags().String("socket", "", "daemon Unix socket path (default: ~/.mousebridge/mb.sock)")

	root.AddCommand(cli.DaemonCmd())
	root.AddCommand(cli.ServeCmd())
	root.AddCommand(cli.ConnectCmd())
	root.AddCommand(cli.StatusCmd())
	root.AddCommand(cli.PairCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 9: Build**

```bash
go build ./cmd/mousebridge/ 2>&1
```
Expected: no errors

- [ ] **Step 10: Commit**

```bash
git add internal/cli/ internal/daemon/ cmd/mousebridge/main.go
git commit -m "feat: thin CLI clients, daemon command, pair subcommands"
```

---

## Task 5: Remove old serve/connect logic + cleanup

**Files:**
- Delete: `internal/cli/serve.go` helper functions that are now in `helpers.go`
- Verify: no leftover `startStdinBroadcast`, `stdinLines`, `handleInbound`, `loadConfig`, `ts()`, `nowMs()` in cli package

- [ ] **Step 1: Check for duplicate symbols**

```bash
grep -rn "func loadConfig\|func ts()\|func nowMs\|stdinLines\|startStdinBroadcast" internal/cli/
```
Expected: each function defined exactly once (in `helpers.go` or the file that owns it)

- [ ] **Step 2: Fix any duplicates found**

If `loadConfig`, `ts()`, `nowMs()` appear in old serve.go/connect.go, remove them — they now live in `helpers.go`.

- [ ] **Step 3: Run all tests**

```bash
go test ./... 2>&1
```
Expected: all PASS

- [ ] **Step 4: Build final binary**

```bash
go build -o mousebridge ./cmd/mousebridge/
```

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "chore: remove legacy CLI helpers, all tests pass"
```

---

## Task 6: Local two-daemon smoke test

This task has no code — it verifies the system works with two daemons on one Mac.

- [ ] **Step 1: Build**

```bash
go build -o mousebridge ./cmd/mousebridge/
```

- [ ] **Step 2: Start daemon A (slave role)**

Terminal A:
```bash
./mousebridge daemon --socket /tmp/mb-a.sock --port 39172
```
Expected:
```
[daemon] started — socket=/tmp/mb-a.sock port=39172 device=<hostname>
```

- [ ] **Step 3: Tell daemon A to serve**

Terminal B:
```bash
./mousebridge --socket /tmp/mb-a.sock serve
```
Expected:
```
[serve] listening on :39172
```

- [ ] **Step 4: Start daemon B (host role)**

Terminal C:
```bash
./mousebridge daemon --socket /tmp/mb-b.sock --port 39173
```

- [ ] **Step 5: Tell daemon B to connect to daemon A**

Terminal D:
```bash
./mousebridge --socket /tmp/mb-b.sock connect 127.0.0.1 --port 39172
```
Expected on Terminal D:
```
[pair] request from <hostname> — PIN: xxxxxx
[pair] run: mousebridge pair accept  OR  mousebridge pair reject
```
Expected on Terminal B (watching daemon A):
```
[pair] request from <hostname> — PIN: xxxxxx
```

- [ ] **Step 6: Accept pairing from daemon A side**

Terminal E:
```bash
./mousebridge --socket /tmp/mb-a.sock pair accept
```
Expected on Terminal B and D:
```
[pair] paired with <hostname>
[conn] connected to <hostname> (127.0.0.1:...)
```

- [ ] **Step 7: Check status**

```bash
./mousebridge --socket /tmp/mb-a.sock status
./mousebridge --socket /tmp/mb-b.sock status
```
Expected: each shows the other device connected.

- [ ] **Step 8: Commit smoke test confirmation**

```bash
git commit --allow-empty -m "test: two-daemon smoke test passed on loopback"
```

---

## Definition of Done

- [ ] `go test ./...` all PASS
- [ ] `mousebridge daemon --socket /tmp/mb-a.sock` starts and creates socket file
- [ ] `mousebridge --socket /tmp/mb-a.sock serve` tells daemon to listen
- [ ] `mousebridge --socket /tmp/mb-b.sock connect 127.0.0.1` triggers pair_request event on both sides
- [ ] `mousebridge --socket /tmp/mb-a.sock pair accept` completes pairing
- [ ] `mousebridge --socket /tmp/mb-a.sock status` shows connected device
- [ ] Both daemons keep running after CLI client disconnects
- [ ] Every task has its own git commit
