package net

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

const dialTimeout = 10 * time.Second

// Dial connects to ip:port and returns a Conn.
func Dial(ip string, port int) (*Conn, error) {
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	raw, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("net: dial %s: %w", addr, err)
	}
	return NewConn(raw), nil
}
