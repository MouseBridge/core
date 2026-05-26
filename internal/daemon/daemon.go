package daemon

import (
	"fmt"
	"net"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/p2p"
	"github.com/mousebridge/core/internal/session"
	sw "github.com/mousebridge/core/internal/switch"
	"github.com/mousebridge/core/internal/transport"
	"github.com/mousebridge/core/internal/trusted"
)

// Options configures the daemon.
type Options struct {
	SocketPath string
	TCPPort    int
	DeviceID   string
	DeviceName string
	ConfigPath string
}

// Daemon owns all long-running state: IPC server, sessions, connection tracking.
type Daemon struct {
	opts       Options
	configPath string
	cfg        *config.Config
	ipc        *IPCServer
	sess       *session.Manager
	ctrl       *sw.Controller
	trust      *trusted.Store

	pairMu          sync.Mutex
	pendingPairs    map[string]*p2p.PendingSlaveEntry
	pendingHostConn *transport.Conn

	connsMu sync.Mutex
	conns   map[*transport.Conn]string

	subsMu sync.RWMutex
	subs   map[chan Event]struct{}

	// httpConnCh receives raw HTTP connections from the mux for the API server.
	httpConnCh chan net.Conn

	// serveLn is the listener opened by cmdServe; nil when not serving.
	serveLn net.Listener
}

// New creates a Daemon with the given options.
func New(opts Options) *Daemon {
	cfg := config.Default()
	if opts.DeviceID != "" {
		cfg.DeviceID = opts.DeviceID
	}
	if opts.DeviceName != "" {
		cfg.DeviceName = opts.DeviceName
	}
	if opts.TCPPort != 0 {
		cfg.Port = opts.TCPPort
		if opts.DeviceID == "" {
			cfg.DeviceID = config.DeriveDeviceID(cfg.Port)
		}
	}
	trustStore, err := trusted.New(cfg.TrustedDevicesFile)
	if err != nil {
		log.Warnf("[daemon] failed to load trusted devices: %v", err)
		trustStore, _ = trusted.New("")
	}
	d := &Daemon{
		opts:         opts,
		configPath:   opts.ConfigPath,
		cfg:          cfg,
		sess:         session.NewManager(),
		ctrl:         sw.NewController("local"),
		trust:        trustStore,
		pendingPairs: make(map[string]*p2p.PendingSlaveEntry),
		conns:        make(map[*transport.Conn]string),
		subs:         make(map[chan Event]struct{}),
		httpConnCh:   make(chan net.Conn, 64),
	}
	d.ipc = NewIPCServer(opts.SocketPath, d.handleCommand)
	return d
}

// Start begins listening on the Unix Socket.
func (d *Daemon) Start() error {
	if err := d.ipc.Start(); err != nil {
		return fmt.Errorf("daemon: ipc: %w", err)
	}
	log.Printf("[daemon] socket: %s", d.opts.SocketPath)
	return nil
}

// Stop shuts down IPC and closes all tracked P2P connections.
func (d *Daemon) Stop() {
	d.connsMu.Lock()
	conns := d.conns
	d.conns = make(map[*transport.Conn]string)
	d.connsMu.Unlock()
	for c := range conns {
		c.Close()
	}
	d.ipc.Stop()
	close(d.httpConnCh)
}

// HTTPConnCh returns the channel that receives raw HTTP connections from the mux.
// The api.Server should drain this channel via a chanListener.
func (d *Daemon) HTTPConnCh() <-chan net.Conn {
	return d.httpConnCh
}

// handleHTTP is called by the mux for each HTTP connection.
func (d *Daemon) handleHTTP(c net.Conn) {
	select {
	case d.httpConnCh <- c:
	default:
		log.Printf("[daemon] httpConnCh full, dropping connection from %s", c.RemoteAddr())
		_ = c.Close()
	}
}

// handleP2P is called by the mux for each P2P connection.
func (d *Daemon) handleP2P(c *transport.Conn) {
	go p2p.HandleInbound(c, d, d, d.emitP2PEvent)
}

// emitP2PEvent converts a p2p.Event to a daemon.Event and broadcasts it.
func (d *Daemon) emitP2PEvent(ev p2p.Event) {
	switch ev.Kind {
	case "error":
		d.broadcast(Event{Event: "error", Msg: ev.Msg})
	case "log":
		d.broadcast(Event{Event: "log", Msg: ev.Msg})
	case "paired":
		d.broadcast(Event{Event: "paired", DeviceID: ev.DeviceID, Name: ev.Name})
		log.Printf("[daemon] paired with %s (id=%s) — to trust: mousebridge trust add %s --name '%s'",
			ev.Name, ev.DeviceID, ev.DeviceID, ev.Name)
	case "pair_request":
		d.broadcast(Event{Event: "pair_request", DeviceID: ev.DeviceID, Name: ev.Name, PIN: ev.PIN, Role: ev.Role})
		if ev.PIN != "" {
			log.Printf("[daemon] pair_request from %s (id=%s) — PIN: %s", ev.Name, ev.DeviceID, ev.PIN)
		} else {
			log.Printf("[daemon] pairing with %s — enter PIN shown on remote device", ev.Name)
		}
	case "connected":
		d.broadcast(Event{Event: "connected", DeviceID: ev.DeviceID, Name: ev.Name, IP: ev.IP})
		log.Printf("[daemon] connected to %s (%s)", ev.Name, ev.IP)
	case "disconnected":
		d.broadcast(Event{Event: "disconnected", DeviceID: ev.DeviceID, Name: ev.Name})
	}
}

