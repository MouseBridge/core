package p2p

import (
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/remembered"
	"github.com/mousebridge/core/internal/transport"
)

// SessionHost is the subset of daemon that active sessions need.
type SessionHost interface {
	TrackConn(c *transport.Conn, deviceID string)
	UntrackConn(c *transport.Conn)
	SessionAdd(connectionID, deviceID, name, ip, role string)
	SessionRemove(connectionID string)
	SessionRecordLatency(connectionID string, ms float64)
}

// RunSession runs the message loop for an authenticated session.
// role is "server" or "client".
func RunSession(c *transport.Conn, deviceID, name, role string, h SessionHost, emit func(BusEvent)) {
	connID := newConnectionID()
	h.TrackConn(c, deviceID)
	defer h.UntrackConn(c)
	defer c.Close()

	h.SessionAdd(connID, deviceID, name, c.RemoteAddr().String(), role)
	emit(BusEvent{Kind: "session_connected", DeviceID: deviceID, Name: name,
		RemoteIP: c.RemoteAddr().String(), Role: role})
	log.Printf("[p2p] session active with %s (%s) role=%s", name, deviceID[:12], role)

	var seq int64 = 10
	for {
		msg, err := c.Recv()
		if err != nil {
			emit(BusEvent{Kind: "session_disconnected", DeviceID: deviceID, Name: name})
			h.SessionRemove(connID)
			return
		}
		latency := float64(time.Now().UnixMilli() - msg.Ts)
		h.SessionRecordLatency(connID, latency)

		switch msg.Type {
		case event.TypePing:
			_ = c.Send(event.Message{
				V: 1, Seq: seq, Type: event.TypePong, Ts: time.Now().UnixMilli(),
				Payload: event.PongPayload{EchoTs: msg.Ts},
			})
			seq++
			emit(BusEvent{Kind: "log", Msg: fmt.Sprintf("ping latency=%.1fms", latency)})

		case event.TypePong:
			var pay event.PongPayload
			_ = event.DecodePayload(msg, &pay)
			rtt := float64(time.Now().UnixMilli() - pay.EchoTs)
			h.SessionRecordLatency(connID, rtt)
			emit(BusEvent{Kind: "log", Msg: fmt.Sprintf("pong rtt=%.1fms", rtt)})

		case event.TypeSwitchRequest:
			var pay event.SwitchRequestPayload
			_ = event.DecodePayload(msg, &pay)
			emit(BusEvent{Kind: "log", Msg: fmt.Sprintf("switch_request trigger=%s edge=%s latency=%.1fms",
				pay.Trigger, pay.Edge, latency)})
			_ = c.Send(event.Message{V: 1, Seq: seq, Type: event.TypeSwitchAck, Ts: time.Now().UnixMilli(), Payload: struct{}{}})
			seq++

		case event.TypeSwitchAck:
			emit(BusEvent{Kind: "log", Msg: fmt.Sprintf("switch_ack from %s latency=%.1fms", name, latency)})

		default:
			emit(BusEvent{Kind: "log", Msg: fmt.Sprintf("recv %s latency=%.1fms", msg.Type, latency)})
		}
	}
}

// DialAndPair dials a remote daemon, exchanges hellos, completes PIN pairing as the
// client side, and starts a session on success.
//
// tracker is used to register the outbound pairing so the daemon can forward the
// PIN from POST /api/pair/pin. The connection is stored in the tracker entry so
// the daemon can call conn.Send(pair_confirm).
// remStore, if non-nil, is used to save the server as a remembered device on success.
func DialAndPair(c *transport.Conn, localID, localDisplayID, localName string,
	h SessionHost, tracker *OutboundTracker, remStore *remembered.Store, emit func(BusEvent)) {

	connID := newConnectionID()
	adopted := false
	defer func() {
		if !adopted {
			c.Close()
		}
	}()

	// Send hello first as client.
	if err := sendHello(c, localID, localDisplayID, localName); err != nil {
		emit(BusEvent{Kind: "error", Msg: "send hello: " + err.Error()})
		return
	}
	serverHello, err := recvHello(c)
	if err != nil {
		emit(BusEvent{Kind: "error", Msg: "recv hello: " + err.Error()})
		return
	}

	serverID := serverHello.DeviceID
	serverName := serverHello.Name

	if serverID == localID {
		emit(BusEvent{Kind: "error", Msg: "remote device_id equals local; self-connection rejected"})
		return
	}

	// Wait for pair_challenge from server.
	msg, err := c.Recv()
	if err != nil {
		emit(BusEvent{Kind: "error", Msg: "connection lost waiting for pair_challenge"})
		return
	}
	if msg.Type == event.TypePairAccept {
		// Remembered auto-connect: server already knows us; update last_seen.
		if remStore != nil {
			_ = remStore.UpdateLastSeen(serverID, time.Now())
		}
		adopted = true
		go RunSession(c, serverID, serverName, "client", h, emit)
		return
	}
	if msg.Type != event.TypePairChallenge {
		emit(BusEvent{Kind: "error", Msg: "unexpected message: " + msg.Type})
		return
	}
	var challenge event.PairChallengePayload
	_ = event.DecodePayload(msg, &challenge)
	pairingID := challenge.PairingID

	// Register in tracker BEFORE emitting pair_request so SendPIN can immediately
	// find the connection when the UI/test reacts to the event.
	if tracker != nil {
		tracker.Add(&OutboundPairing{
			PairingID:       pairingID,
			ConnectionID:    connID,
			RemoteName:      serverName,
			RemoteDisplayID: serverID[:12],
			ExpiresAt:       time.Now().Add(2 * time.Minute),
			Conn:            c,
		})
		defer tracker.Remove(pairingID)
	}

	emit(BusEvent{
		Kind: "pair_request", PairingID: pairingID, ConnectionID: connID,
		ClaimedDeviceID: serverID, DisplayID: serverID[:12], Name: serverName,
		RemoteIP: c.RemoteAddr().String(), Role: "client",
	})
	log.Printf("[p2p] pairing with %s — enter PIN shown on remote device", serverName)

	for {
		reply, err := c.Recv()
		if err != nil {
			emit(BusEvent{Kind: "error", Msg: "connection lost during PIN exchange"})
			return
		}
		switch reply.Type {
		case event.TypePairAccept:
			// Save server as remembered device on client side.
			if remStore != nil {
				var pay event.PairAcceptPayload
				_ = event.DecodePayload(reply, &pay)
				now := time.Now()
				_ = remStore.Add(remembered.Record{
					DeviceID:   serverID,
					DisplayID:  serverID[:12],
					Name:       serverName,
					SecretID:   pay.SecretID,
					PairSecret: pay.PairSecret,
					CreatedAt:  now,
					LastSeenAt: now,
				})
			}
			emit(BusEvent{Kind: "paired", PairingID: pairingID, ConnectionID: connID,
				DeviceID: serverID, Name: serverName})
			log.Printf("[p2p] paired with server %s (%s)", serverName, serverID[:12])
			adopted = true
			go RunSession(c, serverID, serverName, "client", h, emit)
			return
		case event.TypePairReject:
			emit(BusEvent{Kind: "pair_reject", PairingID: pairingID, Msg: "rejected by server"})
			return
		case event.TypePairRetry:
			var pay event.PairRetryPayload
			_ = event.DecodePayload(reply, &pay)
			emit(BusEvent{Kind: "pair_retry", PairingID: pairingID, AttemptsRemaining: pay.AttemptsRemaining})
		}
	}
}
