package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func StatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show connection status",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("No active session. Run `mousebridge serve` or `mousebridge connect`.")
			return nil
		},
	}
}
