package transport

import (
	"io"
	"net"
)

const P2PHandshakeLine = "MOUSEBRIDGE/1.0\n"

// peekLine reads bytes from c until '\n' (or 256 bytes), then returns:
//   - the line as a string (including the trailing '\n')
//   - a net.Conn whose read stream starts from the beginning (bytes put back)
func peekLine(c net.Conn) (string, net.Conn, error) {
	buf := make([]byte, 0, 32)
	tmp := make([]byte, 1)
	for {
		_, err := c.Read(tmp)
		if err != nil {
			return "", nil, err
		}
		buf = append(buf, tmp[0])
		if tmp[0] == '\n' || len(buf) >= 256 {
			break
		}
	}
	multi := &prependConn{
		Reader: io.MultiReader(&bytesOnce{data: append([]byte(nil), buf...)}, c),
		Conn:   c,
	}
	return string(buf), multi, nil
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
