package p2p

import (
	"crypto/subtle"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/pending"
	"github.com/mousebridge/core/internal/remembered"
	"github.com/mousebridge/core/internal/transport"
	"github.com/mousebridge/core/internal/validate"
)

// ServerHost is the subset of daemon that the inbound handshake needs.
type ServerHost interface {
	LocalID() string
	LocalName() string
	LocalDisplayID() string
	PendingManager() *pending.Manager
	RememberedStore() *remembered.Store
	RememberedEnabled() bool
	RememberedAutoConnectEnabled() bool
	TrackPendingConn(connectionID string, c *transport.Conn)
	UntrackPendingConn(connectionID string)
}

// BusEvent carries session lifecycle updates to the daemon for broadcasting.
type BusEvent struct {
	Kind string `json:"kind"` // "error","log","pair_request","pair_retry","pair_reject","pair_timeout","paired","session_connected","session_disconnected"

	PairingID         string `json:"pairing_id,omitempty"`
	ConnectionID      string `json:"connection_id,omitempty"`
	ClaimedDeviceID   string `json:"claimed_device_id,omitempty"`
	DisplayID         string `json:"display_id,omitempty"`
	Name              string `json:"name,omitempty"`
	RemoteIP          string `json:"remote_ip,omitempty"`
	AttemptsRemaining int    `json:"attempts_remaining,omitempty"`
	ExpiresAt         int64  `json:"expires_at,omitempty"`
	Remembered        bool   `json:"remembered,omitempty"`

	DeviceID   string `json:"device_id,omitempty"`
	Role       string `json:"role,omitempty"`
	DisplayPIN string `json:"display_pin,omitempty"`

	Msg string `json:"msg,omitempty"`
}

