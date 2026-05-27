package validate

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
)

// ConnectRequest validates the POST /api/connect body fields.
func ConnectRequest(host string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port: %d", port)
	}
	if host == "" {
		return fmt.Errorf("host is required")
	}
	// Reject scheme
	if strings.Contains(host, "://") {
		return fmt.Errorf("host must not contain scheme (e.g. http://)")
	}
	// Reject path
	if strings.Contains(host, "/") {
		return fmt.Errorf("host must not contain path")
	}
	// Reject userinfo
	if strings.Contains(host, "@") {
		return fmt.Errorf("host must not contain userinfo")
	}
	// Try parse as URL to catch other issues
	u, err := url.Parse("tcp://" + host)
	if err != nil {
		return fmt.Errorf("invalid host: %w", err)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("invalid host: empty hostname")
	}
	return nil
}

// SafeString checks that s contains no NUL, CR, LF, or ANSI escape sequences.
func SafeString(s string) error {
	for i, r := range s {
		if r == 0 || r == '\r' || r == '\n' {
			return fmt.Errorf("invalid character at position %d", i)
		}
		if r == 0x1b { // ESC
			return fmt.Errorf("ANSI escape sequence not allowed")
		}
		if r != '\t' && unicode.IsControl(r) {
			return fmt.Errorf("control character at position %d", i)
		}
	}
	return nil
}

// DeviceID validates a 32-hex-char device ID.
func DeviceID(id string) error {
	if len(id) != 32 {
		return fmt.Errorf("device_id must be 32 hex characters, got %d", len(id))
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("device_id contains invalid character: %c", c)
		}
	}
	return nil
}

// IsLoopback returns true if addr is a loopback address.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost"
}
