package event_test

import (
	"testing"

	"github.com/mousebridge/core/internal/event"
)

func TestEncodeDecodeMouseMove(t *testing.T) {
	msg := event.Message{
		V:   1,
		Seq: 42,
		Type: event.TypeMouseMove,
		Ts:  1716000000123,
		Payload: event.MouseMovePayload{
			DX: 3, DY: -2,
			AbsX: 1440, AbsY: 900,
			ScreenW: 2560, ScreenH: 1440,
		},
	}

	line, err := event.Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if line[len(line)-1] != '\n' {
		t.Fatal("encoded line must end with newline")
	}

	got, err := event.Decode(line)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Type != event.TypeMouseMove {
		t.Fatalf("type: want %q got %q", event.TypeMouseMove, got.Type)
	}
	if got.Seq != 42 {
		t.Fatalf("seq: want 42 got %d", got.Seq)
	}
}

func TestDecodeKeyDown(t *testing.T) {
	raw := []byte(`{"v":1,"seq":1,"type":"key_down","ts":100,"payload":{"code":65,"mods":0}}` + "\n")
	msg, err := event.Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != event.TypeKeyDown {
		t.Fatalf("type: want key_down got %q", msg.Type)
	}
}
