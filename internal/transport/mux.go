package transport

import (
	"io"
	"net"
	"time"
)

const P2PHandshakeLine = "MOUSEBRIDGE/1.0\n"

// peekLine reads bytes from c until '\n' (or maxPeek bytes), then returns:
//   - the line as a string (including the trailing '\n' if present)
//   - a net.Conn whose read stream starts from the beginning (bytes put back)
//
// A 3-second deadline is applied during the peek to avoid hanging on idle conns.
func peekLine(c net.Conn) (string, net.Conn, error) {
	const maxPeek = 512
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer c.SetReadDeadline(time.Time{}) //nolint:errcheck

	buf := make([]byte, 0, 32)
	tmp := make([]byte, 1)
	for {
		_, err := c.Read(tmp)
		if err != nil {
			return "", nil, err
		}
		buf = append(buf, tmp[0])
		if tmp[0] == '\n' || len(buf) >= maxPeek {
			break
		}
	}
	multi := &prependConn{
		Reader: io.MultiReader(&bytesOnce{data: append([]byte(nil), buf...)}, c),
		Conn:   c,
	}
	return string(buf), multi, nil
}

// isHTTPPrefix returns true if b starts with a known HTTP method token.
func isHTTPPrefix(b []byte) bool {
	methods := []string{
		"GET ",
		"POST ",
		"PUT ",
		"PATCH ",
		"DELETE ",
		"OPTIONS ",
		"HEAD ",
	}
	for _, m := range methods {
		if len(b) >= len(m) && string(b[:len(m)]) == m {
			return true
		}
	}
	return false
}

// prependConn is a net.Conn whose Read is served from a prepended reader first.
type prependConn struct {
	io.Reader
	net.Conn
}

func (p *prependConn) Read(b []byte) (int, error) {
	return p.Reader.Read(b)
}

type bytesOnce struct {
	data []byte
	pos  int
}

func (r *bytesOnce) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
