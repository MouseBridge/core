package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	log "github.com/sirupsen/logrus"

	"github.com/spf13/cobra"
	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/daemon"
)

// envPort returns the port from MB_PORT env var, or 0 if unset/invalid.
func envPort() int {
	if s := os.Getenv("MB_PORT"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			return v
		}
	}
	return 0
}

// socketFlag returns the Unix socket path for this daemon instance.
// --socket takes precedence; otherwise ~/.mousebridge/mb-<port>.sock
// where port comes from --port flag or config default.
func socketFlag(cmd *cobra.Command) string {
	root := cmd.Root()
	if s, _ := root.PersistentFlags().GetString("socket"); s != "" {
		if len(s) >= 2 && s[:2] == "~/" {
			home, err := os.UserHomeDir()
			if err == nil {
				return filepath.Join(home, s[2:])
			}
		}
		return s
	}

	port := 0
	for c := cmd; c != nil; c = c.Parent() {
		if c.Flags().Lookup("port") != nil {
			if v, err := c.Flags().GetInt("port"); err == nil && v != 0 {
				port = v
				break
			}
		}
	}
	if port == 0 {
		port = envPort()
	}
	if port == 0 {
		cfg, _ := loadConfig()
		if cfg != nil {
			port = cfg.Port
		} else {
			port = config.Default().Port
		}
	}

	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mousebridge", fmt.Sprintf("mb-%d.sock", port))
}

// printEvent formats and prints a daemon event to stdout.
func printEvent(ev daemon.Event) {
	switch ev.Event {
	case "log":
		log.Printf("%s", ev.Msg)
	case "error":
		log.Printf("[error] %s", ev.Msg)
	case "pair_request":
		if ev.PIN != "" {
			// slave side: show PIN to user
			log.Printf("[pair] request from %s — PIN: %s (share this with the remote user)", ev.Name, ev.PIN)
			log.Printf("[pair] run: mousebridge pair accept %s  OR  mousebridge pair reject %s", ev.DeviceID, ev.DeviceID)
		} else {
			// host side: user must get PIN from remote
			log.Printf("[pair] pairing with %s — run: mousebridge pair pin <PIN> -p <port>", ev.Name)
		}
	case "paired":
		log.Printf("[pair] paired with %s", ev.Name)
	case "connected":
		log.Printf("[conn] connected to %s (%s)", ev.Name, ev.IP)
	case "disconnected":
		log.Printf("[conn] disconnected from %s", ev.Name)
	case "listening":
		log.Printf("[serve] listening on :%d", ev.Port)
	case "status":
		if len(ev.Devices) == 0 {
			fmt.Println("No connected devices.")
			return
		}
		for _, d := range ev.Devices {
			fmt.Printf("  %-20s %s  avg=%.1fms\n", d.Name, d.IP, d.AvgLatencyMs)
		}
	}
}

func loadConfig() (*config.Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return config.Default(), nil
	}
	return config.Load(filepath.Join(home, ".mousebridge", "config.json"))
}
