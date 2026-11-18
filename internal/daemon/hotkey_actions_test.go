package daemon

import (
	"net"
	"testing"
	"time"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/helper"
	"github.com/mousebridge/core/internal/transport"
)

func attachSwitchTestPeer(t *testing.T, d *Daemon, deviceID string) <-chan event.Message {
	t.Helper()
	local, remote := net.Pipe()
	conn := transport.NewConn(local)
	d.TrackConn(conn, deviceID)
	t.Cleanup(func() { _ = conn.Close() })

	messages := make(chan event.Message, 256)
	go func() {
		defer close(messages)
		peer := transport.NewConn(remote)
		defer peer.Close()
		for {
			msg, err := peer.Recv()
			if err != nil {
				return
			}
			messages <- msg
			if msg.Type == event.TypeSwitchRequest {
				var request event.SwitchRequestPayload
				if event.DecodePayload(msg, &request) == nil {
					_ = peer.Send(event.Message{V: 1, Type: event.TypeSwitchAck, Payload: event.SwitchAckPayload{
						RequestID: request.RequestID,
						Granted:   true,
					}})
				}
			} else if msg.Type == event.TypeSwitchBack {
				var release event.SwitchBackPayload
				if event.DecodePayload(msg, &release) == nil && release.RequestID != "" {
					_ = peer.Send(event.Message{V: 1, Type: event.TypeSwitchAck, Payload: event.SwitchAckPayload{
						RequestID: release.RequestID,
						Released:  true,
					}})
				}
			}
		}
	}()
	go func() {
		for {
			msg, err := conn.Recv()
			if err != nil {
				return
			}
			if msg.Type != event.TypeSwitchAck {
				continue
			}
			var ack event.SwitchAckPayload
			if event.DecodePayload(msg, &ack) == nil {
				d.HandleRemoteSwitchAck(deviceID, ack)
			}
		}
	}()
	return messages
}

func attachLegacyAckTestPeer(t *testing.T, d *Daemon, deviceID string) <-chan event.Message {
	t.Helper()
	local, remote := net.Pipe()
	conn := transport.NewConn(local)
	d.TrackConn(conn, deviceID)
	t.Cleanup(func() { _ = conn.Close() })
	messages := make(chan event.Message, 16)
	go func() {
		peer := transport.NewConn(remote)
		defer peer.Close()
		for {
			msg, err := peer.Recv()
			if err != nil {
				return
			}
			messages <- msg
			if msg.Type == event.TypeSwitchRequest {
				_ = peer.Send(event.Message{V: 1, Type: event.TypeSwitchAck, Payload: struct{}{}})
			} else if msg.Type == event.TypeSwitchBack {
				_ = peer.Send(event.Message{V: 1, Type: event.TypeSwitchAck, Payload: struct{}{}})
			}
		}
	}()
	go func() {
		for {
			msg, err := conn.Recv()
			if err != nil {
				return
			}
			if msg.Type == event.TypeSwitchAck {
				var ack event.SwitchAckPayload
				if event.DecodePayload(msg, &ack) == nil {
					d.HandleRemoteSwitchAck(deviceID, ack)
				}
			}
		}
	}()
	return messages
}

func attachRejectingReleaseTestPeer(t *testing.T, d *Daemon, deviceID string) <-chan event.Message {
	t.Helper()
	local, remote := net.Pipe()
	conn := transport.NewConn(local)
	d.TrackConn(conn, deviceID)
	t.Cleanup(func() { _ = conn.Close() })
	messages := make(chan event.Message, 16)
	go func() {
		peer := transport.NewConn(remote)
		defer peer.Close()
		for {
			msg, err := peer.Recv()
			if err != nil {
				return
			}
			messages <- msg
			if msg.Type == event.TypeSwitchBack {
				var release event.SwitchBackPayload
				if event.DecodePayload(msg, &release) == nil && release.RequestID != "" {
					_ = peer.Send(event.Message{V: 1, Type: event.TypeSwitchAck, Payload: event.SwitchAckPayload{
						RequestID: release.RequestID,
						Reason:    "peer_busy",
					}})
				}
			}
		}
	}()
	go func() {
		for {
			msg, err := conn.Recv()
			if err != nil {
				return
			}
			if msg.Type != event.TypeSwitchAck {
				continue
			}
			var ack event.SwitchAckPayload
			if event.DecodePayload(msg, &ack) == nil {
				d.HandleRemoteSwitchAck(deviceID, ack)
			}
		}
	}()
	return messages
}

