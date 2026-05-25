package daemon_test

import (
	"encoding/json"
	"testing"

	"github.com/mousebridge/core/internal/daemon"
)

func TestEncodeCommand(t *testing.T) {
	cmd := daemon.Command{Cmd: "connect", IP: "192.168.1.5", Port: 39172}
	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got daemon.Command
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Cmd != "connect" || got.IP != "192.168.1.5" || got.Port != 39172 {
		t.Fatalf("round-trip failed: %+v", got)
	}
}

func TestEncodeEvent(t *testing.T) {
	ev := daemon.Event{Event: "pair_request", Name: "MacBook-Pro", PIN: "847291"}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got daemon.Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Event != "pair_request" || got.PIN != "847291" {
		t.Fatalf("round-trip failed: %+v", got)
	}
}
