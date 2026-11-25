package daemon

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	"github.com/mousebridge/core/internal/helper"
	"github.com/mousebridge/core/internal/transport"
	log "github.com/sirupsen/logrus"
)

// ClipboardStatus represents the current state of cross-machine clipboard synchronization.
type ClipboardStatus struct {
	Enabled             bool      `json:"enabled"`
	AllowServerSync     bool      `json:"allow_server_sync"`
	LastText            string    `json:"last_text,omitempty"`
	LastSHA256          string    `json:"last_sha256,omitempty"`
	LastFormats         []string  `json:"last_formats,omitempty"`
	LastSourceID        string    `json:"last_source_id,omitempty"`
	UpdatedAt           int64     `json:"updated_at,omitempty"`
	lastSystemWriteSHA  string    `json:"-"`
	lastRemoteReceiveAt time.Time `json:"-"`
}

func darwinUTF8Env() []string {
	base := os.Environ()
	filtered := make([]string, 0, len(base)+3)
	for _, kv := range base {
		if strings.HasPrefix(kv, "LANG=") ||
			strings.HasPrefix(kv, "LC_ALL=") ||
			strings.HasPrefix(kv, "LC_CTYPE=") ||
			strings.HasPrefix(kv, "__CF_USER_TEXT_ENCODING=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	return append(filtered,
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"LC_CTYPE=UTF-8",
	)
}

func readSystemClipboard() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("system clipboard unsupported on %s", runtime.GOOS)
	}
	cmd := exec.Command("/usr/bin/pbpaste", "-Prefer", "txt")
	cmd.Env = darwinUTF8Env()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.ToValidUTF8(string(out), ""), nil
}

func writeSystemClipboard(text string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	clean := strings.ToValidUTF8(text, "")
	cmd := exec.Command("/usr/bin/pbcopy")
	cmd.Env = darwinUTF8Env()
	cmd.Stdin = strings.NewReader(clean)
	return cmd.Run()
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
	d.cfg.ClipboardSyncEnabled = snap.Enabled
	d.cfg.ClipboardAllowServerSync = snap.AllowServerSync
	if d.configPath != "" {
		if err := d.cfg.Save(d.configPath); err != nil {
			log.Warnf("[clipboard] save settings failed: %v", err)
		}
	}
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
	d.broadcast(BusEvent{Kind: "log", Msg: fmt.Sprintf("clipboard settings updated enabled=%t allow_server_sync=%t", snap.Enabled, snap.AllowServerSync)})
	return snap
}

// SendClipboard sends clipboard text to a specific connected peer (or all peers if targetDeviceID is empty).
func (d *Daemon) SendClipboard(targetDeviceID string, text string) (ClipboardStatus, error) {
	if text == "" && runtime.GOOS == "darwin" {
		if out, err := readSystemClipboard(); err == nil {
			text = out
		}
	}
	text = strings.ToValidUTF8(text, "")
	if text == "" {
		return d.ClipboardStatusSnapshot(), fmt.Errorf("clipboard text is empty")
	}
	return d.SendClipboardEntry(targetDeviceID, event.ClipboardSyncPayload{Text: text})
}

