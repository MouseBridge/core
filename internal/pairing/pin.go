package pairing

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// GeneratePIN returns a cryptographically random 6-digit PIN string.
func GeneratePIN() (string, error) {
	max := big.NewInt(1_000_000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("pairing: generate PIN: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
