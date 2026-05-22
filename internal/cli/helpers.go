package cli

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"mousebridge/internal/config"
	"mousebridge/internal/daemon"
)

const defaultSocketPath = "~/.mousebridge/mb.sock"

// socketFlag returns the --socket flag value from the root command, expanding ~ if needed.
func socketFlag(cmd *cobra.Command) string {
	root := cmd.Root()
	s, _ := root.PersistentFlags().GetString("socket")
	if s == "" {
		s = defaultSocketPath
	}
	if len(s) >= 2 && s[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err == nil {
			s = filepath.Join(home, s[2:])
		}
	}
	return s
}

// printEvent formats and prints a daemon event to stdout.
func printEvent(ev daemon.Event) {
	switch ev.Event {
	case "log":
		log.Printf("%s", ev.Msg)
	case "error":
		log.Printf("[error] %s", ev.Msg)
	case "pair_request":
		log.Printf("[pair] request from %s — PIN: %s", ev.Name, ev.PIN)
		log.Printf("[pair] run: mousebridge pair accept  OR  mousebridge pair reject")
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
