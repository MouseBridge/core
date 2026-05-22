package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"mousebridge/internal/daemon"
)

func ServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Tell daemon to listen for incoming TCP connections",
		RunE:  runServe,
	}
	cmd.Flags().IntP("port", "p", 0, "TCP port (overrides config)")
	return cmd
}

func runServe(cmd *cobra.Command, args []string) error {
	socketPath := socketFlag(cmd)
	port, _ := cmd.Flags().GetInt("port")

	conn, err := daemon.DialSocket(socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := daemon.SendCommand(conn, daemon.Command{Cmd: "serve", Port: port}); err != nil {
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