// SendClipboardEntry sends the native representations of one clipboard item.
func (d *Daemon) SendClipboardEntry(targetDeviceID string, payload event.ClipboardSyncPayload) (ClipboardStatus, error) {
	if payload.Text == "" && payload.HTML == "" && payload.ImageBase64 == "" && len(payload.Files) == 0 {
		return d.ClipboardStatusSnapshot(), fmt.Errorf("clipboard entry has no supported formats")
	}
	if len(payload.Text)+len(payload.HTML)+len(payload.ImageBase64) > 14*1024*1024 {
		return d.ClipboardStatusSnapshot(), fmt.Errorf("clipboard entry exceeds 14 MB")
	}
	if len(payload.Files) > 32 {
		return d.ClipboardStatusSnapshot(), fmt.Errorf("clipboard entry contains more than 32 files")
	}
	if payload.ImageBase64 != "" {
		if payload.ImageMIMEType != "image/png" && payload.ImageMIMEType != "image/tiff" {
			return d.ClipboardStatusSnapshot(), fmt.Errorf("clipboard image must be PNG or TIFF")
		}
		if _, err := base64.StdEncoding.DecodeString(payload.ImageBase64); err != nil {
			return d.ClipboardStatusSnapshot(), fmt.Errorf("invalid clipboard image encoding")
		}
	}
	payload.Text = strings.ToValidUTF8(payload.Text, "")
	payload.HTML = strings.ToValidUTF8(payload.HTML, "")
	payload.ContentSHA = clipboardEntrySHA(payload)
	payload.SourceID = d.identity.DeviceID

	d.clipMu.Lock()
	if !d.clipStatus.Enabled {
		snap := d.clipStatus
		d.clipMu.Unlock()
		return snap, fmt.Errorf("clipboard sync is disabled")
	}
	sha := payload.ContentSHA
	now := time.Now().UnixMilli()
	d.clipStatus.LastText = payload.Text
	d.clipStatus.LastSHA256 = sha
	d.clipStatus.LastFormats = clipboardFormats(payload)
	d.clipStatus.LastSourceID = d.identity.DeviceID
	d.clipStatus.lastSystemWriteSHA = ""
	d.clipStatus.UpdatedAt = now
	snap := d.clipStatus
	d.clipMu.Unlock()

	conns := d.targetConns(targetDeviceID)
	if len(conns) == 0 {
		return snap, fmt.Errorf("no active session for clipboard sync")
	}

	msg := event.Message{
		V:       1,
		Seq:     now,
		Type:    event.TypeClipboardSync,
		Ts:      now,
		Payload: payload,
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
		Msg:      fmt.Sprintf("clipboard sent (%d bytes, %s)", len(payload.Text)+len(payload.HTML)+len(payload.ImageBase64), strings.Join(clipboardFormats(payload), ",")),
	})
	return snap, nil
}

func clipboardEntrySHA(payload event.ClipboardSyncPayload) string {
	payload.ContentSHA = ""
	payload.SourceID = ""
	payload.PasteAfterWrite = false
	for i := range payload.Files {
		payload.Files[i].TransferID = ""
	}
	data, _ := json.Marshal(payload)
	return sha256Hex(data)
}

func (d *Daemon) sessionDeviceName(deviceID string) string {
	d.sessMu.RLock()
	defer d.sessMu.RUnlock()
	for _, session := range d.sessions {
		if session.DeviceID == deviceID {
			return session.Name
		}
	}
	return deviceID
}

func clipboardFormats(payload event.ClipboardSyncPayload) []string {
	formats := make([]string, 0, 3)
	if payload.Text != "" {
		formats = append(formats, "text/plain")
	}
	if payload.HTML != "" {
		formats = append(formats, "text/html")
	}
	if payload.ImageBase64 != "" {
		formats = append(formats, payload.ImageMIMEType)
	}
	if len(payload.Files) > 0 {
		formats = append(formats, "public.file-url")
	}
	return formats
}

// HandleLocalClipboard accepts native clipboard changes reported by the macOS helper.
func (d *Daemon) HandleLocalClipboard(payload helper.ClipboardPayload) {
	if payload.Sensitive {
		return
	}
	d.recordClipboardHistory(payload, d.identity.DeviceID, d.identity.Name)
	base := event.ClipboardSyncPayload{
		Text:          payload.Text,
		HTML:          payload.HTML,
		ImageBase64:   payload.ImageBase64,
		ImageMIMEType: payload.ImageMIMEType,
	}
	if len(payload.Files) > 0 {
		go d.sendClipboardFiles(payload, base)
		return
	}
	_, err := d.SendClipboardEntry("", base)
	if err != nil && err.Error() != "clipboard sync is disabled" && err.Error() != "no active session for clipboard sync" {
		log.Warnf("[clipboard] local clipboard sync failed: %v", err)
	}
}

