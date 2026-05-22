package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
)

// DialSocket connects to a daemon Unix Socket and returns the connection.
func DialSocket(socketPath string) (net.Conn, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to daemon at %s — is it running? (mousebridge daemon --socket %s)", socketPath, socketPath)
	}
	return conn, nil
}

// SendCommand sends one Command to the daemon.
func SendCommand(conn net.Conn, cmd Command) error {
	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

// ReadEvents reads Event lines from conn and calls handler for each.
// Returns when conn closes or handler returns false.
func ReadEvents(conn net.Conn, handler func(Event) bool) error {
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if !handler(ev) {
			return nil
		}
	}
	return scanner.Err()
}
