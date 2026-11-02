package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/transport"
)

// ClipboardStatus represents the current state of cross-machine clipboard synchronization.
type ClipboardStatus struct {
	Enabled         bool   `json:"enabled"`
	AllowServerSync bool   `json:"allow_server_sync"`
	LastText        string `json:"last_text,omitempty"`
	LastSHA256      string `json:"last_sha256,omitempty"`
	LastSourceID    string `json:"last_source_id,omitempty"`
	UpdatedAt       int64  `json:"updated_at,omitempty"`
}

// FileTransferStatus represents the state of an outgoing or incoming file transfer.
type FileTransferStatus struct {
	TransferID       string `json:"transfer_id"`
	DeviceID         string `json:"device_id"`
	Direction        string `json:"direction"` // "send" | "receive"
	FileName         string `json:"file_name"`
	TotalBytes       int64  `json:"total_bytes"`
	TransferredBytes int64  `json:"transferred_bytes"`
	Progress         int    `json:"progress"`
	Status           string `json:"status"` // "transferring" | "completed" | "failed" | "canceled"
	SavedPath        string `json:"saved_path,omitempty"`
	SHA256           string `json:"sha256,omitempty"`
	Error            string `json:"error,omitempty"`
	UpdatedAt        int64  `json:"updated_at"`
}

type activeFileReceive struct {
	transferID string
	deviceID   string
	fileName   string
	partPath   string
	finalPath  string
	totalBytes int64
	written    int64
	expected   string
	file       *os.File
	hasher     hash.Hash
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newTransferID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ClipboardStatusSnapshot returns a copy of the current clipboard status.
func (d *Daemon) ClipboardStatusSnapshot() ClipboardStatus {
	d.clipMu.RLock()
	defer d.clipMu.RUnlock()
	return d.clipStatus
}

// UpdateClipboardSettings updates the clipboard sync configuration.
func (d *Daemon) UpdateClipboardSettings(enabled *bool, allowServerSync *bool) ClipboardStatus {
	d.clipMu.Lock()
	if enabled != nil {
		d.clipStatus.Enabled = *enabled
	}
	if allowServerSync != nil {
		d.clipStatus.AllowServerSync = *allowServerSync
	}
	snap := d.clipStatus
	d.clipMu.Unlock()
	d.broadcast(BusEvent{Kind: "log", Msg: fmt.Sprintf("clipboard settings updated enabled=%t allow_server_sync=%t", snap.Enabled, snap.AllowServerSync)})
	return snap
}

// SendClipboard sends clipboard text to a specific connected peer (or all peers if targetDeviceID is empty).
func (d *Daemon) SendClipboard(targetDeviceID string, text string) (ClipboardStatus, error) {
	if text == "" && runtime.GOOS == "darwin" {
		if out, err := exec.Command("pbpaste").Output(); err == nil {
			text = string(out)
		}
	}
	if text == "" {
		return d.ClipboardStatusSnapshot(), fmt.Errorf("clipboard text is empty")
	}

	d.clipMu.Lock()
	if !d.clipStatus.Enabled {
		snap := d.clipStatus
		d.clipMu.Unlock()
		return snap, fmt.Errorf("clipboard sync is disabled")
	}
	sha := sha256Hex([]byte(text))
	now := time.Now().UnixMilli()
	d.clipStatus.LastText = text
	d.clipStatus.LastSHA256 = sha
	d.clipStatus.LastSourceID = d.identity.DeviceID
	d.clipStatus.UpdatedAt = now
	snap := d.clipStatus
	d.clipMu.Unlock()

	conns := d.targetConns(targetDeviceID)
	if len(conns) == 0 {
		return snap, fmt.Errorf("no active session for clipboard sync")
	}

	msg := event.Message{
		V:    1,
		Seq:  now,
		Type: event.TypeClipboardSync,
		Ts:   now,
		Payload: event.ClipboardSyncPayload{
			Text:       text,
			ContentSHA: sha,
			SourceID:   d.identity.DeviceID,
		},
	}

	var firstErr error
	sentCount := 0
	for _, c := range conns {
		if err := c.Send(msg); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			sentCount++
		}
	}
	if sentCount == 0 && firstErr != nil {
		return snap, firstErr
	}

	d.broadcast(BusEvent{
		Kind:     "clipboard_synced",
		DeviceID: targetDeviceID,
		Msg:      fmt.Sprintf("clipboard sent (%d bytes)", len(text)),
	})
	return snap, nil
}

