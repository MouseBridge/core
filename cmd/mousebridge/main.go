package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/mousebridge/core/internal/cli"
)

func main() {
	root := &cobra.Command{
		Use:   "mousebridge",
		Short: "LAN mouse/keyboard sharing",
	}

	root.PersistentFlags().String("socket", "", "daemon Unix socket path (default: ~/.mousebridge/mb.sock)")

	root.AddCommand(cli.DaemonCmd())
	root.AddCommand(cli.ServeCmd())
	root.AddCommand(cli.ConnectCmd())
	root.AddCommand(cli.StatusCmd())
	root.AddCommand(cli.PairCmd())
	root.AddCommand(cli.DisconnectCmd())
	root.AddCommand(cli.StopServeCmd())
	root.AddCommand(cli.TrustCmd())
	root.AddCommand(cli.DevicesCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
