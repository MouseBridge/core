package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"mousebridge/internal/daemon"
	"mousebridge/internal/httpapi"
)

func newTestDaemon(t *testing.T, port int) *daemon.Daemon {
	t.Helper()
	d := daemon.New(daemon.Options{
		SocketPath: t.TempDir() + "/mb.sock",
		TCPPort:    port,
	})
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	return d
}

func TestServerStartStop(t *testing.T) {
	d := newTestDaemon(t, 0)
	srv := httpapi.New(d, "127.0.0.1", 39290)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	resp, err := http.Get("http://127.0.0.1:39290/api/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestServerStopIsIdempotent(t *testing.T) {
	d := newTestDaemon(t, 0)
	srv := httpapi.New(d, "127.0.0.1", 39291)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	srv.Stop()
	srv.Stop() // must not panic
	time.Sleep(10 * time.Millisecond)
}

func TestRESTServe(t *testing.T) {
	d := newTestDaemon(t, 39292)
	srv := httpapi.New(d, "127.0.0.1", 39293)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	resp, err := http.Post("http://127.0.0.1:39293/api/serve", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestRESTConnect(t *testing.T) {
	d := newTestDaemon(t, 0)
	srv := httpapi.New(d, "127.0.0.1", 39294)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	body := strings.NewReader(`{"ip":"127.0.0.1","port":39999}`)
	resp, err := http.Post("http://127.0.0.1:39294/api/connect", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestRESTStatus(t *testing.T) {
	d := newTestDaemon(t, 0)
	srv := httpapi.New(d, "127.0.0.1", 39295)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	resp, err := http.Get("http://127.0.0.1:39295/api/status")
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
		t.Fatal("response missing 'devices' key")
	}
}

func TestSSEReceivesEvents(t *testing.T) {
	d := newTestDaemon(t, 0)
	srv := httpapi.New(d, "127.0.0.1", 39296)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SSE connects and blocks — run in goroutine, send lines to channel
	lines := make(chan string, 10)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:39296/api/events", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				lines <- line
			}
		}
	}()

	// wait for SSE connection to be established
	time.Sleep(50 * time.Millisecond)

	// trigger a status event via separate transport (no shared connection pool)
	statusClient := &http.Client{Transport: &http.Transport{}}
	statusResp, err := statusClient.Get("http://127.0.0.1:39296/api/status")
	if err != nil {
		t.Fatal(err)
	}
	statusResp.Body.Close()

	select {
	case line := <-lines:
		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("failed to parse SSE JSON: %v", err)
		}
		if ev["event"] == nil {
			t.Fatal("missing 'event' field")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE event")
	}
}

func TestCORSHeaders(t *testing.T) {
	d := newTestDaemon(t, 0)
	srv := httpapi.New(d, "127.0.0.1", 39297)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	req, _ := http.NewRequest(http.MethodOptions, "http://127.0.0.1:39297/api/status", nil)
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
