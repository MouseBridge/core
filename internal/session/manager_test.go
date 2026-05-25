package session_test

import (
	"testing"

	"github.com/mousebridge/core/internal/session"
)

func TestAddRemoveDevice(t *testing.T) {
	m := session.NewManager()

	m.Add("dev-1", "MacBook-Pro")
	devs := m.Devices()
	if len(devs) != 1 {
		t.Fatalf("want 1 device got %d", len(devs))
	}
	if devs[0].ID != "dev-1" {
		t.Fatalf("id: want dev-1 got %q", devs[0].ID)
	}

	m.Remove("dev-1")
	if len(m.Devices()) != 0 {
		t.Fatal("want 0 devices after remove")
	}
}

func TestRecordLatency(t *testing.T) {
	m := session.NewManager()
	m.Add("dev-1", "MacBook-Pro")

	m.RecordLatency("dev-1", 1.2)
	m.RecordLatency("dev-1", 2.4)
	m.RecordLatency("dev-1", 1.8)

	devs := m.Devices()
	avg := devs[0].AvgLatencyMs
	if avg < 1.0 || avg > 3.0 {
		t.Fatalf("avg latency out of range: %f", avg)
	}
}
