package localhelper

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatusUsesConnectedHelperAsAccessibilitySourceOfTruth(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "helper-check")
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho 'Accessibility permission: not granted'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MB_HELPER_PROGRAM", program)

	runtime := NewRuntime(dir, func() int { return 1 })
	status := runtime.Status()
	if !status.Connected {
		t.Fatal("expected connected helper")
	}
	if !status.AccessibilityGranted {
		t.Fatal("a connected helper must be reported as accessibility-ready")
	}
	if status.RecommendedAction == actionGrantAccessibility {
		t.Fatalf("connected helper must not request accessibility again: %+v", status)
	}
}

func TestInstallDoesNotCreateLaunchAgentForAppManagedRuntime(t *testing.T) {
	t.Setenv("MB_HELPER_APP_MANAGED", "1")
	runtime := NewRuntime(t.TempDir(), nil)
	if err := runtime.Install(); err == nil {
		t.Fatal("app-managed runtime must not install a LaunchAgent")
	}
}

func TestAppManagedConnectedRuntimeIsReadyWithoutLaunchAgent(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "helper-check")
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho 'Accessibility permission: not granted'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MB_HELPER_PROGRAM", program)
	t.Setenv("MB_HELPER_APP_MANAGED", "1")

	status := NewRuntime(dir, func() int { return 1 }).Status()
	if status.RecommendedAction != actionReady {
		t.Fatalf("connected app-managed runtime should be ready without LaunchAgent, got %q", status.RecommendedAction)
	}
}

func TestAppManagedRuntimeReportsPermissionBeforeLaunchAgent(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "helper-check")
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho 'Accessibility permission: not granted'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MB_HELPER_PROGRAM", program)
	t.Setenv("MB_HELPER_APP_MANAGED", "1")

	status := NewRuntime(dir, nil).Status()
	if status.RecommendedAction != actionGrantAccessibility {
		t.Fatalf("app-managed runtime should request permission, got %q", status.RecommendedAction)
	}
}

func TestDefaultLaunchAgentLabelIsStableForRelativeAndAbsoluteDataDir(t *testing.T) {
	dir, err := os.MkdirTemp(".", "mb-runtime-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	runtime := NewRuntime(dir, nil)
	if runtime.dataDir == dir {
		t.Fatalf("dataDir should be canonicalized, got %q", runtime.dataDir)
	}

	rel, err := filepath.Rel(".", dir)
	if err != nil {
		t.Fatal(err)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.dataDir != filepath.Clean(absDir) {
		t.Fatalf("dataDir=%q want %q", runtime.dataDir, filepath.Clean(absDir))
	}

	labelAbs := defaultLaunchAgentLabel(dir)
	labelRel := defaultLaunchAgentLabel(rel)
	if labelAbs != labelRel {
		t.Fatalf("launch agent label mismatch: abs=%q rel=%q", labelAbs, labelRel)
	}
}
