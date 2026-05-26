package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"

	log "github.com/sirupsen/logrus"
)

// ipcClient represents one connected CLI/UI client on the Unix Socket.
type ipcClient struct {
	conn   net.Conn
	writer *bufio.Writer
	mu     sync.Mutex
}

func newIPCClient(conn net.Conn) *ipcClient {
	return &ipcClient{conn: conn, writer: bufio.NewWriter(conn)}
}

// send writes an Event as a JSON Line to the client.
func (c *ipcClient) send(ev Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("ipc: marshal event: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.writer.Write(append(data, '\n')); err != nil {
		return err
	}
	return c.writer.Flush()
}

// readCommands reads JSON Lines from the client and calls handler for each.
func (c *ipcClient) readCommands(handler func(Command)) {
	scanner := bufio.NewScanner(c.conn)
	for scanner.Scan() {
		var cmd Command
		if err := json.Unmarshal(scanner.Bytes(), &cmd); err != nil {
			log.Printf("ipc: decode command: %v", err)
			continue
		}
		handler(cmd)
	}
}

func (c *ipcClient) close() {
	_ = c.conn.Close()
}
