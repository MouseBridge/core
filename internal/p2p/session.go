package p2p

import (
	"fmt"
	"os"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/helper"
	"github.com/mousebridge/core/internal/remembered"
	"github.com/mousebridge/core/internal/transport"
)

var verboseSessionInputLogs = os.Getenv("MB_VERBOSE_INPUT_LOGS") == "1"

func emitVerboseSessionLog(emit func(BusEvent), msg string) {
	if verboseSessionInputLogs {
		emit(BusEvent{Kind: "log", Msg: msg})
	}
}

// SessionHost is the subset of daemon that active sessions need.
type SessionHost interface {
	TrackConn(c *transport.Conn, deviceID string)
	UntrackConn(c *transport.Conn)
	SessionAdd(connectionID, deviceID, name, ip, role string)
	SessionRemove(connectionID string)
	SessionRecordLatency(connectionID string, ms float64)
	PushHelperInput(input helper.InputPayload)
}

// ClipboardSessionHost is an optional interface implemented by SessionHost for clipboard sync.
type ClipboardSessionHost interface {
	HandleRemoteClipboard(deviceID string, pay event.ClipboardSyncPayload)
}

// FileTransferSessionHost is an optional interface implemented by SessionHost for file transfer.
type FileTransferSessionHost interface {
	HandleFileStart(deviceID string, pay event.FileStartPayload)
	HandleFileChunk(deviceID string, pay event.FileChunkPayload)
	HandleFileEnd(deviceID string, pay event.FileEndPayload)
}

// SwitchSessionHost coordinates control changes across both sides of a session.
type SwitchSessionHost interface {
	HandleRemoteSwitchRequest(deviceID string, pay event.SwitchRequestPayload) event.SwitchAckPayload
	HandleRemoteSwitchBack(deviceID string, pay event.SwitchBackPayload) event.SwitchAckPayload
}

// SwitchAckSessionHost receives correlated grant/reject responses.
type SwitchAckSessionHost interface {
	HandleRemoteSwitchAck(deviceID string, pay event.SwitchAckPayload)
}

// SwitchGrantSessionHost activates input routing after the grant ACK is written.
type SwitchGrantSessionHost interface {
	ConfirmRemoteSwitchGrant(deviceID, requestID string)
}

// InputAuthorizationHost gates data-plane input on an active switch grant.
type InputAuthorizationHost interface {
	CanReceiveInput(deviceID string) bool
}