// HandleRemoteClipboard processes incoming clipboard_sync messages from a connected peer.
func (d *Daemon) HandleRemoteClipboard(deviceID string, pay event.ClipboardSyncPayload) {
	if pay.Text == "" {
		return
	}
	sha := pay.ContentSHA
	if sha == "" {
		sha = sha256Hex([]byte(pay.Text))
	}

	d.clipMu.Lock()
	if !d.clipStatus.Enabled || !d.clipStatus.AllowServerSync {
		d.clipMu.Unlock()
		return
	}
	// Anti-loop guard: ignore if content hash matches the last synced clipboard.
	if d.clipStatus.LastSHA256 != "" && d.clipStatus.LastSHA256 == sha {
		d.clipMu.Unlock()
		return
	}
	d.clipStatus.LastText = pay.Text
	d.clipStatus.LastSHA256 = sha
	d.clipStatus.LastSourceID = deviceID
	d.clipStatus.UpdatedAt = time.Now().UnixMilli()
	d.clipMu.Unlock()

	if os.Getenv("MB_SYNC_SYSTEM_CLIPBOARD") == "1" && runtime.GOOS == "darwin" {
		cmd := exec.Command("pbcopy")
		cmd.Stdin = strings.NewReader(pay.Text)
		_ = cmd.Run()
	}

	d.broadcast(BusEvent{
		Kind:     "clipboard_received",
		DeviceID: deviceID,
		Msg:      pay.Text,
	})
}

func (d *Daemon) runClipboardWatcher(ctx context.Context) {
	if os.Getenv("MB_SYNC_SYSTEM_CLIPBOARD") != "1" || runtime.GOOS != "darwin" {
		return
	}
	var lastObservedSHA string
	if out, err := exec.Command("pbpaste").Output(); err == nil && len(out) > 0 {
		lastObservedSHA = sha256Hex(out)
	}
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap := d.ClipboardStatusSnapshot()
			if !snap.Enabled || len(d.targetConns("")) == 0 {
				continue
			}
			out, err := exec.Command("pbpaste").Output()
			if err != nil || len(out) == 0 {
				continue
			}
			sha := sha256Hex(out)
			if sha == lastObservedSHA || sha == snap.LastSHA256 {
				lastObservedSHA = sha
				continue
			}
			lastObservedSHA = sha
			_, _ = d.SendClipboard("", string(out))
		}
	}
}

func expandHomePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// FileDownloadDir returns the current directory where received files are saved.
func (d *Daemon) FileDownloadDir() string {
	d.fileMu.Lock()
	dir := d.fileDownloadDir
	d.fileMu.Unlock()
	if dir == "" {
		dir = filepath.Join(d.dataDir, "received")
	}
	return expandHomePath(dir)
}

// SetFileDownloadDir updates and ensures the directory where received files are saved.
func (d *Daemon) SetFileDownloadDir(dir string) (string, error) {
	resolved := expandHomePath(dir)
	if resolved == "" {
		return "", fmt.Errorf("download_dir cannot be empty")
	}
	if err := os.MkdirAll(resolved, 0o755); err != nil {
		return "", fmt.Errorf("create download_dir: %w", err)
	}
	d.fileMu.Lock()
	d.fileDownloadDir = resolved
	d.fileMu.Unlock()
	return resolved, nil
}

