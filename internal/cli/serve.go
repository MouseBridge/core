package cli

import (
	"github.com/spf13/cobra"
	"github.com/mousebridge/core/internal/daemon"
)

func ServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Tell daemon to listen for incoming connections",
		RunE: func(cmd *cobra.Command, args []string) error {
			port, _ := cmd.Flags().GetInt("port")
			conn, err := daemon.DialSocket(socketFlag(cmd))
			if err != nil {
				return err
			}
			defer conn.Close()
			return daemon.SendCommand(conn, daemon.Command{Cmd: "serve", Port: port})
		},
	}
	cmd.Flags().IntP("port", "p", 0, "TCP port (default: from config)")
	return cmd
}
