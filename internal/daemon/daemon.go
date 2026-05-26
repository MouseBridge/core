package daemon

import (
	"fmt"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/event"
	mnet "github.com/mousebridge/core/internal/net"
	"github.com/mousebridge/core/internal/pairing"
	"github.com/mousebridge/core/internal/session"
	sw "github.com/mousebridge/core/internal/switch"
	"github.com/mousebridge/core/internal/trusted"
)


// Options configures the daemon.
type Options struct {
	SocketPath string
	TCPPort    int
	DeviceName string
	HTTPHost   string
	HTTPPort   int
	ConfigPath string
}

type pendingSlaveEntry struct {
	mgr  *pairing.Manager
	conn *mnet.Conn
	name string
}

// Daemon owns all long-running state: TCP listener, IPC server, sessions.
type Daemon struct {
	opts       Options
	configPath string
	cfg        *config.Config
	ipc    *IPCServer
	tcpSrv *mnet.Server
	sess   *session.Manager
	ctrl   *sw.Controller
	trust  *trusted.Store

	pairMu          sync.Mutex
	pendingPairs    map[string]*pendingSlaveEntry // device_id → slave-side pending pair
	pendingHostConn *mnet.Conn                   // host-side: waiting for PIN confirmation

	connsMu sync.Mutex
	conns   map[*mnet.Conn]string // conn → deviceID ("" while not yet paired)

	subsMu sync.RWMutex
	subs   map[chan Event]struct{}

	httpSrv interface {
		Start() error
		Stop()
	}
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
		pendingPairs: make(map[string]*pendingSlaveEntry),
		conns:        make(map[*mnet.Conn]string),
		subs:         make(map[chan Event]struct{}),
	}
	d.ipc = NewIPCServer(opts.SocketPath, d.handleCommand)
	d.tcpSrv = mnet.NewServer(d.handleInbound)
	return d
}

// Start begins listening on the Unix Socket.
func (d *Daemon) Start() error {
	if err := d.ipc.Start(); err != nil {
		return fmt.Errorf("daemon: ipc: %w", err)
	}
	log.Printf("[daemon] socket: %s", d.opts.SocketPath)
	if d.httpSrv != nil {
		if err := d.httpSrv.Start(); err != nil {
			return fmt.Errorf("daemon: http: %w", err)
		}
	}
	return nil
}

// Stop shuts down all listeners and closes active connections.
func (d *Daemon) Stop() {
	if d.httpSrv != nil {
		d.httpSrv.Stop()
	}
	d.tcpSrv.Stop()
	d.connsMu.Lock()
	conns := d.conns
	d.conns = make(map[*mnet.Conn]string)
	d.connsMu.Unlock()
	for c := range conns {
		c.Close()
	}
	d.ipc.Stop()
}

func (d *Daemon) trackConn(c *mnet.Conn, deviceID string) {
	d.connsMu.Lock()
	d.conns[c] = deviceID
	d.connsMu.Unlock()
}

func (d *Daemon) untrackConn(c *mnet.Conn) {
	d.connsMu.Lock()
	delete(d.conns, c)
	d.connsMu.Unlock()
}

