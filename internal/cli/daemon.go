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
)

func DaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the background daemon",
		RunE:  runDaemon,
	}
	cmd.Flags().Bool("serve", false, "start listening for incoming connections on startup")
	cmd.Flags().StringArray("connect", nil, "connect to remote device IP on startup (repeatable)")
	return cmd
}

func runDaemon(cmd *cobra.Command, args []string) error {
	socketPath := socketFlag(cmd)
	doServe, _ := cmd.Flags().GetBool("serve")
	connectIPs, _ := cmd.Flags().GetStringArray("connect")

	cfg, err := loadConfig()
	if err != nil {
		cfg = config.Default()
	}
	port := envPort()
	if port == 0 {
		port = cfg.Port
	}

	d := daemon.New(daemon.Options{
		SocketPath: socketPath,
		TCPPort:    port,
		DeviceID:   cfg.DeviceID,
		DeviceName: cfg.DeviceName,
	})

	apiSrv := api.New(d)

	if doServe {
		// Host: P2P + HTTP share the same TCP port via mux.
		httpLn := api.NewChanListener(d.HTTPConnCh(), &net.TCPAddr{IP: net.IPv4zero, Port: port})
		apiSrv.Start(httpLn)
	} else {
		// Client: no P2P inbound needed, open a plain TCP listener for HTTP only.
		httpLn, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
		if err != nil {
			return fmt.Errorf("daemon: http listen: %w", err)
		}
		apiSrv.Start(httpLn)
	}

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
	apiSrv.Stop()
	d.Stop()
	return nil
}
