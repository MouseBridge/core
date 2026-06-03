package daemon

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/device"
	"github.com/mousebridge/core/internal/p2p"
	"github.com/mousebridge/core/internal/pending"
	"github.com/mousebridge/core/internal/remembered"
	"github.com/mousebridge/core/internal/transport"
)

// Options configures the daemon at startup.
type Options struct {
	ConfigPath string
	DataDir    string
}

// Daemon owns all long-running state.
type Daemon struct {
	cfg      *config.Config
	identity device.Identity

	rem     *remembered.Store
	pending *pending.Manager
	tracker *p2p.OutboundTracker

	sessMu  sync.RWMutex
	sessions map[string]*sessionEntry // keyed by connectionID

	connsMu sync.Mutex
	conns   map[*transport.Conn]string // conn → deviceID (empty until authenticated)

	subsMu sync.RWMutex
	subs   map[chan BusEvent]struct{}

	// httpConnCh receives raw HTTP connections from the MUX.
	httpConnCh chan net.Conn

	ln      net.Listener
	lnMu    sync.Mutex

	cancel context.CancelFunc
}

type sessionEntry struct {
	ConnectionID string
	DeviceID     string
	Name         string
	IP           string
	Role         string
	AvgLatencyMs float64
	latencies    []float64
}

// New creates and initialises a Daemon.
func New(opts Options) (*Daemon, error) {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("daemon: load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}

	identity, err := device.LoadOrCreate(
		opts.DataDir+"/device.json",
		func() string { return config.DeriveDeviceID(cfg.Port) },
		cfg.DeviceName,
	)
	if err != nil {
		return nil, fmt.Errorf("daemon: device identity: %w", err)
	}
	if cfg.DeviceName == "" {
		cfg.DeviceName = identity.Name
	}

	rem, err := remembered.New(opts.DataDir + "/remembered.json")
	if err != nil {
		log.Warnf("[daemon] remembered store load failed: %v — starting empty", err)
		rem, _ = remembered.New("")
	}

	ttl := time.Duration(cfg.PairingPINTTLSeconds) * time.Second
	pm := pending.New(ttl, cfg.PairingPINMaxAttempts)

	d := &Daemon{
		cfg:        cfg,
		identity:   identity,
		rem:        rem,
		pending:    pm,
		tracker:    p2p.NewOutboundTracker(),
		sessions:   make(map[string]*sessionEntry),
		conns:      make(map[*transport.Conn]string),
		subs:       make(map[chan BusEvent]struct{}),
		httpConnCh: make(chan net.Conn, 64),
	}
	return d, nil
}

// Start begins listening on listen_host:port and starts background goroutines.
func (d *Daemon) Start() error {
	addr := fmt.Sprintf("%s:%d", d.cfg.ListenHost, d.cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("daemon: listen %s: %w", addr, err)
	}
	d.lnMu.Lock()
	d.ln = ln
	d.lnMu.Unlock()

	if d.cfg.UnsafeHTTPLAN {
		log.Warn("WARNING: unsafe_http_lan is enabled. MouseBridge HTTP API has no authentication and is reachable from the LAN.")
	}
	log.Printf("[daemon] listening on %s (device_id=%s name=%s)", addr, d.identity.DisplayID, d.identity.Name)

	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel

	// GC expired pending pairs and emit pair_timeout events.
	go d.pending.RunGC(ctx, func(e *pending.Entry) {
		d.broadcast(BusEvent{Kind: "pair_timeout", PairingID: e.PairingID})
		log.Printf("[daemon] pairing timed out: %s (%s)", e.Name, e.PairingID)
	})

	go transport.Serve(ln, d.handleP2P, d.handleHTTP)

	d.broadcast(BusEvent{Kind: "listening"})
	return nil
}

// Stop shuts down the daemon.
func (d *Daemon) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
	d.lnMu.Lock()
	if d.ln != nil {
		_ = d.ln.Close()
		d.ln = nil
	}
	d.lnMu.Unlock()

	d.connsMu.Lock()
	for c := range d.conns {
		_ = c.Close()
	}
	d.conns = make(map[*transport.Conn]string)
	d.connsMu.Unlock()

	close(d.httpConnCh)

	d.subsMu.Lock()
	for ch := range d.subs {
		close(ch)
	}
	d.subs = make(map[chan BusEvent]struct{})
	d.subsMu.Unlock()
}

// HTTPConnCh returns the channel of raw HTTP connections for the API server.
func (d *Daemon) HTTPConnCh() <-chan net.Conn { return d.httpConnCh }

func (d *Daemon) handleHTTP(c net.Conn) {
	select {
	case d.httpConnCh <- c:
	default:
		log.Printf("[daemon] httpConnCh full, dropping %s", c.RemoteAddr())
		_ = c.Close()
	}
}

func (d *Daemon) handleP2P(c *transport.Conn) {
	go p2p.HandleInbound(c, d.identity.DeviceID, d, d, d.emitBus)
}

// p2p.ServerHost interface
func (d *Daemon) LocalID() string              { return d.identity.DeviceID }
func (d *Daemon) LocalName() string            { return d.identity.Name }
func (d *Daemon) LocalDisplayID() string       { return d.identity.DisplayID }
func (d *Daemon) PendingManager() *pending.Manager  { return d.pending }
func (d *Daemon) RememberedStore() *remembered.Store { return d.rem }
func (d *Daemon) RememberedEnabled() bool      { return d.cfg.RememberedEnabled }