// closeConnByDeviceID finds and closes the TCP connection for a given deviceID.
func (d *Daemon) closeConnByDeviceID(deviceID string) {
	d.connsMu.Lock()
	var target *mnet.Conn
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

// handleCommand processes one command from a CLI/UI client.
func (d *Daemon) handleCommand(cmd Command) {
	switch cmd.Cmd {
	case "serve":
		d.cmdServe(cmd.Port)
	case "connect":
		go d.cmdConnect(cmd.IP, cmd.Port)
	case "pair_accept":
		d.cmdPairAccept(cmd.DeviceID)
	case "pair_reject":
		d.cmdPairReject(cmd.DeviceID)
	case "pair_pin":
		go d.cmdPairPIN(cmd.PIN)
	case "status":
		d.cmdStatus()
	case "disconnect":
		d.cmdDisconnect(cmd.DeviceID)
	case "stop_serve":
		d.tcpSrv.Stop()
		d.broadcast(Event{Event: "stopped"})
		log.Println("[daemon] TCP listener stopped")
	case "trust":
		d.cmdTrust(cmd.DeviceID, cmd.Name)
	case "untrust":
		d.cmdUntrust(cmd.DeviceID)
	case "trusted_list":
		d.cmdTrustedList()
	default:
		d.broadcast(Event{Event: "error", Msg: fmt.Sprintf("unknown command: %s", cmd.Cmd)})
	}
}

// Serve is the public entry point used by the daemon CLI flag --serve.
func (d *Daemon) Serve(port int) { d.cmdServe(port) }

// Connect is the public entry point used by the daemon CLI flag --connect.
func (d *Daemon) Connect(ip string, port int) { d.cmdConnect(ip, port) }

func (d *Daemon) cmdServe(overridePort int) {
	port := d.cfg.Port
	if d.opts.TCPPort != 0 {
		port = d.opts.TCPPort
	}
	if overridePort != 0 {
		port = overridePort
	}
	if err := d.tcpSrv.Listen(port); err != nil {
		log.Printf("[daemon] cmdServe error: %v", err)
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

// cmdPairAccept accepts the pending pair request for the given device_id.
// If deviceID is empty and there is exactly one pending request, it accepts that one.
func (d *Daemon) cmdPairAccept(deviceID string) {
	d.pairMu.Lock()
	entry, deviceID := d.resolvePendingSlave(deviceID)
	if entry == nil {
		d.pairMu.Unlock()
		d.broadcast(Event{Event: "error", Msg: "no pending pair request"})
		return
	}
	delete(d.pendingPairs, deviceID)
	d.pairMu.Unlock()

	if err := entry.mgr.Accept(); err != nil {
		d.broadcast(Event{Event: "error", Msg: err.Error()})
		return
	}
	_ = entry.conn.Send(event.Message{
		V: 1, Seq: 3, Type: event.TypePairAccept, Ts: nowMs(), Payload: struct{}{},
	})
	d.broadcast(Event{Event: "paired", DeviceID: deviceID, Name: entry.name})
	log.Printf("[daemon] paired with %s (accept)", entry.name)
	go d.runSlaveSession(entry.conn, deviceID, entry.name)
}

// cmdPairReject rejects the pending pair request for the given device_id.
// If deviceID is empty and there is exactly one pending request, it rejects that one.
func (d *Daemon) cmdPairReject(deviceID string) {
	d.pairMu.Lock()
	entry, deviceID := d.resolvePendingSlave(deviceID)
	if entry == nil {
		d.pairMu.Unlock()
		d.broadcast(Event{Event: "error", Msg: "no pending pair request"})
		return
	}
	delete(d.pendingPairs, deviceID)
	d.pairMu.Unlock()

	_ = entry.mgr.Reject()
	_ = entry.conn.Send(event.Message{
		V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{},
	})
	d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("pairing rejected: %s", entry.name)})
	entry.conn.Close()
}

// resolvePendingSlave looks up the entry by deviceID, or returns the only entry if deviceID is empty.
// Must be called with pairMu held. Returns (nil, "") if not found or ambiguous.
func (d *Daemon) resolvePendingSlave(deviceID string) (*pendingSlaveEntry, string) {
	if deviceID != "" {
		return d.pendingPairs[deviceID], deviceID
	}
	if len(d.pendingPairs) == 1 {
		for id, e := range d.pendingPairs {
			return e, id
		}
	}
	return nil, ""
}

func (d *Daemon) cmdPairPIN(pin string) {
	d.pairMu.Lock()
	conn := d.pendingHostConn
	d.pairMu.Unlock()
	if conn == nil {
		d.broadcast(Event{Event: "error", Msg: "no pending pairing (host side)"})
		return
	}
	if err := conn.Send(event.Message{
		V: 1, Seq: 3, Type: event.TypePairConfirm, Ts: nowMs(),
		Payload: event.PairConfirmPayload{PIN: pin},
	}); err != nil {
		d.broadcast(Event{Event: "error", Msg: "pair_pin send: " + err.Error()})
	}
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
	d.closeConnByDeviceID(deviceID)
}

func (d *Daemon) cmdTrust(deviceID, name string) {
	if deviceID == "" {
		d.broadcast(Event{Event: "error", Msg: "device_id required"})
		return
	}
	if err := d.trust.Add(deviceID, name); err != nil {
		d.broadcast(Event{Event: "error", Msg: "trust: " + err.Error()})
		return
	}
	d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("trusted: %s (%s)", name, deviceID)})
	log.Printf("[daemon] trusted device %s (%s)", name, deviceID)
}

