package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mousebridge/core/internal/api"
	"github.com/mousebridge/core/internal/daemon"
)

func newTestServer(t *testing.T) (*api.Server, string) {
	t.Helper()
	d := daemon.New(daemon.Options{
		SocketPath: t.TempDir() + "/mb.sock",
		TCPPort:    0,
	})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	srv := api.New(d)
	srv.Start(ln)
	t.Cleanup(srv.Stop)
	return srv, addr
}

func TestStatus(t *testing.T) {
	_, addr := newTestServer(t)
	resp, err := http.Get("http://" + addr + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if _, ok := result["devices"]; !ok {
		t.Fatal("missing 'devices'")
	}
	if _, ok := result["serving"]; !ok {
		t.Fatal("missing 'serving'")
	}
}

func TestCORSHeaders(t *testing.T) {
	_, addr := newTestServer(t)
	req, _ := http.NewRequest(http.MethodOptions, "http://"+addr+"/api/status", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Origin") == "" {
		t.Fatal("missing CORS header")
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS want 204, got %d", resp.StatusCode)
	}
}

func TestSSEReceivesEvents(t *testing.T) {
	_, addr := newTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lines := make(chan string, 10)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/events", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data:") {
				lines <- line
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	http.Post("http://"+addr+"/api/serve", "application/json", strings.NewReader(`{"port":0}`))
	defer http.Post("http://"+addr+"/api/stop-serve", "application/json", nil) //nolint

	select {
	case line := <-lines:
		// Gin SSEvent format: "data:{...}" or "data: {...}"
		jsonPart := strings.TrimPrefix(strings.TrimPrefix(line, "data: "), "data:")
		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(jsonPart), &ev); err != nil {
			t.Fatalf("parse SSE JSON %q: %v", line, err)
		}
		if ev["event"] == nil {
			t.Fatal("missing 'event' field")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE event")
	}
}

func TestGetShortcuts(t *testing.T) {
	_, addr := newTestServer(t)
	resp, err := http.Get("http://" + addr + "/api/shortcuts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if _, ok := result["switch_next"]; !ok {
		t.Fatal("missing 'switch_next'")
	}
}
