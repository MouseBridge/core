package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHelperSocketPathIsStableForRelativeAndAbsoluteDataDir(t *testing.T) {
	dir, err := os.MkdirTemp(".", "mb-socket-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	rel, err := filepath.Rel(".", dir)
	if err != nil {
		t.Fatal(err)
	}

	gotRel := helperSocketPath(rel, 39172)
	gotAbs := helperSocketPath(dir, 39172)
	if gotRel != gotAbs {
		t.Fatalf("socket path mismatch: rel=%q abs=%q", gotRel, gotAbs)
	}
}
