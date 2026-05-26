package cli

import (
	"fmt"
	"net"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/mousebridge/core/internal/daemon"
)

func ConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect <ip>[:<port>]",
		Short: "Tell daemon to connect to a remote device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ip, port, err := parseConnectTarget(args[0])
			if err != nil {
				return err
			}
			if flagPort, _ := cmd.Flags().GetInt("port"); flagPort != 0 {
				port = flagPort
			}
			conn, err := daemon.DialSocket(socketFlag(cmd))
			if err != nil {
				return err
			}
			defer conn.Close()
			return daemon.SendCommand(conn, daemon.Command{Cmd: "connect", IP: ip, Port: port})
		},
	}
	cmd.Flags().Int("port", 0, "TCP port on the remote device")
	return cmd
}

// parseConnectTarget parses "ip:port" or bare "ip".
// Returns ip, port (0 if not specified).
func parseConnectTarget(arg string) (ip string, port int, err error) {
	host, portStr, splitErr := net.SplitHostPort(arg)
	if splitErr == nil {
		p, convErr := strconv.Atoi(portStr)
		if convErr != nil {
			return "", 0, fmt.Errorf("invalid port %q: %w", portStr, convErr)
		}
		return host, p, nil
	}
	// No port — treat entire arg as IP/host.
	return arg, 0, nil
}
