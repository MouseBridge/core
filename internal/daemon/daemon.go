package daemon

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/event"
	mnet "github.com/mousebridge/core/internal/net"
	"github.com/mousebridge/core/internal/pairing"
	"github.com/mousebridge/core/internal/session"
	sw "github.com/mousebridge/core/internal/switch"
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

// Daemon owns all long-running state: TCP listener, IPC server, sessions.
type Daemon struct {
	opts       Options
	configPath string
	cfg        *config.Config
	ipc    *IPCServer
	tcpSrv *mnet.Server
	sess   *session.Manager
	ctrl   *sw.Controller

	pairMu         sync.Mutex
	pendingPair    *pairing.Manager // slave-side: waiting for accept/reject
	pendingPairID  string
	pendingConn    *mnet.Conn
	pendingHostConn *mnet.Conn // host-side: waiting for PIN confirmation

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
	d := &Daemon{
		opts:       opts,
		configPath: opts.ConfigPath,
		cfg:        cfg,
		sess:  session.NewManager(),
		ctrl:  sw.NewController("local"),
		conns: make(map[*mnet.Conn]string),
		subs:  make(map[chan Event]struct{}),
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
		d.cmdPairAccept()
	case "pair_reject":
		d.cmdPairReject()
	case "pair_pin":
		go d.cmdPairPIN(cmd.PIN)
	case "status":
		d.cmdStatus()
	case "disconnect":
		d.cmdDisconnect(cmd.DeviceID)
	case "stop_serve":
		d.tcpSrv.Stop()
		log.Println("[daemon] TCP listener stopped")
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
	d.pairMu.Lock()
	if d.pendingPair == nil {
		d.pairMu.Unlock()
		d.broadcast(Event{Event: "error", Msg: "no pending pair request"})
		return
	}
	peerName := d.pendingPair.PeerName()
	peerID := d.pendingPairID
	conn := d.pendingConn
	mgr := d.pendingPair
	d.pairMu.Unlock()

	if err := mgr.Accept(); err != nil {
		d.broadcast(Event{Event: "error", Msg: err.Error()})
		return
	}
	_ = conn.Send(event.Message{
		V: 1, Seq: 3, Type: event.TypePairAccept, Ts: nowMs(), Payload: struct{}{},
	})
	d.pairMu.Lock()
	d.pendingPair = nil
	d.pendingConn = nil
	d.pendingPairID = ""
	d.pairMu.Unlock()
	d.broadcast(Event{Event: "paired", DeviceID: peerID, Name: peerName})
	log.Printf("[daemon] paired with %s (accept)", peerName)
	go d.runSlaveSession(conn, peerID, peerName)
}

func (d *Daemon) cmdPairReject() {
	d.pairMu.Lock()
	if d.pendingPair == nil {
		d.pairMu.Unlock()
		d.broadcast(Event{Event: "error", Msg: "no pending pair request"})
		return
	}
	mgr := d.pendingPair
	conn := d.pendingConn
	d.pendingPair = nil
	d.pendingConn = nil
	d.pendingPairID = ""
	d.pairMu.Unlock()

	_ = mgr.Reject()
	_ = conn.Send(event.Message{
		V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{},
	})
	d.broadcast(Event{Event: "log", Msg: "pairing rejected"})
	conn.Close()
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
	// closing the conn causes runHostSession/runSlaveSession to detect the error,
	// broadcast "disconnected", and call sess.Remove — no need to do it here.
	d.closeConnByDeviceID(deviceID)
}

// handleInbound is called by the TCP server for each incoming connection (slave role).
func (d *Daemon) handleInbound(c *mnet.Conn) {
	// adopted is set to true when the connection ownership is transferred to runSlaveSession.
	// In that case the defer must not close c.
	adopted := false
	defer func() {
		d.pairMu.Lock()
		if d.pendingConn == c {
			d.pendingConn = nil
			d.pendingPair = nil
			d.pendingPairID = ""
		}
		d.pairMu.Unlock()
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

	mgr := pairing.NewManager()
	pin, err := mgr.StartAsSlave(prPay.DeviceID, prPay.Name)
	if err != nil {
		return
	}

	d.pairMu.Lock()
	d.pendingPair = mgr
	d.pendingPairID = prPay.DeviceID
	d.pendingConn = c
	d.pairMu.Unlock()

	_ = c.Send(event.Message{
		V: 1, Seq: 2, Type: event.TypePairPin, Ts: nowMs(),
		Payload: event.PairPinPayload{PIN: pin},
	})
	d.broadcast(Event{Event: "pair_request", DeviceID: prPay.DeviceID, Name: prPay.Name, PIN: pin})
	log.Printf("[daemon] pair_request from %s, PIN=%s", prPay.Name, pin)

	// confirmCh receives the PIN submitted by the host (via pair_confirm TCP message).
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

	// Poll for CLI accept/reject (via cmdPairAccept/cmdPairReject) or host PIN.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(60 * time.Second)
	for {
		select {
		case <-timeout:
			d.broadcast(Event{Event: "error", Msg: "pairing timed out"})
			_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{}})
			d.pairMu.Lock()
			d.pendingPair = nil
			d.pendingConn = nil
			d.pendingPairID = ""
			d.pairMu.Unlock()
			return

		case res := <-confirmCh:
			if err := mgr.ConfirmPIN(res.pin); err != nil {
				_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{}})
				d.broadcast(Event{Event: "error", Msg: "wrong PIN"})
				d.pairMu.Lock()
				d.pendingPair = nil
				d.pendingConn = nil
				d.pendingPairID = ""
				d.pairMu.Unlock()
				return
			}
			_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairAccept, Ts: nowMs(), Payload: struct{}{}})
			d.broadcast(Event{Event: "paired", DeviceID: prPay.DeviceID, Name: prPay.Name})
			d.pairMu.Lock()
			d.pendingPair = nil
			d.pendingConn = nil
			d.pendingPairID = ""
			d.pairMu.Unlock()
			adopted = true
			go d.runSlaveSession(c, prPay.DeviceID, prPay.Name)
			return

		case <-ticker.C:
			state := mgr.State()
			if state == pairing.StatePaired {
				// cmdPairAccept took ownership and will start runSlaveSession.
				adopted = true
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
	d.trackConn(c, "") // deviceID unknown until handshake
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
	d.trackConn(c, remoteID) // now we know the deviceID

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
	d.broadcast(Event{Event: "pair_request", DeviceID: remoteID, Name: remoteName, PIN: pinPay.PIN})
	log.Printf("[daemon] remote PIN=%s (waiting for accept)", pinPay.PIN)

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
	d.trackConn(c, peerID)
	defer d.untrackConn(c)
	defer c.Close()
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
// The caller must call Unsubscribe when done.
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
		default: // drop if subscriber is slow
		}
	}
}

// Config returns the daemon's current configuration.
func (d *Daemon) Config() *config.Config {
	return d.cfg
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
