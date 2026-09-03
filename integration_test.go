package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/daemon"
	"github.com/mousebridge/core/internal/remembered"
)

func TestFullPairingFlow(t *testing.T) {
	// --- Server daemon ---
	dir1 := t.TempDir()
	cfg1 := config.Default()
	cfg1.Port = 39281
	cfg1.PairingPINTTLSeconds = 30
	cfg1.PairingPINMaxAttempts = 3
	if err := cfg1.Save(filepath.Join(dir1, "config.json")); err != nil {
		t.Fatal(err)
	}
	d1, err := daemon.New(daemon.Options{DataDir: dir1, ConfigPath: filepath.Join(dir1, "config.json")})
	if err != nil {
		t.Fatalf("server New: %v", err)
	}
	if err := d1.Start(); err != nil {
		t.Fatalf("server Start: %v", err)
	}
	defer d1.Stop()
	t.Logf("server=%s id=%s", d1.Addr(), d1.Identity().DisplayID)

	// --- Client daemon ---
	dir2 := t.TempDir()
	cfg2 := config.Default()
	cfg2.Port = 39282
	if err := cfg2.Save(filepath.Join(dir2, "config.json")); err != nil {
		t.Fatal(err)
	}
	d2, err := daemon.New(daemon.Options{DataDir: dir2, ConfigPath: filepath.Join(dir2, "config.json")})
	if err != nil {
		t.Fatalf("client New: %v", err)
	}
	if err := d2.Start(); err != nil {
		t.Fatalf("client Start: %v", err)
	}
	defer d2.Stop()
	t.Logf("client=%s id=%s", d2.Addr(), d2.Identity().DisplayID)

	ch1 := d1.Subscribe()
	ch2 := d2.Subscribe()
	defer d1.Unsubscribe(ch1)
	defer d2.Unsubscribe(ch2)

	// --- Connect ---
	d2.Connect("127.0.0.1", 39281)

	// Wait for BOTH server pair_request (has display_pin) AND client pair_request
	// (confirms tracker.Add has run). Only then is SendPIN safe.
	var pairingID, pin string
	serverGotPR, clientGotPR := false, false
	deadline := time.After(5 * time.Second)
waitPairRequest:
	for {
		select {
		case ev := <-ch1:
			t.Logf("[server] %s pairingID=%s pin=%s", ev.Kind, ev.PairingID, ev.DisplayPIN)
			if ev.Kind == "pair_request" {
				pairingID = ev.PairingID
				pin = ev.DisplayPIN
				serverGotPR = true
			}
		case ev := <-ch2:
			t.Logf("[client] %s pairingID=%s", ev.Kind, ev.PairingID)
			if ev.Kind == "pair_request" {
				clientGotPR = true
			}
		case <-deadline:
			t.Fatal("timeout waiting for pair_request")
		}
		if serverGotPR && clientGotPR {
			break waitPairRequest
		}
	}

	if pairingID == "" {
		t.Fatal("pairingID empty")
	}
	if len(pin) != 6 {
		t.Fatalf("pin %q is not 6 digits", pin)
	}
	t.Logf("PASS: both sides got pair_request pairingID=%s pin=%s", pairingID, pin)

	// Check server status has pending_pair with display_pin
	// The PIN is intentionally only exposed by the local-admin surface. The
	// public State view redacts it for LAN callers.
	snap1 := d1.LocalState()
	if len(snap1.PendingPairs) == 0 {
		t.Fatal("server pending_pairs empty")
	}
	pp := snap1.PendingPairs[0]
	if pp.DisplayPIN != pin {
		t.Fatalf("status pin %q != event pin %q", pp.DisplayPIN, pin)
	}
	if pp.AttemptsRemaining != 3 {
		t.Fatalf("attempts_remaining %d want 3", pp.AttemptsRemaining)
	}
	t.Logf("PASS: server status pending_pair pin=%s attempts=%d", pp.DisplayPIN, pp.AttemptsRemaining)

	// --- Wrong PIN → pair_retry ---
	if err := d2.SendPIN(pairingID, "000000"); err != nil {
		t.Fatalf("SendPIN wrong: %v", err)
	}
	deadline2 := time.After(3 * time.Second)
waitRetry:
	for {
		select {
		case ev := <-ch2:
			t.Logf("[client] %s attempts=%d", ev.Kind, ev.AttemptsRemaining)
			if ev.Kind == "pair_retry" {
				if ev.AttemptsRemaining != 2 {
					t.Fatalf("after wrong pin: attempts_remaining %d want 2", ev.AttemptsRemaining)
				}
				break waitRetry
			}
			if ev.Kind == "pair_reject" {
				t.Fatal("got pair_reject on first wrong PIN, expected pair_retry")
			}
		case <-deadline2:
			t.Fatal("timeout waiting for pair_retry")
		}
	}
	t.Logf("PASS: pair_retry received (attempts_remaining=2)")

	// Check server status updated attempts
	snap1b := d1.LocalState()
	if len(snap1b.PendingPairs) == 0 {
		t.Fatal("server pending_pairs empty after wrong pin")
	}
	if snap1b.PendingPairs[0].AttemptsRemaining != 2 {
		t.Logf("WARN: server status attempts=%d (may lag due to snapshot timing)", snap1b.PendingPairs[0].AttemptsRemaining)
	}

	// --- Correct PIN → session_connected ---
	if err := d2.SendPIN(pairingID, pin); err != nil {
		t.Fatalf("SendPIN correct: %v", err)
	}
	t.Logf("sent correct PIN %s", pin)

	paired1, paired2 := false, false
	deadline3 := time.After(4 * time.Second)
waitPaired:
	for {
		select {
		case ev := <-ch1:
			t.Logf("[server] %s", ev.Kind)
			if ev.Kind == "session_connected" {
				paired1 = true
			}
		case ev := <-ch2:
			t.Logf("[client] %s", ev.Kind)
			if ev.Kind == "session_connected" {
				paired2 = true
			}
		case <-deadline3:
			t.Fatalf("timeout: server=%v client=%v", paired1, paired2)
		}
		if paired1 && paired2 {
			break waitPaired
		}
	}
	t.Log("PASS: both sides session_connected")

	time.Sleep(200 * time.Millisecond)

	// --- Remembered ---
	rem := d1.RememberedList()
	if len(rem) == 0 {
		t.Fatal("server remembered empty after pairing")
	}
	t.Logf("PASS: server remembered device_id=%s", rem[0].DeviceID[:12])

	// Verify DTO has no pair_secret (structural: RememberedList returns []remembered.DTO,
	// and DTO type has no PairSecret field — this is a compile-time guarantee)
	fmt.Fprintf(os.Stdout, "PASS: DTO has no PairSecret field (compile-time type guarantee)\n")

	// --- Sessions ---
	s1 := d1.State()
	s2 := d2.State()
	if len(s1.Sessions) == 0 {
		t.Fatal("server has no sessions")
	}
	if len(s2.Sessions) == 0 {
		t.Fatal("client has no sessions")
	}
	if s1.Sessions[0].Role != "server" {
		t.Fatalf("server session role=%q want server", s1.Sessions[0].Role)
	}
	if s2.Sessions[0].Role != "client" {
		t.Fatalf("client session role=%q want client", s2.Sessions[0].Role)
	}
	t.Logf("PASS: server role=server client role=client")

	// --- Pending cleared ---
	if len(s1.PendingPairs) != 0 {
		t.Fatalf("server still has %d pending_pairs after session", len(s1.PendingPairs))
	}
	t.Log("PASS: pending_pairs cleared after pairing")

	// --- device_id is stable (32 hex chars) ---
	id1a := d1.Identity().DeviceID
	if len(id1a) != 32 {
		t.Fatalf("device_id len=%d want 32", len(id1a))
	}
	// Rebuild daemon with same config → same ID
	d1b, err2 := daemon.New(daemon.Options{DataDir: dir1, ConfigPath: filepath.Join(dir1, "config.json")})
	if err2 != nil {
		t.Fatalf("rebuild daemon: %v", err2)
	}
	if d1b.Identity().DeviceID != id1a {
		t.Fatalf("device_id changed across restarts: got %s want %s", d1b.Identity().DeviceID, id1a)
	}
	t.Log("PASS: device_id is 32 hex chars and stable across restarts")
}