func requireSwitchMessage(t *testing.T, messages <-chan event.Message, want string) {
	t.Helper()
	select {
	case msg := <-messages:
		if msg.Type != want {
			t.Fatalf("switch message type=%q want %q", msg.Type, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}

func TestHelperHotkeySwitchNextUpdatesActiveTarget(t *testing.T) {
	d, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}

	d.SessionAdd("c1", "remote-b", "Remote B", "127.0.0.1", "client")
	d.SessionAdd("c2", "remote-a", "Remote A", "127.0.0.1", "client")
	peerA := attachSwitchTestPeer(t, d, "remote-a")
	peerB := attachSwitchTestPeer(t, d, "remote-b")

	d.handleHelperHotkey(helper.HotkeyPayload{Action: "switch_next", Combo: "ctrl+alt+right"})
	requireSwitchMessage(t, peerA, event.TypeSwitchRequest)
	if got := d.helperConfigSnapshot().ActiveTarget; got != "remote-a" {
		t.Fatalf("first switch_next active_target=%q want remote-a", got)
	}

	d.handleHelperHotkey(helper.HotkeyPayload{Action: "switch_next", Combo: "ctrl+alt+right"})
	requireSwitchMessage(t, peerA, event.TypeSwitchBack)
	requireSwitchMessage(t, peerB, event.TypeSwitchRequest)
	if got := d.helperConfigSnapshot().ActiveTarget; got != "remote-b" {
		t.Fatalf("second switch_next active_target=%q want remote-b", got)
	}

	d.handleHelperHotkey(helper.HotkeyPayload{Action: "switch_prev", Combo: "ctrl+alt+left"})
	requireSwitchMessage(t, peerB, event.TypeSwitchBack)
	requireSwitchMessage(t, peerA, event.TypeSwitchRequest)
	if got := d.helperConfigSnapshot().ActiveTarget; got != "remote-a" {
		t.Fatalf("switch_prev active_target=%q want remote-a", got)
	}

	d.handleHelperHotkey(helper.HotkeyPayload{Action: "switch_to_host", Combo: "ctrl+alt+home"})
	requireSwitchMessage(t, peerA, event.TypeSwitchBack)
	if got := d.helperConfigSnapshot().ActiveTarget; got != d.identity.DeviceID {
		t.Fatalf("switch_to_host active_target=%q want local %q", got, d.identity.DeviceID)
	}
}

func TestHelperHotkeyTogglePauseUpdatesSnapshot(t *testing.T) {
	d, err := New(newTestOptions(t))
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
	d, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}

	d.cfg.EdgeTargets.Right = "remote-a"
	d.ctrl.SetDwell(0)
	d.SessionAdd("c1", "remote-a", "Remote A", "127.0.0.1", "client")
	peer := attachSwitchTestPeer(t, d, "remote-a")

	d.handleHelperEdge(helper.EdgePayload{Edge: "right", Pct: 0.5})
	d.handleHelperEdge(helper.EdgePayload{Edge: "right", Pct: 0.5})
	requireSwitchMessage(t, peer, event.TypeSwitchRequest)

	if got := d.helperConfigSnapshot().ActiveTarget; got != "remote-a" {
		t.Fatalf("edge switch active_target=%q want remote-a", got)
	}
}

func TestHelperEdgeSwitchIgnoresUnknownTarget(t *testing.T) {
	d, err := New(newTestOptions(t))
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