// p2p.SessionHost interface
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

func (d *Daemon) SessionAdd(connectionID, deviceID, name, ip, role string) {
	d.sessMu.Lock()
	d.sessions[connectionID] = &sessionEntry{
		ConnectionID: connectionID,
		DeviceID:     deviceID,
		Name:         name,
		IP:           ip,
		Role:         role,
	}
	d.sessMu.Unlock()
}

func (d *Daemon) SessionRemove(connectionID string) {
	d.sessMu.Lock()
	delete(d.sessions, connectionID)
	d.sessMu.Unlock()
}

func (d *Daemon) SessionRecordLatency(connectionID string, ms float64) {
	d.sessMu.Lock()
	s, ok := d.sessions[connectionID]
	if ok {
		s.latencies = append(s.latencies, ms)
		if len(s.latencies) > 20 {
			s.latencies = s.latencies[len(s.latencies)-20:]
		}
		var sum float64
		for _, v := range s.latencies {
			sum += v
		}
		s.AvgLatencyMs = sum / float64(len(s.latencies))
	}
	d.sessMu.Unlock()
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
		_ = target.Close()
	}
}

// Connect dials a remote daemon and starts the client-side pairing flow.
func (d *Daemon) Connect(host string, port int) {
	go func() {
		dialTimeout := time.Duration(d.cfg.ConnectTimeoutSeconds) * time.Second
		c, err := transport.DialTimeout(host, port, dialTimeout)
		if err != nil {
			d.broadcast(BusEvent{Kind: "error", Msg: "connect: " + err.Error()})
			return
		}
		p2p.DialAndPair(c, d.identity.DeviceID, d.identity.DisplayID, d.identity.Name, d, d.tracker, d.rem, d.emitBus)
	}()
}

// SendPIN forwards a PIN to the pending outbound pairing connection.
func (d *Daemon) SendPIN(pairingID, pin string) error {
	op, ok := d.tracker.Get(pairingID)
	if !ok {
		return fmt.Errorf("no outbound pairing for pairing_id %s", pairingID)
	}
	return op.Conn.Send(buildPairConfirm(pairingID, pin))
}

// RejectOutbound rejects an outbound pairing by closing its connection.
func (d *Daemon) RejectOutbound(pairingID string) {
	op, ok := d.tracker.Get(pairingID)
	if !ok {
		return
	}
	d.tracker.Remove(pairingID)
	_ = op.Conn.Close()
}

// RejectInbound rejects an inbound pairing (server side).
func (d *Daemon) RejectInbound(pairingID string) {
	d.pending.Reject(pairingID)
}

func (d *Daemon) emitBus(ev p2p.BusEvent) {
	d.broadcast(BusEvent(ev))
	switch ev.Kind {
	case "pair_request":
		log.Printf("[daemon] pair_request from %s (pairing_id=%s)", ev.Name, ev.PairingID)
	case "paired":
		log.Printf("[daemon] paired with %s", ev.Name)
	case "session_connected":
		log.Printf("[daemon] session connected: %s (%s) role=%s", ev.Name, ev.DeviceID[:12], ev.Role)
	case "session_disconnected":
		log.Printf("[daemon] session disconnected: %s", ev.Name)
	case "error":
		log.Printf("[daemon] error: %s", ev.Msg)
	}
}

// Subscribe returns a channel that receives every broadcasted BusEvent.
func (d *Daemon) Subscribe() chan BusEvent {
	ch := make(chan BusEvent, 64)
	d.subsMu.Lock()
	d.subs[ch] = struct{}{}
	d.subsMu.Unlock()
	return ch
}

// Unsubscribe removes and closes the channel.
func (d *Daemon) Unsubscribe(ch chan BusEvent) {
	d.subsMu.Lock()
	_, exists := d.subs[ch]
	delete(d.subs, ch)
	d.subsMu.Unlock()
	if exists {
		close(ch)
	}
}

func (d *Daemon) broadcast(ev BusEvent) {
	d.subsMu.RLock()
	subs := make([]chan BusEvent, 0, len(d.subs))
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

// Addr returns the listener address (empty if not listening).
func (d *Daemon) Addr() string {
	d.lnMu.Lock()
	defer d.lnMu.Unlock()
	if d.ln == nil {
		return ""
	}
	return d.ln.Addr().String()
}

// Config returns the daemon config.
func (d *Daemon) Config() *config.Config { return d.cfg }

// Identity returns the device identity.
func (d *Daemon) Identity() device.Identity { return d.identity }

// RememberedList returns all remembered devices as DTOs (no secrets).
func (d *Daemon) RememberedList() []remembered.DTO { return d.rem.List() }

// RememberedForget removes a remembered device by device_id.
func (d *Daemon) RememberedForget(deviceID string) error { return d.rem.Remove(deviceID) }

// RememberedRename updates the alias of a remembered device.
func (d *Daemon) RememberedRename(deviceID, alias string) error { return d.rem.Rename(deviceID, alias) }
