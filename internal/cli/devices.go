package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/mousebridge/core/internal/daemon"
)

func DevicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "List connected devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := daemon.DialSocket(socketFlag(cmd))
			if err != nil {
				return err
			}
			defer conn.Close()

			if err := daemon.SendCommand(conn, daemon.Command{Cmd: "status"}); err != nil {
				return err
			}

			return daemon.ReadEvents(conn, func(ev daemon.Event) bool {
				if ev.Event != "status" {
					return true
				}
				if len(ev.Devices) == 0 {
					fmt.Println("No connected devices.")
					return false
				}
				fmt.Printf("%-20s  %-21s  %-5s  %s\n", "NAME", "IP", "ROLE", "AVG LATENCY")
				for _, d := range ev.Devices {
					fmt.Printf("%-20s  %-21s  %-5s  %.1fms\n", d.Name, d.IP, d.Role, d.AvgLatencyMs)
				}
				return false
			})
		},
	}
}
