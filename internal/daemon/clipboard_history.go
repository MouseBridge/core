package daemon

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/helper"
)

const (
	clipboardHistoryMagic       = "MBCH1"
	clipboardHistoryMaxFileSize = 64 * 1024 * 1024
	clipboardHistoryMaxPayload  = clipboardHistoryMaxFileSize - 1024
)

// ClipboardHistoryEntry is one text/HTML item retained for local paste history.
type ClipboardHistoryEntry struct {
	ID               string   `json:"id"`
	Text             string   `json:"text,omitempty"`
	HTML             string   `json:"html,omitempty"`
	CreatedAt        int64    `json:"created_at"`
	SourceID         string   `json:"source_id,omitempty"`
	Source           string   `json:"source,omitempty"`
	Formats          []string `json:"formats"`
	PreviewTruncated bool     `json:"preview_truncated,omitempty"`
}

// ClipboardHistorySnapshot reports the local history setting and retained entries.
type ClipboardHistorySnapshot struct {
	Enabled bool                    `json:"enabled"`
	Limit   int                     `json:"limit"`
	Entries []ClipboardHistoryEntry `json:"entries"`
	Error   string                  `json:"error,omitempty"`
}

// ClipboardHistorySnapshot returns the newest retained local text/HTML items.
func (d *Daemon) ClipboardHistorySnapshot() ClipboardHistorySnapshot {
	d.historyMu.Lock()
	defer d.historyMu.Unlock()
	if err := d.loadClipboardHistoryLocked(); err != nil {
		d.historyError = err.Error()
	}
	entries := make([]ClipboardHistoryEntry, len(d.historyEntries))
	copy(entries, d.historyEntries)
	for i := range entries {
		entries[i].Formats = append([]string(nil), entries[i].Formats...)
		entries[i].Text, entries[i].PreviewTruncated = truncateHistoryPreview(entries[i].Text, 4096)
		var htmlTruncated bool
		entries[i].HTML, htmlTruncated = truncateHistoryPreview(entries[i].HTML, 16*1024)
		entries[i].PreviewTruncated = entries[i].PreviewTruncated || htmlTruncated
	}
	return ClipboardHistorySnapshot{
		Enabled: d.cfg.ClipboardHistoryEnabled,
		Limit:   d.cfg.ClipboardHistoryLimit,
		Entries: entries,
		Error:   d.historyError,
	}
}

// UpdateClipboardHistorySettings persists whether history is recording and its retention limit.
func (d *Daemon) UpdateClipboardHistorySettings(enabled bool, limit int) (ClipboardHistorySnapshot, error) {
	if limit < 10 || limit > 500 {
		return ClipboardHistorySnapshot{}, fmt.Errorf("history limit must be between 10 and 500")
	}
	previousEnabled, previousLimit := d.cfg.ClipboardHistoryEnabled, d.cfg.ClipboardHistoryLimit
	d.cfg.ClipboardHistoryEnabled = enabled
	d.cfg.ClipboardHistoryLimit = limit
	if d.configPath != "" {
		if err := d.cfg.Save(d.configPath); err != nil {
			d.cfg.ClipboardHistoryEnabled = previousEnabled
			d.cfg.ClipboardHistoryLimit = previousLimit
			return d.ClipboardHistorySnapshot(), err
		}
	}
	d.historyMu.Lock()
	err := d.loadClipboardHistoryLocked()
	if err == nil {
		previousEntries := d.historyEntries
		d.historyEntries = trimClipboardHistory(d.historyEntries, limit)
		if len(d.historyEntries) != len(previousEntries) {
			err = d.saveClipboardHistoryLocked()
			if err != nil {
				d.historyEntries = previousEntries
			}
		}
	}
	d.historyMu.Unlock()
	if err != nil {
		d.cfg.ClipboardHistoryEnabled = previousEnabled
		d.cfg.ClipboardHistoryLimit = previousLimit
		if d.configPath != "" {
			if rollbackErr := d.cfg.Save(d.configPath); rollbackErr != nil {
				err = fmt.Errorf("%w (also failed to restore config: %v)", err, rollbackErr)
			}
		}
		return d.ClipboardHistorySnapshot(), err
	}
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
	d.broadcast(BusEvent{Kind: "log", Msg: fmt.Sprintf("clipboard history enabled=%t limit=%d", enabled, limit)})
	return d.ClipboardHistorySnapshot(), nil
}