// HandleInbound drives the inbound pairing flow for one new P2P connection.
// On success, calls RunSession in a new goroutine.
func HandleInbound(c *transport.Conn, localID string, h ServerHost, sessionHost SessionHost, emit func(BusEvent)) {
	connID := newConnectionID()
	h.TrackPendingConn(connID, c)
	defer h.UntrackPendingConn(connID)
	adopted := false
	var pairingID string

	defer func() {
		if !adopted {
			c.Close()
		}
	}()

	remoteAddr := c.RemoteAddr().String()

	// Exchange hellos.
	if err := sendHello(c, localID, h.LocalDisplayID(), h.LocalName()); err != nil {
		log.Printf("[p2p] send hello to %s: %v", remoteAddr, err)
		return
	}
	peerHello, err := recvHello(c)
	if err != nil {
		log.Printf("[p2p] recv hello from %s: %v", remoteAddr, err)
		return
	}

	// Validate claimed device_id from remote.
	claimedID := peerHello.DeviceID
	if err := validate.DeviceID(claimedID); err != nil {
		log.Printf("[p2p] invalid claimed device_id from %s: %v", remoteAddr, err)
		return
	}
	if claimedID == localID {
		log.Printf("[p2p] remote device_id equals local; rejecting self-connection from %s", remoteAddr)
		return
	}
	if err := validate.SafeString(peerHello.Name); err != nil {
		log.Printf("[p2p] invalid name from %s: %v", remoteAddr, err)
		return
	}
	peerName := peerHello.Name
	peerDisplayID := claimedID[:12]

	// Check remembered.
	rem := h.RememberedStore()
	if h.RememberedAutoConnectEnabled() && peerHello.SupportsRemembered {
		if rec, ok := rem.Get(claimedID); ok {
			if rec.TrustedAutoConnect {
				nonce, err := randomHex16()
				if err == nil {
					_ = c.Send(event.Message{
						V: 1, Seq: nextSeq(), Type: event.TypeRememberedChallenge, Ts: nowMs(),
						Payload: event.RememberedChallengePayload{SecretID: rec.SecretID, Nonce: nonce},
					})

					msg, recvErr := c.Recv()
					if recvErr != nil {
						emit(BusEvent{Kind: "error", Msg: "connection lost during trusted reconnect check"})
						return
					}
					if msg.Type == event.TypeRememberedProof {
						var pay event.RememberedProofPayload
						_ = event.DecodePayload(msg, &pay)
						expected := computeRememberedProof(rec.PairSecret, claimedID, localID, rec.SecretID, nonce)
						if pay.SecretID == rec.SecretID && subtle.ConstantTimeCompare([]byte(pay.Proof), []byte(expected)) == 1 {
							_ = c.Send(event.Message{
								V: 1, Seq: nextSeq(), Type: event.TypePairAccept, Ts: nowMs(),
								Payload: event.PairAcceptPayload{Remembered: true, SecretID: rec.SecretID, PairSecret: rec.PairSecret},
							})
							_ = rem.UpdateLastSeen(claimedID, time.Now())
							log.Printf("[p2p] remembered device %s (%s) authenticated and auto-connected", peerName, claimedID[:12])
							adopted = true
							go RunSession(c, claimedID, peerName, "server", sessionHost, emit)
							return
						}
						log.Printf("[p2p] remembered proof invalid for %s (%s); falling back to PIN", peerName, claimedID[:12])
					} else {
						log.Printf("[p2p] unexpected message %s during remembered auth for %s (%s); falling back to PIN", msg.Type, peerName, claimedID[:12])
					}
				}
			}
		}
	}

	// New device: start PIN pairing.
	pm := h.PendingManager()
	entry, err := pm.Add(connID, claimedID, peerDisplayID, peerName, remoteAddr)
	if err != nil {
		log.Printf("[p2p] pending add: %v", err)
		return
	}
	pairingID = entry.PairingID
	pin := pm.PIN(pairingID)

	// Send pair_challenge to client.
	_ = c.Send(event.Message{
		V: 1, Seq: nextSeq(), Type: event.TypePairChallenge, Ts: nowMs(),
		Payload: event.PairChallengePayload{PairingID: pairingID, ServerName: h.LocalName()},
	})

	emit(BusEvent{
		Kind: "pair_request", PairingID: pairingID, ConnectionID: connID,
		ClaimedDeviceID: claimedID, DisplayID: peerDisplayID, Name: peerName,
		RemoteIP: remoteAddr, AttemptsRemaining: entry.MaxAttempts,
		ExpiresAt:  entry.ExpiresAt.Unix(),
		DisplayPIN: pin, Role: "server",
	})
	log.Printf("[p2p] pair_request from %s (%s) — PIN: %s", peerName, claimedID[:12], pin)

	// Wait for pair_confirm messages.
	approvedByReceiver := false
	for {
		msg, err := c.Recv()
		if err != nil {
			pm.Reject(pairingID)
			emit(BusEvent{Kind: "error", PairingID: pairingID, Msg: "connection lost during pairing"})
			return
		}

		var result pending.VerifyResult
		var e *pending.Entry
		switch msg.Type {
		case event.TypePairConfirm:
			var pay event.PairConfirmPayload
			_ = event.DecodePayload(msg, &pay)
			result, e = pm.Verify(pairingID, pay.PIN)
		case event.TypePairApprove:
			var approval event.PairConfirmPayload
			_ = event.DecodePayload(msg, &approval)
			if approval.PairingID != pairingID {
				continue
			}
			// Explicit approval from the receiver's local UI is the authorization
			// step. PIN confirmation remains supported for older clients.
			e = pm.Reject(pairingID)
			if e == nil {
				continue
			}
			approvedByReceiver = true
			result = pending.VerifyOK
		default:
			continue
		}
		switch result {
		case pending.VerifyOK:
			// Pairing success.
			var accepted event.PairAcceptPayload
			if h.RememberedEnabled() {
				rec, saveErr := buildAndSaveRemembered(claimedID, peerDisplayID, peerName, remoteAddr, rem, approvedByReceiver)
				if saveErr == nil {
					accepted = event.PairAcceptPayload{Remembered: true, Trusted: rec.TrustedAutoConnect, SecretID: rec.SecretID, PairSecret: rec.PairSecret}
				} else {
					log.Printf("[p2p] remembered save failed: %v", saveErr)
					accepted = event.PairAcceptPayload{Remembered: false}
				}
			}
			_ = c.Send(event.Message{
				V: 1, Seq: nextSeq(), Type: event.TypePairAccept, Ts: nowMs(),
				Payload: accepted,
			})
			emit(BusEvent{Kind: "paired", PairingID: pairingID, ConnectionID: connID,
				DeviceID: claimedID, Name: peerName, Remembered: accepted.Remembered})
			log.Printf("[p2p] paired with %s (%s)", peerName, claimedID[:12])
			adopted = true
			go RunSession(c, claimedID, peerName, "server", sessionHost, emit)
			return

		case pending.VerifyRetry:
			remaining := e.AttemptsRemaining()
			_ = c.Send(event.Message{
				V: 1, Seq: nextSeq(), Type: event.TypePairRetry, Ts: nowMs(),
				Payload: event.PairRetryPayload{PairingID: pairingID, AttemptsRemaining: remaining},
			})
			emit(BusEvent{Kind: "pair_retry", PairingID: pairingID, AttemptsRemaining: remaining})

		case pending.VerifyReject:
			_ = c.Send(event.Message{
				V: 1, Seq: nextSeq(), Type: event.TypePairReject, Ts: nowMs(),
				Payload: event.PairRejectPayload{Reason: "invalid_pin"},
			})
			emit(BusEvent{Kind: "pair_reject", PairingID: pairingID, Msg: "invalid_pin"})
			return

		case pending.VerifyExpired:
			_ = c.Send(event.Message{
				V: 1, Seq: nextSeq(), Type: event.TypePairReject, Ts: nowMs(),
				Payload: event.PairRejectPayload{Reason: "expired"},
			})
			emit(BusEvent{Kind: "pair_timeout", PairingID: pairingID})
			return
		}
	}
}

func buildAndSaveRemembered(deviceID, displayID, name, endpoint string, rem *remembered.Store, trusted bool) (remembered.Record, error) {
	secretID, err := randomHex16()
	if err != nil {
		return remembered.Record{}, err
	}
	pairSecret, err := randomHex32()
	if err != nil {
		return remembered.Record{}, err
	}
	now := time.Now()
	rec := remembered.Record{
		DeviceID:           deviceID,
		DisplayID:          displayID,
		Name:               name,
		SecretID:           secretID,
		PairSecret:         pairSecret,
		TrustedAutoConnect: trusted,
		CreatedAt:          now,
		LastSeenAt:         now,
		Endpoint:           endpoint,
	}
	return rec, rem.Add(rec)
}

func randomHex16() (string, error) { return randomHex(8) }
func randomHex32() (string, error) { return randomHex(16) }
