package net

import (
	"bufio"
	"fmt"
	"net"
	"sync"

	"mousebridge/internal/event"
)

// Conn wraps a net.Conn with JSON Lines framing.
type Conn struct {
	raw     net.Conn
	scanner *bufio.Scanner
	mu      sync.Mutex
}

// NewConn wraps an existing net.Conn.
func NewConn(raw net.Conn) *Conn {
	return &Conn{
		raw:     raw,
		scanner: bufio.NewScanner(raw),
	}
}

// Send serialises msg as a JSON Line and writes it.
func (c *Conn) Send(msg event.Message) error {
	data, err := event.Encode(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.raw.Write(data)
	return err
}

// Recv reads the next JSON Line and decodes it.
func (c *Conn) Recv() (event.Message, error) {
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return event.Message{}, fmt.Errorf("net: recv: %w", err)
		}
		return event.Message{}, fmt.Errorf("net: connection closed")
	}
	return event.Decode(c.scanner.Bytes())
}

// Close closes the underlying connection.
func (c *Conn) Close() error {
	return c.raw.Close()
}

// RemoteAddr returns the remote network address.
func (c *Conn) RemoteAddr() net.Addr {
	return c.raw.RemoteAddr()
}
