package daemon

import (
	"bufio"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/helper"
	"github.com/mousebridge/core/internal/p2p"
	"github.com/mousebridge/core/internal/transport"
)

func newSwitchProtocolPair(t *testing.T) (*Daemon, *Daemon, <-chan event.Message, <-chan event.Message) {
	t.Helper()
	d1, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	d2, err := New(newTestOptions(t))
	if err != nil {
		d1.Stop()
		t.Fatal(err)
	}
	t.Cleanup(d1.Stop)
	t.Cleanup(d2.Stop)

	d1.SessionAdd("d1-d2", d2.LocalID(), "Device 2", "127.0.0.1", "client")
	d2.SessionAdd("d2-d1", d1.LocalID(), "Device 1", "127.0.0.1", "server")
	d1ToD2 := attachSwitchTestPeer(t, d1, d2.LocalID())
	d2ToD1 := attachSwitchTestPeer(t, d2, d1.LocalID())
	return d1, d2, d1ToD2, d2ToD1
}

func requireSwitchMessages(t *testing.T, messages <-chan event.Message, want ...string) {
	t.Helper()
	for _, typ := range want {
		requireSwitchMessage(t, messages, typ)
	}
}

func requireNoSwitchMessages(t *testing.T, messages <-chan event.Message, quiet time.Duration) {
	t.Helper()
	select {
	case msg := <-messages:
		t.Fatalf("unexpected switch message %q", msg.Type)
	case <-time.After(quiet):
	}
}

func remotePeerOf(d *Daemon) string {
	d.controlMu.RLock()
	defer d.controlMu.RUnlock()
	return d.remotePeer
}

func activeLeaseIDOf(d *Daemon) string {
	d.controlMu.RLock()
	defer d.controlMu.RUnlock()
	return d.activeLeaseID
}

func connectSwitchSessionPair(t *testing.T, left, right *Daemon) {
	t.Helper()
	leftConn, rightConn := net.Pipe()
	leftTransport := transport.NewConn(leftConn)
	rightTransport := transport.NewConn(rightConn)
	t.Cleanup(func() {
		_ = leftTransport.Close()
		_ = rightTransport.Close()
	})
	go p2p.RunSession(leftTransport, right.LocalID(), "right", "client", left, func(p2p.BusEvent) {})
	go p2p.RunSession(rightTransport, left.LocalID(), "left", "server", right, func(p2p.BusEvent) {})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(left.State().Sessions) >= 1 && len(right.State().Sessions) >= 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session pair did not connect: left=%d right=%d", len(left.State().Sessions), len(right.State().Sessions))
}

type switchGrantConfirmGate struct {
	*Daemon
	started chan struct{}
	allow   chan struct{}
	once    sync.Once
}

func (g *switchGrantConfirmGate) ConfirmRemoteSwitchGrant(deviceID, requestID string) {
	g.once.Do(func() { close(g.started) })
	<-g.allow
	g.Daemon.ConfirmRemoteSwitchGrant(deviceID, requestID)
}

type switchAckDispatchGate struct {
	*Daemon
	started chan struct{}
	allow   chan struct{}
	once    sync.Once
}

type switchRequestAckRecordHost struct {
	*Daemon
	acks chan event.SwitchAckPayload
}

func (h *switchRequestAckRecordHost) HandleRemoteSwitchRequest(deviceID string, request event.SwitchRequestPayload) event.SwitchAckPayload {
	ack := h.Daemon.HandleRemoteSwitchRequest(deviceID, request)
	h.acks <- ack
	return ack
}

func (g *switchAckDispatchGate) HandleRemoteSwitchAck(deviceID string, ack event.SwitchAckPayload) {
	if ack.Granted {
		g.once.Do(func() { close(g.started) })
		<-g.allow
	}
	g.Daemon.HandleRemoteSwitchAck(deviceID, ack)
}

func connectSwitchSessionPairWithHosts(t *testing.T, left, right p2p.SessionHost, leftID, rightID string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	leftConn, rightConn := transport.NewConn(server), transport.NewConn(client)
	t.Cleanup(func() {
		_ = leftConn.Close()
		_ = rightConn.Close()
	})
	go p2p.RunSession(leftConn, rightID, "right", "client", left, func(p2p.BusEvent) {})
	go p2p.RunSession(rightConn, leftID, "left", "server", right, func(p2p.BusEvent) {})
}

