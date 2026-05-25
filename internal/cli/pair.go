package cli

import (
	"github.com/spf13/cobra"
	"mousebridge/internal/daemon"
)

func PairCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Manage pairing with a remote device",
	}
	cmd.PersistentFlags().IntP("port", "p", 0, "TCP port of target daemon (default: from config)")
	cmd.AddCommand(pairAcceptCmd())
	cmd.AddCommand(pairRejectCmd())
	cmd.AddCommand(pairPINCmd())
	return cmd
}

func pairAcceptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "accept",
		Short: "Accept the pending pair request",
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendOneCommand(socketFlag(cmd), daemon.Command{Cmd: "pair_accept"})
		},
	}
}

func pairRejectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reject",
		Short: "Reject the pending pair request",
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendOneCommand(socketFlag(cmd), daemon.Command{Cmd: "pair_reject"})
		},
	}
}

func pairPINCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pin <PIN>",
		Short: "Submit a PIN to the remote device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendOneCommand(socketFlag(cmd), daemon.Command{Cmd: "pair_pin", PIN: args[0]})
		},
	}
}

// sendOneCommand sends a command and reads back one event (ack or error).
func sendOneCommand(socketPath string, cmd daemon.Command) error {
	conn, err := daemon.DialSocket(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := daemon.SendCommand(conn, cmd); err != nil {
		return err
	}

	return daemon.ReadEvents(conn, func(ev daemon.Event) bool {
		printEvent(ev)
		return false
	})
}
