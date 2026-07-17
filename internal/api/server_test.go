package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	if _, ok := result["helper_runtime"]; !ok {
		t.Fatal("missing 'helper_runtime' field in status response")
	}
	if _, ok := result["active_target_device_id"]; !ok {
		t.Fatal("missing 'active_target_device_id' field in status response")
	}
	if _, ok := result["controlling_remote"]; !ok {
		t.Fatal("missing 'controlling_remote' field in status response")
	}
	if _, ok := result["paused"]; !ok {
		t.Fatal("missing 'paused' field in status response")
	}
	if _, ok := result["capture_enabled"]; !ok {
		t.Fatal("missing 'capture_enabled' field in status response")
	}
}

func TestLocalHelperStatus(t *testing.T) {
	_, _, addr := newTestServer(t)
	resp, err := http.Get("http://" + addr + "/api/local/helper")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result["recommended_action"]; !ok {
		t.Fatal("missing recommended_action in helper status response")
	}
}

func TestLocalValidationEndpoints(t *testing.T) {
	repoDir := writeValidationScript(t, "#!/bin/sh\nset -eu\nSUMMARY=\"${MB_VALIDATION_SUMMARY_PATH:?}\"\necho \"run:$1:$2:$3:$4:$5\"\necho \"validation ok\" > \"$SUMMARY\"\nprintf 'validation finished\\n'\n")
	t.Setenv("MB_CORE_REPO_DIR", repoDir)

	_, _, addr := newTestServer(t)

	resp, err := http.Get("http://" + addr + "/api/local/validation")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var initial map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&initial); err != nil {
		t.Fatal(err)
	}
	if initial["available"] != true {
		t.Fatalf("available=%v want true", initial["available"])
	}

	body := bytes.NewBufferString(`{"text":"suite smoke","burst_count":42,"burst_batch_size":7,"latency_samples":3,"latency_interval":0.2}`)
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/local/validation/run", body)
	req.Header.Set("Content-Type", "application/json")
	runResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer runResp.Body.Close()
	if runResp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", runResp.StatusCode)
	}

	status := waitForValidationDone(t, "http://"+addr+"/api/local/validation")
	if exitCode, ok := status["last_exit_code"].(float64); !ok || exitCode != 0 {
		t.Fatalf("last_exit_code=%v want 0", status["last_exit_code"])
	}
	if !strings.Contains(statusString(status, "summary_excerpt"), "validation ok") {
		t.Fatalf("summary_excerpt=%q missing validation ok", statusString(status, "summary_excerpt"))
	}
	if !strings.Contains(statusString(status, "log_excerpt"), "validation finished") {
		t.Fatalf("log_excerpt=%q missing validation finished", statusString(status, "log_excerpt"))
	}
	lastRequest := status["last_request"].(map[string]interface{})
	if lastRequest["text"] != "suite smoke" {
		t.Fatalf("text=%v want suite smoke", lastRequest["text"])
	}
	if lastRequest["burst_count"] != float64(42) {
		t.Fatalf("burst_count=%v want 42", lastRequest["burst_count"])
	}
}