func waitForCondition(t *testing.T, desc string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

func readHelperEvent(t *testing.T, scanner *bufio.Scanner) helper.Envelope {
	t.Helper()
	if !scanner.Scan() {
		t.Fatalf("read helper envelope: %v", scanner.Err())
	}
	var env helper.Envelope
	if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env
}

func readHelperEventType(t *testing.T, scanner *bufio.Scanner, want string) helper.Envelope {
	t.Helper()
	for {
		env := readHelperEvent(t, scanner)
		if env.Type == want {
			return env
		}
	}
}

func TestThreePeersGrantOneControllerAndRouteOneInputDirection(t *testing.T) {
	newDaemon := func() *Daemon {
		d, err := New(newTestOptions(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Stop)
		return d
	}
	a, b, c := newDaemon(), newDaemon(), newDaemon()
	if err := c.helper.Start(); err != nil {
		t.Fatal(err)
	}
	connectSwitchSessionPair(t, a, c)
	connectSwitchSessionPair(t, b, c)

	helperConn, err := net.Dial("unix", c.HelperSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helperConn.Close() })
	scanner := bufio.NewScanner(helperConn)
	register, err := helper.Encode(helper.TypeRegister, helper.RegisterPayload{Name: "switch-competition-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := helperConn.Write(register); err != nil {
		t.Fatal(err)
	}
	if got := readHelperEvent(t, scanner).Type; got != helper.TypeAck {
		t.Fatalf("helper registration first message=%q want ack", got)
	}
	if got := readHelperEvent(t, scanner).Type; got != helper.TypeConfigPush {
		t.Fatalf("helper registration second message=%q want config push", got)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.SwitchNext() }()
	go func() { defer wg.Done(); b.SwitchNext() }()
	wg.Wait()

	aOwns := a.ctrl.ActiveTarget() == c.LocalID()
	bOwns := b.ctrl.ActiveTarget() == c.LocalID()
	if aOwns == bOwns {
		t.Fatalf("expected exactly one granted controller: a=%q b=%q", a.ctrl.ActiveTarget(), b.ctrl.ActiveTarget())
	}
	winner, loser := a, b
	if bOwns {
		winner, loser = b, a
	}
	if c.ctrl.ActiveTarget() != c.LocalID() {
		t.Fatalf("receiver active target=%q want local %q", c.ctrl.ActiveTarget(), c.LocalID())
	}
	if remotePeerOf(c) != winner.LocalID() {
		t.Fatalf("receiver lease owner=%q want %q", remotePeerOf(c), winner.LocalID())
	}
	if !c.CanReceiveInput(winner.LocalID()) || c.CanReceiveInput(loser.LocalID()) {
		t.Fatalf("receiver input grants winner=%t loser=%t", c.CanReceiveInput(winner.LocalID()), c.CanReceiveInput(loser.LocalID()))
	}
	if len(winner.pendingSwitches) != 0 || len(loser.pendingSwitches) != 0 {
		t.Fatalf("pending requests leaked: winner=%d loser=%d", len(winner.pendingSwitches), len(loser.pendingSwitches))
	}

	winner.handleHelperInput(helper.InputPayload{Kind: "text", Text: "winner-only"})
	helperConn.SetReadDeadline(time.Now().Add(time.Second))
	env := readHelperEventType(t, scanner, helper.TypeInput)
	var delivered helper.InputPayload
	if err := helper.DecodePayload(env, &delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.Text != "winner-only" {
		t.Fatalf("delivered input=%q want winner-only", delivered.Text)
	}

	// Bypass the sender-side active-target check to verify C also rejects data
	// from the losing session at the receive boundary.
	if err := loser.SendSessionInput(c.LocalID(), helper.InputPayload{Kind: "text", Text: "loser-must-not-arrive"}); err != nil {
		t.Fatal(err)
	}
	helperConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	for scanner.Scan() {
		var extra helper.Envelope
		if err := json.Unmarshal(scanner.Bytes(), &extra); err != nil {
			t.Fatal(err)
		}
		if extra.Type == helper.TypeInput {
			t.Fatalf("losing peer input reached helper: %s", extra.Payload)
		}
	}
	if err := scanner.Err(); err != nil {
		if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
			t.Fatalf("reading helper after loser input: %v", err)
		}
	}
}

func TestRunSessionSwitchToHostRacingGrantDoesNotLeaveStaleController(t *testing.T) {
	t.Run("release arrives before sender commits grant", func(t *testing.T) {
		receiver, err := New(newTestOptions(t))
		if err != nil {
			t.Fatal(err)
		}
		sender, err := New(newTestOptions(t))
		if err != nil {
			receiver.Stop()
			t.Fatal(err)
		}
		t.Cleanup(receiver.Stop)
		t.Cleanup(sender.Stop)
		confirmGate := &switchGrantConfirmGate{Daemon: receiver, started: make(chan struct{}), allow: make(chan struct{})}
		ackGate := &switchAckDispatchGate{Daemon: sender, started: make(chan struct{}), allow: make(chan struct{})}
		connectSwitchSessionPairWithHosts(t, confirmGate, ackGate, receiver.LocalID(), sender.LocalID())
		waitForCondition(t, "both real session registrations", func() bool {
			return len(receiver.State().Sessions) == 1 && len(sender.State().Sessions) == 1
		})

		switchDone := make(chan struct{})
		go func() { sender.SwitchNext(); close(switchDone) }()
		select {
		case <-confirmGate.started:
		case <-time.After(2 * time.Second):
			t.Fatal("receiver did not reach grant confirmation")
		}
		select {
		case <-ackGate.started:
		case <-time.After(2 * time.Second):
			t.Fatal("sender did not receive grant ACK")
		}

		hostDone := make(chan struct{})
		go func() { receiver.SwitchToHost(); close(hostDone) }()
		select {
		case <-hostDone:
			t.Fatal("SwitchToHost must wait until the sent grant is confirmed")
		case <-time.After(40 * time.Millisecond):
		}
		if receiver.CanReceiveInput(sender.LocalID()) {
			t.Fatal("receiver enabled input before grant confirmation")
		}
		close(confirmGate.allow)
		select {
		case <-hostDone:
		case <-time.After(2 * time.Second):
			t.Fatal("SwitchToHost did not finish after grant confirmation")
		}
		// The correlated release is now on the session before the sender is
		// allowed to consume its grant ACK and commit the target.
		close(ackGate.allow)
		select {
		case <-switchDone:
		case <-time.After(2 * time.Second):
			t.Fatal("sender transition did not settle")
		}
		waitForCondition(t, "both peers to settle on local control", func() bool {
			return sender.ctrl.ActiveTarget() == sender.LocalID() &&
				receiver.ctrl.ActiveTarget() == receiver.LocalID() &&
				remotePeerOf(receiver) == "" && !receiver.CanReceiveInput(sender.LocalID())
		})
	})

	t.Run("release arrives after sender commits grant", func(t *testing.T) {
		receiver, err := New(newTestOptions(t))
		if err != nil {
			t.Fatal(err)
		}
		sender, err := New(newTestOptions(t))
		if err != nil {
			receiver.Stop()
			t.Fatal(err)
		}
		t.Cleanup(receiver.Stop)
		t.Cleanup(sender.Stop)
		connectSwitchSessionPair(t, receiver, sender)
		sender.SwitchNext()
		waitForCondition(t, "sender to commit granted target", func() bool {
			return sender.ctrl.ActiveTarget() == receiver.LocalID() && receiver.CanReceiveInput(sender.LocalID())
		})
		receiver.SwitchToHost()
		waitForCondition(t, "release after commit to settle both sides", func() bool {
			return sender.ctrl.ActiveTarget() == sender.LocalID() && receiver.ctrl.ActiveTarget() == receiver.LocalID() &&
				remotePeerOf(receiver) == "" && !receiver.CanReceiveInput(sender.LocalID())
		})
	})
}

func TestReleaseAckTimeoutSendsOneCleanupAndDoesNotActivateNextTarget(t *testing.T) {
	d, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	d.SessionAdd("old", "a-old", "Old owner", "127.0.0.1", "client")
	d.SessionAdd("next", "b-new", "Next target", "127.0.0.1", "client")
	d.ctrl.SwitchTo("a-old")
	d.setActiveLease("a-old", "old-lease")

	local, remote := net.Pipe()
	conn := transport.NewConn(local)
	d.TrackConn(conn, "a-old")
	t.Cleanup(func() { _ = conn.Close() })
	messages := make(chan event.Message, 8)
	grantNext := make(chan struct{}, 1)
	go func() {
		peer := transport.NewConn(remote)
		defer peer.Close()
		for {
			msg, recvErr := peer.Recv()
			if recvErr != nil {
				return
			}
			messages <- msg
			if msg.Type == event.TypeSwitchRequest {
				select {
				case <-grantNext:
					var request event.SwitchRequestPayload
					if event.DecodePayload(msg, &request) == nil {
						_ = peer.Send(event.Message{V: 1, Type: event.TypeSwitchAck, Payload: event.SwitchAckPayload{
							RequestID: request.RequestID,
							Granted:   true,
						}})
					}
				default:
				}
			}
		}
	}()
	go func() {
		for {
			msg, recvErr := conn.Recv()
			if recvErr != nil {
				return
			}
			if msg.Type == event.TypeSwitchAck {
				var ack event.SwitchAckPayload
				if event.DecodePayload(msg, &ack) == nil {
					d.HandleRemoteSwitchAck("a-old", ack)
				}
			}
		}
	}()
	nextTarget := attachSwitchTestPeer(t, d, "b-new")

	done := make(chan struct{})
	go func() { d.SwitchNext(); close(done) }()
	var first, cleanup event.Message
	select {
	case first = <-messages:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for correlated release")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("switch did not return after release ACK timeout")
	}
	select {
	case cleanup = <-messages:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for bounded cleanup release")
	}
	var firstPayload, cleanupPayload event.SwitchBackPayload
	if err := event.DecodePayload(first, &firstPayload); err != nil {
		t.Fatal(err)
	}
	if err := event.DecodePayload(cleanup, &cleanupPayload); err != nil {
		t.Fatal(err)
	}
	if first.Type != event.TypeSwitchBack || firstPayload.RequestID == "" || firstPayload.LeaseID != "old-lease" {
		t.Fatalf("first release=%+v payload=%+v", first, firstPayload)
	}
	if cleanup.Type != event.TypeSwitchBack || cleanupPayload.RequestID != "" || cleanupPayload.LeaseID != "old-lease" {
		t.Fatalf("cleanup release=%+v payload=%+v", cleanup, cleanupPayload)
	}
	if got := d.ctrl.ActiveTarget(); got != d.LocalID() {
		t.Fatalf("target after release timeout=%q want local %q", got, d.LocalID())
	}
	if len(d.pendingSwitches) != 0 {
		t.Fatalf("pending requests leaked after release timeout: %d", len(d.pendingSwitches))
	}
	if d.activeLeasePeerSnapshot() != "" || d.activeLeaseIDSnapshot() != "" {
		t.Fatalf("active lease remained after release timeout: peer=%q id=%q", d.activeLeasePeerSnapshot(), d.activeLeaseIDSnapshot())
	}
	requireNoSwitchMessages(t, nextTarget, 50*time.Millisecond)
	grantNext <- struct{}{}
	d.SwitchNext()
	if d.ctrl.ActiveTarget() != "a-old" || d.activeLeasePeerSnapshot() != "a-old" || d.activeLeaseIDSnapshot() == "" {
		t.Fatalf("fresh request after timeout was not arbitrated: target=%q lease=%q/%q", d.ctrl.ActiveTarget(), d.activeLeasePeerSnapshot(), d.activeLeaseIDSnapshot())
	}
	d.SessionRemove("old")
	if d.ctrl.ActiveTarget() != d.LocalID() || d.activeLeasePeerSnapshot() != "" || d.activeLeaseIDSnapshot() != "" {
		t.Fatalf("disconnect did not clear active lease: target=%q lease=%q/%q", d.ctrl.ActiveTarget(), d.activeLeasePeerSnapshot(), d.activeLeaseIDSnapshot())
	}
}

func attachReleaseThenRejectSwitchPeer(t *testing.T, d *Daemon, deviceID string, rejectRequest bool) {
	t.Helper()
	local, remote := net.Pipe()
	conn := transport.NewConn(local)
	d.TrackConn(conn, deviceID)
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		peer := transport.NewConn(remote)
		defer peer.Close()
		for {
			msg, err := peer.Recv()
			if err != nil {
				return
			}
			switch msg.Type {
			case event.TypeSwitchBack:
				var release event.SwitchBackPayload
				if event.DecodePayload(msg, &release) == nil && release.RequestID != "" {
					_ = peer.Send(event.Message{V: 1, Type: event.TypeSwitchAck, Payload: event.SwitchAckPayload{
						RequestID: release.RequestID,
						Released:  true,
					}})
				}
			case event.TypeSwitchRequest:
				var request event.SwitchRequestPayload
				if rejectRequest && event.DecodePayload(msg, &request) == nil {
					_ = peer.Send(event.Message{V: 1, Type: event.TypeSwitchAck, Payload: event.SwitchAckPayload{
						RequestID: request.RequestID,
						Reason:    "busy",
					}})
				}
			}
		}
	}()
	go func() {
		for {
			msg, err := conn.Recv()
			if err != nil {
				return
			}
			if msg.Type == event.TypeSwitchAck {
				var ack event.SwitchAckPayload
				if event.DecodePayload(msg, &ack) == nil {
					d.HandleRemoteSwitchAck(deviceID, ack)
				}
			}
		}
	}()
}

func TestRunSessionRejectsGrantWhileSwitchToHostTransitionIsInFlight(t *testing.T) {
	receiver, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	sender, err := New(newTestOptions(t))
	if err != nil {
		receiver.Stop()
		t.Fatal(err)
	}
	t.Cleanup(receiver.Stop)
	t.Cleanup(sender.Stop)
	receiver.SessionAdd("old-owner", "old-owner", "Old owner", "127.0.0.1", "client")
	oldLocal, oldRemote := net.Pipe()
	oldConn := transport.NewConn(oldLocal)
	receiver.TrackConn(oldConn, "old-owner")
	t.Cleanup(func() {
		_ = oldConn.Close()
		_ = oldRemote.Close()
	})
	receiver.ctrl.SwitchTo("old-owner")
	receiver.setActiveLease("old-owner", "old-owner-lease")

	host := &switchRequestAckRecordHost{Daemon: receiver, acks: make(chan event.SwitchAckPayload, 1)}
	connectSwitchSessionPairWithHosts(t, host, sender, receiver.LocalID(), sender.LocalID())
	waitForCondition(t, "real session setup", func() bool {
		return len(receiver.State().Sessions) == 2 && len(sender.State().Sessions) == 1
	})

	hostDone := make(chan struct{})
	go func() { receiver.SwitchToHost(); close(hostDone) }()
	waitForCondition(t, "local switch transition reservation", func() bool {
		receiver.controlMu.RLock()
		defer receiver.controlMu.RUnlock()
		return receiver.localTransitionPending
	})
	sender.SwitchNext()
	var ack event.SwitchAckPayload
	select {
	case ack = <-host.acks:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for inbound request result")
	}
	if ack.Granted || ack.Reason != "local_switch_transition_pending" {
		t.Fatalf("inbound request ack=%+v want local transition rejection", ack)
	}
	if got := sender.ctrl.ActiveTarget(); got != sender.LocalID() {
		t.Fatalf("sender activated despite rejected grant: target=%q", got)
	}
	if remotePeerOf(receiver) != "" {
		t.Fatalf("receiver reserved inbound lease during local transition: %q", remotePeerOf(receiver))
	}
	_ = oldRemote.Close()
	select {
	case <-hostDone:
	case <-time.After(2 * time.Second):
		t.Fatal("SwitchToHost did not finish after old session closed")
	}
	if got := receiver.ctrl.ActiveTarget(); got != receiver.LocalID() {
		t.Fatalf("receiver target=%q want local %q", got, receiver.LocalID())
	}
}

func TestFailedReverseHandoffBroadcastsLocalHelperConfigAndRestoresInput(t *testing.T) {
	t.Run("request rejected", func(t *testing.T) { runReverseHandoffFailureHelperTest(t, true) })
	t.Run("request times out", func(t *testing.T) { runReverseHandoffFailureHelperTest(t, false) })
}

func runReverseHandoffFailureHelperTest(t *testing.T, rejectRequest bool) {
	t.Helper()
	d, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.helper.Start(); err != nil {
		t.Fatal(err)
	}
	d.SessionAdd("peer-session", "peer", "Peer", "127.0.0.1", "client")
	attachReleaseThenRejectSwitchPeer(t, d, "peer", rejectRequest)
	if ack := d.HandleRemoteSwitchRequest("peer", event.SwitchRequestPayload{RequestID: "prior-lease", Trigger: "hotkey"}); !ack.Granted {
		t.Fatalf("initial control grant rejected: %+v", ack)
	}
	d.ConfirmRemoteSwitchGrant("peer", "prior-lease")

	helperConn, err := net.Dial("unix", d.HelperSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helperConn.Close() })
	scanner := bufio.NewScanner(helperConn)
	register, err := helper.Encode(helper.TypeRegister, helper.RegisterPayload{Name: "reverse-handoff-reject-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := helperConn.Write(register); err != nil {
		t.Fatal(err)
	}
	readHelperEventType(t, scanner, helper.TypeConfigPush)
	if !d.helperConfigSnapshot().ControlledByRemote {
		t.Fatal("precondition: helper config should report remote control")
	}

	d.SwitchNext()
	if d.ctrl.ActiveTarget() != d.LocalID() {
		t.Fatalf("rejected reverse request changed target to %q", d.ctrl.ActiveTarget())
	}
	if d.CanReceiveInput("peer") || d.helperConfigSnapshot().ControlledByRemote {
		t.Fatal("released reverse lease remained active locally")
	}
	updated := readHelperEventType(t, scanner, helper.TypeConfigPush)
	var config helper.ConfigPushPayload
	if err := json.Unmarshal(updated.Payload, &config); err != nil {
		t.Fatal(err)
	}
	if config.ControlledByRemote {
		t.Fatal("helper kept stale controlled_by_remote=true after failed reverse request")
	}

	if d.CanReceiveInput("peer") {
		t.Fatal("remote input gate remained open after rejected reverse request")
	}
}

func TestSwitchProtocolExplicitReverseHandoffAndReturn(t *testing.T) {
	d1, d2, d1ToD2, d2ToD1 := newSwitchProtocolPair(t)

	// Device 1 explicitly selects device 2. The receiver marks the source but
	// does not change its own active target.
	d1.SwitchNext()
	requireSwitchMessages(t, d1ToD2, event.TypeSwitchRequest)
	firstLease := activeLeaseIDOf(d1)
	d2.HandleRemoteSwitchRequest(d1.LocalID(), event.SwitchRequestPayload{RequestID: firstLease, Trigger: "hotkey"})
	d2.ConfirmRemoteSwitchGrant(d1.LocalID(), firstLease)
	if got := d1.ctrl.ActiveTarget(); got != d2.LocalID() {
		t.Fatalf("controller target=%q want remote %q", got, d2.LocalID())
	}
	if got := d2.ctrl.ActiveTarget(); got != d2.LocalID() {
		t.Fatalf("receiver target=%q want its own device %q", got, d2.LocalID())
	}
	if got := remotePeerOf(d2); got != d1.LocalID() {
		t.Fatalf("receiver remotePeer=%q want controller %q", got, d1.LocalID())
	}
	if !d2.helperConfigSnapshot().ControlledByRemote {
		t.Fatal("receiver helper config must mark remote control")
	}

	// Repeated delivery is idempotent and does not create a reverse handoff.
	for i := 0; i < 100; i++ {
		d2.HandleRemoteSwitchRequest(d1.LocalID(), event.SwitchRequestPayload{RequestID: firstLease, Trigger: "hotkey"})
	}
	requireNoSwitchMessages(t, d2ToD1, 25*time.Millisecond)
	if got := d2.ctrl.ActiveTarget(); got != d2.LocalID() {
		t.Fatalf("duplicate request changed receiver target to %q", got)
	}

	// A user hotkey on device 2 is an explicit reverse handoff: release the old
	// direction first, then request control in the opposite direction.
	d2.SwitchNext()
	requireSwitchMessages(t, d2ToD1, event.TypeSwitchBack, event.TypeSwitchRequest)
	d1.HandleRemoteSwitchBack(d2.LocalID(), event.SwitchBackPayload{LeaseID: firstLease})
	secondLease := activeLeaseIDOf(d2)
	d1.HandleRemoteSwitchRequest(d2.LocalID(), event.SwitchRequestPayload{RequestID: secondLease, Trigger: "hotkey"})
	d1.ConfirmRemoteSwitchGrant(d2.LocalID(), secondLease)
	if got := d2.ctrl.ActiveTarget(); got != d1.LocalID() {
		t.Fatalf("reverse controller target=%q want %q", got, d1.LocalID())
	}
	if got := d1.ctrl.ActiveTarget(); got != d1.LocalID() {
		t.Fatalf("reverse receiver target=%q want its own device %q", got, d1.LocalID())
	}
	if got := remotePeerOf(d1); got != d2.LocalID() {
		t.Fatalf("reverse receiver remotePeer=%q want controller %q", got, d2.LocalID())
	}
	if got := remotePeerOf(d2); got != "" {
		t.Fatalf("former receiver still marks remotePeer=%q", got)
	}

	// The active controller can return to the remote device exactly once.
	d2.SwitchToHost()
	requireSwitchMessages(t, d2ToD1, event.TypeSwitchBack)
	d1.HandleRemoteSwitchBack(d2.LocalID(), event.SwitchBackPayload{LeaseID: secondLease})
	for i := 0; i < 100; i++ {
		d1.HandleRemoteSwitchBack(d2.LocalID(), event.SwitchBackPayload{LeaseID: secondLease})
		d2.HandleRemoteSwitchBack(d1.LocalID(), event.SwitchBackPayload{LeaseID: secondLease})
	}
	if d1.ctrl.ActiveTarget() != d1.LocalID() || d2.ctrl.ActiveTarget() != d2.LocalID() {
		t.Fatalf("return did not settle locally: d1=%q d2=%q", d1.ctrl.ActiveTarget(), d2.ctrl.ActiveTarget())
	}
	if remotePeerOf(d1) != "" || remotePeerOf(d2) != "" {
		t.Fatalf("return left control markers: d1=%q d2=%q", remotePeerOf(d1), remotePeerOf(d2))
	}
	requireNoSwitchMessages(t, d1ToD2, 25*time.Millisecond)
	requireNoSwitchMessages(t, d2ToD1, 25*time.Millisecond)
}

func TestSwitchProtocolConcurrentReturnEdgesSendOneBackAndStaySettled(t *testing.T) {
	d1, d2, d1ToD2, d2ToD1 := newSwitchProtocolPair(t)
	d1.SwitchNext()
	requireSwitchMessages(t, d1ToD2, event.TypeSwitchRequest)
	leaseID := activeLeaseIDOf(d1)
	d2.HandleRemoteSwitchRequest(d1.LocalID(), event.SwitchRequestPayload{RequestID: leaseID, Trigger: "edge", Edge: "right"})
	d2.ConfirmRemoteSwitchGrant(d1.LocalID(), leaseID)
	d2.ctrl.SetDwell(0)

	// Let the helper neutral/cooldown interval pass, then replay the remote
	// return edge concurrently as if queued callbacks arrived in a burst.
	time.Sleep(controlTransitionCooldown + 20*time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d2.handleHelperEdge(helper.EdgePayload{Edge: "left", Pct: 0.5})
		}()
	}
	wg.Wait()
	requireSwitchMessages(t, d2ToD1, event.TypeSwitchBack)
	d1.HandleRemoteSwitchBack(d2.LocalID(), event.SwitchBackPayload{LeaseID: leaseID})

	// Replayed edge events after the one-shot return must not be reinterpreted
	// as a fresh request in the opposite direction.
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d2.handleHelperEdge(helper.EdgePayload{Edge: "right", Pct: 0.5})
		}()
	}
	wg.Wait()
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d2.HandleRemoteSwitchBack(d1.LocalID(), event.SwitchBackPayload{LeaseID: leaseID})
		}()
	}
	wg.Wait()
	requireNoSwitchMessages(t, d2ToD1, 50*time.Millisecond)
	if d1.ctrl.ActiveTarget() != d1.LocalID() || d2.ctrl.ActiveTarget() != d2.LocalID() {
		t.Fatalf("edge replay changed settled targets: d1=%q d2=%q", d1.ctrl.ActiveTarget(), d2.ctrl.ActiveTarget())
	}
	if remotePeerOf(d1) != "" || remotePeerOf(d2) != "" {
		t.Fatalf("edge replay restored control markers: d1=%q d2=%q", remotePeerOf(d1), remotePeerOf(d2))
	}
}