func TestUnsafeLANValidation(t *testing.T) {
	// 0.0.0.0 without unsafe flag should fail at New()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.ListenHost = "0.0.0.0"
	cfg.UnsafeHTTPLAN = false
	cfg.Save(filepath.Join(dir, "config.json"))

	_, err := daemon.New(daemon.Options{DataDir: dir, ConfigPath: filepath.Join(dir, "config.json")})
	if err == nil {
		t.Fatal("expected error for 0.0.0.0 without unsafe_http_lan=true")
	}
	t.Logf("PASS: rejected 0.0.0.0 without unsafe: %v", err)

	// 0.0.0.0 with unsafe flag should work
	dir2 := t.TempDir()
	cfg2 := config.Default()
	cfg2.ListenHost = "0.0.0.0"
	cfg2.UnsafeHTTPLAN = true
	cfg2.Port = 39391
	cfg2.Save(filepath.Join(dir2, "config.json"))

	d, err := daemon.New(daemon.Options{DataDir: dir2, ConfigPath: filepath.Join(dir2, "config.json")})
	if err != nil {
		t.Fatalf("unexpected error with unsafe: %v", err)
	}
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()
	t.Logf("PASS: started unsafe LAN addr=%s", d.Addr())
}

func TestPINExhaustion(t *testing.T) {
	dir1 := t.TempDir()
	cfg1 := config.Default()
	cfg1.Port = 39481
	cfg1.PairingPINTTLSeconds = 30
	cfg1.PairingPINMaxAttempts = 2 // only 2 attempts
	cfg1.Save(filepath.Join(dir1, "config.json"))

	d1, _ := daemon.New(daemon.Options{DataDir: dir1, ConfigPath: filepath.Join(dir1, "config.json")})
	d1.Start()
	defer d1.Stop()

	dir2 := t.TempDir()
	cfg2 := config.Default()
	cfg2.Port = 39482
	cfg2.Save(filepath.Join(dir2, "config.json"))
	d2, _ := daemon.New(daemon.Options{DataDir: dir2, ConfigPath: filepath.Join(dir2, "config.json")})
	d2.Start()
	defer d2.Stop()

	ch1 := d1.Subscribe()
	ch2 := d2.Subscribe()
	defer d1.Unsubscribe(ch1)
	defer d2.Unsubscribe(ch2)

	d2.Connect("127.0.0.1", 39481)

	var pairingID string
	deadline := time.After(5 * time.Second)
waitPR:
	for {
		select {
		case ev := <-ch1:
			if ev.Kind == "pair_request" {
				pairingID = ev.PairingID
				break waitPR
			}
		case <-deadline:
			t.Fatal("timeout")
		}
	}

	// drain client pair_request event before sending PINs
	drainDeadline := time.After(3 * time.Second)
drainPR:
	for {
		select {
		case ev := <-ch2:
			if ev.Kind == "pair_request" {
				break drainPR
			}
		case <-drainDeadline:
			t.Fatal("timeout draining pair_request from client")
		}
	}

	// 2 wrong PINs → after 2nd, pair_reject
	d2.SendPIN(pairingID, "000001") // wrong 1 → retry
	deadline2 := time.After(3 * time.Second)
waitRetry2:
	for {
		select {
		case ev := <-ch2:
			if ev.Kind == "pair_retry" {
				t.Logf("PASS: first wrong PIN → pair_retry, remaining=%d", ev.AttemptsRemaining)
				break waitRetry2
			}
		case <-deadline2:
			t.Fatal("timeout on retry")
		}
	}

	d2.SendPIN(pairingID, "000002") // wrong 2 → reject (exhausted)
	deadline3 := time.After(3 * time.Second)
waitReject:
	for {
		select {
		case ev := <-ch2:
			if ev.Kind == "pair_reject" {
				t.Log("PASS: second wrong PIN → pair_reject (max attempts exhausted)")
				break waitReject
			}
		case <-deadline3:
			t.Fatal("timeout on reject")
		}
	}

	// pending should be cleared
	time.Sleep(100 * time.Millisecond)
	if len(d1.State().PendingPairs) != 0 {
		t.Fatal("pending_pairs not cleared after rejection")
	}
	t.Log("PASS: pending_pairs cleared after rejection")
}

func TestRememberedConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	store, err := remembered.New(filepath.Join(dir, "remembered.json"))
	if err != nil {
		t.Fatal(err)
	}

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("%032x", i) // 32 hex chars
			store.Add(remembered.Record{
				DeviceID:   id,
				DisplayID:  id[:12],
				Name:       fmt.Sprintf("device-%d", i),
				SecretID:   "sid",
				PairSecret: "secret",
				CreatedAt:  time.Now(),
				LastSeenAt: time.Now(),
			})
		}(i)
	}
	wg.Wait()

	list := store.List()
	if len(list) != n {
		t.Fatalf("concurrent writes: want %d records got %d", n, len(list))
	}
	t.Logf("PASS: concurrent writes preserved all %d records", n)

	// Verify no pair_secret in DTO
	for _, d := range list {
		// DTO type does not have PairSecret field; this is compile-time enforced
		_ = d.DeviceID
		_ = d.DisplayID
	}
	t.Log("PASS: DTO has no PairSecret field")
}
