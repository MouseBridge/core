package cli

import (
	"os"
	"os/signal"
	"syscall"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/daemon"
	"github.com/mousebridge/core/internal/httpapi"
)

func DaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the background daemon",
		RunE:  runDaemon,
	}
	cmd.Flags().IntP("port", "p", 0, "TCP port (default: from config)")
	cmd.Flags().Int("http-port", 0, "HTTP API port (default: from config)")
	cmd.Flags().Bool("serve", false, "start listening for incoming connections on startup")
	cmd.Flags().StringArray("connect", nil, "connect to remote device IP on startup (repeatable)")
	return cmd
}

func runDaemon(cmd *cobra.Command, args []string) error {
	socketPath := socketFlag(cmd)
	port, _ := cmd.Flags().GetInt("port")
	httpPort, _ := cmd.Flags().GetInt("http-port")
	doServe, _ := cmd.Flags().GetBool("serve")
	connectIPs, _ := cmd.Flags().GetStringArray("connect")

	cfg, err := loadConfig()
	if err != nil {
		cfg = config.Default()
	}
	if port == 0 {
		port = cfg.Port
	}
	if httpPort != 0 {
		cfg.HTTPPort = httpPort
	}

	d := daemon.New(daemon.Options{
		SocketPath: socketPath,
		TCPPort:    port,
		DeviceID:   cfg.DeviceID,
		DeviceName: cfg.DeviceName,
	})

	httpHost := cfg.HTTPHost
	if httpHost == "" {
		httpHost = "127.0.0.1"
	}
	if cfg.HTTPPort != 0 {
		d.SetHTTPServer(httpapi.New(d, httpHost, cfg.HTTPPort))
		log.Printf("[daemon] HTTP API will listen on %s:%d", httpHost, cfg.HTTPPort)
	}

	if err := d.Start(); err != nil {
		return err
	}
	log.Printf("[daemon] started — socket=%s port=%d device=%s", socketPath, port, cfg.DeviceName)

	if doServe {
		d.Serve(0)
	}
	for _, ip := range connectIPs {
		go d.Connect(ip, 0)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[daemon] shutting down...")
	d.Stop()
	return nil
}