// FileTransfersSnapshot returns the recent file transfers sorted by most recent first.
func (d *Daemon) FileTransfersSnapshot() []FileTransferStatus {
	d.fileMu.Lock()
	out := make([]FileTransferStatus, 0, len(d.fileTransfers))
	for _, st := range d.fileTransfers {
		out = append(out, *st)
	}
	d.fileMu.Unlock()
	slices.SortFunc(out, func(a, b FileTransferStatus) int {
		if a.UpdatedAt > b.UpdatedAt {
			return -1
		}
		if a.UpdatedAt < b.UpdatedAt {
			return 1
		}
		return strings.Compare(a.TransferID, b.TransferID)
	})
	if len(out) > 25 {
		out = out[:25]
	}
	return out
}

func (d *Daemon) targetConns(targetDeviceID string) []*transport.Conn {
	d.connsMu.Lock()
	defer d.connsMu.Unlock()
	var list []*transport.Conn
	for c, id := range d.conns {
		if id == "" {
			continue
		}
		if targetDeviceID != "" && id != targetDeviceID {
			continue
		}
		list = append(list, c)
	}
	return list
}

func (d *Daemon) firstTargetConn(targetDeviceID string) (*transport.Conn, string, error) {
	d.connsMu.Lock()
	defer d.connsMu.Unlock()
	for c, id := range d.conns {
		if id == "" {
			continue
		}
		if targetDeviceID != "" && id != targetDeviceID {
			continue
		}
		return c, id, nil
	}
	return nil, "", fmt.Errorf("no active session for device %q", targetDeviceID)
}

const fileChunkSize = 64 * 1024

// SendFileFromBytes streams an in-memory file payload in chunks over an active P2P connection.
func (d *Daemon) SendFileFromBytes(targetDeviceID, fileName string, data []byte) (FileTransferStatus, error) {
	cleanName := filepath.Base(strings.TrimSpace(fileName))
	if cleanName == "" || cleanName == "." || cleanName == "/" {
		return FileTransferStatus{}, fmt.Errorf("invalid file_name")
	}

	conn, peerID, err := d.firstTargetConn(targetDeviceID)
	if err != nil {
		return FileTransferStatus{}, err
	}

	transferID := newTransferID()
	totalBytes := int64(len(data))
	digest := sha256Hex(data)
	now := time.Now().UnixMilli()

	status := &FileTransferStatus{
		TransferID:       transferID,
		DeviceID:         peerID,
		Direction:        "send",
		FileName:         cleanName,
		TotalBytes:       totalBytes,
		TransferredBytes: 0,
		Progress:         0,
		Status:           "transferring",
		SHA256:           digest,
		UpdatedAt:        now,
	}

	d.fileMu.Lock()
	d.fileTransfers[transferID] = status
	d.fileMu.Unlock()

	if err := conn.Send(event.Message{
		V:    1,
		Seq:  now,
		Type: event.TypeFileStart,
		Ts:   now,
		Payload: event.FileStartPayload{
			TransferID: transferID,
			FileName:   cleanName,
			TotalBytes: totalBytes,
			SHA256:     digest,
		},
	}); err != nil {
		d.markTransferFailed(transferID, err)
		return d.getTransferStatus(transferID), err
	}

	for offset := int64(0); offset < totalBytes; offset += fileChunkSize {
		end := offset + fileChunkSize
		if end > totalBytes {
			end = totalBytes
		}
		encoded := base64.StdEncoding.EncodeToString(data[offset:end])
		if err := conn.Send(event.Message{
			V:    1,
			Seq:  time.Now().UnixMilli(),
			Type: event.TypeFileChunk,
			Ts:   time.Now().UnixMilli(),
			Payload: event.FileChunkPayload{
				TransferID: transferID,
				Offset:     offset,
				DataBase64: encoded,
			},
		}); err != nil {
			d.markTransferFailed(transferID, err)
			return d.getTransferStatus(transferID), err
		}

		d.fileMu.Lock()
		if st, ok := d.fileTransfers[transferID]; ok {
			st.TransferredBytes = end
			if totalBytes > 0 {
				st.Progress = int((end * 100) / totalBytes)
			} else {
				st.Progress = 100
			}
			st.UpdatedAt = time.Now().UnixMilli()
		}
		d.fileMu.Unlock()
	}

	if err := conn.Send(event.Message{
		V:    1,
		Seq:  time.Now().UnixMilli(),
		Type: event.TypeFileEnd,
		Ts:   time.Now().UnixMilli(),
		Payload: event.FileEndPayload{
			TransferID: transferID,
			SHA256:     digest,
		},
	}); err != nil {
		d.markTransferFailed(transferID, err)
		return d.getTransferStatus(transferID), err
	}

	d.fileMu.Lock()
	if st, ok := d.fileTransfers[transferID]; ok {
		st.TransferredBytes = totalBytes
		st.Progress = 100
		st.Status = "completed"
		st.UpdatedAt = time.Now().UnixMilli()
	}
	d.fileMu.Unlock()

	d.broadcast(BusEvent{
		Kind:     "file_sent",
		DeviceID: peerID,
		Msg:      fmt.Sprintf("file sent: %s (%d bytes)", cleanName, totalBytes),
	})

	return d.getTransferStatus(transferID), nil
}