// ClearClipboardHistory deletes the encrypted local history file and in-memory entries.
func (d *Daemon) ClearClipboardHistory() error {
	d.historyMu.Lock()
	defer d.historyMu.Unlock()
	previousEntries, previousLoaded := d.historyEntries, d.historyLoaded
	d.historyEntries = nil
	d.historyLoaded = true
	d.historyError = ""
	if err := os.Remove(d.clipboardHistoryPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		d.historyEntries, d.historyLoaded = previousEntries, previousLoaded
		d.historyError = err.Error()
		return err
	}
	return nil
}

// PasteClipboardHistory applies one retained item and asks the active target to paste it.
func (d *Daemon) PasteClipboardHistory(entryID string) error {
	d.historyMu.Lock()
	if err := d.loadClipboardHistoryLocked(); err != nil {
		d.historyMu.Unlock()
		return err
	}
	var selected *ClipboardHistoryEntry
	for i := range d.historyEntries {
		if d.historyEntries[i].ID == entryID {
			copyEntry := d.historyEntries[i]
			selected = &copyEntry
			break
		}
	}
	d.historyMu.Unlock()
	if selected == nil {
		return fmt.Errorf("clipboard history entry not found")
	}
	if selected.Text == "" && selected.HTML == "" {
		return fmt.Errorf("clipboard history entry has no pasteable text")
	}
	payload := event.ClipboardSyncPayload{
		Text:            selected.Text,
		HTML:            selected.HTML,
		PasteAfterWrite: true,
		SourceID:        d.identity.DeviceID,
	}
	payload.ContentSHA = clipboardEntrySHA(payload)
	target := d.ctrl.ActiveTarget()
	if target == "" || target == d.identity.DeviceID {
		if d.helper == nil || d.helper.ClientCount() == 0 {
			return fmt.Errorf("local helper is not connected")
		}
		d.helper.BroadcastClipboard(helper.ClipboardPayload{
			Text:            payload.Text,
			HTML:            payload.HTML,
			ContentSHA:      payload.ContentSHA,
			PasteAfterWrite: true,
		})
		return nil
	}
	conns := d.targetConns(target)
	if len(conns) == 0 {
		return fmt.Errorf("active target is not connected")
	}
	msg := event.Message{
		V:       1,
		Seq:     time.Now().UnixMilli(),
		Type:    event.TypeClipboardSync,
		Ts:      time.Now().UnixMilli(),
		Payload: payload,
	}
	return conns[0].Send(msg)
}

func (d *Daemon) recordClipboardHistory(payload helper.ClipboardPayload, sourceID, sourceName string) {
	if !d.cfg.ClipboardHistoryEnabled || payload.Sensitive || (payload.Text == "" && payload.HTML == "") {
		return
	}
	if len(payload.Text) > 512*1024 || len(payload.HTML) > 2*1024*1024 {
		return
	}
	d.historyMu.Lock()
	defer d.historyMu.Unlock()
	if err := d.loadClipboardHistoryLocked(); err != nil {
		d.historyError = err.Error()
		return
	}
	formats := make([]string, 0, 2)
	if payload.Text != "" {
		formats = append(formats, "text/plain")
	}
	if payload.HTML != "" {
		formats = append(formats, "text/html")
	}
	entry := ClipboardHistoryEntry{
		ID:        newTransferID(),
		Text:      payload.Text,
		HTML:      payload.HTML,
		CreatedAt: time.Now().UnixMilli(),
		SourceID:  sourceID,
		Source:    sourceName,
		Formats:   formats,
	}
	previousEntries := d.historyEntries
	d.historyEntries = append([]ClipboardHistoryEntry{entry}, d.historyEntries...)
	limit := d.cfg.ClipboardHistoryLimit
	if limit < 10 {
		limit = 50
	}
	d.historyEntries = trimClipboardHistory(d.historyEntries, limit)
	if err := d.saveClipboardHistoryLocked(); err != nil {
		d.historyEntries = previousEntries
		d.historyError = err.Error()
		return
	}
	d.historyError = ""
	d.broadcast(BusEvent{Kind: "clipboard_history_updated", Msg: entry.ID})
}

