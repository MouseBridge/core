package localhelper

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	actionReady               = "ready"
	actionInstallHelperBinary = "install_helper_binary"
	actionInstallLaunchAgent  = "install_launch_agent"
	actionGrantAccessibility  = "grant_accessibility"
	actionRestartLaunchAgent  = "restart_launch_agent"
)

// Status is the daemon-visible readiness snapshot for the local helper runtime.
type Status struct {
	ExecutableFound      bool   `json:"executable_found"`
	ExecutablePath       string `json:"executable_path,omitempty"`
	LaunchAgentLabel     string `json:"launch_agent_label"`
	LaunchAgentPlist     string `json:"launch_agent_plist"`
	LaunchAgentInstalled bool   `json:"launch_agent_installed"`
	LaunchAgentLoaded    bool   `json:"launch_agent_loaded"`
	AccessibilityGranted bool   `json:"accessibility_granted"`
	Connected            bool   `json:"connected"`
	ClientCount          int    `json:"client_count"`
	LogsDir              string `json:"logs_dir"`
	RecommendedAction    string `json:"recommended_action"`
	LastError            string `json:"last_error,omitempty"`
}

// Runtime inspects and controls the local macOS helper process.
type Runtime struct {
	dataDir       string
	clientCountFn func() int
}

// NewRuntime creates a Runtime bound to one daemon data dir.
func NewRuntime(dataDir string, clientCountFn func() int) *Runtime {
	return &Runtime{
		dataDir:       dataDir,
		clientCountFn: clientCountFn,
	}
}

// Status returns the current readiness snapshot.
func (r *Runtime) Status() Status {
	label := defaultLaunchAgentLabel(r.dataDir)
	plistPath := launchAgentPlistPath(label)
	logsDir := filepath.Join(r.dataDir, "logs")

	status := Status{
		LaunchAgentLabel: label,
		LaunchAgentPlist: plistPath,
		LogsDir:          logsDir,
	}

	if r.clientCountFn != nil {
		status.ClientCount = r.clientCountFn()
		status.Connected = status.ClientCount > 0
	}

	program, found, lookupErr := lookupProgram()
	status.ExecutablePath = program
	status.ExecutableFound = found
	if lookupErr != nil {
		status.LastError = lookupErr.Error()
	}

	status.LaunchAgentInstalled = fileExists(plistPath)

	if status.ExecutableFound {
		granted, err := checkAccessibility(program)
		status.AccessibilityGranted = granted
		if err != nil && status.LastError == "" {
			status.LastError = err.Error()
		}
	}

	if status.LaunchAgentInstalled {
		loaded, err := checkLaunchAgentLoaded(label)
		status.LaunchAgentLoaded = loaded
		if err != nil && status.LastError == "" {
			status.LastError = err.Error()
		}
	}

	status.RecommendedAction = recommendedAction(status)
	return status
}

// Install installs or refreshes the helper LaunchAgent.
func (r *Runtime) Install() error {
	program, found, err := lookupProgram()
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("mousebridge-helper executable not found")
	}
	_, err = run(program, "install-launch-agent", "--data-dir", r.dataDir)
	return err
}

// Restart restarts the helper LaunchAgent. The helper installer is idempotent and
// already bootouts/bootstrap/kickstarts the job, so restart maps to install.
func (r *Runtime) Restart() error {
	return r.Install()
}

// OpenAccessibility opens the macOS Accessibility settings pane.
func (r *Runtime) OpenAccessibility() error {
	program, found, err := lookupProgram()
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("mousebridge-helper executable not found")
	}
	_, err = run(program, "open-accessibility")
	return err
}

func lookupProgram() (string, bool, error) {
	if configured := strings.TrimSpace(os.Getenv("MB_HELPER_PROGRAM")); configured != "" {
		resolved := configured
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Clean(configured)
		}
		if fileExists(resolved) {
			return resolved, true, nil
		}
		return resolved, false, fmt.Errorf("MB_HELPER_PROGRAM points to a missing file: %s", resolved)
	}

	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), "mousebridge-helper")
		if fileExists(candidate) {
			return candidate, true, nil
		}
	}

	if resolved, err := exec.LookPath("mousebridge-helper"); err == nil {
		return resolved, true, nil
	}

	return "", false, nil
}

func recommendedAction(status Status) string {
	switch {
	case !status.ExecutableFound:
		return actionInstallHelperBinary
	case !status.LaunchAgentInstalled:
		return actionInstallLaunchAgent
	case !status.AccessibilityGranted:
		return actionGrantAccessibility
	case !status.LaunchAgentLoaded || !status.Connected:
		return actionRestartLaunchAgent
	default:
		return actionReady
	}
}

func checkAccessibility(program string) (bool, error) {
	output, err := run(program, "check-accessibility")
	if err == nil {
		return true, nil
	}
	if strings.Contains(output, "Accessibility permission: not granted") {
		return false, nil
	}
	return false, fmt.Errorf("check helper accessibility: %w", err)
}

func checkLaunchAgentLoaded(label string) (bool, error) {
	domain := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	_, err := run("/bin/launchctl", "print", domain)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, fmt.Errorf("launchctl print %s: %w", domain, err)
}

func run(program string, args ...string) (string, error) {
	cmd := exec.Command(program, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return strings.TrimSpace(output.String()), err
}

func defaultLaunchAgentLabel(dataDir string) string {
	sum := sha256.Sum256([]byte(dataDir))
	return "com.mousebridge.helper." + hex.EncodeToString(sum[:4])
}

func launchAgentPlistPath(label string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("~", "Library", "LaunchAgents", label+".plist")
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
