package cli

import (
	"fmt"

	"github.com/mousebridge/core/internal/daemon"
	"github.com/spf13/cobra"
)

func TrustCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Manage trusted devices (auto-connect without PIN)",
	}
	cmd.AddCommand(trustAddCmd())
	cmd.AddCommand(trustRemoveCmd())
	cmd.AddCommand(trustListCmd())
	return cmd
}

func trustAddCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "add <device-id>",
		Short: "Trust a device (will auto-connect without PIN)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendOneCommand(socketFlag(cmd), daemon.Command{Cmd: "trust", DeviceID: args[0], Name: name})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "friendly name for the device")
	return cmd
}

func trustRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <device-id>",
		Short: "Remove a trusted device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendOneCommand(socketFlag(cmd), daemon.Command{Cmd: "untrust", DeviceID: args[0]})
		},
	}
}

func trustListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List trusted devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := daemon.DialSocket(socketFlag(cmd))
			if err != nil {
				return err
			}
			defer conn.Close()
			if err := daemon.SendCommand(conn, daemon.Command{Cmd: "trusted_list"}); err != nil {
				return err
			}
			return daemon.ReadEvents(conn, func(ev daemon.Event) bool {
				if ev.Event == "trusted_list" {
					if len(ev.Devices) == 0 {
						fmt.Println("no trusted devices")
					}
					for _, d := range ev.Devices {
						fmt.Printf("  %s  %s\n", d.ID, d.Name)
					}
					return false
				}
				printEvent(ev)
				return false
			})
		},
	}
}
