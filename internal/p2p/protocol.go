package p2p

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/transport"
)

func nowMs() int64 { return time.Now().UnixMilli() }

func nextSeq() int64 { return nowMs() }

// sendHello sends a hello message on the connection.
func sendHello(c *transport.Conn, deviceID, displayID, name string) error {
	return c.Send(event.Message{
		V: 1, Seq: nextSeq(), Type: event.TypeHello, Ts: nowMs(),
		Payload: event.HelloPayload{
			DeviceID:           deviceID,
			DisplayID:          displayID,
			Name:               name,
			ProtocolVersion:    1,
			SupportsRemembered: true,
		},
	})
}

// recvHello reads and decodes a hello message.
func recvHello(c *transport.Conn) (event.HelloPayload, error) {
	msg, err := c.Recv()
	if err != nil {
		return event.HelloPayload{}, err
	}
	if msg.Type != event.TypeHello {
		return event.HelloPayload{}, fmt.Errorf("p2p: expected hello, got %s", msg.Type)
	}
	var pay event.HelloPayload
	if err := event.DecodePayload(msg, &pay); err != nil {
		return event.HelloPayload{}, fmt.Errorf("p2p: decode hello: %w", err)
	}
	return pay, nil
}

// newConnectionID generates a random local connection identifier.
func newConnectionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// randomHex returns n random bytes encoded as a lowercase hex string (2n chars).
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

func computeRememberedProof(pairSecret, clientDeviceID, serverDeviceID, secretID, nonce string) string {
	mac := hmac.New(sha256.New, []byte(pairSecret))
	mac.Write([]byte("mousebridge-remembered-proof"))
	mac.Write([]byte{0})
	mac.Write([]byte(clientDeviceID))
	mac.Write([]byte{0})
	mac.Write([]byte(serverDeviceID))
	mac.Write([]byte{0})
	mac.Write([]byte(secretID))
	mac.Write([]byte{0})
	mac.Write([]byte(nonce))
	return hex.EncodeToString(mac.Sum(nil))
}
