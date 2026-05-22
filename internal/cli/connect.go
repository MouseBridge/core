package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"mousebridge/internal/daemon"
)

func ConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect <ip>",
		Short: "Tell daemon to connect to a remote device",
		Args:  cobra.ExactArgs(1),
		RunE:  runConnect,
	}
	cmd.Flags().IntP("port", "p", 39172, "remote TCP port")
	return cmd
}

func runConnect(cmd *cobra.Command, args []string) error {
	socketPath := socketFlag(cmd)
	ip := args[0]
	port, _ := cmd.Flags().GetInt("port")

	conn, err := daemon.DialSocket(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := daemon.SendCommand(conn, daemon.Command{Cmd: "connect", IP: ip, Port: port}); err != nil {
		return err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	doneCh := make(chan struct{})
	go func() {
		daemon.ReadEvents(conn, func(ev daemon.Event) bool {
			printEvent(ev)
			return true
		})
		close(doneCh)
	}()

	select {
	case <-sigCh:
		fmt.Println()
	case <-doneCh:
	}
	return nil
}
