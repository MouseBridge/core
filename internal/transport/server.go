package transport

import (
	"net"

	log "github.com/sirupsen/logrus"
)

// ConnHandler is called for each accepted P2P connection.
type ConnHandler func(c *Conn)

// HTTPHandler is called for each accepted HTTP connection (raw net.Conn with bytes prepended).
type HTTPHandler func(c net.Conn)

// Serve accepts connections from ln and dispatches each one:
// P2P connections (starting with P2PHandshakeLine) go to connHandler,
// everything else goes to httpHandler.
// Blocks until ln is closed.
func Serve(ln net.Listener, connHandler ConnHandler, httpHandler HTTPHandler) {
	for {
		raw, err := ln.Accept()
		if err != nil {
			log.Printf("transport: accept: %v", err)
			return
		}
		go dispatch(raw, connHandler, httpHandler)
	}
}

func dispatch(raw net.Conn, connHandler ConnHandler, httpHandler HTTPHandler) {
	line, multi, err := peekLine(raw)
	if err != nil {
		log.Printf("transport: peek: %v", err)
		_ = raw.Close()
		return
	}
	if line == P2PHandshakeLine {
		connHandler(NewConn(multi))
	} else {
		httpHandler(multi)
	}
}
