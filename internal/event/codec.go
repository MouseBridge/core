package event

import (
	"encoding/json"
	"fmt"
)

// Encode serialises msg to a JSON Line (ending with \n).
func Encode(msg Message) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("event: encode: %w", err)
	}
	return append(data, '\n'), nil
}

// Decode parses a JSON Line into a Message.
// The Payload field will be a map[string]interface{}; callers use DecodePayload.
func Decode(line []byte) (Message, error) {
	var msg Message
	if err := json.Unmarshal(line, &msg); err != nil {
		return msg, fmt.Errorf("event: decode: %w", err)
	}
	return msg, nil
}

// DecodePayload re-decodes msg.Payload into dst (a pointer to a payload struct).
func DecodePayload(msg Message, dst interface{}) error {
	raw, err := json.Marshal(msg.Payload)
	if err != nil {
		return fmt.Errorf("event: re-marshal payload: %w", err)
	}
	return json.Unmarshal(raw, dst)
}
