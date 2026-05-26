package daemon

import (
	"fmt"
	"net"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/p2p"
	"github.com/mousebridge/core/internal/transport"
)

func nowMsCmd() int64 { return time.Now().UnixMilli() }

// servingMu guards servingPort.
var servingMu sync.Mutex

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
		d.cmdStopServe()
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

// cmdServe starts a new TCP listener on port (or cfg.Port if 0), runs mux dispatch.
func (d *Daemon) cmdServe(overridePort int) {
	port := d.cfg.Port
	if overridePort != 0 {
		port = overridePort
	}

	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		log.Printf("[daemon] cmdServe error: %v", err)
		d.broadcast(Event{Event: "error", Msg: err.Error()})
		return
	}

	servingMu.Lock()
	old := d.serveLn
	d.serveLn = ln
	actualPort := ln.Addr().(*net.TCPAddr).Port
	servingMu.Unlock()

	if old != nil {
		_ = old.Close()
	}

	d.broadcast(Event{Event: "listening", Port: actualPort})
	log.Printf("[daemon] TCP listening on :%d", actualPort)

	go transport.Serve(ln, d.HandleP2P, d.HandleHTTP)
}

func (d *Daemon) cmdStopServe() {
	servingMu.Lock()
	ln := d.serveLn
	d.serveLn = nil
	servingMu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	d.broadcast(Event{Event: "stopped"})
	log.Println("[daemon] TCP listener stopped")
}

func (d *Daemon) servingState() (bool, int) {
	servingMu.Lock()
	defer servingMu.Unlock()
	if d.serveLn == nil {
		return false, 0
	}
	addr, ok := d.serveLn.Addr().(*net.TCPAddr)
	if !ok {
		return true, 0
	}
	return true, addr.Port
}

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

	if err := entry.Mgr.Accept(); err != nil {
		d.broadcast(Event{Event: "error", Msg: err.Error()})
		return
	}
	// Signal slave-side: send pair_accept over the wire.
	// The handshake goroutine in p2p.HandleInbound is polling mgr.State(),
	// so setting Accept() is sufficient — no need to send a message here.
	d.broadcast(Event{Event: "paired", DeviceID: deviceID, Name: entry.Name})
	log.Printf("[daemon] paired with %s (id=%s)", entry.Name, deviceID)
}

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

	_ = entry.Mgr.Reject()
	d.broadcast(Event{Event: "log", Msg: fmt.Sprintf("pairing rejected: %s", entry.Name)})
}

func (d *Daemon) resolvePendingSlave(deviceID string) (*p2p.PendingSlaveEntry, string) {
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
	msg := event.Message{
		V: 1, Seq: 3, Type: event.TypePairConfirm, Ts: nowMsCmd(),
		Payload: event.PairConfirmPayload{PIN: pin},
	}
	if err := conn.Send(msg); err != nil {
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
			IP:           dv.IP,
			Role:         dv.Role,
			AvgLatencyMs: dv.AvgLatencyMs,
		})
	}
	serving, port := d.servingState()
	d.broadcast(Event{
		Event:     "status",
		Devices:   statuses,
		Serving:   serving,
		Port:      port,
		LocalID:   d.cfg.DeviceID,
		LocalName: d.cfg.DeviceName,
	})
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