func (d *Daemon) sendClipboardFiles(local helper.ClipboardPayload, base event.ClipboardSyncPayload) {
	d.clipMu.RLock()
	enabled := d.clipStatus.Enabled
	d.clipMu.RUnlock()
	if !enabled {
		return
	}
	if len(local.Files) > 32 {
		local.Files = local.Files[:32]
	}
	peers := make(map[string]struct{})
	d.connsMu.Lock()
	for _, deviceID := range d.conns {
		if deviceID != "" {
			peers[deviceID] = struct{}{}
		}
	}
	d.connsMu.Unlock()
	for peerID := range peers {
		entry := base
		entry.Files = make([]event.ClipboardFile, 0, len(local.Files))
		var totalBytes int64
		for _, file := range local.Files {
			path := expandHomePath(file.Path)
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				continue
			}
			digest, size, err := clipboardPathDigest(path, info.IsDir())
			if err != nil || size > 2*1024*1024*1024 || totalBytes+size > 2*1024*1024*1024 {
				continue
			}
			transfer, err := d.SendFileFromPath(peerID, path)
			if err != nil || transfer.Status != "completed" {
				continue
			}
			totalBytes += size
			entry.Files = append(entry.Files, event.ClipboardFile{
				TransferID: transfer.TransferID,
				Name:       filepath.Base(path),
				Directory:  info.IsDir(),
				SHA256:     digest,
			})
		}
		if entry.Text == "" && entry.HTML == "" && entry.ImageBase64 == "" && len(entry.Files) == 0 {
			continue
		}
		if _, err := d.SendClipboardEntry(peerID, entry); err != nil {
			log.Warnf("[clipboard] send file clipboard to %s failed: %v", peerID, err)
		}
	}
}

func clipboardPathDigest(path string, isDirectory bool) (string, int64, error) {
	h := sha256.New()
	var total int64
	addFile := func(filePath, relative string, isDir bool) error {
		name := filepath.ToSlash(relative)
		_, _ = fmt.Fprintf(h, "%d:%s:", len(name), name)
		if isDir {
			_, _ = h.Write([]byte("directory\n"))
			return nil
		}
		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer file.Close()
		copied, err := io.Copy(h, file)
		total += copied
		_, _ = h.Write([]byte("\n"))
		return err
	}
	if !isDirectory {
		if err := addFile(path, "", false); err != nil {
			return "", 0, err
		}
		return hex.EncodeToString(h.Sum(nil)), total, nil
	}
	root := filepath.Clean(path)
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return addFile(current, relative, true)
		}
		if entry.Type().IsRegular() {
			return addFile(current, relative, false)
		}
		return nil
	})
	if err != nil {
		return "", total, err
	}
	return hex.EncodeToString(h.Sum(nil)), total, nil
}

