package p2p

import (
	"net"
	"testing"
	"time"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/helper"
	"github.com/mousebridge/core/internal/transport"
)

type fakeSessionHost struct {
	inputs chan helper.InputPayload
}

func (f *fakeSessionHost) TrackConn(c *transport.Conn, deviceID string)             {}
func (f *fakeSessionHost) UntrackConn(c *transport.Conn)                            {}
func (f *fakeSessionHost) SessionAdd(connectionID, deviceID, name, ip, role string) {}
func (f *fakeSessionHost) SessionRemove(connectionID string)                        {}
func (f *fakeSessionHost) SessionRecordLatency(connectionID string, ms float64)     {}
func (f *fakeSessionHost) PushHelperInput(input helper.InputPayload) {
	f.inputs <- input
}

func TestRunSessionForwardsMouseMoveToHelper(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	host := &fakeSessionHost{inputs: make(chan helper.InputPayload, 1)}
	events := make(chan BusEvent, 4)

	go RunSession(transport.NewConn(server), "1234567890abcdef1234567890abcdef", "Remote", "client", host, func(ev BusEvent) {
		events <- ev
	})

	msg := event.Message{
		V:    1,
		Seq:  1,
		Type: event.TypeMouseMove,
		Ts:   time.Now().UnixMilli(),
		Payload: event.MouseMovePayload{
			DX:     12,
			DY:     -4,
			Button: "left",
		},
	}
	if err := transport.NewConn(client).Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case input := <-host.inputs:
		if input.Kind != "mouse_move" {
			t.Fatalf("kind=%q want mouse_move", input.Kind)
		}
		if input.DX != 12 || input.DY != -4 {
			t.Fatalf("dx/dy=(%v,%v) want (12,-4)", input.DX, input.DY)
		}
		if input.Button != "left" {
			t.Fatalf("button=%q want left", input.Button)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for forwarded helper input")
	}
}
