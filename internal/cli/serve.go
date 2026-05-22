package cli

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"mousebridge/internal/config"
	"mousebridge/internal/event"
	mnet "mousebridge/internal/net"
	"mousebridge/internal/pairing"
	"mousebridge/internal/session"
	sw "mousebridge/internal/switch"
)

func ServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Listen for incoming connections",
		RunE:  runServe,
	}
	cmd.Flags().IntP("port", "p", 0, "port to listen on (overrides config)")
	return cmd
}

// stdinLines broadcasts each line typed in the terminal to all listeners.
// This is needed because handleInbound runs in a goroutine and cannot read
// stdin directly on macOS — the terminal is only attached to the main goroutine.
var stdinLines = make(chan string, 4)

func startStdinBroadcast() {
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			stdinLines <- strings.TrimSpace(scanner.Text())
		}
	}()
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	port, _ := cmd.Flags().GetInt("port")
	if port == 0 {
		port = cfg.Port
	}

	startStdinBroadcast()

	sess := session.NewManager()
	ctrl := sw.NewController("local")

	srv := mnet.NewServer(func(c *mnet.Conn) {
		handleInbound(c, cfg, sess, ctrl)
	})

	if err := srv.Listen(port); err != nil {
		return err
	}
	log.Printf("[%s] listening on :%d", ts(), port)
	log.Printf("[%s] device name: %s", ts(), cfg.DeviceName)
	log.Println("Press Ctrl+C to stop")

	select {}
}

func handleInbound(c *mnet.Conn, cfg *config.Config, sess *session.Manager, ctrl *sw.Controller) {
	defer c.Close()
	remote := c.RemoteAddr().String()

	// Handshake
	hs, err := c.Recv()
	if err != nil {
		log.Printf("[%s] handshake recv from %s: %v", ts(), remote, err)
		return
	}
	var hsPay event.HandshakePayload
	if err := event.DecodePayload(hs, &hsPay); err != nil || hs.Type != event.TypeHandshake {
		log.Printf("[%s] bad handshake from %s", ts(), remote)
		return
	}
	_ = c.Send(event.Message{
		V: 1, Seq: 1, Type: event.TypeHandshake, Ts: nowMs(),
		Payload: event.HandshakePayload{DeviceID: "local", Name: cfg.DeviceName, Platform: "macos"},
	})

	// Pairing
	pairMsg, err := c.Recv()
	if err != nil || pairMsg.Type != event.TypePairRequest {
		log.Printf("[%s] expected pair_request from %s", ts(), remote)
		return
	}
	var prPay event.PairRequestPayload
	_ = event.DecodePayload(pairMsg, &prPay)

	mgr := pairing.NewManager()
	pin, err := mgr.StartAsSlave(prPay.DeviceID, prPay.Name)
	if err != nil {
		return
	}

	_ = c.Send(event.Message{
		V: 1, Seq: 2, Type: event.TypePairPin, Ts: nowMs(),
		Payload: event.PairPinPayload{PIN: pin},
	})

	log.Printf("[%s] pair_request from %s (id=%s)", ts(), prPay.Name, prPay.DeviceID)
	log.Printf("[%s] PIN: %s   [A] Accept  [R] Reject  (60s timeout)", ts(), pin)

	type result struct {
		accepted bool
		fromNet  bool
	}
	resultCh := make(chan result, 2)

	// Terminal: accept/reject from the main-goroutine stdin broadcast.
	go func() {
		for {
			select {
			case line := <-stdinLines:
				upper := strings.ToUpper(line)
				if upper == "A" {
					resultCh <- result{accepted: true}
					return
				}
				if upper == "R" {
					resultCh <- result{accepted: false}
					return
				}
			case <-time.After(61 * time.Second):
				return
			}
		}
	}()

	// Network: host may send pair_confirm with PIN
	go func() {
		msg, err := c.Recv()
		if err != nil {
			resultCh <- result{accepted: false, fromNet: true}
			return
		}
		if msg.Type == event.TypePairConfirm {
			var pay event.PairConfirmPayload
			_ = event.DecodePayload(msg, &pay)
			err := mgr.ConfirmPIN(pay.PIN)
			resultCh <- result{accepted: err == nil, fromNet: true}
		}
	}()

	var res result
	select {
	case res = <-resultCh:
	case <-time.After(60 * time.Second):
		log.Printf("[%s] pairing timed out", ts())
		_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{}})
		return
	}

	if !res.accepted {
		log.Printf("[%s] pairing rejected", ts())
		_ = mgr.Reject()
		_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairReject, Ts: nowMs(), Payload: struct{}{}})
		return
	}
	if !res.fromNet {
		_ = mgr.Accept()
	}
	_ = c.Send(event.Message{V: 1, Seq: 3, Type: event.TypePairAccept, Ts: nowMs(), Payload: struct{}{}})
	log.Printf("[%s] paired with %s", ts(), prPay.Name)

	sess.Add(prPay.DeviceID, prPay.Name)
	defer sess.Remove(prPay.DeviceID)
	log.Printf("[%s] connected — %s (%s)", ts(), prPay.Name, remote)

	// Event loop
	var seq int64 = 4
	for {
		msg, err := c.Recv()
		if err != nil {
			log.Printf("[%s] disconnected — %s", ts(), prPay.Name)
			return
		}
		latency := float64(nowMs() - msg.Ts)
		sess.RecordLatency(prPay.DeviceID, latency)

		switch msg.Type {
		case event.TypePing:
			_ = c.Send(event.Message{
				V: 1, Seq: seq, Type: event.TypePong, Ts: nowMs(),
				Payload: event.PongPayload{EchoTs: msg.Ts},
			})
			seq++
		case event.TypeSwitchRequest:
			var pay event.SwitchRequestPayload
			_ = event.DecodePayload(msg, &pay)
			log.Printf("[%s] recv  switch_request  trigger=%s entry=%s@%.0f%%  latency=%.1fms",
				ts(), pay.Trigger, pay.Edge, pay.EntryPct*100, latency)
			ctrl.SwitchTo(prPay.DeviceID)
			_ = c.Send(event.Message{V: 1, Seq: seq, Type: event.TypeSwitchAck, Ts: nowMs(), Payload: struct{}{}})
			seq++
		default:
			log.Printf("[%s] recv  %-16s  latency=%.1fms", ts(), msg.Type, latency)
		}
	}
}

func loadConfig() (*config.Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return config.Default(), nil
	}
	return config.Load(fmt.Sprintf("%s/.mousebridge/config.json", home))
}

func ts() string {
	return time.Now().Format("15:04:05")
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}
