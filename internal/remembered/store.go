package remembered

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is the full internal representation, including secrets.
type Record struct {
	DeviceID   string    `json:"device_id"`
	DisplayID  string    `json:"display_id"`
	Name       string    `json:"name"`
	Alias      string    `json:"alias,omitempty"`
	SecretID   string    `json:"secret_id"`
	PairSecret string    `json:"pair_secret"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// DTO is the API/SSE-safe view; never contains pair_secret or secret_id.
type DTO struct {
	DeviceID   string `json:"device_id"`
	DisplayID  string `json:"display_id"`
	Name       string `json:"name"`
	Alias      string `json:"alias,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
}

func toDTO(r Record) DTO {
	return DTO{
		DeviceID:   r.DeviceID,
		DisplayID:  r.DisplayID,
		Name:       r.Name,
		Alias:      r.Alias,
		CreatedAt:  r.CreatedAt.Unix(),
		LastSeenAt: r.LastSeenAt.Unix(),
	}
}

// Store is a concurrent-safe remembered device store with atomic persistence.
type Store struct {
	mu      sync.RWMutex
	writeMu sync.Mutex
	path    string
	records map[string]Record
}

// New loads the store from path (or starts empty if file doesn't exist).
func New(path string) (*Store, error) {
	s := &Store{
		path:    path,
		records: make(map[string]Record),
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("remembered: read %s: %w", path, err)
	}
	var recs []Record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("remembered: parse %s: %w", path, err)
	}
	for _, r := range recs {
		s.records[r.DeviceID] = r
	}
	return s, nil
}

// Add adds or overwrites a record.
func (s *Store) Add(rec Record) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	next := cloneMap(s.records)
	next[rec.DeviceID] = rec
	err := s.persist(next)
	if err == nil {
		s.records = next
	}
	s.mu.Unlock()
	return err
}

// Remove deletes a record by device ID.
func (s *Store) Remove(deviceID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	next := cloneMap(s.records)
	delete(next, deviceID)
	err := s.persist(next)
	if err == nil {
		s.records = next
	}
	s.mu.Unlock()
	return err
}

// Rename updates the alias of a record.
func (s *Store) Rename(deviceID, alias string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	rec, ok := s.records[deviceID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("remembered: device %s not found", deviceID)
	}
	next := cloneMap(s.records)
	rec.Alias = alias
	next[deviceID] = rec
	err := s.persist(next)
	if err == nil {
		s.records = next
	}
	s.mu.Unlock()
	return err
}

// UpdateLastSeen updates the last_seen_at timestamp.
func (s *Store) UpdateLastSeen(deviceID string, t time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	rec, ok := s.records[deviceID]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	next := cloneMap(s.records)
	rec.LastSeenAt = t
	next[deviceID] = rec
	err := s.persist(next)
	if err == nil {
		s.records = next
	}
	s.mu.Unlock()
	return err
}

// Get returns the full record for internal use (e.g. auto-connect HMAC).
func (s *Store) Get(deviceID string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[deviceID]
	return r, ok
}

// IsTrusted returns true if deviceID has a remembered record.
func (s *Store) IsTrusted(deviceID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.records[deviceID]
	return ok
}

// List returns all records as DTOs (no secrets).
func (s *Store) List() []DTO {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DTO, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, toDTO(r))
	}
	return out
}

func cloneMap(m map[string]Record) map[string]Record {
	out := make(map[string]Record, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Store) persist(records map[string]Record) error {
	recs := make([]Record, 0, len(records))
	for _, r := range records {
		recs = append(recs, r)
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
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
	return os.Rename(tmp, s.path)
}
