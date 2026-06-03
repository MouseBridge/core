package cli

import (
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/mousebridge/core/internal/api"
	"github.com/mousebridge/core/internal/daemon"
)

func DaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the background daemon",
		RunE:  runDaemon,
	}
	cmd.Flags().StringArray("connect", nil, "connect to remote device IP on startup (repeatable)")
	cmd.Flags().String("data-dir", "", "data directory (default: ~/.mousebridge)")
	return cmd
}

func runDaemon(cmd *cobra.Command, _ []string) error {
	connectIPs, _ := cmd.Flags().GetStringArray("connect")

	dataDir, _ := cmd.Flags().GetString("data-dir")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".mousebridge")
	}
	configPath := filepath.Join(dataDir, "config.json")

	d, err := daemon.New(daemon.Options{
		ConfigPath: configPath,
		DataDir:    dataDir,
	})
	if err != nil {
		return err
	}

	apiSrv := api.New(d)
	port := d.Config().Port
	httpLn := api.NewChanListener(d.HTTPConnCh(), &net.TCPAddr{IP: net.IPv4zero, Port: port})
	apiSrv.Start(httpLn)

	if err := d.Start(); err != nil {
		return err
	}
	log.Printf("[daemon] started — addr=%s name=%s", d.Addr(), d.Identity().Name)

	for _, ip := range connectIPs {
		d.Connect(ip, port)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("[daemon] shutting down...")
	d.Stop()      // closes sub channels → SSE streams exit → Shutdown unblocks
	apiSrv.Stop()
	log.Println("[daemon] done")
	return nil
}