func canReceiveInput(h SessionHost, deviceID string) bool {
	auth, ok := h.(InputAuthorizationHost)
	return !ok || auth.CanReceiveInput(deviceID)
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

	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		timer := time.NewTimer(80 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-stopPing:
			return
		case <-timer.C:
			_ = c.Send(event.Message{V: 1, Seq: 1, Type: event.TypePing, Ts: time.Now().UnixMilli(), Payload: struct{}{}})
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-ticker.C:
				_ = c.Send(event.Message{V: 1, Seq: 1, Type: event.TypePing, Ts: time.Now().UnixMilli(), Payload: struct{}{}})
			}
		}
	}()

	var seq int64 = 10
	for {
		msg, err := c.Recv()
		if err != nil {
			h.SessionRemove(connID)
			emit(BusEvent{Kind: "session_disconnected", DeviceID: deviceID, Name: name, Role: role})
			return
		}
		latency := float64(time.Now().UnixMilli() - msg.Ts)
		if latency >= 0 && latency < 250 {
			h.SessionRecordLatency(connID, latency)
		}

		switch msg.Type {
		case event.TypeMouseMove:
			if !canReceiveInput(h, deviceID) {
				continue
			}
			var pay event.MouseMovePayload
			_ = event.DecodePayload(msg, &pay)
			h.PushHelperInput(helper.InputPayload{
				Kind:   "mouse_move",
				DX:     pay.DX,
				DY:     pay.DY,
				Button: pay.Button,
			})
			emitVerboseSessionLog(emit, fmt.Sprintf("recv mouse_move dx=%.1f dy=%.1f button=%s latency=%.1fms", pay.DX, pay.DY, pay.Button, latency))

		case event.TypeMouseMoveAbs:
			if !canReceiveInput(h, deviceID) {
				continue
			}
			var pay event.MouseMoveAbsPayload
			_ = event.DecodePayload(msg, &pay)
			h.PushHelperInput(helper.InputPayload{
				Kind:   "mouse_move_abs",
				X:      &pay.X,
				Y:      &pay.Y,
				Button: pay.Button,
			})
			emitVerboseSessionLog(emit, fmt.Sprintf("recv mouse_move_abs x=%.1f y=%.1f button=%s latency=%.1fms", pay.X, pay.Y, pay.Button, latency))

		case event.TypeMouseButton:
			if !canReceiveInput(h, deviceID) {
				continue
			}
			var pay event.MouseButtonPayload
			_ = event.DecodePayload(msg, &pay)
			h.PushHelperInput(helper.InputPayload{
				Kind:       "mouse_button",
				Button:     pay.Button,
				Pressed:    pay.Pressed,
				ClickCount: pay.ClickCount,
			})
			emitVerboseSessionLog(emit, fmt.Sprintf("recv mouse_button button=%s pressed=%t latency=%.1fms", pay.Button, pay.Pressed, latency))

		case event.TypeKeyDown:
			if !canReceiveInput(h, deviceID) {
				continue
			}
			var pay event.KeyDownPayload
			_ = event.DecodePayload(msg, &pay)
			h.PushHelperInput(helper.InputPayload{
				Kind:      "key_down",
				KeyCode:   int64(pay.Code),
				Modifiers: int64(pay.Mods),
			})
			emitVerboseSessionLog(emit, fmt.Sprintf("recv key_down code=%d mods=%d latency=%.1fms", pay.Code, pay.Mods, latency))

		case event.TypeKeyUp:
			if !canReceiveInput(h, deviceID) {
				continue
			}
			var pay event.KeyUpPayload
			_ = event.DecodePayload(msg, &pay)
			h.PushHelperInput(helper.InputPayload{
				Kind:      "key_up",
				KeyCode:   int64(pay.Code),
				Modifiers: int64(pay.Mods),
			})
			emitVerboseSessionLog(emit, fmt.Sprintf("recv key_up code=%d mods=%d latency=%.1fms", pay.Code, pay.Mods, latency))

		case event.TypeText:
			if !canReceiveInput(h, deviceID) {
				continue
			}
			var pay event.TextPayload
			_ = event.DecodePayload(msg, &pay)
			h.PushHelperInput(helper.InputPayload{
				Kind: "text",
				Text: pay.Text,
			})
			emitVerboseSessionLog(emit, fmt.Sprintf("recv text len=%d latency=%.1fms", len(pay.Text), latency))

		case event.TypeScroll:
			if !canReceiveInput(h, deviceID) {
				continue
			}
			var pay event.ScrollPayload
			_ = event.DecodePayload(msg, &pay)
			h.PushHelperInput(helper.InputPayload{
				Kind: "scroll",
				DX:   pay.DX,
				DY:   pay.DY,
			})
			emitVerboseSessionLog(emit, fmt.Sprintf("recv scroll dx=%.1f dy=%.1f latency=%.1fms", pay.DX, pay.DY, latency))

		case event.TypeLocateCursor:
			if !canReceiveInput(h, deviceID) {
				continue
			}
			h.PushHelperInput(helper.InputPayload{
				Kind: "locate_cursor",
			})
			emitVerboseSessionLog(emit, fmt.Sprintf("recv locate_cursor latency=%.1fms", latency))

		case event.TypePing:
			_ = c.Send(event.Message{
				V: 1, Seq: seq, Type: event.TypePong, Ts: time.Now().UnixMilli(),
				Payload: event.PongPayload{EchoTs: msg.Ts},
			})
			seq++
			emitVerboseSessionLog(emit, fmt.Sprintf("ping latency=%.1fms", latency))

		case event.TypePong:
			var pay event.PongPayload
			_ = event.DecodePayload(msg, &pay)
			rtt := float64(time.Now().UnixMilli() - pay.EchoTs)
			if rtt >= 0 {
				h.SessionRecordLatency(connID, rtt)
			}
			emitVerboseSessionLog(emit, fmt.Sprintf("pong rtt=%.1fms", rtt))

		case event.TypeSwitchRequest:
			var pay event.SwitchRequestPayload
			_ = event.DecodePayload(msg, &pay)
			ack := event.SwitchAckPayload{RequestID: pay.RequestID, Granted: false, Reason: "switch_handler_unavailable"}
			if sh, ok := h.(SwitchSessionHost); ok {
				ack = sh.HandleRemoteSwitchRequest(deviceID, pay)
			}
			status := "rejected"
			if ack.Granted {
				status = "granted"
			}
			emit(BusEvent{Kind: "log", Msg: fmt.Sprintf("switch_request %s trigger=%s edge=%s latency=%.1fms",
				status, pay.Trigger, pay.Edge, latency)})
			if err := c.Send(event.Message{V: 1, Seq: seq, Type: event.TypeSwitchAck, Ts: time.Now().UnixMilli(), Payload: ack}); err == nil && ack.Granted {
				if gh, ok := h.(SwitchGrantSessionHost); ok {
					gh.ConfirmRemoteSwitchGrant(deviceID, ack.RequestID)
				}
			}
			seq++

		case event.TypeSwitchBack:
			var pay event.SwitchBackPayload
			_ = event.DecodePayload(msg, &pay)
			ack := event.SwitchAckPayload{RequestID: pay.RequestID, Reason: "switch_handler_unavailable"}
			if sh, ok := h.(SwitchSessionHost); ok {
				ack = sh.HandleRemoteSwitchBack(deviceID, pay)
			}
			emit(BusEvent{Kind: "log", Msg: fmt.Sprintf("switch_back from %s latency=%.1fms", name, latency)})
			_ = c.Send(event.Message{V: 1, Seq: seq, Type: event.TypeSwitchAck, Ts: time.Now().UnixMilli(), Payload: ack})
			seq++

		case event.TypeSwitchAck:
			var pay event.SwitchAckPayload
			if event.DecodePayload(msg, &pay) == nil && pay.RequestID != "" {
				if sh, ok := h.(SwitchAckSessionHost); ok {
					sh.HandleRemoteSwitchAck(deviceID, pay)
				}
			}
			emit(BusEvent{Kind: "log", Msg: fmt.Sprintf("switch_ack from %s granted=%t latency=%.1fms", name, pay.Granted, latency)})

		case event.TypeClipboardSync:
			var pay event.ClipboardSyncPayload
			_ = event.DecodePayload(msg, &pay)
			if ch, ok := h.(ClipboardSessionHost); ok {
				ch.HandleRemoteClipboard(deviceID, pay)
			}

		case event.TypeFileStart:
			var pay event.FileStartPayload
			_ = event.DecodePayload(msg, &pay)
			if fh, ok := h.(FileTransferSessionHost); ok {
				fh.HandleFileStart(deviceID, pay)
			}

		case event.TypeFileChunk:
			var pay event.FileChunkPayload
			_ = event.DecodePayload(msg, &pay)
			if fh, ok := h.(FileTransferSessionHost); ok {
				fh.HandleFileChunk(deviceID, pay)
			}

		case event.TypeFileEnd:
			var pay event.FileEndPayload
			_ = event.DecodePayload(msg, &pay)
			if fh, ok := h.(FileTransferSessionHost); ok {
				fh.HandleFileEnd(deviceID, pay)
			}

		default:
			emitVerboseSessionLog(emit, fmt.Sprintf("recv %s latency=%.1fms", msg.Type, latency))
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

	pairingID := ""

	for {
		reply, err := c.Recv()
		if err != nil {
			emit(BusEvent{Kind: "error", Msg: "connection lost during pairing"})
			return
		}
		switch reply.Type {
		case event.TypePairAccept:
			var pay event.PairAcceptPayload
			_ = event.DecodePayload(reply, &pay)
			if pairingID == "" && pay.Remembered {
				if remStore != nil {
					_ = remStore.UpdateLastSeen(serverID, time.Now())
					_ = remStore.UpdateEndpoint(serverID, c.RemoteAddr().String())
					trusted := true
					clientRole := "client"
					_ = remStore.Patch(serverID, remembered.Patch{TrustedAutoConnect: &trusted, Role: &clientRole})
				}
				adopted = true
				go RunSession(c, serverID, serverName, "client", h, emit)
				return
			}

			// Save server as remembered device on client side after successful PIN pairing.
			if remStore != nil {
				if pay.Remembered {
					now := time.Now()
					_ = remStore.Add(remembered.Record{
						DeviceID:           serverID,
						DisplayID:          serverID[:12],
						Name:               serverName,
						SecretID:           pay.SecretID,
						PairSecret:         pay.PairSecret,
						TrustedAutoConnect: pay.Trusted,
						CreatedAt:          now,
						LastSeenAt:         now,
						Endpoint:           c.RemoteAddr().String(),
						Role:               "client",
					})
				}
			}
			emit(BusEvent{Kind: "paired", PairingID: pairingID, ConnectionID: connID,
				DeviceID: serverID, Name: serverName})
			log.Printf("[p2p] paired with server %s (%s)", serverName, serverID[:12])
			adopted = true
			go RunSession(c, serverID, serverName, "client", h, emit)
			return
		case event.TypeRememberedChallenge:
			var pay event.RememberedChallengePayload
			_ = event.DecodePayload(reply, &pay)
			proof := ""
			if remStore != nil {
				if rec, ok := remStore.Get(serverID); ok && rec.SecretID == pay.SecretID {
					proof = computeRememberedProof(rec.PairSecret, localID, serverID, pay.SecretID, pay.Nonce)
				}
			}
			if sendErr := c.Send(event.Message{
				V: 1, Seq: nextSeq(), Type: event.TypeRememberedProof, Ts: nowMs(),
				Payload: event.RememberedProofPayload{SecretID: pay.SecretID, Proof: proof},
			}); sendErr != nil {
				emit(BusEvent{Kind: "error", Msg: "failed to send trusted reconnect proof"})
				return
			}
		case event.TypePairChallenge:
			var challenge event.PairChallengePayload
			_ = event.DecodePayload(reply, &challenge)
			// A PairChallenge means the remote side does not support (or did
			// not use) remembered authentication. Keep the legacy PIN flow so
			// a new client can still connect to an older server binary.
			pairingID = challenge.PairingID
			expiresAt := time.Now().Add(2 * time.Minute)

			// Register in tracker BEFORE emitting pair_request so SendPIN can immediately
			// find the connection when the UI/test reacts to the event.
			if tracker != nil {
				tracker.Add(&OutboundPairing{
					PairingID:       pairingID,
					ConnectionID:    connID,
					RemoteName:      serverName,
					RemoteDisplayID: serverID[:12],
					ExpiresAt:       expiresAt,
					Conn:            c,
				})
				defer tracker.Remove(pairingID)
			}

			emit(BusEvent{
				Kind: "pair_request", PairingID: pairingID, ConnectionID: connID,
				ClaimedDeviceID: serverID, DisplayID: serverID[:12], Name: serverName,
				RemoteIP: c.RemoteAddr().String(), Role: "client",
				AttemptsRemaining: 3, ExpiresAt: expiresAt.Unix(),
			})
			log.Printf("[p2p] pairing with %s — enter PIN shown on remote device", serverName)
		case event.TypePairApprove:
			// Receiver approval is sent to the client first so the client UI
			// can acknowledge it on the same connection. The server-side
			// handshake consumes this echoed approval and completes pairing.
			var approval event.PairConfirmPayload
			_ = event.DecodePayload(reply, &approval)
			if approval.PairingID == "" || approval.PairingID != pairingID {
				emit(BusEvent{Kind: "error", Msg: "received invalid pairing approval"})
				return
			}
			if err := c.Send(event.Message{
				V: 1, Seq: nextSeq(), Type: event.TypePairApprove, Ts: nowMs(),
				Payload: approval,
			}); err != nil {
				emit(BusEvent{Kind: "error", Msg: "failed to acknowledge pairing approval"})
				return
			}
		case event.TypePairReject:
			emit(BusEvent{Kind: "pair_reject", PairingID: pairingID, Msg: "rejected by server"})
			return
		case event.TypePairRetry:
			var pay event.PairRetryPayload
			_ = event.DecodePayload(reply, &pay)
			emit(BusEvent{Kind: "pair_retry", PairingID: pay.PairingID, AttemptsRemaining: pay.AttemptsRemaining})
		}
	}
}
