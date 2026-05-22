package cli

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"mousebridge/internal/config"
	"mousebridge/internal/daemon"
)

func DaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the background daemon",
		RunE:  runDaemon,
	}
	cmd.Flags().IntP("port", "p", 0, "TCP port (default: from config)")
	return cmd
}

func runDaemon(cmd *cobra.Command, args []string) error {
	socketPath := socketFlag(cmd)
	port, _ := cmd.Flags().GetInt("port")

	cfg, err := loadConfig()
	if err != nil {
		cfg = config.Default()
	}
	if port == 0 {
		port = cfg.Port
	}

	d := daemon.New(daemon.Options{
		SocketPath: socketPath,
		TCPPort:    port,
		DeviceName: cfg.DeviceName,
	})

	if err := d.Start(); err != nil {
		return err
	}
	log.Printf("[daemon] started — socket=%s port=%d device=%s", socketPath, port, cfg.DeviceName)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[daemon] shutting down...")
	d.Stop()
	return nil
}
