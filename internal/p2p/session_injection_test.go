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

func TestRunSessionForwardsMouseMoveAbsToHelper(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	host := &fakeSessionHost{inputs: make(chan helper.InputPayload, 1)}

	go RunSession(transport.NewConn(server), "1234567890abcdef1234567890abcdef", "Remote", "client", host, func(BusEvent) {})

	msg := event.Message{
		V:    1,
		Seq:  1,
		Type: event.TypeMouseMoveAbs,
		Ts:   time.Now().UnixMilli(),
		Payload: event.MouseMoveAbsPayload{
			X:      640,
			Y:      360,
			Button: "left",
		},
	}
	if err := transport.NewConn(client).Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case input := <-host.inputs:
		if input.Kind != "mouse_move_abs" {
			t.Fatalf("kind=%q want mouse_move_abs", input.Kind)
		}
		if input.X == nil || input.Y == nil {
			t.Fatal("expected absolute coordinates in forwarded helper input")
		}
		if *input.X != 640 || *input.Y != 360 {
			t.Fatalf("x/y=(%v,%v) want (640,360)", *input.X, *input.Y)
		}
		if input.Button != "left" {
			t.Fatalf("button=%q want left", input.Button)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for forwarded absolute helper input")
	}
}

func TestRunSessionForwardsTextToHelper(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	host := &fakeSessionHost{inputs: make(chan helper.InputPayload, 1)}

	go RunSession(transport.NewConn(server), "1234567890abcdef1234567890abcdef", "Remote", "client", host, func(BusEvent) {})

	msg := event.Message{
		V:       1,
		Seq:     1,
		Type:    event.TypeText,
		Ts:      time.Now().UnixMilli(),
		Payload: event.TextPayload{Text: "hello"},
	}
	if err := transport.NewConn(client).Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case input := <-host.inputs:
		if input.Kind != "text" {
			t.Fatalf("kind=%q want text", input.Kind)
		}
		if input.Text != "hello" {
			t.Fatalf("text=%q want hello", input.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for forwarded helper input")
	}
}
