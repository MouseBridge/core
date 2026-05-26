package cli

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/mousebridge/core/internal/api"
	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/daemon"
	"github.com/mousebridge/core/internal/transport"
)

func DaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the background daemon",
		RunE:  runDaemon,
	}
	cmd.Flags().IntP("port", "p", 0, "TCP port for P2P and HTTP API (default: from config)")
	cmd.Flags().Bool("serve", false, "start listening for incoming connections on startup")
	cmd.Flags().StringArray("connect", nil, "connect to remote device IP on startup (repeatable)")
	return cmd
}

func runDaemon(cmd *cobra.Command, args []string) error {
	socketPath := socketFlag(cmd)
	port, _ := cmd.Flags().GetInt("port")
	doServe, _ := cmd.Flags().GetBool("serve")
	connectIPs, _ := cmd.Flags().GetStringArray("connect")

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
		DeviceID:   cfg.DeviceID,
		DeviceName: cfg.DeviceName,
	})

	// Single shared listener for both P2P and HTTP traffic.
	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return fmt.Errorf("daemon: listen :%d: %w", port, err)
	}

	// Start HTTP API server on the channel-based listener.
	apiSrv := api.New(d)
	httpLn := api.NewChanListener(d.HTTPConnCh(), ln.Addr())
	apiSrv.Start(httpLn)

	// Mux: dispatch P2P vs HTTP from the single TCP listener.
	go transport.Serve(ln, d.HandleP2P, d.HandleHTTP)

	if err := d.Start(); err != nil {
		return err
	}
	log.Printf("[daemon] started — port=%d socket=%s device=%s", port, socketPath, cfg.DeviceName)

	if doServe {
		d.Serve(port)
	}
	for _, ip := range connectIPs {
		go d.Connect(ip, 0)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[daemon] shutting down...")
	_ = ln.Close()
	apiSrv.Stop()
	d.Stop()
	return nil
}
