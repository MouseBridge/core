package daemon

import (
	"testing"

	"github.com/mousebridge/core/internal/helper"
)

func TestHelperHotkeySwitchNextUpdatesActiveTarget(t *testing.T) {
	d, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	d.SessionAdd("c1", "remote-b", "Remote B", "127.0.0.1", "client")
	d.SessionAdd("c2", "remote-a", "Remote A", "127.0.0.1", "client")

	d.handleHelperHotkey(helper.HotkeyPayload{Action: "switch_next", Combo: "ctrl+alt+right"})
	if got := d.helperConfigSnapshot().ActiveTarget; got != "remote-a" {
		t.Fatalf("first switch_next active_target=%q want remote-a", got)
	}

	d.handleHelperHotkey(helper.HotkeyPayload{Action: "switch_next", Combo: "ctrl+alt+right"})
	if got := d.helperConfigSnapshot().ActiveTarget; got != "remote-b" {
		t.Fatalf("second switch_next active_target=%q want remote-b", got)
	}

	d.handleHelperHotkey(helper.HotkeyPayload{Action: "switch_prev", Combo: "ctrl+alt+left"})
	if got := d.helperConfigSnapshot().ActiveTarget; got != "remote-a" {
		t.Fatalf("switch_prev active_target=%q want remote-a", got)
	}

	d.handleHelperHotkey(helper.HotkeyPayload{Action: "switch_to_host", Combo: "ctrl+alt+home"})
	if got := d.helperConfigSnapshot().ActiveTarget; got != d.identity.DeviceID {
		t.Fatalf("switch_to_host active_target=%q want local %q", got, d.identity.DeviceID)
	}
}

func TestHelperHotkeyTogglePauseUpdatesSnapshot(t *testing.T) {
	d, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	if d.helperConfigSnapshot().Paused {
		t.Fatal("paused should default false")
	}

	d.handleHelperHotkey(helper.HotkeyPayload{Action: "toggle_pause", Combo: "ctrl+alt+p"})
	if !d.helperConfigSnapshot().Paused {
		t.Fatal("paused should be true after toggle")
	}

	d.handleHelperHotkey(helper.HotkeyPayload{Action: "toggle_pause", Combo: "ctrl+alt+p"})
	if d.helperConfigSnapshot().Paused {
		t.Fatal("paused should be false after second toggle")
	}
}

func TestHelperEdgeSwitchUpdatesActiveTarget(t *testing.T) {
	d, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	d.cfg.EdgeTargets.Right = "remote-a"
	d.ctrl.SetDwell(0)
	d.SessionAdd("c1", "remote-a", "Remote A", "127.0.0.1", "client")

	d.handleHelperEdge(helper.EdgePayload{Edge: "right", Pct: 0.5})
	d.handleHelperEdge(helper.EdgePayload{Edge: "right", Pct: 0.5})

	if got := d.helperConfigSnapshot().ActiveTarget; got != "remote-a" {
		t.Fatalf("edge switch active_target=%q want remote-a", got)
	}
}

func TestHelperEdgeSwitchIgnoresUnknownTarget(t *testing.T) {
	d, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	d.cfg.EdgeTargets.Right = "remote-a"
	d.ctrl.SetDwell(0)

	d.handleHelperEdge(helper.EdgePayload{Edge: "right", Pct: 0.5})
	d.handleHelperEdge(helper.EdgePayload{Edge: "right", Pct: 0.5})

	if got := d.helperConfigSnapshot().ActiveTarget; got != d.identity.DeviceID {
		t.Fatalf("active_target=%q want local %q", got, d.identity.DeviceID)
	}
}
