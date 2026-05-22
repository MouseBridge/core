package cli

import "github.com/spf13/cobra"

func ConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect [ip]",
		Short: "Connect to a remote device",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println("connect: not yet implemented")
			return nil
		},
	}
	cmd.Flags().IntP("port", "p", 39172, "remote port")
	cmd.Flags().Bool("discover", false, "mDNS discover mode")
	return cmd
}