// SendFileFromPath reads a local file from disk and streams it in 64 KB chunks over an active P2P connection.
func (d *Daemon) SendFileFromPath(targetDeviceID, filePath string) (FileTransferStatus, error) {
	resolved := expandHomePath(filePath)
	f, err := os.Open(resolved)
	if err != nil {
		return FileTransferStatus{}, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return FileTransferStatus{}, fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return FileTransferStatus{}, fmt.Errorf("cannot send a directory directly")
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return FileTransferStatus{}, fmt.Errorf("hash file: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return FileTransferStatus{}, fmt.Errorf("seek file: %w", err)
	}

	cleanName := filepath.Base(resolved)
	totalBytes := info.Size()
	digest := hex.EncodeToString(hasher.Sum(nil))

	conn, peerID, err := d.firstTargetConn(targetDeviceID)
	if err != nil {
		return FileTransferStatus{}, err
	}

	transferID := newTransferID()
	now := time.Now().UnixMilli()
	status := &FileTransferStatus{
		TransferID:       transferID,
		DeviceID:         peerID,
		Direction:        "send",
		FileName:         cleanName,
		TotalBytes:       totalBytes,
		TransferredBytes: 0,
		Progress:         0,
		Status:           "transferring",
		SHA256:           digest,
		UpdatedAt:        now,
	}

	d.fileMu.Lock()
	d.fileTransfers[transferID] = status
	d.fileMu.Unlock()

	if err := conn.Send(event.Message{
		V:    1,
		Seq:  now,
		Type: event.TypeFileStart,
		Ts:   now,
		Payload: event.FileStartPayload{
			TransferID: transferID,
			FileName:   cleanName,
			TotalBytes: totalBytes,
			SHA256:     digest,
		},
	}); err != nil {
		d.markTransferFailed(transferID, err)
		return d.getTransferStatus(transferID), err
	}

	buf := make([]byte, fileChunkSize)
	var offset int64
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			encoded := base64.StdEncoding.EncodeToString(buf[:n])
			if err := conn.Send(event.Message{
				V:    1,
				Seq:  time.Now().UnixMilli(),
				Type: event.TypeFileChunk,
				Ts:   time.Now().UnixMilli(),
				Payload: event.FileChunkPayload{
					TransferID: transferID,
					Offset:     offset,
					DataBase64: encoded,
				},
			}); err != nil {
				d.markTransferFailed(transferID, err)
				return d.getTransferStatus(transferID), err
			}
			offset += int64(n)
			d.fileMu.Lock()
			if st, ok := d.fileTransfers[transferID]; ok {
				st.TransferredBytes = offset
				if totalBytes > 0 {
					st.Progress = int((offset * 100) / totalBytes)
				} else {
					st.Progress = 100
				}
				st.UpdatedAt = time.Now().UnixMilli()
			}
			d.fileMu.Unlock()
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			d.markTransferFailed(transferID, readErr)
			return d.getTransferStatus(transferID), readErr
		}
	}

	if err := conn.Send(event.Message{
		V:    1,
		Seq:  time.Now().UnixMilli(),
		Type: event.TypeFileEnd,
		Ts:   time.Now().UnixMilli(),
		Payload: event.FileEndPayload{
			TransferID: transferID,
			SHA256:     digest,
		},
	}); err != nil {
		d.markTransferFailed(transferID, err)
		return d.getTransferStatus(transferID), err
	}

	d.fileMu.Lock()
	if st, ok := d.fileTransfers[transferID]; ok {
		st.TransferredBytes = totalBytes
		st.Progress = 100
		st.Status = "completed"
		st.UpdatedAt = time.Now().UnixMilli()
	}
	d.fileMu.Unlock()

	d.broadcast(BusEvent{
		Kind:     "file_sent",
		DeviceID: peerID,
		Msg:      fmt.Sprintf("file sent: %s (%d bytes)", cleanName, totalBytes),
	})

	return d.getTransferStatus(transferID), nil
}

