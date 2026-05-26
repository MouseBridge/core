package cli

import (
	"github.com/spf13/cobra"
	"github.com/mousebridge/core/internal/daemon"
)

func ConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect <ip>",
		Short: "Tell daemon to connect to a remote device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetPort, _ := cmd.Flags().GetInt("target-port")
			conn, err := daemon.DialSocket(socketFlag(cmd))
			if err != nil {
				return err
			}
			defer conn.Close()
			return daemon.SendCommand(conn, daemon.Command{Cmd: "connect", IP: args[0], Port: targetPort})
		},
	}
	cmd.Flags().IntP("port", "p", 0, "local daemon TCP port, also selects the daemon socket")
	cmd.Flags().Int("target-port", 0, "TCP port on the remote device (default: same as local daemon port)")
	return cmd
}
