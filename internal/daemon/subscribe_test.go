package daemon

import (
	"testing"
	"time"
)

func TestSubscribeReceivesEvents(t *testing.T) {
	d := New(Options{SocketPath: "/tmp/mb-sub-test.sock", TCPPort: 0})
	ch := d.Subscribe()
	defer d.Unsubscribe(ch)

	d.broadcast(Event{Event: "log", Msg: "hello"})

	select {
	case ev := <-ch:
		if ev.Msg != "hello" {
			t.Fatalf("got %q, want %q", ev.Msg, "hello")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for event")
	}
}