func TestSwitchProtocolDisconnectClearsControlState(t *testing.T) {
	d1, d2, d1ToD2, d2ToD1 := newSwitchProtocolPair(t)
	d1.SwitchNext()
	requireSwitchMessages(t, d1ToD2, event.TypeSwitchRequest)
	d2.HandleRemoteSwitchRequest(d1.LocalID(), event.SwitchRequestPayload{RequestID: "d1-1", Trigger: "hotkey"})

	d2.SessionRemove("d2-d1")
	if got := d2.ctrl.ActiveTarget(); got != d2.LocalID() {
		t.Fatalf("disconnected receiver target=%q want local %q", got, d2.LocalID())
	}
	if got := remotePeerOf(d2); got != "" || d2.helperConfigSnapshot().ControlledByRemote {
		t.Fatalf("receiver retained remote control after disconnect: peer=%q controlled=%v", got, d2.helperConfigSnapshot().ControlledByRemote)
	}

	d1.SessionRemove("d1-d2")
	if got := d1.ctrl.ActiveTarget(); got != d1.LocalID() {
		t.Fatalf("disconnected controller target=%q want local %q", got, d1.LocalID())
	}
	if d1.activeLeasePeerSnapshot() != "" || d1.activeLeaseIDSnapshot() != "" {
		t.Fatalf("controller retained active lease after disconnect: peer=%q id=%q", d1.activeLeasePeerSnapshot(), d1.activeLeaseIDSnapshot())
	}
	requireNoSwitchMessages(t, d2ToD1, 25*time.Millisecond)
}

