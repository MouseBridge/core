package cli

import "github.com/spf13/cobra"

func ServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Listen for incoming connections",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println("serve: not yet implemented")
			return nil
		},
	}
}
