package cli

import "github.com/spf13/cobra"

func StatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show connection status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println("status: not yet implemented")
			return nil
		},
	}
}