func TestSwitchRequestLegacyEmptyAckTimesOutAndRollsBack(t *testing.T) {
	d, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	d.SessionAdd("legacy", "legacy-peer", "Legacy", "127.0.0.1", "client")
	peer := attachLegacyAckTestPeer(t, d, "legacy-peer")
	done := make(chan struct{})
	go func() {
		d.SwitchNext()
		close(done)
	}()
	requireSwitchMessages(t, peer, event.TypeSwitchRequest)
	if got := d.ctrl.ActiveTarget(); got != d.LocalID() {
		t.Fatalf("target changed before correlated grant: %q", got)
	}
	select {
	case <-done:
		t.Fatal("legacy empty switch_ack was treated as a grant")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-done:
	case <-time.After(switchRequestTimeout + time.Second):
		t.Fatal("switch request did not time out")
	}
	requireSwitchMessages(t, peer, event.TypeSwitchBack)
	if got := d.ctrl.ActiveTarget(); got != d.LocalID() {
		t.Fatalf("target after timeout=%q want local %q", got, d.LocalID())
	}
	if len(d.pendingSwitches) != 0 {
		t.Fatalf("pending request leaked after timeout: %d", len(d.pendingSwitches))
	}
}

func TestSwitchRequestDisconnectCancelsPendingWait(t *testing.T) {
	d, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	d.SessionAdd("peer", "peer", "Peer", "127.0.0.1", "client")
	peer := attachLegacyAckTestPeer(t, d, "peer")
	done := make(chan struct{})
	go func() {
		d.SwitchNext()
		close(done)
	}()
	requireSwitchMessages(t, peer, event.TypeSwitchRequest)
	d.SessionRemove("peer")
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("disconnect did not cancel pending switch request")
	}
	if got := d.ctrl.ActiveTarget(); got != d.LocalID() {
		t.Fatalf("target after disconnect=%q want local %q", got, d.LocalID())
	}
	if len(d.pendingSwitches) != 0 {
		t.Fatalf("pending request leaked after disconnect: %d", len(d.pendingSwitches))
	}
}

