package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"mousebridge/internal/daemon"
)

func StatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon connection status",
		RunE: func(cmd *cobra.Command, args []string) error {
			socketPath := socketFlag(cmd)
			conn, err := daemon.DialSocket(socketPath)
			if err != nil {
				return err
			}
			defer conn.Close()

			if err := daemon.SendCommand(conn, daemon.Command{Cmd: "status"}); err != nil {
				return err
			}

			return daemon.ReadEvents(conn, func(ev daemon.Event) bool {
				if ev.Event == "status" {
					if len(ev.Devices) == 0 {
						fmt.Println("No connected devices.")
					}
					for _, d := range ev.Devices {
						fmt.Printf("  %s (%s)  avg_latency=%.1fms\n", d.Name, d.ID[:8], d.AvgLatencyMs)
					}
					return false
				}
				return true
			})
		},
	}
}