// HandleRemoteClipboard processes incoming clipboard_sync messages from a connected peer.
func (d *Daemon) HandleRemoteClipboard(deviceID string, pay event.ClipboardSyncPayload) {
	cleanText := strings.ToValidUTF8(pay.Text, "")
	if cleanText == "" && pay.HTML == "" && pay.ImageBase64 == "" && len(pay.Files) == 0 {
		return
	}
	if len(pay.Text)+len(pay.HTML)+len(pay.ImageBase64) > 14*1024*1024 {
		return
	}
	if len(pay.Files) > 32 {
		return
	}
	for _, file := range pay.Files {
		if file.TransferID == "" || file.Name == "" || len(file.Name) > 255 || filepath.Base(file.Name) != file.Name || file.SHA256 == "" {
			return
		}
	}
	if pay.ImageBase64 != "" {
		if (pay.ImageMIMEType != "image/png" && pay.ImageMIMEType != "image/tiff") || func() bool {
			_, err := base64.StdEncoding.DecodeString(pay.ImageBase64)
			return err != nil
		}() {
			return
		}
	}
	pay.Text = cleanText
	sha := clipboardEntrySHA(pay)
	if pay.PasteAfterWrite {
		d.controlMu.RLock()
		isActiveController := d.remotePeer == deviceID
		d.controlMu.RUnlock()
		if !isActiveController || len(pay.Files) > 0 || pay.ImageBase64 != "" {
			return
		}
	} else {
		d.clipMu.RLock()
		allowed := d.clipStatus.Enabled && d.clipStatus.AllowServerSync
		duplicate := d.clipStatus.LastSHA256 == sha || d.clipStatus.lastSystemWriteSHA == sha
		d.clipMu.RUnlock()
		if !allowed || duplicate {
			return
		}
	}
	localFiles := make([]helper.ClipboardLocalFile, 0, len(pay.Files))
	for _, file := range pay.Files {
		transfer := d.getTransferStatus(file.TransferID)
		if transfer.Status != "completed" || transfer.SavedPath == "" {
			continue
		}
		localPath := transfer.SavedPath
		if file.Directory {
			localPath, _ = d.extractClipboardDirectory(transfer.SavedPath, file.Name)
			if localPath == "" {
				continue
			}
		}
		digest, _, err := clipboardPathDigest(localPath, file.Directory)
		if err != nil || digest != file.SHA256 {
			continue
		}
		localFiles = append(localFiles, helper.ClipboardLocalFile{Path: localPath, Name: filepath.Base(localPath), Directory: file.Directory})
	}
	if cleanText == "" && pay.HTML == "" && pay.ImageBase64 == "" && len(localFiles) == 0 {
		return
	}
	d.clipMu.Lock()
	if !pay.PasteAfterWrite && (!d.clipStatus.Enabled || !d.clipStatus.AllowServerSync) {
		d.clipMu.Unlock()
		return
	}
	// Anti-loop guard: ignore if content hash matches the last synced clipboard.
	if !pay.PasteAfterWrite && ((d.clipStatus.LastSHA256 != "" && d.clipStatus.LastSHA256 == sha) ||
		(d.clipStatus.lastSystemWriteSHA != "" && d.clipStatus.lastSystemWriteSHA == sha)) {
		d.clipMu.Unlock()
		return
	}
	d.clipStatus.LastText = cleanText
	d.clipStatus.LastFormats = clipboardFormats(pay)
	d.clipStatus.LastSHA256 = sha
	d.clipStatus.lastSystemWriteSHA = sha
	d.clipStatus.lastRemoteReceiveAt = time.Now()
	d.clipStatus.LastSourceID = deviceID
	d.clipStatus.UpdatedAt = time.Now().UnixMilli()
	d.clipMu.Unlock()
	if !pay.PasteAfterWrite {
		d.recordClipboardHistory(helper.ClipboardPayload{Text: pay.Text, HTML: pay.HTML}, deviceID, d.sessionDeviceName(deviceID))
	}

	if runtime.GOOS == "darwin" && d.helper != nil {
		d.helper.BroadcastClipboard(helper.ClipboardPayload{
			Text:            pay.Text,
			HTML:            pay.HTML,
			ImageBase64:     pay.ImageBase64,
			ImageMIMEType:   pay.ImageMIMEType,
			Files:           localFiles,
			ContentSHA:      sha,
			PasteAfterWrite: pay.PasteAfterWrite,
		})
	}

	d.broadcast(BusEvent{
		Kind:     "clipboard_received",
		DeviceID: deviceID,
		Msg:      cleanText,
	})
}

func (d *Daemon) extractClipboardDirectory(archivePath, directoryName string) (string, error) {
	cleanName := filepath.Base(strings.TrimSpace(directoryName))
	if cleanName == "" || cleanName == "." || cleanName == ".." || cleanName == string(filepath.Separator) {
		return "", fmt.Errorf("invalid directory name")
	}
	root := uniqueDestinationPath(d.FileDownloadDir(), cleanName)
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(root)
		}
	}()
	var total int64
	for _, file := range reader.File {
		parts := strings.Split(filepath.ToSlash(file.Name), "/")
		if len(parts) == 0 || parts[0] != cleanName {
			return "", fmt.Errorf("invalid directory archive root")
		}
		relative := filepath.FromSlash(strings.Join(parts[1:], "/"))
		if relative == "" {
			continue
		}
		target := filepath.Join(root, relative)
		cleanTarget := filepath.Clean(target)
		rel, err := filepath.Rel(root, cleanTarget)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("directory archive path escapes destination")
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(cleanTarget, 0o700); err != nil {
				return "", err
			}
			continue
		}
		if !file.Mode().IsRegular() {
			return "", fmt.Errorf("directory archive contains a non-regular file")
		}
		total += int64(file.UncompressedSize64)
		if total > 2*1024*1024*1024 {
			return "", fmt.Errorf("directory archive exceeds 2 GB")
		}
		if err := os.MkdirAll(filepath.Dir(cleanTarget), 0o700); err != nil {
			return "", err
		}
		source, err := file.Open()
		if err != nil {
			return "", err
		}
		out, err := os.OpenFile(cleanTarget, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = source.Close()
			return "", err
		}
		_, copyErr := io.Copy(out, io.LimitReader(source, int64(file.UncompressedSize64)+1))
		closeOutErr := out.Close()
		closeSourceErr := source.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeOutErr != nil {
			return "", closeOutErr
		}
		if closeSourceErr != nil {
			return "", closeSourceErr
		}
	}
	success = true
	return root, nil
}

