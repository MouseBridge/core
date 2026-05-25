//go:build !darwin

package switch_

import "log"

// HotkeyListener is a no-op stub for non-macOS platforms.
type HotkeyListener struct {
	onSwitch func(direction string)
	stopCh   chan struct{}
}

func NewHotkeyListener(switchNext, switchPrev string, onSwitch func(direction string)) *HotkeyListener {
	return &HotkeyListener{onSwitch: onSwitch, stopCh: make(chan struct{})}
}

func (h *HotkeyListener) Start() {
	log.Println("[hotkey] not supported on this platform")
}

func (h *HotkeyListener) Stop() {
	close(h.stopCh)
}
