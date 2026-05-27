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
	d, err := daemon.New(daemon.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
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
	if _, ok := result["daemon"]; !ok {
		t.Fatal("missing 'daemon' field in status response")
	}
	if _, ok := result["sessions"]; !ok {
		t.Fatal("missing 'sessions' field in status response")
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

func TestSSEReceivesInitialStatus(t *testing.T) {
	_, addr := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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
				return
			}
		}
	}()

	select {
	case line := <-lines:
		jsonPart := strings.TrimPrefix(strings.TrimPrefix(line, "data: "), "data:")
		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(jsonPart), &ev); err != nil {
			t.Fatalf("parse SSE JSON %q: %v", line, err)
		}
		// Initial event should be the full status snapshot.
		if _, ok := ev["daemon"]; !ok {
			t.Fatalf("initial SSE event missing 'daemon' field, got: %v", ev)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for initial SSE status event")
	}
}
