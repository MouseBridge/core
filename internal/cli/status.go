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
					localID := ev.LocalID
					if len(localID) > 8 {
						localID = localID[:8]
					}
					fmt.Printf("Local:   %s  id=%s\n", ev.LocalName, localID)
					if ev.Serving {
						fmt.Printf("Serving: yes  port=%d\n", ev.Port)
					} else {
						fmt.Println("Serving: no")
					}
					if len(ev.Devices) == 0 {
						fmt.Println("Peers:   none")
					} else {
						fmt.Printf("Peers:   %d\n", len(ev.Devices))
						for _, d := range ev.Devices {
							id := d.ID
							if len(id) > 8 {
								id = id[:8]
							}
							fmt.Printf("  %-20s  id=%-8s  role=%-5s  ip=%-21s  avg=%.1fms\n",
								d.Name, id, d.Role, d.IP, d.AvgLatencyMs)
						}
					}
					return false
				}
				return true
			})
		},
	}
	return cmd
}
