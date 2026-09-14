package daemon

import (
	"testing"
	"time"

	"github.com/mousebridge/core/internal/p2p"
	"github.com/mousebridge/core/internal/remembered"
)

func TestStartImmediatelyAttemptsTrustedReconnect(t *testing.T) {
	d, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.rem.Add(remembered.Record{
		DeviceID:           "remembered-device",
		DisplayID:          "remembered",
		Name:               "remembered",
		TrustedAutoConnect: true,
		CreatedAt:          time.Now(),
		Endpoint:           "127.0.0.1:1",
	}); err != nil {
		t.Fatal(err)
	}
	sub := d.Subscribe()
	defer d.Unsubscribe(sub)
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub:
			if ev.Kind == "error" && ev.DeviceID == "remembered-device" && len(ev.Msg) > 0 {
				return
			}
		case <-deadline:
			t.Fatal("trusted reconnect was not attempted immediately after Start")
		}
	}
}

func TestTrustedClientConnectionSelectsRememberedDevice(t *testing.T) {
	d, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.rem.Add(remembered.Record{
		DeviceID:           "remote-device",
		TrustedAutoConnect: true,
		CreatedAt:          time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	d.emitBus(p2p.BusEvent{Kind: "session_connected", DeviceID: "remote-device", Name: "remote", Role: "client"})
	if got := d.ctrl.ActiveTarget(); got != "remote-device" {
		t.Fatalf("active target=%q want remote-device", got)
	}
}
