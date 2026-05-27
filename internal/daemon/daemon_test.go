package daemon_test

import (
	"testing"

	"github.com/mousebridge/core/internal/daemon"
)

func TestDaemonStartStop(t *testing.T) {
	d, err := daemon.New(daemon.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := d.Addr()
	if addr == "" {
		t.Fatal("expected non-empty listen addr after Start")
	}
	d.Stop()
	if d.Addr() != "" {
		t.Fatal("expected empty addr after Stop")
	}
}

func TestDaemonSubscribeBroadcast(t *testing.T) {
	d, err := daemon.New(daemon.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch := d.Subscribe()
	defer d.Unsubscribe(ch)

	// State() should not panic.
	snap := d.State()
	if snap.Daemon.DeviceID == "" {
		t.Fatal("expected non-empty device_id")
	}
}
