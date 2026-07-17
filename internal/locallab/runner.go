package locallab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Status struct {
	Available             bool   `json:"available"`
	ScriptPath            string `json:"script_path,omitempty"`
	Running               bool   `json:"running"`
	LastStartedAt         int64  `json:"last_started_at,omitempty"`
	LastFinishedAt        int64  `json:"last_finished_at,omitempty"`
	LastExitCode          int    `json:"last_exit_code"`
	LastError             string `json:"last_error,omitempty"`
	SummaryPath           string `json:"summary_path,omitempty"`
	LogPath               string `json:"log_path,omitempty"`
	SummaryExcerpt        string `json:"summary_excerpt,omitempty"`
	LogExcerpt            string `json:"log_excerpt,omitempty"`
	ControllerURL         string `json:"controller_url,omitempty"`
	ReceiverURL           string `json:"receiver_url,omitempty"`
	RemoteSessionDeviceID string `json:"remote_session_device_id,omitempty"`
	State                 string `json:"state,omitempty"`
}

type Runner struct {
	dataDir string

	mu             sync.Mutex
	running        bool
	cancel         context.CancelFunc
	cmd            *exec.Cmd
	lastStartedAt  time.Time
	lastFinishedAt time.Time
	lastExitCode   int
	lastError      string
	summaryPath    string
	logPath        string
}

func NewRunner(dataDir string) *Runner {
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = filepath.Clean(abs)
	}
	return &Runner{
		dataDir:      dataDir,
		lastExitCode: -1,
	}
}

func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()

	scriptPath, ok := r.scriptPath()
	status := Status{
		Available:    ok,
		ScriptPath:   scriptPath,
		Running:      r.running,
		LastExitCode: r.lastExitCode,
		LastError:    r.lastError,
		SummaryPath:  r.summaryPath,
		LogPath:      r.logPath,
	}
	if !r.lastStartedAt.IsZero() {
		status.LastStartedAt = r.lastStartedAt.Unix()
	}
	if !r.lastFinishedAt.IsZero() {
		status.LastFinishedAt = r.lastFinishedAt.Unix()
	}
	status.SummaryExcerpt = readTail(r.summaryPath, 120)
	status.LogExcerpt = readTail(r.logPath, 120)
	if fields := readSummaryFields(r.summaryPath); len(fields) > 0 {
		status.ControllerURL = fields["controller_url"]
		status.ReceiverURL = fields["receiver_url"]
		status.RemoteSessionDeviceID = fields["remote_session_device_id"]
		status.State = fields["state"]
	}
	return status
}

func (r *Runner) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.running {
		return fmt.Errorf("local lab is already running")
	}
	scriptPath, ok := r.scriptPath()
	if !ok {
		return fmt.Errorf("local lab script is not available")
	}

	labDir := filepath.Join(r.dataDir, "local-lab")
	if err := os.MkdirAll(labDir, 0755); err != nil {
		return fmt.Errorf("create local lab dir: %w", err)
	}
	summaryPath := filepath.Join(labDir, "local-lab-summary.log")
	logPath := filepath.Join(labDir, "local-lab-run.log")
	if err := os.Remove(summaryPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove local lab summary: %w", err)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create local lab log: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, scriptPath)
	cmd.Dir = filepath.Dir(filepath.Dir(scriptPath))
	cmd.Env = append(
		os.Environ(),
		"MB_LOCAL_LAB_SUMMARY_PATH="+summaryPath,
		"MB_LOCAL_LAB_BASE_DIR="+labDir,
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	r.running = true
	r.cancel = cancel
	r.cmd = cmd
	r.lastStartedAt = time.Now()
	r.lastFinishedAt = time.Time{}
	r.lastExitCode = -1
	r.lastError = ""
	r.summaryPath = summaryPath
	r.logPath = logPath

	if err := cmd.Start(); err != nil {
		r.running = false
		r.cancel = nil
		r.cmd = nil
		r.lastFinishedAt = time.Now()
		r.lastError = err.Error()
		cancel()
		_ = logFile.Close()
		return fmt.Errorf("start local lab: %w", err)
	}
	go r.wait(cmd, logFile)
	return nil
}

func (r *Runner) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.running || r.cancel == nil {
		return fmt.Errorf("local lab is not running")
	}
	cmd := r.cmd
	cancel := r.cancel
	if cmd != nil && cmd.Process != nil {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err == nil {
			go func(cmd *exec.Cmd, cancel context.CancelFunc) {
				time.Sleep(2 * time.Second)
				r.mu.Lock()
				stillRunning := r.running && r.cmd == cmd
				r.mu.Unlock()
				if stillRunning {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					cancel()
				}
			}(cmd, cancel)
			return nil
		}
	}
	cancel()
	return nil
}

func (r *Runner) wait(cmd *exec.Cmd, logFile *os.File) {
	err := cmd.Wait()
	_ = logFile.Close()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = false
	r.cancel = nil
	r.cmd = nil
	r.lastFinishedAt = time.Now()

	if err == nil {
		r.lastExitCode = 0
		r.lastError = ""
		return
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		r.lastExitCode = exitErr.ExitCode()
	} else {
		r.lastExitCode = -1
	}
	r.lastError = err.Error()
}

func (r *Runner) scriptPath() (string, bool) {
	candidates := []string{}
	if env := strings.TrimSpace(os.Getenv("MB_CORE_REPO_DIR")); env != "" {
		candidates = append(candidates, filepath.Join(env, "verify", "local-lab.sh"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "verify", "local-lab.sh"))
	}
	exe, err := os.Executable()
	if err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "verify", "local-lab.sh"))
	}
	if parent := filepath.Dir(r.dataDir); parent != "" {
		candidates = append(candidates, filepath.Join(parent, "core", "verify", "local-lab.sh"))
	}

	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err == nil {
			candidate = filepath.Clean(abs)
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

func readTail(path string, maxLines int) string {
	if path == "" || maxLines <= 0 {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func readSummaryFields(path string) map[string]string {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return fields
}