func (d *Daemon) cmdUntrust(deviceID string) {
	if deviceID == "" {
		d.broadcast(Event{Event: "error", Msg: "device_id required"})
		return
	}
	if err := d.trust.Remove(deviceID); err != nil {
		d.broadcast(Event{Event: "error", Msg: "untrust: " + err.Error()})
		return
	}
	d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("untrusted: %s", deviceID)})
}

func (d *Daemon) cmdTrustedList() {
	devs := d.trust.List()
	statuses := make([]DeviceStatus, 0, len(devs))
	for _, dv := range devs {
		statuses = append(statuses, DeviceStatus{ID: dv.DeviceID, Name: dv.Name})
	}
	d.broadcast(Event{Event: "trusted_list", Devices: statuses})
}

// handleInbound is called by the TCP server for each incoming connection (slave role).
func (d *Daemon) handleInbound(c *mnet.Conn) {
	var peerDeviceID string
	adopted := false
	defer func() {
		if peerDeviceID != "" {
			d.pairMu.Lock()
			delete(d.pendingPairs, peerDeviceID)
			d.pairMu.Unlock()
		}
		if !adopted {
			c.Close()
		}
	}()
	remote := c.RemoteAddr().String()

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

	pairMsg, err := c.Recv()
	if err != nil || pairMsg.Type != event.TypePairRequest {
		return
	}
	var prPay event.PairRequestPayload
	_ = event.DecodePayload(pairMsg, &prPay)

	// Trusted device: skip PIN flow, auto-accept.
	if d.trust.IsTrusted(prPay.DeviceID) {
		_ = c.Send(event.Message{V: 1, Seq: 2, Type: event.TypePairPin, Ts: nowMs(), Payload: event.PairPinPayload{}})
		_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairAccept, Ts: nowMs(), Payload: struct{}{}})
		d.broadcast(Event{Event: "paired", DeviceID: prPay.DeviceID, Name: prPay.Name})
		log.Printf("[daemon] trusted device %s auto-accepted", prPay.Name)
		adopted = true
		go d.runSlaveSession(c, prPay.DeviceID, prPay.Name)
		return
	}

	mgr := pairing.NewManager()
	pin, err := mgr.StartAsSlave(prPay.DeviceID, prPay.Name)
	if err != nil {
		return
	}

	peerDeviceID = prPay.DeviceID
	entry := &pendingSlaveEntry{mgr: mgr, conn: c, name: prPay.Name}
	d.pairMu.Lock()
	d.pendingPairs[peerDeviceID] = entry
	d.pairMu.Unlock()

	// Send pair_pin without the PIN — host must get it out-of-band from the user on this side.
	_ = c.Send(event.Message{
		V: 1, Seq: 2, Type: event.TypePairPin, Ts: nowMs(),
		Payload: event.PairPinPayload{},
	})
	d.broadcast(Event{Event: "pair_request", DeviceID: prPay.DeviceID, Name: prPay.Name, PIN: pin, Role: "slave"})
	log.Printf("[daemon] pair_request from %s — PIN: %s", prPay.Name, pin)

	type confirmResult struct{ pin string }
	confirmCh := make(chan confirmResult, 1)
	go func() {
		for {
			msg, err := c.Recv()
			if err != nil {
				return
			}
			if msg.Type == event.TypePairConfirm {
				var pay event.PairConfirmPayload
				_ = event.DecodePayload(msg, &pay)
				confirmCh <- confirmResult{pay.PIN}
				return
			}
		}
	}()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(60 * time.Second)
	for {
		select {
		case <-timeout:
			d.broadcast(Event{Event: "error", Msg: "pairing timed out"})
			_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{}})
			d.pairMu.Lock()
			delete(d.pendingPairs, peerDeviceID)
			d.pairMu.Unlock()
			peerDeviceID = ""
			return

		case res := <-confirmCh:
			if err := mgr.ConfirmPIN(res.pin); err != nil {
				_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{}})
				d.broadcast(Event{Event: "error", Msg: "wrong PIN"})
				d.pairMu.Lock()
				delete(d.pendingPairs, peerDeviceID)
				d.pairMu.Unlock()
				peerDeviceID = ""
				return
			}
			_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairAccept, Ts: nowMs(), Payload: struct{}{}})
			d.broadcast(Event{Event: "paired", DeviceID: prPay.DeviceID, Name: prPay.Name})
			d.pairMu.Lock()
			delete(d.pendingPairs, peerDeviceID)
			d.pairMu.Unlock()
			peerDeviceID = ""
			adopted = true
			go d.runSlaveSession(c, prPay.DeviceID, prPay.Name)
			return

		case <-ticker.C:
			state := mgr.State()
			if state == pairing.StatePaired {
				adopted = true
				peerDeviceID = ""
				return
			}
			if state == pairing.StateRejected {
				return
			}
		}
	}
}

