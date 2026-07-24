package daemon

import (
	"testing"
	"time"
)

func TestSubscribeReceivesEvents(t *testing.T) {
	d, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	ch := d.Subscribe()
	defer d.Unsubscribe(ch)

	d.broadcast(BusEvent{Kind: "log", Msg: "hello"})

	select {
	case ev := <-ch:
		if ev.Msg != "hello" {
			t.Fatalf("got %q, want %q", ev.Msg, "hello")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for event")
	}
}

func TestStartListens(t *testing.T) {
	d, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()

	if d.Addr() == "" {
		t.Fatal("expected non-empty addr after Start")
	}
}
