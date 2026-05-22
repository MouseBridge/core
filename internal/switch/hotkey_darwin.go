//go:build darwin

package switch_

import (
	"bufio"
	"log"
	"os"
	"strings"
)

// HotkeyListener listens for switch hotkeys.
// Step 1: terminal fallback — type "right" or "left" + Enter.
// Step 3 will replace with real CGo-based global hotkey capture.
type HotkeyListener struct {
	switchRight string
	switchLeft  string
	onSwitch    func(direction string)
	stopCh      chan struct{}
}

func NewHotkeyListener(switchRight, switchLeft string, onSwitch func(direction string)) *HotkeyListener {
	return &HotkeyListener{
		switchRight: switchRight,
		switchLeft:  switchLeft,
		onSwitch:    onSwitch,
		stopCh:      make(chan struct{}),
	}
}

// Start logs configured hotkeys and begins reading from stdin as a fallback.
func (h *HotkeyListener) Start() {
	log.Printf("[hotkey] switch_right=%s  switch_left=%s  (type 'right' or 'left' + Enter to switch)",
		h.switchRight, h.switchLeft)
	go h.stdinLoop()
}

func (h *HotkeyListener) Stop() {
	close(h.stopCh)
}

func (h *HotkeyListener) stdinLoop() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		select {
		case <-h.stopCh:
			return
		default:
		}
		line := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if line == "right" || line == "left" {
			h.onSwitch(line)
		}
	}
}
