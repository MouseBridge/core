package p2p

import (
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/pairing"
	"github.com/mousebridge/core/internal/transport"
)

// InboundHost is the subset of daemon.Daemon needed for the inbound handshake.
type InboundHost interface {
	LocalID() string
	LocalName() string
	IsTrusted(deviceID string) bool
	AddPendingSlave(deviceID string, entry *PendingSlaveEntry)
	RemovePendingSlave(deviceID string)
}

// PendingSlaveEntry holds state for a slave-side pairing waiting on user accept/reject.
type PendingSlaveEntry struct {
	Mgr  *pairing.Manager
	Conn *transport.Conn
	Name string
}

// HandleInbound drives the inbound pairing flow for one new P2P connection.
// On success, calls RunSlaveSession in a new goroutine.
func HandleInbound(c *transport.Conn, h InboundHost, sessionHost Host, emit func(Event)) {
	nowMs := func() int64 { return time.Now().UnixMilli() }
	var peerDeviceID string
	adopted := false

	defer func() {
		if peerDeviceID != "" {
			h.RemovePendingSlave(peerDeviceID)
		}
		if !adopted {
			c.Close()
		}
	}()

	remote := c.RemoteAddr().String()
	hs, err := c.Recv()
	if err != nil {
		log.Printf("[p2p] handshake from %s: %v", remote, err)
		return
	}
	var hsPay event.HandshakePayload
	if err := event.DecodePayload(hs, &hsPay); err != nil || hs.Type != event.TypeHandshake {
		return
	}
	_ = c.Send(event.Message{
		V: 1, Seq: 1, Type: event.TypeHandshake, Ts: nowMs(),
		Payload: event.HandshakePayload{DeviceID: h.LocalID(), Name: h.LocalName(), Platform: "macos"},
	})

	pairMsg, err := c.Recv()
	if err != nil || pairMsg.Type != event.TypePairRequest {
		return
	}
	var prPay event.PairRequestPayload
	_ = event.DecodePayload(pairMsg, &prPay)

	if h.IsTrusted(prPay.DeviceID) {
		_ = c.Send(event.Message{V: 1, Seq: 2, Type: event.TypePairPin, Ts: nowMs(), Payload: event.PairPinPayload{}})
		_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairAccept, Ts: nowMs(), Payload: struct{}{}})
		emit(Event{Kind: "paired", DeviceID: prPay.DeviceID, Name: prPay.Name})
		log.Printf("[p2p] trusted device %s auto-accepted", prPay.Name)
		adopted = true
		go RunSlaveSession(c, prPay.DeviceID, prPay.Name, sessionHost, emit)
		return
	}

	mgr := pairing.NewManager()
	pin, err := mgr.StartAsSlave(prPay.DeviceID, prPay.Name)
	if err != nil {
		return
	}

	peerDeviceID = prPay.DeviceID
	entry := &PendingSlaveEntry{Mgr: mgr, Conn: c, Name: prPay.Name}
	h.AddPendingSlave(peerDeviceID, entry)

	_ = c.Send(event.Message{
		V: 1, Seq: 2, Type: event.TypePairPin, Ts: nowMs(),
		Payload: event.PairPinPayload{},
	})
	emit(Event{Kind: "pair_request", DeviceID: prPay.DeviceID, Name: prPay.Name, PIN: pin, Role: "slave"})
	log.Printf("[p2p] pair_request from %s (id=%s) — PIN: %s", prPay.Name, prPay.DeviceID, pin)

	confirmCh := make(chan string, 1)
	go func() {
		for {
			msg, err := c.Recv()
			if err != nil {
				return
			}
			if msg.Type == event.TypePairConfirm {
				var pay event.PairConfirmPayload
				_ = event.DecodePayload(msg, &pay)
				confirmCh <- pay.PIN
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
			emit(Event{Kind: "error", Msg: "pairing timed out"})
			_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{}})
			h.RemovePendingSlave(peerDeviceID)
			peerDeviceID = ""
			return
		case pin := <-confirmCh:
			if err := mgr.ConfirmPIN(pin); err != nil {
				_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{}})
				emit(Event{Kind: "error", Msg: "wrong PIN"})
				h.RemovePendingSlave(peerDeviceID)
				peerDeviceID = ""
				return
			}
			_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairAccept, Ts: nowMs(), Payload: struct{}{}})
			emit(Event{Kind: "paired", DeviceID: prPay.DeviceID, Name: prPay.Name})
			h.RemovePendingSlave(peerDeviceID)
			peerDeviceID = ""
			adopted = true
			go RunSlaveSession(c, prPay.DeviceID, prPay.Name, sessionHost, emit)
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