func TestRejectedReleaseKeepsNewTargetPaused(t *testing.T) {
	d, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	d.SessionAdd("a-old", "a-old", "Old owner", "127.0.0.1", "client")
	d.SessionAdd("b-new", "b-new", "Next target", "127.0.0.1", "client")
	oldOwner := attachRejectingReleaseTestPeer(t, d, "a-old")
	nextTarget := attachSwitchTestPeer(t, d, "b-new")
	d.ctrl.SwitchTo("a-old")

	d.SwitchNext()
	requireSwitchMessages(t, oldOwner, event.TypeSwitchBack)
	if got := d.ctrl.ActiveTarget(); got != d.LocalID() {
		t.Fatalf("target after rejected release=%q want local %q", got, d.LocalID())
	}
	requireNoSwitchMessages(t, nextTarget, 50*time.Millisecond)
	if len(d.pendingSwitches) != 0 {
		t.Fatalf("pending requests leaked after rejected release: %d", len(d.pendingSwitches))
	}
}

func TestLegacySwitchRequestIsRejectedAndCannotSendInput(t *testing.T) {
	d, err := New(newTestOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	d.SessionAdd("legacy", "legacy-peer", "Legacy", "127.0.0.1", "client")
	ack := d.HandleRemoteSwitchRequest("legacy-peer", event.SwitchRequestPayload{Trigger: "hotkey"})
	if ack.Granted || ack.Reason != "request_id_required" {
		t.Fatalf("legacy request ack=%+v want explicit request_id_required rejection", ack)
	}
	if d.CanReceiveInput("legacy-peer") {
		t.Fatal("legacy peer without correlated grant must not inject input")
	}
	if got := d.ctrl.ActiveTarget(); got != d.LocalID() {
		t.Fatalf("legacy request changed active target to %q", got)
	}
}
