package daemon

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/helper"
	"github.com/mousebridge/core/internal/p2p"
	"github.com/mousebridge/core/internal/transport"
)

func startConnectedDaemonPair(t *testing.T) (*Daemon, *Daemon, func()) {
	t.Helper()
	d1, err := New(newTestOptions(t))
	if err != nil {
		t.Fatalf("New d1: %v", err)
	}
	d2, err := New(newTestOptions(t))
	if err != nil {
		t.Fatalf("New d2: %v", err)
	}

	c1, c2 := net.Pipe()
	tc1 := transport.NewConn(c1)
	tc2 := transport.NewConn(c2)

	go p2p.RunSession(tc1, d2.LocalID(), "Peer-2", "client", d1, func(ev p2p.BusEvent) {})
	go p2p.RunSession(tc2, d1.LocalID(), "Peer-1", "server", d2, func(ev p2p.BusEvent) {})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(d1.State().Sessions) == 1 && len(d2.State().Sessions) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(d1.State().Sessions) != 1 || len(d2.State().Sessions) != 1 {
		t.Fatalf("sessions not established: d1=%d d2=%d", len(d1.State().Sessions), len(d2.State().Sessions))
	}

	cleanup := func() {
		_ = tc1.Close()
		_ = tc2.Close()
		d1.Stop()
		d2.Stop()
	}
	return d1, d2, cleanup
}