func (d *Daemon) runClipboardWatcher(ctx context.Context) {
	if os.Getenv("MB_SYNC_SYSTEM_CLIPBOARD") != "1" || runtime.GOOS != "darwin" {
		return
	}
	var lastObservedSHA string
	if out, err := readSystemClipboard(); err == nil && len(out) > 0 {
		lastObservedSHA = sha256Hex([]byte(out))
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
			if !snap.lastRemoteReceiveAt.IsZero() && time.Since(snap.lastRemoteReceiveAt) < 900*time.Millisecond {
				continue
			}
			out, err := readSystemClipboard()
			if err != nil || len(out) == 0 {
				continue
			}
			sha := sha256Hex([]byte(out))
			if sha == lastObservedSHA || sha == snap.LastSHA256 || (snap.lastSystemWriteSHA != "" && sha == snap.lastSystemWriteSHA) {
				lastObservedSHA = sha
				continue
			}
			lastObservedSHA = sha
			_, _ = d.SendClipboard("", out)
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
		dir = "~/Downloads/MouseBridge"
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
	encoded, _ := json.Marshal(struct {
		DownloadDir string `json:"download_dir"`
	}{resolved})
	if err := os.WriteFile(filepath.Join(d.dataDir, "file-transfer-settings.json"), encoded, 0o600); err != nil {
		return "", fmt.Errorf("save download_dir: %w", err)
	}
	return resolved, nil
}

func (d *Daemon) loadFileDownloadDir() {
	raw, err := os.ReadFile(filepath.Join(d.dataDir, "file-transfer-settings.json"))
	if err != nil {
		return
	}
	var settings struct {
		DownloadDir string `json:"download_dir"`
	}
	if json.Unmarshal(raw, &settings) == nil && strings.TrimSpace(settings.DownloadDir) != "" {
		d.fileDownloadDir = expandHomePath(settings.DownloadDir)
	}
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
		root := filepath.Clean(resolved)
		rootName := filepath.Base(root)
		tempDir, err := os.MkdirTemp("", "mousebridge-folder-transfer-")
		if err != nil {
			return FileTransferStatus{}, fmt.Errorf("create folder transfer archive: %w", err)
		}
		defer os.RemoveAll(tempDir)
		archivePath := filepath.Join(tempDir, rootName+".zip")
		archiveFile, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return FileTransferStatus{}, fmt.Errorf("create folder transfer archive: %w", err)
		}
		zw := zip.NewWriter(archiveFile)
		if _, err := zw.Create(filepath.ToSlash(rootName) + "/"); err != nil {
			_ = zw.Close()
			_ = archiveFile.Close()
			return FileTransferStatus{}, err
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == root {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			zipName := filepath.ToSlash(filepath.Join(rootName, rel))
			if entry.IsDir() {
				_, err := zw.Create(zipName + "/")
				return err
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			f, err := zw.Create(zipName)
			if err != nil {
				return err
			}
			src, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(f, src)
			closeErr := src.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		})
		if err != nil {
			_ = zw.Close()
			_ = archiveFile.Close()
			return FileTransferStatus{}, fmt.Errorf("zip directory: %w", err)
		}
		if err := zw.Close(); err != nil {
			_ = archiveFile.Close()
			return FileTransferStatus{}, err
		}
		if err := archiveFile.Close(); err != nil {
			return FileTransferStatus{}, err
		}
		return d.SendFileFromPath(targetDeviceID, archivePath)
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
