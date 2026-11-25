package daemon

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/helper"
)

var quickTextIDPattern = regexp.MustCompile(`^[a-f0-9]{16}$`)

// QuickTextEntries returns quick-text metadata without exposing Keychain values.
func (d *Daemon) QuickTextEntries() []config.QuickText {
	entries := make([]config.QuickText, len(d.cfg.QuickTexts))
	for i, entry := range d.cfg.QuickTexts {
		entries[i] = entry
		if entry.Sensitive {
			entries[i].Text = ""
			entries[i].HasSecret = true
		}
	}
	return entries
}

// UpdateQuickTexts validates entries and stores sensitive text in the macOS Keychain.
func (d *Daemon) UpdateQuickTexts(entries []config.QuickText) ([]config.QuickText, error) {
	if len(entries) > 100 {
		return nil, fmt.Errorf("at most 100 quick-text entries are allowed")
	}
	oldByID := make(map[string]config.QuickText, len(d.cfg.QuickTexts))
	for _, entry := range d.cfg.QuickTexts {
		oldByID[entry.ID] = entry
	}
	next := make([]config.QuickText, len(entries))
	usedIDs := make(map[string]bool, len(entries))
	usedHotkeys := make(map[string]string)
	for key, combo := range map[string]string{
		"switch_next": d.cfg.Hotkeys.SwitchNext, "switch_prev": d.cfg.Hotkeys.SwitchPrev,
		"switch_to_host": d.cfg.Hotkeys.SwitchToHost, "disconnect_all": d.cfg.Hotkeys.DisconnectAll,
		"toggle_pause": d.cfg.Hotkeys.TogglePause, "clipboard_history": d.cfg.Hotkeys.ClipboardHistory,
	} {
		if combo == "" {
			continue
		}
		sig, err := normalizeHotkey(combo)
		if err == nil {
			usedHotkeys[sig] = key
		}
	}
	for i, entry := range entries {
		entry.ID = strings.TrimSpace(entry.ID)
		if entry.ID == "" {
			entry.ID = newTransferID()
		}
		if !quickTextIDPattern.MatchString(entry.ID) || usedIDs[entry.ID] {
			return nil, fmt.Errorf("quick-text entry has an invalid or duplicate id")
		}
		usedIDs[entry.ID] = true
		entry.Name = strings.TrimSpace(entry.Name)
		if entry.Name == "" || len(entry.Name) > 80 {
			return nil, fmt.Errorf("quick-text name must contain 1 to 80 characters")
		}
		entry.Hotkey = strings.TrimSpace(entry.Hotkey)
		if entry.Hotkey != "" {
			sig, err := normalizeHotkey(entry.Hotkey)
			if err != nil {
				return nil, fmt.Errorf("quick-text %q: %w", entry.Name, err)
			}
			if owner, exists := usedHotkeys[sig]; exists {
				return nil, fmt.Errorf("quick-text %q hotkey conflicts with %s", entry.Name, owner)
			}
			usedHotkeys[sig] = entry.Name
		}
		entry.Text = strings.ToValidUTF8(entry.Text, "")
		if len(entry.Text) > 8192 {
			return nil, fmt.Errorf("quick-text %q exceeds 8192 bytes", entry.Name)
		}
		old, hadOld := oldByID[entry.ID]
		if entry.Sensitive {
			if d.helper == nil {
				return nil, fmt.Errorf("macOS helper is required to access sensitive quick text")
			}
			if entry.Text == "" {
				if !hadOld || !old.Sensitive {
					return nil, fmt.Errorf("quick-text %q needs content before it can be marked sensitive", entry.Name)
				}
				if _, err := d.helper.RequestSecureValue("get", quickTextSecretKey(entry.ID), ""); err != nil {
					return nil, fmt.Errorf("quick-text %q has no Keychain value: %w", entry.Name, err)
				}
			}
			entry.Text = ""
			entry.HasSecret = true
		} else {
			if entry.Text == "" || len(entry.Text) > 8192 {
				return nil, fmt.Errorf("quick-text %q must contain 1 to 8192 bytes", entry.Name)
			}
			entry.HasSecret = false
		}
		next[i] = entry
	}

	var secretMutations []quickTextSecretMutation
	rollbackSecrets := func(cause error) error {
		if err := d.rollbackQuickTextSecrets(secretMutations); err != nil {
			return fmt.Errorf("%w (also failed to roll back Keychain changes: %v)", cause, err)
		}
		return cause
	}
	for i, entry := range entries {
		if !next[i].Sensitive || entry.Text == "" {
			continue
		}
		mutation := quickTextSecretMutation{key: quickTextSecretKey(next[i].ID)}
		if old, hadOld := oldByID[next[i].ID]; hadOld && old.Sensitive {
			previousValue, err := d.helper.RequestSecureValue("get", mutation.key, "")
			if err != nil {
				return nil, rollbackSecrets(fmt.Errorf("read existing quick-text secret: %w", err))
			}
			mutation.previous = previousValue
			mutation.existed = true
		}
		if _, err := d.helper.RequestSecureValue("set", mutation.key, entry.Text); err != nil {
			return nil, rollbackSecrets(err)
		}
		secretMutations = append(secretMutations, mutation)
	}

	previous := d.cfg.QuickTexts
	d.cfg.QuickTexts = next
	if d.configPath != "" {
		if err := d.cfg.Save(d.configPath); err != nil {
			d.cfg.QuickTexts = previous
			return nil, rollbackSecrets(err)
		}
	}
	if d.helper != nil {
		for id, old := range oldByID {
			nextEntry, retained := findQuickText(next, id)
			if old.Sensitive && (!retained || !nextEntry.Sensitive) {
				if _, err := d.helper.RequestSecureValue("delete", quickTextSecretKey(id), ""); err != nil {
					d.broadcast(BusEvent{Kind: "error", Msg: "quick-text Keychain cleanup failed: " + err.Error()})
				}
			}
		}
		d.helper.BroadcastConfig()
	}
	return d.QuickTextEntries(), nil
}

