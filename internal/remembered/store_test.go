package remembered

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListPreservesCreationOrder(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "remembered.json"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(100, 0)
	for _, rec := range []Record{
		{DeviceID: "device-b", CreatedAt: base.Add(time.Second)},
		{DeviceID: "device-a", CreatedAt: base},
		{DeviceID: "device-c", CreatedAt: base.Add(2 * time.Second)},
	} {
		if err := s.Add(rec); err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < 20; i++ {
		got := s.List()
		if len(got) != 3 || got[0].DeviceID != "device-a" || got[1].DeviceID != "device-b" || got[2].DeviceID != "device-c" {
			t.Fatalf("unstable remembered order: %+v", got)
		}
	}
}

func TestPersistenceUsesStableOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remembered.json")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range []Record{
		{DeviceID: "b", CreatedAt: time.Unix(2, 0)},
		{DeviceID: "a", CreatedAt: time.Unix(1, 0)},
	} {
		if err := s.Add(rec); err != nil {
			t.Fatal(err)
		}
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []Record
	if err := json.Unmarshal(first, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].DeviceID != "a" || records[1].DeviceID != "b" {
		t.Fatalf("persisted order=%v", records)
	}
}