// p2p.InboundHost interface
func (d *Daemon) LocalID() string            { return d.cfg.DeviceID }
func (d *Daemon) LocalName() string          { return d.cfg.DeviceName }
func (d *Daemon) IsTrusted(id string) bool   { return d.trust.IsTrusted(id) }

func (d *Daemon) AddPendingSlave(deviceID string, entry *p2p.PendingSlaveEntry) {
	d.pairMu.Lock()
	d.pendingPairs[deviceID] = entry
	d.pairMu.Unlock()
}

func (d *Daemon) RemovePendingSlave(deviceID string) {
	d.pairMu.Lock()
	delete(d.pendingPairs, deviceID)
	d.pairMu.Unlock()
}

// p2p.Host interface
func (d *Daemon) TrackConn(c *transport.Conn, deviceID string) {
	d.connsMu.Lock()
	d.conns[c] = deviceID
	d.connsMu.Unlock()
}

func (d *Daemon) UntrackConn(c *transport.Conn) {
	d.connsMu.Lock()
	delete(d.conns, c)
	d.connsMu.Unlock()
}

func (d *Daemon) SessionAdd(id, name, ip string)             { d.sess.Add(id, name, ip) }
func (d *Daemon) SessionRemove(id string)                    { d.sess.Remove(id) }
func (d *Daemon) SessionRecordLatency(id string, ms float64) { d.sess.RecordLatency(id, ms) }

func (d *Daemon) SetPendingHostConn(c *transport.Conn) {
	d.pairMu.Lock()
	d.pendingHostConn = c
	d.pairMu.Unlock()
}

func (d *Daemon) ClearPendingHostConn(c *transport.Conn) {
	d.pairMu.Lock()
	if d.pendingHostConn == c {
		d.pendingHostConn = nil
	}
	d.pairMu.Unlock()
}

func (d *Daemon) closeConnByDeviceID(deviceID string) {
	d.connsMu.Lock()
	var target *transport.Conn
	for c, id := range d.conns {
		if id == deviceID {
			target = c
			break
		}
	}
	d.connsMu.Unlock()
	if target != nil {
		target.Close()
	}
}

// Serve starts the TCP listener on the given port (P2P + HTTP mux).
// Called by cmdServe and the --serve flag.
func (d *Daemon) Serve(port int) { d.cmdServe(port) }

// Connect dials a remote daemon (host role).
func (d *Daemon) Connect(ip string, port int) { d.cmdConnect(ip, port) }

func (d *Daemon) cmdConnect(ip string, port int) {
	if port == 0 {
		port = d.cfg.Port
	}
	d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("connecting to %s:%d ...", ip, port)})
	c, err := transport.Dial(ip, port)
	if err != nil {
		d.broadcast(Event{Event: "error", Msg: err.Error()})
		return
	}
	go p2p.RunHostSession(c, d.cfg.DeviceID, d.cfg.DeviceName, d, d.emitP2PEvent)
}

// HandleCommand is the public entry point for REST/UI callers.
func (d *Daemon) HandleCommand(cmd Command) { d.handleCommand(cmd) }

// Subscribe returns a channel that receives every broadcasted Event.
func (d *Daemon) Subscribe() chan Event {
	ch := make(chan Event, 64)
	d.subsMu.Lock()
	d.subs[ch] = struct{}{}
	d.subsMu.Unlock()
	return ch
}

// Unsubscribe removes the channel and closes it.
func (d *Daemon) Unsubscribe(ch chan Event) {
	d.subsMu.Lock()
	delete(d.subs, ch)
	d.subsMu.Unlock()
	close(ch)
}

func (d *Daemon) broadcast(ev Event) {
	d.ipc.Broadcast(ev)
	d.subsMu.RLock()
	subs := make([]chan Event, 0, len(d.subs))
	for ch := range d.subs {
		subs = append(subs, ch)
	}
	d.subsMu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// DaemonState holds a snapshot of the daemon's current runtime state.
type DaemonState struct {
	Serving bool           `json:"serving"`
	Port    int            `json:"port,omitempty"`
	Devices []DeviceStatus `json:"devices"`
}

// State returns a consistent snapshot.
func (d *Daemon) State() DaemonState {
	devs := d.sess.Devices()
	statuses := make([]DeviceStatus, 0, len(devs))
	for _, dv := range devs {
		statuses = append(statuses, DeviceStatus{
			ID:           dv.ID,
			Name:         dv.Name,
			IP:           dv.IP,
			AvgLatencyMs: dv.AvgLatencyMs,
		})
	}
	serving, port := d.servingState()
	return DaemonState{
		Serving: serving,
		Port:    port,
		Devices: statuses,
	}
}

// Config returns the daemon's current configuration.
func (d *Daemon) Config() *config.Config { return d.cfg }

// TrustedList returns all trusted devices.
func (d *Daemon) TrustedList() []trusted.Device { return d.trust.List() }

// UpdateHotkeys replaces the hotkey config and saves to disk if configPath is set.
func (d *Daemon) UpdateHotkeys(h config.Hotkeys) error {
	d.cfg.Hotkeys = h
	if d.configPath != "" {
		return d.cfg.Save(d.configPath)
	}
	return nil
}


