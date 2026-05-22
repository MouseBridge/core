package pairing_test

import (
	"testing"

	"mousebridge/internal/pairing"
)

func TestGeneratePIN(t *testing.T) {
	pin, err := pairing.GeneratePIN()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(pin) != 6 {
		t.Fatalf("length: want 6 got %d", len(pin))
	}
	for _, ch := range pin {
		if ch < '0' || ch > '9' {
			t.Fatalf("non-digit character: %q", ch)
		}
	}
}

func TestGeneratePINUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 20; i++ {
		pin, err := pairing.GeneratePIN()
		if err != nil {
			t.Fatalf("generate[%d]: %v", i, err)
		}
		seen[pin] = true
	}
	if len(seen) < 10 {
		t.Fatalf("too many collisions: only %d unique PINs in 20 attempts", len(seen))
	}
}