func TestClipboardSyncBidirectionalAndAntiLoop(t *testing.T) {
	d1, d2, cleanup := startConnectedDaemonPair(t)
	defer cleanup()

	// Direction 1: Client (d1) -> Server (d2)
	text := "Hello from Machine 1 — 跨端剪贴板测试 12345"
	if _, err := d1.SendClipboard(d2.LocalID(), text); err != nil {
		t.Fatalf("d1.SendClipboard: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d2.ClipboardStatusSnapshot().LastText == text {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := d2.ClipboardStatusSnapshot()
	if got.LastText != text {
		t.Fatalf("d2 LastText=%q want %q", got.LastText, text)
	}
	if got.LastSourceID != d1.LocalID() {
		t.Fatalf("d2 LastSourceID=%q want %q", got.LastSourceID, d1.LocalID())
	}

	// Direction 2: Server (d2) -> Client (d1)
	replyText := "Reply from Machine 2 — 被控端反向同步剪贴板 67890"
	if _, err := d2.SendClipboard(d1.LocalID(), replyText); err != nil {
		t.Fatalf("d2.SendClipboard: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d1.ClipboardStatusSnapshot().LastText == replyText {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	gotClient := d1.ClipboardStatusSnapshot()
	if gotClient.LastText != replyText {
		t.Fatalf("d1 LastText=%q want %q", gotClient.LastText, replyText)
	}
	if gotClient.LastSourceID != d2.LocalID() {
		t.Fatalf("d1 LastSourceID=%q want %q", gotClient.LastSourceID, d2.LocalID())
	}

	// Verify disabling clipboard sync blocks incoming updates.
	disabled := false
	d2.UpdateClipboardSettings(&disabled, nil)
	if _, err := d1.SendClipboard(d2.LocalID(), "should-not-arrive"); err != nil {
		t.Fatalf("d1.SendClipboard 2: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if d2.ClipboardStatusSnapshot().LastText != replyText {
		t.Fatalf("d2 updated while disabled: got %q", d2.ClipboardStatusSnapshot().LastText)
	}
}

func TestFileTransferMultiChunkEndToEnd(t *testing.T) {
	d1, d2, cleanup := startConnectedDaemonPair(t)
	defer cleanup()

	recvDir2 := filepath.Join(t.TempDir(), "downloads-d2")
	if _, err := d2.SetFileDownloadDir(recvDir2); err != nil {
		t.Fatalf("d2.SetFileDownloadDir: %v", err)
	}
	recvDir1 := filepath.Join(t.TempDir(), "downloads-d1")
	if _, err := d1.SetFileDownloadDir(recvDir1); err != nil {
		t.Fatalf("d1.SetFileDownloadDir: %v", err)
	}

	// Direction 1: Client (d1) -> Server (d2) 180 KB payload (spans 3 chunks of 64 KB)
	payload := bytes.Repeat([]byte("MouseBridge-CrossMachine-FileTransfer-Chunk-Test-2026!"), 3500)
	srcPath := filepath.Join(t.TempDir(), "project-spec.bin")
	if err := os.WriteFile(srcPath, payload, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	sentStatus, err := d1.SendFileFromPath(d2.LocalID(), srcPath)
	if err != nil {
		t.Fatalf("SendFileFromPath: %v", err)
	}
	if sentStatus.Status != "completed" || sentStatus.Progress != 100 {
		t.Fatalf("unexpected sentStatus: %+v", sentStatus)
	}

	expectedDest := filepath.Join(recvDir2, "project-spec.bin")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(expectedDest); err == nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	receivedBytes, err := os.ReadFile(expectedDest)
	if err != nil {
		t.Fatalf("ReadFile received file: %v", err)
	}
	if !bytes.Equal(receivedBytes, payload) {
		t.Fatalf("received file content mismatch: got %d bytes, want %d bytes", len(receivedBytes), len(payload))
	}

	transfers := d2.FileTransfersSnapshot()
	if len(transfers) != 1 || transfers[0].Status != "completed" || transfers[0].SavedPath != expectedDest {
		t.Fatalf("unexpected receiver transfers snapshot: %+v", transfers)
	}

	// Direction 2: Server (d2) -> Client (d1) 150 KB payload
	reversePayload := bytes.Repeat([]byte("ServerToClient-Bidirectional-FileTransfer-2026#"), 3200)
	reverseSrcPath := filepath.Join(t.TempDir(), "server-report.bin")
	if err := os.WriteFile(reverseSrcPath, reversePayload, 0o600); err != nil {
		t.Fatalf("WriteFile reverse: %v", err)
	}
	revStatus, err := d2.SendFileFromPath(d1.LocalID(), reverseSrcPath)
	if err != nil {
		t.Fatalf("d2.SendFileFromPath: %v", err)
	}
	if revStatus.Status != "completed" || revStatus.Progress != 100 {
		t.Fatalf("unexpected revStatus: %+v", revStatus)
	}
	expectedRevDest := filepath.Join(recvDir1, "server-report.bin")
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(expectedRevDest); err == nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	revReceivedBytes, err := os.ReadFile(expectedRevDest)
	if err != nil {
		t.Fatalf("ReadFile reverse received file: %v", err)
	}
	if !bytes.Equal(revReceivedBytes, reversePayload) {
		t.Fatalf("reverse received file content mismatch: got %d bytes, want %d bytes", len(revReceivedBytes), len(reversePayload))
	}

	// Send a second file with the same name and verify it does not overwrite the first file.
	secondPayload := []byte("second-version-payload-without-overwrite")
	if _, err := d1.SendFileFromBytes(d2.LocalID(), "project-spec.bin", secondPayload); err != nil {
		t.Fatalf("SendFileFromBytes duplicate name: %v", err)
	}
	expectedDupDest := filepath.Join(recvDir2, "project-spec (1).bin")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(expectedDupDest); err == nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	dupBytes, err := os.ReadFile(expectedDupDest)
	if err != nil {
		t.Fatalf("ReadFile duplicate file: %v", err)
	}
	if !bytes.Equal(dupBytes, secondPayload) {
		t.Fatalf("duplicate file content mismatch: got %q want %q", string(dupBytes), string(secondPayload))
	}
}

func TestRemoteModifierReleaseOnTargetSwitch(t *testing.T) {
	d1, d2, cleanup := startConnectedDaemonPair(t)
	defer cleanup()

	// Switch active control from d1 to d2
	d1.SwitchNext()
	if d1.ctrl.ActiveTarget() != d2.LocalID() {
		t.Fatalf("expected active target %q, got %q", d2.LocalID(), d1.ctrl.ActiveTarget())
	}

	// Simulate Ctrl (59) and Option (58) key_down sent while controlling d2
	if err := d1.SendSessionInput(d2.LocalID(), helper.InputPayload{
		Kind:      "key_down",
		KeyCode:   59,
		Modifiers: 262144,
	}); err != nil {
		t.Fatalf("SendSessionInput Ctrl key_down: %v", err)
	}
	if err := d1.SendSessionInput(d2.LocalID(), helper.InputPayload{
		Kind:      "key_down",
		KeyCode:   58,
		Modifiers: 786432,
	}); err != nil {
		t.Fatalf("SendSessionInput Option key_down: %v", err)
	}

	d1.heldKeysMu.Lock()
	heldBefore := len(d1.heldRemoteKeys[d2.LocalID()])
	d1.heldKeysMu.Unlock()
	if heldBefore != 2 {
		t.Fatalf("expected 2 held remote keys before switch, got %d", heldBefore)
	}

	// Switch back to host (as happens on Ctrl+Alt+Home)
	d1.SwitchToHost()

	d1.heldKeysMu.Lock()
	heldAfter := len(d1.heldRemoteKeys[d2.LocalID()])
	d1.heldKeysMu.Unlock()
	if heldAfter != 0 {
		t.Fatalf("expected 0 held remote keys after SwitchToHost, got %d", heldAfter)
	}
}

func TestPointerSpeedConfiguration(t *testing.T) {
	opts := newTestOptions(t)
	d, err := New(opts)
	if err != nil {
		t.Fatalf("New daemon: %v", err)
	}

	// Default speeds: mouse=1.5, scroll=2.0
	m0, s0 := d.PointerSpeeds()
	if m0 != 1.5 || s0 != 2.0 {
		t.Fatalf("expected default speeds (1.5, 2.0), got (%v, %v)", m0, s0)
	}

	// Update speeds and verify persistence
	newMouse := 2.4
	newScroll := 4.5
	m1, s1, err := d.UpdatePointerSpeeds(&newMouse, &newScroll)
	if err != nil {
		t.Fatalf("UpdatePointerSpeeds: %v", err)
	}
	if m1 != 2.4 || s1 != 4.5 {
		t.Fatalf("expected updated speeds (2.4, 4.5), got (%v, %v)", m1, s1)
	}
	snap := d.State()
	if snap.MouseSpeed != 2.4 || snap.ScrollSpeed != 4.5 {
		t.Fatalf("expected State snapshot speeds (2.4, 4.5), got (%v, %v)", snap.MouseSpeed, snap.ScrollSpeed)
	}

	// Verify persisted config on disk
	reloaded, err := config.Load(opts.ConfigPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.MouseSpeed != 2.4 || reloaded.ScrollSpeed != 4.5 {
		t.Fatalf("expected persisted speeds (2.4, 4.5), got (%v, %v)", reloaded.MouseSpeed, reloaded.ScrollSpeed)
	}

	// Verify out-of-range rejection
	badMouse := 9.9
	if _, _, err := d.UpdatePointerSpeeds(&badMouse, nil); err == nil {
		t.Fatalf("expected error for out-of-range mouse_speed")
	}
	badScroll := 0.05
	if _, _, err := d.UpdatePointerSpeeds(nil, &badScroll); err == nil {
		t.Fatalf("expected error for out-of-range scroll_speed")
	}
}
