package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/mousebridge/core/internal/daemon"
)

func StatusCmd() *cobra.Command {
	cmd := &cobra.Command{
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
						id := d.ID
						if len(id) > 8 {
							id = id[:8]
						}
						fmt.Printf("  %s (%s)  avg_latency=%.1fms\n", d.Name, id, d.AvgLatencyMs)
					}
					return false
				}
				return true
			})
		},
	}
	return cmd
}