func trimClipboardHistory(entries []ClipboardHistoryEntry, limit int) []ClipboardHistoryEntry {
	if limit < 1 {
		return nil
	}
	trimmed := make([]ClipboardHistoryEntry, 0, min(len(entries), limit))
	fileSize := 0
	for _, entry := range entries {
		if len(trimmed) >= limit {
			break
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			break
		}
		if fileSize+len(encoded) > clipboardHistoryMaxPayload {
			break
		}
		fileSize += len(encoded)
		trimmed = append(trimmed, entry)
	}
	return trimmed
}

func truncateHistoryPreview(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end], true
}

func (d *Daemon) loadClipboardHistoryLocked() error {
	if d.historyLoaded {
		return nil
	}
	path := d.clipboardHistoryPath()
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		d.historyLoaded = true
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() > clipboardHistoryMaxFileSize {
		return fmt.Errorf("clipboard history file exceeds the 64 MiB limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	key, err := d.clipboardHistoryKey(false)
	if err != nil {
		return err
	}
	if len(data) < len(clipboardHistoryMagic)+12 || string(data[:len(clipboardHistoryMagic)]) != clipboardHistoryMagic {
		return fmt.Errorf("clipboard history file has an invalid header")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonceStart := len(clipboardHistoryMagic)
	nonce := data[nonceStart : nonceStart+gcm.NonceSize()]
	plain, err := gcm.Open(nil, nonce, data[nonceStart+gcm.NonceSize():], nil)
	if err != nil {
		return fmt.Errorf("decrypt clipboard history: %w", err)
	}
	if err := json.Unmarshal(plain, &d.historyEntries); err != nil {
		return fmt.Errorf("decode clipboard history: %w", err)
	}
	d.historyLoaded = true
	return nil
}

func (d *Daemon) saveClipboardHistoryLocked() error {
	key, err := d.clipboardHistoryKey(true)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(d.historyEntries)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	data := append([]byte(clipboardHistoryMagic), nonce...)
	data = gcm.Seal(data, nonce, plain, nil)
	dir := filepath.Dir(d.clipboardHistoryPath())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tempPath := d.clipboardHistoryPath() + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tempPath, d.clipboardHistoryPath()); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	d.historyLoaded = true
	return nil
}

func (d *Daemon) clipboardHistoryKey(create bool) ([]byte, error) {
	if len(d.historyKey) == 32 {
		return append([]byte(nil), d.historyKey...), nil
	}
	if d.helper == nil {
		return nil, fmt.Errorf("macOS helper is not connected")
	}
	value, err := d.helper.RequestSecureValue("get", "clipboard-history-key", "")
	if err != nil {
		if !create || !strings.Contains(err.Error(), "item not found") {
			return nil, err
		}
		key := make([]byte, 32)
		if _, randomErr := rand.Read(key); randomErr != nil {
			return nil, randomErr
		}
		value = base64.StdEncoding.EncodeToString(key)
		if _, err := d.helper.RequestSecureValue("set", "clipboard-history-key", value); err != nil {
			return nil, err
		}
		d.historyKey = append([]byte(nil), key...)
		return key, nil
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("clipboard history key is invalid")
	}
	d.historyKey = append([]byte(nil), key...)
	return key, nil
}

func (d *Daemon) clipboardHistoryPath() string {
	return filepath.Join(d.dataDir, "clipboard-history.enc")
}
