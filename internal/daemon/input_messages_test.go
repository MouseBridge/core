package daemon

import (
	"testing"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/helper"
)

func TestSessionInputMessagesMouseMoveAbs(t *testing.T) {
	x := 320.0
	y := 240.0
	msgs, err := sessionInputMessages(helper.InputPayload{
		Kind:   "mouse_move_abs",
		X:      &x,
		Y:      &y,
		Button: "left",
	})
	if err != nil {
		t.Fatalf("sessionInputMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs)=%d want 1", len(msgs))
	}
	if msgs[0].Type != event.TypeMouseMoveAbs {
		t.Fatalf("type=%q want %q", msgs[0].Type, event.TypeMouseMoveAbs)
	}
	payload, ok := msgs[0].Payload.(event.MouseMoveAbsPayload)
	if !ok {
		t.Fatalf("payload type=%T want event.MouseMoveAbsPayload", msgs[0].Payload)
	}
	if payload.X != x || payload.Y != y {
		t.Fatalf("payload=(%v,%v) want (%v,%v)", payload.X, payload.Y, x, y)
	}
	if payload.Button != "left" {
		t.Fatalf("button=%q want left", payload.Button)
	}
}

func TestSessionInputMessagesMouseMoveAbsRequiresCoordinates(t *testing.T) {
	_, err := sessionInputMessages(helper.InputPayload{Kind: "mouse_move_abs"})
	if err == nil {
		t.Fatal("expected error when x/y are missing")
	}
}