// runHostSession runs after dialing a remote daemon (host role).
func (d *Daemon) runHostSession(c *mnet.Conn) {
	d.trackConn(c, "")
	defer d.untrackConn(c)
	defer c.Close()

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
	d.trackConn(c, remoteID)

	if err := c.Send(event.Message{
		V: 1, Seq: 2, Type: event.TypePairRequest, Ts: nowMs(),
		Payload: event.PairRequestPayload{DeviceID: "local", Name: d.cfg.DeviceName},
	}); err != nil {
		d.broadcast(Event{Event: "error", Msg: err.Error()})
		return
	}

	pinMsg, err := c.Recv()
	if err != nil || pinMsg.Type != event.TypePairPin {
		d.broadcast(Event{Event: "error", Msg: "expected pair_pin"})
		return
	}
	var pinPay event.PairPinPayload
	_ = event.DecodePayload(pinMsg, &pinPay)
	d.broadcast(Event{Event: "pair_request", DeviceID: remoteID, Name: remoteName, Role: "host"})
	log.Printf("[daemon] pairing with %s — enter PIN shown on remote device", remoteName)

	d.pairMu.Lock()
	d.pendingHostConn = c
	d.pairMu.Unlock()
	defer func() {
		d.pairMu.Lock()
		if d.pendingHostConn == c {
			d.pendingHostConn = nil
		}
		d.pairMu.Unlock()
	}()

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
	d.sess.Add(remoteID, remoteName, c.RemoteAddr().String())
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
	d.trackConn(c, peerID)
	defer d.untrackConn(c)
	defer c.Close()
	d.sess.Add(peerID, peerName, c.RemoteAddr().String())
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

// SetHTTPServer injects an HTTP server that will be started/stopped with the daemon.
func (d *Daemon) SetHTTPServer(srv interface {
	Start() error
	Stop()
}) {
	d.httpSrv = srv
}

// HandleCommand is the public entry point for REST/UI callers.
func (d *Daemon) HandleCommand(cmd Command) {
	d.handleCommand(cmd)
}

// Subscribe returns a channel that receives every broadcasted Event.
func (d *Daemon) Subscribe() chan Event {
	ch := make(chan Event, 64)
	d.subsMu.Lock()
	d.subs[ch] = struct{}{}
	d.subsMu.Unlock()
	return ch
}

// Unsubscribe removes the channel from the subscriber set and closes it.
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

// State returns a consistent snapshot of the daemon's current state.
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
	return DaemonState{
		Serving: d.tcpSrv.IsListening(),
		Port:    d.tcpSrv.Port(),
		Devices: statuses,
	}
}

// Config returns the daemon's current configuration.
func (d *Daemon) Config() *config.Config {
	return d.cfg
}

// TrustedList returns all trusted devices.
func (d *Daemon) TrustedList() []trusted.Device {
	return d.trust.List()
}

// UpdateHotkeys replaces the hotkey config and saves to disk if configPath is set.
func (d *Daemon) UpdateHotkeys(h config.Hotkeys) error {
	d.cfg.Hotkeys = h
	if d.configPath != "" {
		return d.cfg.Save(d.configPath)
	}
	return nil
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}
