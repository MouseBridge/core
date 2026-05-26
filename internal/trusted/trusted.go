package trusted

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Device is a trusted peer persisted to disk.
type Device struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
}

// Store manages the trusted device list.
type Store struct {
	mu   sync.RWMutex
	path string
	devs map[string]Device // device_id → Device
}

// New loads (or creates) the store at path. An empty path creates an in-memory-only store.
func New(path string) (*Store, error) {
	s := &Store{path: path, devs: make(map[string]Device)}
	if path == "" {
		return s, nil
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// IsTrusted reports whether device_id is trusted.
func (s *Store) IsTrusted(deviceID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.devs[deviceID]
	return ok
}

// Add adds or updates a trusted device and persists to disk.
func (s *Store) Add(deviceID, name string) error {
	s.mu.Lock()
	s.devs[deviceID] = Device{DeviceID: deviceID, Name: name}
	s.mu.Unlock()
	return s.save()
}

// Remove removes a device and persists to disk.
func (s *Store) Remove(deviceID string) error {
	s.mu.Lock()
	delete(s.devs, deviceID)
	s.mu.Unlock()
	return s.save()
}

// List returns all trusted devices.
func (s *Store) List() []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Device, 0, len(s.devs))
	for _, d := range s.devs {
		out = append(out, d)
	}
	return out
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []Device
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	for _, d := range list {
		s.devs[d.DeviceID] = d
	}
	return nil
}

func (s *Store) save() error {
	s.mu.RLock()
	list := make([]Device, 0, len(s.devs))
	for _, d := range s.devs {
		list = append(list, d)
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}