func TestLocalLabEndpoints(t *testing.T) {
	repoDir := writeScript(t, "local-lab.sh", "#!/bin/sh\nset -eu\nSUMMARY=\"${MB_LOCAL_LAB_SUMMARY_PATH:?}\"\necho 'state=ready' > \"$SUMMARY\"\necho 'controller_url=http://127.0.0.1:39273' >> \"$SUMMARY\"\necho 'receiver_url=http://127.0.0.1:39272' >> \"$SUMMARY\"\necho 'remote_session_device_id=abc123' >> \"$SUMMARY\"\nprintf 'local lab ready\\n'\nwhile :; do sleep 1; done\n")
	t.Setenv("MB_CORE_REPO_DIR", repoDir)

	_, _, addr := newTestServer(t)

	resp, err := http.Get("http://" + addr + "/api/local/lab")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	startReq, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/local/lab/start", nil)
	startResp, err := http.DefaultClient.Do(startReq)
	if err != nil {
		t.Fatal(err)
	}
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", startResp.StatusCode)
	}

	status := waitForLocalLabRunning(t, "http://"+addr+"/api/local/lab")
	if statusString(status, "state") != "ready" {
		t.Fatalf("state=%q want ready", statusString(status, "state"))
	}
	if statusString(status, "controller_url") != "http://127.0.0.1:39273" {
		t.Fatalf("controller_url=%q", statusString(status, "controller_url"))
	}
	if statusString(status, "remote_session_device_id") != "abc123" {
		t.Fatalf("remote_session_device_id=%q", statusString(status, "remote_session_device_id"))
	}

	stopReq, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/local/lab/stop", nil)
	stopResp, err := http.DefaultClient.Do(stopReq)
	if err != nil {
		t.Fatal(err)
	}
	defer stopResp.Body.Close()
	if stopResp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", stopResp.StatusCode)
	}

	status = waitForLocalLabStopped(t, "http://"+addr+"/api/local/lab")
	if running, _ := status["running"].(bool); running {
		t.Fatalf("running=%v want false", status["running"])
	}
}

func TestLocalValidationStopEndpoint(t *testing.T) {
	repoDir := writeValidationScript(t, "#!/bin/sh\nset -eu\nSUMMARY=\"${MB_VALIDATION_SUMMARY_PATH:?}\"\ntrap 'echo \"stopped\" > \"$SUMMARY\"; exit 130' TERM INT\nprintf 'validation waiting\\n'\nwhile :; do sleep 1; done\n")
	t.Setenv("MB_CORE_REPO_DIR", repoDir)

	_, _, addr := newTestServer(t)

	runReq, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/local/validation/run", bytes.NewBufferString(`{}`))
	runReq.Header.Set("Content-Type", "application/json")
	runResp, err := http.DefaultClient.Do(runReq)
	if err != nil {
		t.Fatal(err)
	}
	defer runResp.Body.Close()
	if runResp.StatusCode != http.StatusAccepted {
		t.Fatalf("want 202, got %d", runResp.StatusCode)
	}

	waitForValidationRunning(t, "http://"+addr+"/api/local/validation")

	stopReq, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/local/validation/stop", nil)
	stopResp, err := http.DefaultClient.Do(stopReq)
	if err != nil {
		t.Fatal(err)
	}
	defer stopResp.Body.Close()
	if stopResp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", stopResp.StatusCode)
	}

	status := waitForValidationDone(t, "http://"+addr+"/api/local/validation")
	if status["running"] != false {
		t.Fatalf("running=%v want false", status["running"])
	}
	if status["last_finished_at"] == nil {
		t.Fatal("last_finished_at should be set after stop")
	}
	if status["last_error"] == nil {
		t.Fatal("last_error should be populated after forced stop")
	}
}

