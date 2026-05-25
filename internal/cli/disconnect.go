package cli

import (
	"github.com/spf13/cobra"
	"mousebridge/internal/daemon"
)

func DisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect <device-id>",
		Short: "Tell daemon to disconnect a device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := daemon.DialSocket(socketFlag(cmd))
			if err != nil {
				return err
			}
			defer conn.Close()
			return daemon.SendCommand(conn, daemon.Command{Cmd: "disconnect", DeviceID: args[0]})
		},
	}
}
