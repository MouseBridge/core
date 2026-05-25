package cli

import (
	"github.com/spf13/cobra"
	"mousebridge/internal/daemon"
)

func StopServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop-serve",
		Short: "Tell daemon to stop listening for incoming connections",
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := daemon.DialSocket(socketFlag(cmd))
			if err != nil {
				return err
			}
			defer conn.Close()
			return daemon.SendCommand(conn, daemon.Command{Cmd: "stop_serve"})
		},
	}
	cmd.Flags().IntP("port", "p", 0, "TCP port of target daemon (default: from config)")
	return cmd
}