type quickTextSecretMutation struct {
	key      string
	previous string
	existed  bool
}

func (d *Daemon) rollbackQuickTextSecrets(mutations []quickTextSecretMutation) error {
	if d.helper == nil || len(mutations) == 0 {
		return nil
	}
	var rollbackErrors []error
	for i := len(mutations) - 1; i >= 0; i-- {
		mutation := mutations[i]
		operation, value := "delete", ""
		if mutation.existed {
			operation, value = "set", mutation.previous
		}
		if _, err := d.helper.RequestSecureValue(operation, mutation.key, value); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	return errors.Join(rollbackErrors...)
}

func findQuickText(entries []config.QuickText, id string) (config.QuickText, bool) {
	for _, entry := range entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return config.QuickText{}, false
}

func quickTextSecretKey(id string) string { return "quick-text:" + id }

func normalizeHotkey(combo string) (string, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(combo)), "+")
	if len(parts) < 2 || len(parts) > 5 {
		return "", fmt.Errorf("hotkey must contain modifiers and one key")
	}
	modifiers := make([]string, 0, len(parts)-1)
	aliases := map[string]string{
		"ctrl": "ctrl", "control": "ctrl", "alt": "alt", "option": "alt",
		"cmd": "cmd", "command": "cmd", "shift": "shift",
	}
	for _, part := range parts[:len(parts)-1] {
		canonical, ok := aliases[strings.TrimSpace(part)]
		if !ok {
			return "", fmt.Errorf("hotkey contains an unsupported modifier")
		}
		for _, used := range modifiers {
			if used == canonical {
				return "", fmt.Errorf("hotkey repeats a modifier")
			}
		}
		modifiers = append(modifiers, canonical)
	}
	key := strings.TrimSpace(parts[len(parts)-1])
	if len(key) != 1 && !map[string]bool{
		"left": true, "right": true, "up": true, "down": true, "home": true,
		"escape": true, "esc": true, "backspace": true, "delete": true,
		"tab": true, "space": true, "return": true, "enter": true,
	}[key] {
		return "", fmt.Errorf("hotkey key is unsupported")
	}
	if len(key) == 1 && !((key[0] >= 'a' && key[0] <= 'z') || (key[0] >= '0' && key[0] <= '9')) {
		return "", fmt.Errorf("hotkey key is unsupported")
	}
	sort.Strings(modifiers)
	return strings.Join(append(modifiers, key), "+"), nil
}

func (d *Daemon) triggerQuickText(id string) {
	for _, entry := range d.cfg.QuickTexts {
		if entry.ID != id {
			continue
		}
		text := entry.Text
		if entry.Sensitive {
			if d.helper == nil {
				return
			}
			value, err := d.helper.RequestSecureValue("get", quickTextSecretKey(entry.ID), "")
			if err != nil {
				d.broadcast(BusEvent{Kind: "error", Msg: "quick-text Keychain read failed: " + err.Error()})
				return
			}
			text = value
		}
		if text == "" || d.Paused() {
			return
		}
		target := d.ctrl.ActiveTarget()
		inputs := []helper.InputPayload{{Kind: "text", Text: text}}
		if entry.AppendEnter {
			inputs = append(inputs, helper.InputPayload{Kind: "key_tap", KeyCode: 36})
		}
		for _, input := range inputs {
			if target != "" && target != d.identity.DeviceID {
				if err := d.SendSessionInput(target, input); err != nil {
					d.broadcast(BusEvent{Kind: "error", Msg: "quick-text input failed: " + err.Error()})
					return
				}
			} else {
				d.PushHelperInput(input)
			}
		}
		d.broadcast(BusEvent{Kind: "quick_text_sent", Msg: entry.Name})
		return
	}
}
