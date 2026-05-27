package device

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var validDeviceID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Identity holds the daemon's stable device identity.
type Identity struct {
	DeviceID  string `json:"device_id"`
	DisplayID string `json:"display_id"`
	Name      string `json:"name"`
}

// LoadOrCreate loads device.json if it exists; otherwise creates it with the
// derived ID. The deriveID func is provided by the caller (config.DeriveDeviceID).
func LoadOrCreate(path string, deriveID func() string, defaultName string) (Identity, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		var id Identity
		if err := json.Unmarshal(data, &id); err != nil {
			return Identity{}, fmt.Errorf("device: parse %s: %w", path, err)
		}
		if !validDeviceID.MatchString(id.DeviceID) {
			return Identity{}, fmt.Errorf("device: invalid device_id in %s", path)
		}
		// Always re-derive to ensure hardware-based stability.
		derived := deriveID()
		id.DeviceID = derived
		id.DisplayID = derived[:12]
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Identity{}, fmt.Errorf("device: read %s: %w", path, err)
	}

	derived := deriveID()
	id := Identity{
		DeviceID:  derived,
		DisplayID: derived[:12],
		Name:      defaultName,
	}
	if err := atomicWriteJSON(path, id); err != nil {
		return Identity{}, fmt.Errorf("device: create %s: %w", path, err)
	}
	return id, nil
}

func atomicWriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, path)
}
