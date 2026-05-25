package daemon_test

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mousebridge/core/internal/daemon"
)

func TestDaemonStartStop(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "mb.sock")
	d := daemon.New(daemon.Options{
		SocketPath: sockPath,
		TCPPort:    0,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket not created: %v", err)
	}
}

func TestDaemonBroadcastsStatusEvent(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "mb.sock")
	d := daemon.New(daemon.Options{
		SocketPath: sockPath,
		TCPPort:    0,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	cmd := daemon.Command{Cmd: "status"}
	data, _ := json.Marshal(cmd)
	if _, err := conn.Write(append(data, '\n')); err != nil {
		t.Fatalf("write cmd: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatalf("no event received: %v", scanner.Err())
	}
	var ev daemon.Event
	if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if ev.Event != "status" {
		t.Fatalf("want status event got %q", ev.Event)
	}
}
