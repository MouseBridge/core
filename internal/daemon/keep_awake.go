package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// KeepAwakeStatus reports whether the configured system-sleep assertion is active.
type KeepAwakeStatus struct {
	Enabled   bool   `json:"enabled"`
	Active    bool   `json:"active"`
	Supported bool   `json:"supported"`
	Error     string `json:"error,omitempty"`
}

// KeepAwakeStatusSnapshot returns the current power assertion state.
func (d *Daemon) KeepAwakeStatusSnapshot() KeepAwakeStatus {
	d.wakeMu.Lock()
	defer d.wakeMu.Unlock()
	status := d.wakeStatus
	status.Enabled = d.cfg.KeepAwakeEnabled
	status.Supported = runtime.GOOS == "darwin"
	return status
}

// UpdateKeepAwakeEnabled persists the preference and applies the temporary
// operating-system request. macOS caffeinate uses an IOPM power assertion;
// stopping the daemon or disabling the preference releases it.
func (d *Daemon) UpdateKeepAwakeEnabled(enabled bool) (KeepAwakeStatus, error) {
	previous := d.cfg.KeepAwakeEnabled
	d.cfg.KeepAwakeEnabled = enabled
	if d.configPath != "" {
		if err := d.cfg.Save(d.configPath); err != nil {
			d.cfg.KeepAwakeEnabled = previous
			return d.KeepAwakeStatusSnapshot(), err
		}
	}
	status, err := d.applyKeepAwake(enabled)
	if err != nil {
		d.cfg.KeepAwakeEnabled = previous
		if d.configPath != "" {
			_ = d.cfg.Save(d.configPath)
		}
		return d.KeepAwakeStatusSnapshot(), err
	}
	d.broadcast(BusEvent{Kind: "log", Msg: fmt.Sprintf("keep-awake enabled=%t active=%t", status.Enabled, status.Active)})
	return status, nil
}

func (d *Daemon) applyKeepAwake(enabled bool) (KeepAwakeStatus, error) {
	d.wakeMu.Lock()
	defer d.wakeMu.Unlock()
	if !enabled {
		cmd := d.wakeCmd
		d.wakeCmd = nil
		d.wakeStatus = KeepAwakeStatus{Supported: runtime.GOOS == "darwin"}
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return d.wakeStatus, nil
	}
	if runtime.GOOS != "darwin" {
		d.wakeStatus = KeepAwakeStatus{Enabled: true, Supported: false, Error: "keep-awake is currently supported on macOS only"}
		return d.wakeStatus, fmt.Errorf("keep-awake is currently supported on macOS only")
	}
	if d.wakeCmd != nil && d.wakeCmd.Process != nil {
		d.wakeStatus = KeepAwakeStatus{Enabled: true, Active: true, Supported: true}
		return d.wakeStatus, nil
	}
	cmd := exec.Command("/usr/bin/caffeinate", "-i", "-w", fmt.Sprint(os.Getpid()))
	if err := cmd.Start(); err != nil {
		d.wakeStatus = KeepAwakeStatus{Enabled: true, Supported: true, Error: err.Error()}
		return d.wakeStatus, fmt.Errorf("start macOS power assertion: %w", err)
	}
	d.wakeCmd = cmd
	d.wakeStatus = KeepAwakeStatus{Enabled: true, Active: true, Supported: true}
	go func() {
		err := cmd.Wait()
		d.wakeMu.Lock()
		defer d.wakeMu.Unlock()
		if d.wakeCmd != cmd {
			return
		}
		d.wakeCmd = nil
		d.wakeStatus.Active = false
		if err != nil && d.cfg.KeepAwakeEnabled {
			d.wakeStatus.Error = err.Error()
		}
	}()
	return d.wakeStatus, nil
}

func (d *Daemon) stopKeepAwake() {
	_, _ = d.applyKeepAwake(false)
}
