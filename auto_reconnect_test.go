package main_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/daemon"
	"github.com/mousebridge/core/internal/remembered"
)

func TestTrustedClientAutoReconnectsAfterUnexpectedDisconnect(t *testing.T) {
	serverDir := t.TempDir()
	serverCfg := config.Default()
	serverCfg.Port = 39581
	serverCfg.DeviceName = "auto-reconnect-server"
	serverCfg.PairingPINTTLSeconds = 20
	if err := serverCfg.Save(filepath.Join(serverDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	server, err := daemon.New(daemon.Options{DataDir: serverDir, ConfigPath: filepath.Join(serverDir, "config.json")})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { server.Stop() }()

	clientDir := t.TempDir()
	clientCfg := config.Default()
	clientCfg.Port = 39582
	clientCfg.DeviceName = "auto-reconnect-client"
	if err := clientCfg.Save(filepath.Join(clientDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	client, err := daemon.New(daemon.Options{DataDir: clientDir, ConfigPath: filepath.Join(clientDir, "config.json")})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	defer client.Stop()

	serverEvents := server.Subscribe()
	clientEvents := client.Subscribe()
	defer server.Unsubscribe(serverEvents)
	defer client.Unsubscribe(clientEvents)

	client.Connect("127.0.0.1", serverCfg.Port)
	var pairingID, pin string
	serverPair, clientPair := false, false
	deadline := time.After(5 * time.Second)
	for !serverPair || !clientPair {
		select {
		case ev := <-serverEvents:
			if ev.Kind == "pair_request" {
				pairingID, pin, serverPair = ev.PairingID, ev.DisplayPIN, true
			}
		case ev := <-clientEvents:
			if ev.Kind == "pair_request" {
				clientPair = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for initial pairing")
		}
	}
	if err := client.SendPIN(pairingID, pin); err != nil {
		t.Fatal(err)
	}
	waitForSessionConnected(t, serverEvents)
	waitForSessionConnected(t, clientEvents)

	serverDeviceID := server.Identity().DeviceID
	clientDeviceID := client.Identity().DeviceID
	if err := server.RememberedPatch(clientDeviceID, rememberedPatch(true)); err != nil {
		t.Fatal(err)
	}
	// Trust is owned by the receiver. The controller may still have a local
	// record marked false and must auto-attempt it; the receiver's remembered
	// proof decides whether PIN can be skipped.
	if err := client.RememberedPatch(serverDeviceID, rememberedPatch(false)); err != nil {
		t.Fatal(err)
	}
	if err := server.UpdateRememberedAutoConnectEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateRememberedAutoConnectEnabled(true); err != nil {
		t.Fatal(err)
	}

	// Closing from the receiver side is unexpected to the client, so the
	// client's trusted reconnect worker should restore the session automatically.
	if err := server.DisconnectDevice(clientDeviceID); err != nil {
		t.Fatal(err)
	}
	waitForSessionDisconnected(t, clientEvents)
	waitForSessionConnected(t, clientEvents)

	if got := len(client.State().Sessions); got != 1 {
		t.Fatalf("client sessions after auto reconnect = %d, want 1", got)
	}
	if got := len(client.State().PendingPairs); got != 0 {
		t.Fatalf("client pending pairs after trusted reconnect = %d, want 0", got)
	}

	// Restart the receiver daemon on the same data directory and port. The
	// client must recover the trusted session without a new PIN request.
	server.Stop()
	waitForSessionDisconnected(t, clientEvents)
	restartedServer, err := daemon.New(daemon.Options{DataDir: serverDir, ConfigPath: filepath.Join(serverDir, "config.json")})
	if err != nil {
		t.Fatal(err)
	}
	server = restartedServer
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	waitForSessionConnected(t, clientEvents)
	if got := len(client.State().PendingPairs); got != 0 {
		t.Fatalf("client pending pairs after receiver restart = %d, want 0", got)
	}
}

func rememberedPatch(enabled bool) remembered.Patch {
	return remembered.Patch{TrustedAutoConnect: &enabled}
}

func waitForSessionConnected(t *testing.T, events <-chan daemon.BusEvent) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == "session_connected" {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for session_connected")
		}
	}
}

func waitForSessionDisconnected(t *testing.T, events <-chan daemon.BusEvent) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == "session_disconnected" {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for session_disconnected")
		}
	}
}