func uniqueDestinationPath(dir, cleanName string) string {
	candidate := filepath.Join(dir, cleanName)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	ext := filepath.Ext(cleanName)
	stem := strings.TrimSuffix(cleanName, ext)
	if stem == "" {
		stem = cleanName
		ext = ""
	}
	for i := 1; i < 1000; i++ {
		alt := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := os.Stat(alt); os.IsNotExist(err) {
			return alt
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, time.Now().UnixMilli(), ext))
}

func (d *Daemon) markTransferFailed(transferID string, err error) {
	d.fileMu.Lock()
	if st, ok := d.fileTransfers[transferID]; ok {
		st.Status = "failed"
		st.Error = err.Error()
		st.UpdatedAt = time.Now().UnixMilli()
	}
	d.fileMu.Unlock()
}

func (d *Daemon) getTransferStatus(transferID string) FileTransferStatus {
	d.fileMu.Lock()
	defer d.fileMu.Unlock()
	if st, ok := d.fileTransfers[transferID]; ok {
		return *st
	}
	return FileTransferStatus{}
}

// HandleFileStart prepares a temporary file in the receive directory for an incoming P2P transfer.
func (d *Daemon) HandleFileStart(deviceID string, pay event.FileStartPayload) {
	if pay.TransferID == "" {
		return
	}
	cleanName := filepath.Base(strings.TrimSpace(pay.FileName))
	if cleanName == "" || cleanName == "." || cleanName == "/" {
		cleanName = "received-file.bin"
	}

	downloadDir := d.FileDownloadDir()
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		d.broadcast(BusEvent{Kind: "error", DeviceID: deviceID, Msg: "file receive mkdir: " + err.Error()})
		return
	}

	partPath := filepath.Join(downloadDir, fmt.Sprintf(".%s.%s.part", cleanName, pay.TransferID))
	finalPath := uniqueDestinationPath(downloadDir, cleanName)
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		d.broadcast(BusEvent{Kind: "error", DeviceID: deviceID, Msg: "file receive open: " + err.Error()})
		return
	}

	now := time.Now().UnixMilli()
	d.fileMu.Lock()
	d.activeReceives[pay.TransferID] = &activeFileReceive{
		transferID: pay.TransferID,
		deviceID:   deviceID,
		fileName:   cleanName,
		partPath:   partPath,
		finalPath:  finalPath,
		totalBytes: pay.TotalBytes,
		written:    0,
		expected:   pay.SHA256,
		file:       f,
		hasher:     sha256.New(),
	}
	d.fileTransfers[pay.TransferID] = &FileTransferStatus{
		TransferID:       pay.TransferID,
		DeviceID:         deviceID,
		Direction:        "receive",
		FileName:         cleanName,
		TotalBytes:       pay.TotalBytes,
		TransferredBytes: 0,
		Progress:         0,
		Status:           "transferring",
		SHA256:           pay.SHA256,
		UpdatedAt:        now,
	}
	d.fileMu.Unlock()

	d.broadcast(BusEvent{
		Kind:     "file_receiving",
		DeviceID: deviceID,
		Msg:      fmt.Sprintf("receiving file %s (%d bytes)", cleanName, pay.TotalBytes),
	})
}

