package api_test

import (
	"bufio"
	"bytes"
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

func newTestServer(t *testing.T) (*api.Server, *daemon.Daemon, string) {
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
	return srv, d, addr
}

func TestStatus(t *testing.T) {
	_, _, addr := newTestServer(t)
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
	_, _, addr := newTestServer(t)
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
	_, _, addr := newTestServer(t)

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

func TestGetShortcuts(t *testing.T) {
	_, d, addr := newTestServer(t)

	resp, err := http.Get("http://" + addr + "/api/shortcuts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var hotkeys map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&hotkeys); err != nil {
		t.Fatal(err)
	}
	if hotkeys["switch_next"] != d.Config().Hotkeys.SwitchNext {
		t.Fatalf("switch_next=%q want %q", hotkeys["switch_next"], d.Config().Hotkeys.SwitchNext)
	}
}

func TestPutShortcuts(t *testing.T) {
	_, d, addr := newTestServer(t)

	body := bytes.NewBufferString(`{"switch_next":"ctrl+1","switch_prev":"ctrl+2","switch_to_host":"ctrl+3","disconnect_all":"ctrl+4","toggle_pause":"ctrl+5"}`)
	req, _ := http.NewRequest(http.MethodPut, "http://"+addr+"/api/shortcuts", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if d.Config().Hotkeys.TogglePause != "ctrl+5" {
		t.Fatalf("toggle_pause=%q want ctrl+5", d.Config().Hotkeys.TogglePause)
	}
}

func TestHelperInputEndpoint(t *testing.T) {
	_, _, addr := newTestServer(t)

	body := bytes.NewBufferString(`{"kind":"mouse_move","dx":12,"dy":-6}`)
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/helper/input", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestSessionInputEndpointWithoutSession(t *testing.T) {
	_, _, addr := newTestServer(t)

	body := bytes.NewBufferString(`{"kind":"mouse_move","dx":12,"dy":-6}`)
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/session/input", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestSessionDeleteWithoutSession(t *testing.T) {
	_, _, addr := newTestServer(t)

	req, _ := http.NewRequest(http.MethodDelete, "http://"+addr+"/api/sessions/1234567890abcdef1234567890abcdef", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}
