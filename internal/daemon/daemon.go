package daemon

import (
	"fmt"
	"log"
	"sync"
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
	opts   Options
	cfg    *config.Config
	ipc    *IPCServer
	tcpSrv *mnet.Server
	sess   *session.Manager
	ctrl   *sw.Controller

	pendingPair   *pairing.Manager
	pendingPairID string
	pendingConn   *mnet.Conn

	connsMu sync.Mutex
	conns   map[*mnet.Conn]string // conn → deviceID ("" while not yet paired)
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
		opts:  opts,
		cfg:   cfg,
		sess:  session.NewManager(),
		ctrl:  sw.NewController("local"),
		conns: make(map[*mnet.Conn]string),
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
	return nil
}

// Stop shuts down all listeners and closes active connections.
func (d *Daemon) Stop() {
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
	if d.pendingPair == nil {
		d.broadcast(Event{Event: "error", Msg: "no pending pair request"})
		return
	}
	// Snapshot all fields before Accept() changes state and triggers the polling
	// goroutine in handleInbound to clear pendingPair concurrently.
	peerName := d.pendingPair.PeerName()
	peerID := d.pendingPairID
	conn := d.pendingConn
	mgr := d.pendingPair

	if err := mgr.Accept(); err != nil {
		d.broadcast(Event{Event: "error", Msg: err.Error()})
		return
	}
	_ = conn.Send(event.Message{
		V: 1, Seq: 3, Type: event.TypePairAccept, Ts: nowMs(), Payload: struct{}{},
	})
	d.pendingPair = nil
	d.pendingConn = nil
	d.pendingPairID = ""
	d.broadcast(Event{Event: "paired", DeviceID: peerID, Name: peerName})
	log.Printf("[daemon] paired with %s (accept)", peerName)
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
	d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("pair_pin %s submitted", pin)})
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
	d.closeConnByDeviceID(deviceID) // closing the conn triggers runHostSession/runSlaveSession to broadcast disconnected
	d.sess.Remove(deviceID)
}

// handleInbound is called by the TCP server for each incoming connection (slave role).
func (d *Daemon) handleInbound(c *mnet.Conn) {
	// adopted is set to true when the connection ownership is transferred to runSlaveSession.
	// In that case the defer must not close c.
	adopted := false
	defer func() {
		if d.pendingConn == c {
			d.pendingConn = nil
			d.pendingPair = nil
			d.pendingPairID = ""
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
			d.pendingPair = nil
			d.pendingConn = nil
			d.pendingPairID = ""
			return

		case res := <-confirmCh:
			if err := mgr.ConfirmPIN(res.pin); err != nil {
				_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{}})
				d.broadcast(Event{Event: "error", Msg: "wrong PIN"})
				d.pendingPair = nil
				d.pendingConn = nil
				d.pendingPairID = ""
				return
			}
			_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairAccept, Ts: nowMs(), Payload: struct{}{}})
			d.broadcast(Event{Event: "paired", DeviceID: prPay.DeviceID, Name: prPay.Name})
			d.pendingPair = nil
			d.pendingConn = nil
			d.pendingPairID = ""
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

func (d *Daemon) broadcast(ev Event) {
	d.ipc.Broadcast(ev)
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}
