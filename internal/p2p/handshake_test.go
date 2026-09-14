package p2p

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/helper"
	"github.com/mousebridge/core/internal/pending"
	"github.com/mousebridge/core/internal/remembered"
	"github.com/mousebridge/core/internal/transport"
)

type fakeServerHost struct {
	localID                      string
	localName                    string
	localDisplayID               string
	pending                      *pending.Manager
	rem                          *remembered.Store
	rememberedEnabled            bool
	rememberedAutoConnectEnabled bool
}

func (f *fakeServerHost) LocalID() string                          { return f.localID }
func (f *fakeServerHost) LocalName() string                        { return f.localName }
func (f *fakeServerHost) LocalDisplayID() string                   { return f.localDisplayID }
func (f *fakeServerHost) PendingManager() *pending.Manager         { return f.pending }
func (f *fakeServerHost) RememberedStore() *remembered.Store       { return f.rem }
func (f *fakeServerHost) RememberedEnabled() bool                  { return f.rememberedEnabled }
func (f *fakeServerHost) RememberedAutoConnectEnabled() bool       { return f.rememberedAutoConnectEnabled }
func (f *fakeServerHost) TrackPendingConn(string, *transport.Conn) {}
func (f *fakeServerHost) UntrackPendingConn(string)                {}

func TestHandleInboundRememberedDeviceRequiresPINWhenAutoConnectDisabled(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	rem, err := remembered.New(filepath.Join(t.TempDir(), "remembered.json"))
	if err != nil {
		t.Fatalf("remembered.New: %v", err)
	}

	clientID := "1234567890abcdef1234567890abcdef"
	now := time.Now()
	if err := rem.Add(remembered.Record{
		DeviceID:   clientID,
		DisplayID:  clientID[:12],
		Name:       "Client",
		SecretID:   "remembered-secret-id",
		PairSecret: "remembered-pair-secret",
		CreatedAt:  now,
		LastSeenAt: now,
	}); err != nil {
		t.Fatalf("rem.Add: %v", err)
	}

	host := &fakeServerHost{
		localID:                      "abcdef1234567890abcdef1234567890",
		localName:                    "Server",
		localDisplayID:               "abcdef123456",
		pending:                      pending.New(2*time.Minute, 3),
		rem:                          rem,
		rememberedEnabled:            true,
		rememberedAutoConnectEnabled: false,
	}
	sessionHost := &fakeSessionHost{inputs: make(chan helper.InputPayload, 1)}
	events := make(chan BusEvent, 4)

	go HandleInbound(transport.NewConn(server), host.localID, host, sessionHost, func(ev BusEvent) {
		events <- ev
	})

	clientConn := transport.NewConn(client)
	errCh := make(chan error, 1)
	go func() {
		errCh <- sendHello(clientConn, clientID, clientID[:12], "Client")
	}()
	if _, err := recvHello(clientConn); err != nil {
		t.Fatalf("recvHello: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("sendHello: %v", err)
	}

	msg, err := clientConn.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if msg.Type != event.TypePairChallenge {
		t.Fatalf("msg.Type=%q want %q", msg.Type, event.TypePairChallenge)
	}

	var request BusEvent
	select {
	case ev := <-events:
		if ev.Kind != "pair_request" {
			t.Fatalf("event kind=%q want pair_request", ev.Kind)
		}
		request = ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pair_request")
	}
	if err := clientConn.Send(event.Message{
		V: 1, Seq: 3, Type: event.TypePairApprove, Ts: time.Now().UnixMilli(),
		Payload: event.PairConfirmPayload{PairingID: request.PairingID, Trusted: false},
	}); err != nil {
		t.Fatalf("Send pair_approve: %v", err)
	}
	accepted, err := clientConn.Recv()
	if err != nil {
		t.Fatalf("Recv pair_accept: %v", err)
	}
	if accepted.Type != event.TypePairAccept {
		t.Fatalf("accepted.Type=%q want %q", accepted.Type, event.TypePairAccept)
	}
	var acceptedPayload event.PairAcceptPayload
	if err := event.DecodePayload(accepted, &acceptedPayload); err != nil {
		t.Fatalf("DecodePayload(pair_accept): %v", err)
	}
	if !acceptedPayload.Remembered || acceptedPayload.Trusted {
		t.Fatalf("one-time pair approval should not create a trusted device: %+v", acceptedPayload)
	}
	record, ok := rem.Get(clientID)
	if !ok || record.TrustedAutoConnect {
		t.Fatalf("server did not persist one-time remembered device: ok=%t record=%+v", ok, record)
	}
}

func TestHandleInboundRememberedDeviceIsAcceptedWithoutPINWhenTrusted(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	storePath := filepath.Join(t.TempDir(), "remembered.json")
	rem, err := remembered.New(storePath)
	if err != nil {
		t.Fatalf("remembered.New: %v", err)
	}

	clientID := "1234567890abcdef1234567890abcdef"
	now := time.Now()
	rec := remembered.Record{
		DeviceID:           clientID,
		DisplayID:          clientID[:12],
		Name:               "Client",
		SecretID:           "remembered-secret-id",
		PairSecret:         "remembered-pair-secret",
		TrustedAutoConnect: true,
		CreatedAt:          now,
		LastSeenAt:         now,
	}
	if err := rem.Add(rec); err != nil {
		t.Fatalf("rem.Add: %v", err)
	}
	// Reopen the persisted store to prove receiver trust survives a restart.
	rem, err = remembered.New(storePath)
	if err != nil {
		t.Fatalf("remembered.New after reload: %v", err)
	}

	host := &fakeServerHost{
		localID:                      "abcdef1234567890abcdef1234567890",
		localName:                    "Server",
		localDisplayID:               "abcdef123456",
		pending:                      pending.New(2*time.Minute, 3),
		rem:                          rem,
		rememberedEnabled:            true,
		rememberedAutoConnectEnabled: true,
	}
	sessionHost := &fakeSessionHost{inputs: make(chan helper.InputPayload, 1)}
	events := make(chan BusEvent, 4)

	go HandleInbound(transport.NewConn(server), host.localID, host, sessionHost, func(ev BusEvent) {
		events <- ev
	})

	clientConn := transport.NewConn(client)
	errCh := make(chan error, 1)
	go func() {
		errCh <- sendHello(clientConn, clientID, clientID[:12], "Client")
	}()
	if _, err := recvHello(clientConn); err != nil {
		t.Fatalf("recvHello: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("sendHello: %v", err)
	}

	msg, err := clientConn.Recv()
	if err != nil {
		t.Fatalf("Recv remembered pair_accept: %v", err)
	}
	if msg.Type != event.TypePairAccept {
		t.Fatalf("msg.Type=%q want pair_accept", msg.Type)
	}

	select {
	case ev := <-events:
		if ev.Kind != "session_connected" {
			t.Fatalf("event kind=%q want session_connected", ev.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session_connected event")
	}
}

func TestDialAndPairAcknowledgesReceiverApproval(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	rem, err := remembered.New(filepath.Join(t.TempDir(), "remembered.json"))
	if err != nil {
		t.Fatalf("remembered.New: %v", err)
	}

	host := &fakeSessionHost{inputs: make(chan helper.InputPayload, 1)}
	tracker := NewOutboundTracker()
	events := make(chan BusEvent, 8)

	clientConn := transport.NewConn(client)
	go DialAndPair(
		clientConn,
		"fedcba0987654321fedcba0987654321",
		"fedcba098765",
		"Client",
		host,
		tracker,
		rem,
		func(ev BusEvent) { events <- ev },
	)

	serverConn := transport.NewConn(server)
	if _, err := recvHello(serverConn); err != nil {
		t.Fatalf("recvHello: %v", err)
	}
	if err := sendHello(serverConn, "0123456789abcdef0123456789abcdef", "0123456789ab", "Server"); err != nil {
		t.Fatalf("sendHello: %v", err)
	}

	pairingID := "pairing-123"
	if err := serverConn.Send(event.Message{
		V: 1, Seq: 1, Type: event.TypePairChallenge, Ts: time.Now().UnixMilli(),
		Payload: event.PairChallengePayload{PairingID: pairingID, ServerName: "Server"},
	}); err != nil {
		t.Fatalf("Send pair_challenge: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Kind != "pair_request" {
			t.Fatalf("event kind=%q want pair_request", ev.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pair_request")
	}

	if err := serverConn.Send(event.Message{
		V: 1, Seq: 2, Type: event.TypePairApprove, Ts: time.Now().UnixMilli(),
		Payload: event.PairConfirmPayload{PairingID: pairingID, Trusted: true},
	}); err != nil {
		t.Fatalf("Send pair_approve: %v", err)
	}

	reply, err := serverConn.Recv()
	if err != nil {
		t.Fatalf("Recv echoed pair_approve: %v", err)
	}
	if reply.Type != event.TypePairApprove {
		t.Fatalf("reply.Type=%q want %q", reply.Type, event.TypePairApprove)
	}

	if err := serverConn.Send(event.Message{
		V: 1, Seq: 3, Type: event.TypePairAccept, Ts: time.Now().UnixMilli(),
		Payload: event.PairAcceptPayload{Remembered: true, Trusted: true, SecretID: "test-secret", PairSecret: "test-pair-secret"},
	}); err != nil {
		t.Fatalf("Send pair_accept: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Kind != "paired" {
			t.Fatalf("event kind=%q want paired", ev.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for paired event")
	}

	record, ok := rem.Get("0123456789abcdef0123456789abcdef")
	if !ok || !record.TrustedAutoConnect {
		t.Fatalf("receiver approval was not persisted as trusted: ok=%t record=%+v", ok, record)
	}
}
