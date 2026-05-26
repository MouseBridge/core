package transport_test

import (
	"net"
	"testing"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/transport"
)

func TestConnSendReceive(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	sc := transport.NewConn(server)
	cc := transport.NewConn(client)

	sent := event.Message{V: 1, Seq: 1, Type: event.TypePing, Ts: 123, Payload: event.PingPayload{}}

	errCh := make(chan error, 1)
	go func() { errCh <- sc.Send(sent) }()

	got, err := cc.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("send: %v", err)
	}
	if got.Type != event.TypePing {
		t.Fatalf("type: want ping got %q", got.Type)
	}
}
