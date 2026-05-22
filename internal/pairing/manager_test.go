package pairing_test

import (
	"testing"

	"mousebridge/internal/pairing"
)

func TestPairingAcceptPath(t *testing.T) {
	m := pairing.NewManager()

	pin, err := m.StartAsSlave("host-id", "HostMac")
	if err != nil {
		t.Fatalf("StartAsSlave: %v", err)
	}
	if len(pin) != 6 {
		t.Fatalf("pin length: want 6 got %d", len(pin))
	}

	if err := m.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if m.State() != pairing.StatePaired {
		t.Fatalf("state: want Paired got %s", m.State())
	}
}

func TestPairingPINPath(t *testing.T) {
	m := pairing.NewManager()
	pin, err := m.StartAsSlave("host-id", "HostMac")
	if err != nil {
		t.Fatalf("StartAsSlave: %v", err)
	}

	if err := m.ConfirmPIN(pin); err != nil {
		t.Fatalf("ConfirmPIN: %v", err)
	}
	if m.State() != pairing.StatePaired {
		t.Fatalf("state: want Paired got %s", m.State())
	}
}

func TestPairingWrongPIN(t *testing.T) {
	m := pairing.NewManager()
	_, err := m.StartAsSlave("host-id", "HostMac")
	if err != nil {
		t.Fatalf("StartAsSlave: %v", err)
	}

	err = m.ConfirmPIN("000000")
	if err == nil {
		t.Fatal("expected error for wrong PIN")
	}
	if m.State() != pairing.StateRejected {
		t.Fatalf("state: want Rejected got %s", m.State())
	}
}

func TestPairingReject(t *testing.T) {
	m := pairing.NewManager()
	_, err := m.StartAsSlave("host-id", "HostMac")
	if err != nil {
		t.Fatalf("StartAsSlave: %v", err)
	}

	if err := m.Reject(); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if m.State() != pairing.StateRejected {
		t.Fatalf("state: want Rejected got %s", m.State())
	}
}
