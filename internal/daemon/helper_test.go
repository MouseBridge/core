package daemon_test

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/mousebridge/core/internal/daemon"
	"github.com/mousebridge/core/internal/helper"
)

func TestHelperRegisterReceivesConfigPush(t *testing.T) {
	d, err := daemon.New(newTestOptions(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	conn, scanner := connectHelper(t, d.HelperSocketPath())
	defer conn.Close()

	sendHelperMessage(t, conn, helper.TypeRegister, helper.RegisterPayload{
		PID:     4242,
		Name:    "test-helper",
		Version: "0.0.1",
	})

	ack := readHelperEnvelope(t, scanner)
	if ack.Type != helper.TypeAck {
		t.Fatalf("first message type=%q want %q", ack.Type, helper.TypeAck)
	}

	cfgEnv := readHelperEnvelope(t, scanner)
	if cfgEnv.Type != helper.TypeConfigPush {
		t.Fatalf("second message type=%q want %q", cfgEnv.Type, helper.TypeConfigPush)
	}

	var cfg helper.ConfigPushPayload
	if err := helper.DecodePayload(cfgEnv, &cfg); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if cfg.Daemon.DeviceID != d.Identity().DeviceID {
		t.Fatalf("daemon device_id=%q want %q", cfg.Daemon.DeviceID, d.Identity().DeviceID)
	}
	if cfg.Daemon.Name != d.Identity().Name {
		t.Fatalf("daemon name=%q want %q", cfg.Daemon.Name, d.Identity().Name)
	}
	if len(cfg.Sessions) != 0 {
		t.Fatalf("sessions=%d want 0", len(cfg.Sessions))
	}
	if cfg.Hotkeys["switch_next"] != "ctrl+alt+right" {
		t.Fatalf("switch_next=%q want ctrl+alt+right", cfg.Hotkeys["switch_next"])
	}
	if cfg.Hotkeys["switch_to_host"] != "ctrl+alt+escape" {
		t.Fatalf("switch_to_host=%q want ctrl+alt+escape", cfg.Hotkeys["switch_to_host"])
	}
	if cfg.EdgeTargets["right"] != "" {
		t.Fatalf("edge_targets.right=%q want empty", cfg.EdgeTargets["right"])
	}
	if !cfg.CaptureEnabled {
		t.Fatal("capture_enabled should default true")
	}
}

func TestHelperHotkeyProducesBusLogEvent(t *testing.T) {
	d, err := daemon.New(newTestOptions(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	sub := d.Subscribe()
	defer d.Unsubscribe(sub)

	conn, scanner := connectHelper(t, d.HelperSocketPath())
	defer conn.Close()

	sendHelperMessage(t, conn, helper.TypeRegister, helper.RegisterPayload{Name: "test-helper"})
	_ = readHelperEnvelope(t, scanner)
	_ = readHelperEnvelope(t, scanner)

	sendHelperMessage(t, conn, helper.TypeHotkey, helper.HotkeyPayload{
		Action: "switch_next",
		Combo:  "ctrl+alt+right",
	})

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub:
			if ev.Kind == "log" && ev.Msg == "helper hotkey action=switch_next combo=ctrl+alt+right" {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for helper hotkey bus event")
		}
	}
}

func connectHelper(t *testing.T, socketPath string) (net.Conn, *bufio.Scanner) {
	t.Helper()

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("Dial unix %s: %v", socketPath, err)
	}
	return conn, bufio.NewScanner(conn)
}

func sendHelperMessage(t *testing.T, conn net.Conn, msgType string, payload interface{}) {
	t.Helper()

	data, err := helper.Encode(msgType, payload)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

func readHelperEnvelope(t *testing.T, scanner *bufio.Scanner) helper.Envelope {
	t.Helper()

	if !scanner.Scan() {
		t.Fatal("expected helper message")
	}
	var env helper.Envelope
	if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	return env
}