// HandleFileChunk appends one incoming file chunk to the temporary file and updates progress.
func (d *Daemon) HandleFileChunk(deviceID string, pay event.FileChunkPayload) {
	raw, err := base64.StdEncoding.DecodeString(pay.DataBase64)
	if err != nil {
		d.failActiveReceive(pay.TransferID, fmt.Errorf("invalid base64 chunk: %w", err))
		return
	}

	d.fileMu.Lock()
	rec, ok := d.activeReceives[pay.TransferID]
	if !ok {
		d.fileMu.Unlock()
		return
	}
	if _, err := rec.file.Write(raw); err != nil {
		d.fileMu.Unlock()
		d.failActiveReceive(pay.TransferID, fmt.Errorf("write chunk: %w", err))
		return
	}
	_, _ = rec.hasher.Write(raw)
	rec.written += int64(len(raw))
	if st, ok := d.fileTransfers[pay.TransferID]; ok {
		st.TransferredBytes = rec.written
		if rec.totalBytes > 0 {
			st.Progress = int((rec.written * 100) / rec.totalBytes)
		} else {
			st.Progress = 100
		}
		st.UpdatedAt = time.Now().UnixMilli()
	}
	d.fileMu.Unlock()
}

// HandleFileEnd finalizes the incoming file, verifies its SHA-256 digest, and moves it to the target path.
func (d *Daemon) HandleFileEnd(deviceID string, pay event.FileEndPayload) {
	d.fileMu.Lock()
	rec, ok := d.activeReceives[pay.TransferID]
	if ok {
		delete(d.activeReceives, pay.TransferID)
	}
	d.fileMu.Unlock()
	if !ok {
		return
	}

	_ = rec.file.Close()

	if pay.Canceled {
		_ = os.Remove(rec.partPath)
		d.fileMu.Lock()
		if st, ok := d.fileTransfers[pay.TransferID]; ok {
			st.Status = "canceled"
			st.UpdatedAt = time.Now().UnixMilli()
		}
		d.fileMu.Unlock()
		return
	}

	gotSHA := hex.EncodeToString(rec.hasher.Sum(nil))
	expectedSHA := pay.SHA256
	if expectedSHA == "" {
		expectedSHA = rec.expected
	}
	if expectedSHA != "" && !strings.EqualFold(gotSHA, expectedSHA) {
		_ = os.Remove(rec.partPath)
		d.markTransferFailed(pay.TransferID, fmt.Errorf("sha256 mismatch: got %s want %s", gotSHA, expectedSHA))
		d.broadcast(BusEvent{Kind: "error", DeviceID: deviceID, Msg: "file receive sha256 mismatch for " + rec.fileName})
		return
	}

	if err := os.Rename(rec.partPath, rec.finalPath); err != nil {
		_ = os.Remove(rec.partPath)
		d.markTransferFailed(pay.TransferID, fmt.Errorf("rename received file: %w", err))
		return
	}

	d.fileMu.Lock()
	if st, ok := d.fileTransfers[pay.TransferID]; ok {
		st.TransferredBytes = rec.written
		st.Progress = 100
		st.Status = "completed"
		st.SavedPath = rec.finalPath
		st.SHA256 = gotSHA
		st.UpdatedAt = time.Now().UnixMilli()
	}
	d.fileMu.Unlock()

	d.broadcast(BusEvent{
		Kind:     "file_received",
		DeviceID: deviceID,
		Msg:      rec.finalPath,
	})
}

func (d *Daemon) failActiveReceive(transferID string, err error) {
	d.fileMu.Lock()
	rec, ok := d.activeReceives[transferID]
	if ok {
		delete(d.activeReceives, transferID)
	}
	d.fileMu.Unlock()
	if ok && rec.file != nil {
		_ = rec.file.Close()
		_ = os.Remove(rec.partPath)
	}
	d.markTransferFailed(transferID, err)
}
