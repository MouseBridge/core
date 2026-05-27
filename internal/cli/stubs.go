package cli

import "github.com/spf13/cobra"

func ConnectCmd() *cobra.Command {
	return &cobra.Command{Use: "connect", Short: "Connect to a remote device (use HTTP API)", RunE: notImplemented}
}

func DevicesCmd() *cobra.Command {
	return &cobra.Command{Use: "devices", Short: "List connected devices (use GET /api/status)", RunE: notImplemented}
}

func DisconnectCmd() *cobra.Command {
	return &cobra.Command{Use: "disconnect", Short: "Disconnect a device (use HTTP API)", RunE: notImplemented}
}

func PairCmd() *cobra.Command {
	return &cobra.Command{Use: "pair", Short: "Manage pairing (use HTTP API)", RunE: notImplemented}
}

func ServeCmd() *cobra.Command {
	return &cobra.Command{Use: "serve", Short: "Start serving (use daemon command)", RunE: notImplemented}
}

func StatusCmd() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Show daemon status (use GET /api/status)", RunE: notImplemented}
}

func StopServeCmd() *cobra.Command {
	return &cobra.Command{Use: "stop-serve", Short: "Stop serving (use HTTP API)", RunE: notImplemented}
}

func TrustCmd() *cobra.Command {
	return &cobra.Command{Use: "trust", Short: "Manage remembered devices (use HTTP API)", RunE: notImplemented}
}

func notImplemented(_ *cobra.Command, _ []string) error {
	return nil
}