func TestLocalHelperInstall(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "helper-args.log")
	helperPath := filepath.Join(dir, "mousebridge-helper")
	script := "#!/bin/sh\nprintf '%s\n' \"$@\" >> " + logPath + "\n"
	if err := os.WriteFile(helperPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MB_HELPER_PROGRAM", helperPath)

	_, _, addr := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/local/helper/install", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	args := string(data)
	if !strings.Contains(args, "install-launch-agent") {
		t.Fatalf("helper args %q missing install-launch-agent", args)
	}
	if !strings.Contains(args, "--data-dir") {
		t.Fatalf("helper args %q missing --data-dir", args)
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

func writeValidationScript(t *testing.T, content string) string {
	t.Helper()
	return writeScript(t, "local-validation-suite.sh", content)
}

func writeScript(t *testing.T, name, content string) string {
	t.Helper()
	repoDir := t.TempDir()
	verifyDir := filepath.Join(repoDir, "verify")
	if err := os.MkdirAll(verifyDir, 0755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(verifyDir, name)
	if err := os.WriteFile(scriptPath, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return repoDir
}

func waitForValidationRunning(t *testing.T, url string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := getValidationStatus(t, url)
		if running, _ := status["running"].(bool); running {
			return status
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for validation runner to start")
	return nil
}

func waitForValidationDone(t *testing.T, url string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := getValidationStatus(t, url)
		if running, _ := status["running"].(bool); !running && status["last_finished_at"] != nil {
			return status
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for validation runner to finish")
	return nil
}

func waitForLocalLabRunning(t *testing.T, url string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := getValidationStatus(t, url)
		if running, _ := status["running"].(bool); running && statusString(status, "state") == "ready" {
			return status
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for local lab to become ready")
	return nil
}

func waitForLocalLabStopped(t *testing.T, url string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := getValidationStatus(t, url)
		if running, _ := status["running"].(bool); !running && status["last_finished_at"] != nil {
			return status
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for local lab to stop")
	return nil
}

func getValidationStatus(t *testing.T, url string) map[string]interface{} {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func statusString(status map[string]interface{}, key string) string {
	value, _ := status[key].(string)
	return value
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

func TestControlTogglePause(t *testing.T) {
	_, d, addr := newTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/control/toggle-pause", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if !d.Paused() {
		t.Fatal("daemon should be paused after control toggle")
	}
}

func TestControlSwitchToHost(t *testing.T) {
	_, d, addr := newTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/control/switch-to-host", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if got := d.State().ActiveTargetID; got != d.State().Daemon.DeviceID {
		t.Fatalf("active_target_device_id=%q want local %q", got, d.State().Daemon.DeviceID)
	}
}

func TestControlSwitchNext(t *testing.T) {
	_, d, addr := newTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/control/switch-next", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if got := d.State().ActiveTargetID; got != d.State().Daemon.DeviceID {
		t.Fatalf("active_target_device_id=%q want local %q with no sessions", got, d.State().Daemon.DeviceID)
	}
}

func TestControlSwitchPrev(t *testing.T) {
	_, d, addr := newTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/control/switch-prev", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if got := d.State().ActiveTargetID; got != d.State().Daemon.DeviceID {
		t.Fatalf("active_target_device_id=%q want local %q with no sessions", got, d.State().Daemon.DeviceID)
	}
}

func TestControlCapturePut(t *testing.T) {
	_, d, addr := newTestServer(t)

	body := bytes.NewBufferString(`{"enabled":false}`)
	req, _ := http.NewRequest(http.MethodPut, "http://"+addr+"/api/control/capture", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if d.CaptureEnabled() {
		t.Fatal("capture should be disabled after control request")
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

func TestHelperInputEndpointAbsoluteMove(t *testing.T) {
	_, _, addr := newTestServer(t)

	body := bytes.NewBufferString(`{"kind":"mouse_move_abs","x":128,"y":96}`)
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

func TestHelperInputEndpointAbsoluteMoveRequiresCoordinates(t *testing.T) {
	_, _, addr := newTestServer(t)

	body := bytes.NewBufferString(`{"kind":"mouse_move_abs","x":128}`)
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/helper/input", body)
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

func TestHelperInputBatchEndpoint(t *testing.T) {
	_, _, addr := newTestServer(t)

	body := bytes.NewBufferString(`{"inputs":[{"kind":"mouse_move","dx":12,"dy":-6},{"kind":"text","text":"hello"}],"step_delay_ms":0}`)
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/helper/input/batch", body)
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

func TestSessionInputBatchEndpointWithoutSession(t *testing.T) {
	_, _, addr := newTestServer(t)

	body := bytes.NewBufferString(`{"device_id":"abc","inputs":[{"kind":"text","text":"hello"}],"step_delay_ms":0}`)
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/session/input/batch", body)
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
