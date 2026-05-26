package p2p

import (
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/transport"
)

// Host is the subset of daemon.Daemon that p2p sessions need.
type Host interface {
	TrackConn(c *transport.Conn, deviceID string)
	UntrackConn(c *transport.Conn)
	SessionAdd(id, name, ip string)
	SessionRemove(id string)
	SessionRecordLatency(id string, ms float64)
	SetPendingHostConn(c *transport.Conn)
	ClearPendingHostConn(c *transport.Conn)
}

// Event carries session lifecycle updates back to the daemon for broadcasting.
type Event struct {
	Kind     string // "error","log","paired","pair_request","connected","disconnected"
	DeviceID string
	Name     string
	IP       string
	PIN      string
	Role     string
	Msg      string
}

// RunHostSession runs the event loop after dialing a remote daemon (host role).
func RunHostSession(c *transport.Conn, localID, localName string, h Host, emit func(Event)) {
	h.TrackConn(c, "")
	defer h.UntrackConn(c)
	defer c.Close()

	nowMs := func() int64 { return time.Now().UnixMilli() }

	if err := c.Send(event.Message{
		V: 1, Seq: 1, Type: event.TypeHandshake, Ts: nowMs(),
		Payload: event.HandshakePayload{DeviceID: localID, Name: localName, Platform: "macos"},
	}); err != nil {
		emit(Event{Kind: "error", Msg: "handshake: " + err.Error()})
		return
	}
	hsReply, err := c.Recv()
	if err != nil || hsReply.Type != event.TypeHandshake {
		emit(Event{Kind: "error", Msg: "handshake reply failed"})
		return
	}
	var hsPay event.HandshakePayload
	_ = event.DecodePayload(hsReply, &hsPay)
	remoteID, remoteName := hsPay.DeviceID, hsPay.Name
	h.TrackConn(c, remoteID)

	if err := c.Send(event.Message{
		V: 1, Seq: 2, Type: event.TypePairRequest, Ts: nowMs(),
		Payload: event.PairRequestPayload{DeviceID: localID, Name: localName},
	}); err != nil {
		emit(Event{Kind: "error", Msg: err.Error()})
		return
	}
	pinMsg, err := c.Recv()
	if err != nil || pinMsg.Type != event.TypePairPin {
		emit(Event{Kind: "error", Msg: "expected pair_pin"})
		return
	}

	emit(Event{Kind: "pair_request", DeviceID: remoteID, Name: remoteName, Role: "host"})
	log.Printf("[p2p] pairing with %s — enter PIN shown on remote device", remoteName)

	h.SetPendingHostConn(c)
	defer h.ClearPendingHostConn(c)

	pairResult, err := c.Recv()
	if err != nil {
		emit(Event{Kind: "error", Msg: "pairing connection lost"})
		return
	}
	if pairResult.Type == event.TypePairReject {
		emit(Event{Kind: "error", Msg: "pairing rejected by remote"})
		return
	}
	if pairResult.Type != event.TypePairAccept {
		emit(Event{Kind: "error", Msg: "unexpected pairing message: " + pairResult.Type})
		return
	}

	emit(Event{Kind: "paired", DeviceID: remoteID, Name: remoteName})
	h.SessionAdd(remoteID, remoteName, c.RemoteAddr().String())
	emit(Event{Kind: "connected", DeviceID: remoteID, Name: remoteName, IP: c.RemoteAddr().String()})
	log.Printf("[p2p] connected to %s (%s)", remoteName, c.RemoteAddr())

	for {
		msg, err := c.Recv()
		if err != nil {
			emit(Event{Kind: "disconnected", DeviceID: remoteID, Name: remoteName})
			h.SessionRemove(remoteID)
			return
		}
		latency := float64(nowMs() - msg.Ts)
		h.SessionRecordLatency(remoteID, latency)
		switch msg.Type {
		case event.TypePong:
			var pay event.PongPayload
			_ = event.DecodePayload(msg, &pay)
			rtt := float64(nowMs() - pay.EchoTs)
			h.SessionRecordLatency(remoteID, rtt)
			emit(Event{Kind: "log", Msg: fmt.Sprintf("pong rtt=%.1fms", rtt)})
		case event.TypeSwitchAck:
			emit(Event{Kind: "log", Msg: fmt.Sprintf("switch_ack from %s", remoteName)})
		default:
			emit(Event{Kind: "log", Msg: fmt.Sprintf("recv %s latency=%.1fms", msg.Type, latency)})
		}
	}
}

// RunSlaveSession runs the event loop after pairing on the slave side.
func RunSlaveSession(c *transport.Conn, peerID, peerName string, h Host, emit func(Event)) {
	h.TrackConn(c, peerID)
	defer h.UntrackConn(c)
	defer c.Close()

	nowMs := func() int64 { return time.Now().UnixMilli() }

	h.SessionAdd(peerID, peerName, c.RemoteAddr().String())
	emit(Event{Kind: "connected", DeviceID: peerID, Name: peerName, IP: c.RemoteAddr().String()})
	log.Printf("[p2p] slave session started with %s (id=%s)", peerName, peerID)

	var seq int64 = 4
	for {
		msg, err := c.Recv()
		if err != nil {
			emit(Event{Kind: "disconnected", DeviceID: peerID, Name: peerName})
			h.SessionRemove(peerID)
			return
		}
		latency := float64(nowMs() - msg.Ts)
		h.SessionRecordLatency(peerID, latency)
		switch msg.Type {
		case event.TypePing:
			_ = c.Send(event.Message{
				V: 1, Seq: seq, Type: event.TypePong, Ts: nowMs(),
				Payload: event.PongPayload{EchoTs: msg.Ts},
			})
			emit(Event{Kind: "log", Msg: fmt.Sprintf("recv ping latency=%.1fms", latency)})
			seq++
		case event.TypeSwitchRequest:
			var pay event.SwitchRequestPayload
			_ = event.DecodePayload(msg, &pay)
			emit(Event{Kind: "log", Msg: fmt.Sprintf("recv switch_request trigger=%s entry=%s@%.0f%% latency=%.1fms",
				pay.Trigger, pay.Edge, pay.EntryPct*100, latency)})
			_ = c.Send(event.Message{V: 1, Seq: seq, Type: event.TypeSwitchAck, Ts: nowMs(), Payload: struct{}{}})
			seq++
		default:
			emit(Event{Kind: "log", Msg: fmt.Sprintf("recv %s latency=%.1fms", msg.Type, latency)})
		}
	}
}
