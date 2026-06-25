package localhelper

import (
	"os"
	"path/filepath"
	"testing"
)

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
