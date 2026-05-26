package cli

import (
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
	// Recompute DeviceID based on the actual port being used.
	cfg.DeviceID = config.DeriveDeviceID(port)

	d := daemon.New(daemon.Options{
		SocketPath: socketPath,
		TCPPort:    port,
		DeviceID:   cfg.DeviceID,
		DeviceName: cfg.DeviceName,
	})

	apiSrv := api.New(d)
	httpLn := api.NewChanListener(d.HTTPConnCh(), &net.TCPAddr{IP: net.IPv4zero, Port: port})
	apiSrv.Start(httpLn)

	if err := d.Start(); err != nil {
		return err
	}
	log.Printf("[daemon] started — port=%d socket=%s device=%s", port, socketPath, cfg.DeviceName)

	// Always open TCP (P2P + HTTP mux). --serve means auto-start host mode.
	d.Serve(port)
	if doServe {
		d.HandleCommand(daemon.Command{Cmd: "serve"})
	}
	for _, ip := range connectIPs {
		go d.Connect(ip, 0)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[daemon] shutting down...")
	log.Println("[daemon] stopping api server...")
	apiSrv.Stop()
	log.Println("[daemon] api server stopped, stopping daemon...")
	d.Stop()
	log.Println("[daemon] done")
	return nil
}
