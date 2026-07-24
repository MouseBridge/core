package daemon

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/mousebridge/core/internal/config"
)

func newTestOptions(t *testing.T) Options {
	t.Helper()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	cfg := config.Default()
	cfg.Port = reserveTestPort(t)
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("Save config: %v", err)
	}
	return Options{
		DataDir:    dir,
		ConfigPath: configPath,
	}
}

func reserveTestPort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
