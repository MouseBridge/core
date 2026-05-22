package cli

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"mousebridge/internal/event"
	mnet "mousebridge/internal/net"
	"mousebridge/internal/session"
	sw "mousebridge/internal/switch"
)

func ConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect <ip>",
		Short: "Connect to a remote device",
		Args:  cobra.ExactArgs(1),
		RunE:  runConnect,
	}
	cmd.Flags().IntP("port", "p", 39172, "remote port")
	return cmd
}

func runConnect(cmd *cobra.Command, args []string) error {
	ip := args[0]
	port, _ := cmd.Flags().GetInt("port")

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	sess := session.NewManager()
	ctrl := sw.NewController("local")
	_ = ctrl

	log.Printf("[%s] connecting to %s:%d ...", ts(), ip, port)
	c, err := mnet.Dial(ip, port)
	if err != nil {
		return err
	}
	defer c.Close()

	deviceID := uuid.New().String()

	// Handshake
	if err := c.Send(event.Message{
		V: 1, Seq: 1, Type: event.TypeHandshake, Ts: nowMs(),
		Payload: event.HandshakePayload{DeviceID: deviceID, Name: cfg.DeviceName, Platform: "macos"},
	}); err != nil {
		return fmt.Errorf("handshake send: %w", err)
	}
	hsReply, err := c.Recv()
	if err != nil || hsReply.Type != event.TypeHandshake {
		return fmt.Errorf("handshake reply: %v", err)
	}
	var hsPay event.HandshakePayload
	_ = event.DecodePayload(hsReply, &hsPay)
	remoteName := hsPay.Name
	remoteID := hsPay.DeviceID

	// Pairing
	if err := c.Send(event.Message{
		V: 1, Seq: 2, Type: event.TypePairRequest, Ts: nowMs(),
		Payload: event.PairRequestPayload{DeviceID: deviceID, Name: cfg.DeviceName},
	}); err != nil {
		return err
	}
	log.Printf("[%s] pair_request sent, waiting...", ts())

	pinMsg, err := c.Recv()
	if err != nil || pinMsg.Type != event.TypePairPin {
		return fmt.Errorf("expected pair_pin: %v", err)
	}
	var pinPay event.PairPinPayload
	_ = event.DecodePayload(pinMsg, &pinPay)
	log.Printf("[%s] remote PIN: %s  (enter PIN or wait for remote to Accept)", ts(), pinPay.PIN)

	// Wait for pair_accept or pair_reject
	pairResult, err := c.Recv()
	if err != nil {
		return fmt.Errorf("pairing: %w", err)
	}
	if pairResult.Type == event.TypePairReject {
		return fmt.Errorf("pairing rejected by remote")
	}
	if pairResult.Type != event.TypePairAccept {
		return fmt.Errorf("unexpected message during pairing: %s", pairResult.Type)
	}

	log.Printf("[%s] paired ✓  —  connected to %s (id=%s)", ts(), remoteName, remoteID)
	sess.Add(remoteID, remoteName)
	log.Printf("[%s] active target: local", ts())
	log.Printf("[%s] sending ping every 2s, Ctrl+C to stop", ts())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var seq int64 = 3
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	recvCh := make(chan event.Message, 16)
	recvErrCh := make(chan error, 1)
	go func() {
		for {
			msg, err := c.Recv()
			if err != nil {
				recvErrCh <- err
				return
			}
			recvCh <- msg
		}
	}()

	for {
		select {
		case <-sigCh:
			log.Printf("[%s] interrupted", ts())
			return nil

		case err := <-recvErrCh:
			log.Printf("[%s] disconnected: %v", ts(), err)
			return nil

		case msg := <-recvCh:
			handleHostRecv(msg, sess, remoteID, remoteName)

		case <-ticker.C:
			sendTs := nowMs()
			if err := c.Send(event.Message{
				V: 1, Seq: seq, Type: event.TypePing, Ts: sendTs,
				Payload: event.PingPayload{},
			}); err != nil {
				log.Printf("[%s] send ping: %v", ts(), err)
				return nil
			}
			log.Printf("[%s] sent  ping  seq=%d", ts(), seq)
			seq++
		}
	}
}

func handleHostRecv(msg event.Message, sess *session.Manager, remoteID, remoteName string) {
	switch msg.Type {
	case event.TypePong:
		var pay event.PongPayload
		_ = event.DecodePayload(msg, &pay)
		rtt := float64(nowMs() - pay.EchoTs)
		sess.RecordLatency(remoteID, rtt)
		var avg float64
		for _, d := range sess.Devices() {
			if d.ID == remoteID {
				avg = d.AvgLatencyMs
			}
		}
		log.Printf("[%s] recv  pong  rtt=%.1fms  avg=%.1fms", ts(), rtt, avg)
	case event.TypeSwitchAck:
		log.Printf("[%s] recv  switch_ack  — now forwarding to %s", ts(), remoteName)
	default:
		log.Printf("[%s] recv  %s", ts(), msg.Type)
	}
}
