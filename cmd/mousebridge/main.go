package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"mousebridge/internal/cli"
)

func main() {
	root := &cobra.Command{
		Use:   "mousebridge",
		Short: "LAN mouse/keyboard sharing",
	}

	root.AddCommand(cli.ServeCmd())
	root.AddCommand(cli.ConnectCmd())
	root.AddCommand(cli.StatusCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
