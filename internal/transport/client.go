package transport

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

const dialTimeout = 10 * time.Second

// Dial connects to ip:port, writes the P2P handshake line, and returns a Conn.
func Dial(ip string, port int) (*Conn, error) {
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	raw, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("transport: dial %s: %w", addr, err)
	}
	if _, err := fmt.Fprint(raw, P2PHandshakeLine); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("transport: write handshake line: %w", err)
	}
	return NewConn(raw), nil
}
