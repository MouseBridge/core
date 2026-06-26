package localvalidate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultText            = "MouseBridge local suite"
	defaultBurstCount      = 240
	defaultBurstBatchSize  = 40
	defaultLatencySamples  = 12
	defaultLatencyInterval = 0.1
)

type RunRequest struct {
	Text            string  `json:"text"`
	BurstCount      int     `json:"burst_count"`
	BurstBatchSize  int     `json:"burst_batch_size"`
	LatencySamples  int     `json:"latency_samples"`
	LatencyInterval float64 `json:"latency_interval"`
}

type Status struct {
	Available      bool       `json:"available"`
	ScriptPath     string     `json:"script_path,omitempty"`
	Running        bool       `json:"running"`
	LastStartedAt  int64      `json:"last_started_at,omitempty"`
	LastFinishedAt int64      `json:"last_finished_at,omitempty"`
	LastExitCode   int        `json:"last_exit_code"`
	LastError      string     `json:"last_error,omitempty"`
	SummaryPath    string     `json:"summary_path,omitempty"`
	LogPath        string     `json:"log_path,omitempty"`
	SummaryExcerpt string     `json:"summary_excerpt,omitempty"`
	LogExcerpt     string     `json:"log_excerpt,omitempty"`
	LastRequest    RunRequest `json:"last_request"`
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
	lastRequest    RunRequest
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
		LastRequest:  r.lastRequest,
	}
	if !r.lastStartedAt.IsZero() {
		status.LastStartedAt = r.lastStartedAt.Unix()
	}
	if !r.lastFinishedAt.IsZero() {
		status.LastFinishedAt = r.lastFinishedAt.Unix()
	}
	status.SummaryExcerpt = readTail(r.summaryPath, 120)
	status.LogExcerpt = readTail(r.logPath, 120)
	return status
}

func (r *Runner) Start(req RunRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.running {
		return fmt.Errorf("local validation is already running")
	}
	scriptPath, ok := r.scriptPath()
	if !ok {
		return fmt.Errorf("local validation suite script is not available")
	}

	req = normalizeRequest(req)
	validationDir := filepath.Join(r.dataDir, "validation")
	if err := os.MkdirAll(validationDir, 0755); err != nil {
		return fmt.Errorf("create validation dir: %w", err)
	}
	summaryPath := filepath.Join(validationDir, "local-validation-summary.log")
	logPath := filepath.Join(validationDir, "local-validation-run.log")
	if err := os.Remove(summaryPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove summary log: %w", err)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create run log: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(
		ctx,
		scriptPath,
		req.Text,
		strconv.Itoa(req.BurstCount),
		strconv.Itoa(req.BurstBatchSize),
		strconv.Itoa(req.LatencySamples),
		formatInterval(req.LatencyInterval),
	)
	cmd.Dir = filepath.Dir(filepath.Dir(scriptPath))
	cmd.Env = append(os.Environ(), "MB_VALIDATION_SUMMARY_PATH="+summaryPath)
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
	r.lastRequest = req

	if err := cmd.Start(); err != nil {
		r.running = false
		r.cancel = nil
		r.cmd = nil
		r.lastFinishedAt = time.Now()
		r.lastError = err.Error()
		cancel()
		_ = logFile.Close()
		return fmt.Errorf("start validation suite: %w", err)
	}
	go r.wait(cmd, logFile)
	return nil
}

func (r *Runner) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running || r.cancel == nil {
		return fmt.Errorf("local validation is not running")
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
		if err := cmd.Process.Signal(syscall.SIGTERM); err == nil {
			go func(cmd *exec.Cmd, cancel context.CancelFunc) {
				time.Sleep(2 * time.Second)
				r.mu.Lock()
				stillRunning := r.running && r.cmd == cmd
				r.mu.Unlock()
				if stillRunning {
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
		candidates = append(candidates, filepath.Join(env, "verify", "local-validation-suite.sh"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "verify", "local-validation-suite.sh"))
	}
	exe, err := os.Executable()
	if err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "verify", "local-validation-suite.sh"))
	}
	if parent := filepath.Dir(r.dataDir); parent != "" {
		candidates = append(candidates, filepath.Join(parent, "core", "verify", "local-validation-suite.sh"))
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

func normalizeRequest(req RunRequest) RunRequest {
	if strings.TrimSpace(req.Text) == "" {
		req.Text = defaultText
	}
	if req.BurstCount <= 0 {
		req.BurstCount = defaultBurstCount
	}
	if req.BurstBatchSize <= 0 {
		req.BurstBatchSize = defaultBurstBatchSize
	}
	if req.LatencySamples <= 0 {
		req.LatencySamples = defaultLatencySamples
	}
	if req.LatencyInterval <= 0 {
		req.LatencyInterval = defaultLatencyInterval
	}
	return req
}

func formatInterval(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
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
