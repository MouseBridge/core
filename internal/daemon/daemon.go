package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/device"
	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/helper"
	"github.com/mousebridge/core/internal/localhelper"
	"github.com/mousebridge/core/internal/p2p"
	"github.com/mousebridge/core/internal/pending"
	"github.com/mousebridge/core/internal/remembered"
	switch_ "github.com/mousebridge/core/internal/switch"
	"github.com/mousebridge/core/internal/transport"
)

// Options configures the daemon at startup.
type Options struct {
	ConfigPath string
	DataDir    string
}

// Daemon owns all long-running state.
type Daemon struct {
	configPath string
	dataDir    string
	cfg        *config.Config
	identity   device.Identity

	rem         *remembered.Store
	pending     *pending.Manager
	tracker     *p2p.OutboundTracker
	helper      *helper.Manager
	localHelper *localhelper.Runtime
	ctrl        *switch_.Controller

	pausedMu sync.RWMutex
	paused   bool

	sessMu   sync.RWMutex
	sessions map[string]*sessionEntry // keyed by connectionID

	connsMu sync.Mutex
	conns   map[*transport.Conn]string // conn → deviceID (empty until authenticated)

	subsMu sync.RWMutex
	subs   map[chan BusEvent]struct{}

	// httpConnCh receives raw HTTP connections from the MUX.
	httpConnCh chan net.Conn

	ln   net.Listener
	lnMu sync.Mutex

	cancel context.CancelFunc
}

type sessionEntry struct {
	ConnectionID string
	DeviceID     string
	Name         string
	IP           string
	Role         string
	AvgLatencyMs float64
	latencies    []float64
}

// New creates and initialises a Daemon.
func New(opts Options) (*Daemon, error) {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("daemon: load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}

	identity, err := device.LoadOrCreate(
		opts.DataDir+"/device.json",
		func() string { return config.DeriveDeviceID(cfg.Port) },
		cfg.DeviceName,
	)
	if err != nil {
		return nil, fmt.Errorf("daemon: device identity: %w", err)
	}
	if cfg.DeviceName == "" {
		cfg.DeviceName = identity.Name
	}

	rem, err := remembered.New(opts.DataDir + "/remembered.json")
	if err != nil {
		log.Warnf("[daemon] remembered store load failed: %v — starting empty", err)
		rem, _ = remembered.New("")
	}

	ttl := time.Duration(cfg.PairingPINTTLSeconds) * time.Second
	pm := pending.New(ttl, cfg.PairingPINMaxAttempts)

	d := &Daemon{
		configPath: opts.ConfigPath,
		dataDir:    opts.DataDir,
		cfg:        cfg,
		identity:   identity,
		rem:        rem,
		pending:    pm,
		tracker:    p2p.NewOutboundTracker(),
		sessions:   make(map[string]*sessionEntry),
		conns:      make(map[*transport.Conn]string),
		subs:       make(map[chan BusEvent]struct{}),
		httpConnCh: make(chan net.Conn, 64),
	}
	d.ctrl = switch_.NewController(identity.DeviceID)
	d.ctrl.OnSwitch = d.handleSwitchEvent
	d.helper = helper.NewManager(
		helperSocketPath(opts.DataDir, cfg.Port),
		d.helperConfigSnapshot,
		d.handleHelperHotkey,
		d.handleHelperEdge,
		d.handleHelperInput,
	)
	d.localHelper = localhelper.NewRuntime(opts.DataDir, d.helper.ClientCount)
	return d, nil
}

// Start begins listening on listen_host:port and starts background goroutines.
func (d *Daemon) Start() error {
	if err := d.helper.Start(); err != nil {
		return err
	}

	addr := fmt.Sprintf("%s:%d", d.cfg.ListenHost, d.cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		d.helper.Stop()
		return fmt.Errorf("daemon: listen %s: %w", addr, err)
	}
	d.lnMu.Lock()
	d.ln = ln
	d.lnMu.Unlock()

	if d.cfg.UnsafeHTTPLAN {
		log.Warn("WARNING: unsafe_http_lan is enabled. MouseBridge HTTP API has no authentication and is reachable from the LAN.")
	}
	log.Printf("[daemon] listening on %s (device_id=%s name=%s)", addr, d.identity.DisplayID, d.identity.Name)

	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel

	// GC expired pending pairs and emit pair_timeout events.
	go d.pending.RunGC(ctx, func(e *pending.Entry) {
		d.broadcast(BusEvent{Kind: "pair_timeout", PairingID: e.PairingID})
		log.Printf("[daemon] pairing timed out: %s (%s)", e.Name, e.PairingID)
	})

	go transport.Serve(ln, d.handleP2P, d.handleHTTP)

	d.broadcast(BusEvent{Kind: "listening"})
	d.helper.BroadcastConfig()
	return nil
}

// Stop shuts down the daemon.
func (d *Daemon) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
	if d.helper != nil {
		d.helper.Stop()
	}
	d.lnMu.Lock()
	if d.ln != nil {
		_ = d.ln.Close()
		d.ln = nil
	}
	d.lnMu.Unlock()

	d.connsMu.Lock()
	for c := range d.conns {
		_ = c.Close()
	}
	d.conns = make(map[*transport.Conn]string)
	d.connsMu.Unlock()

	close(d.httpConnCh)

	d.subsMu.Lock()
	for ch := range d.subs {
		close(ch)
	}
	d.subs = make(map[chan BusEvent]struct{})
	d.subsMu.Unlock()
}

// HTTPConnCh returns the channel of raw HTTP connections for the API server.
func (d *Daemon) HTTPConnCh() <-chan net.Conn { return d.httpConnCh }

func (d *Daemon) handleHTTP(c net.Conn) {
	select {
	case d.httpConnCh <- c:
	default:
		log.Printf("[daemon] httpConnCh full, dropping %s", c.RemoteAddr())
		_ = c.Close()
	}
}

func (d *Daemon) handleP2P(c *transport.Conn) {
	go p2p.HandleInbound(c, d.identity.DeviceID, d, d, d.emitBus)
}

// p2p.ServerHost interface
func (d *Daemon) LocalID() string                    { return d.identity.DeviceID }
func (d *Daemon) LocalName() string                  { return d.identity.Name }
func (d *Daemon) LocalDisplayID() string             { return d.identity.DisplayID }
func (d *Daemon) PendingManager() *pending.Manager   { return d.pending }
func (d *Daemon) RememberedStore() *remembered.Store { return d.rem }
func (d *Daemon) RememberedEnabled() bool            { return d.cfg.RememberedEnabled }

// p2p.SessionHost interface
func (d *Daemon) TrackConn(c *transport.Conn, deviceID string) {
	d.connsMu.Lock()
	d.conns[c] = deviceID
	d.connsMu.Unlock()
}

func (d *Daemon) UntrackConn(c *transport.Conn) {
	d.connsMu.Lock()
	delete(d.conns, c)
	d.connsMu.Unlock()
}

func (d *Daemon) SessionAdd(connectionID, deviceID, name, ip, role string) {
	d.sessMu.Lock()
	d.sessions[connectionID] = &sessionEntry{
		ConnectionID: connectionID,
		DeviceID:     deviceID,
		Name:         name,
		IP:           ip,
		Role:         role,
	}
	d.sessMu.Unlock()
	d.helper.BroadcastConfig()
}

func (d *Daemon) SessionRemove(connectionID string) {
	d.sessMu.Lock()
	delete(d.sessions, connectionID)
	d.sessMu.Unlock()
	d.helper.BroadcastConfig()
}

func (d *Daemon) SessionRecordLatency(connectionID string, ms float64) {
	d.sessMu.Lock()
	s, ok := d.sessions[connectionID]
	if ok {
		s.latencies = append(s.latencies, ms)
		if len(s.latencies) > 20 {
			s.latencies = s.latencies[len(s.latencies)-20:]
		}
		var sum float64
		for _, v := range s.latencies {
			sum += v
		}
		s.AvgLatencyMs = sum / float64(len(s.latencies))
	}
	d.sessMu.Unlock()
}

func (d *Daemon) closeConnByDeviceID(deviceID string) {
	d.connsMu.Lock()
	var target *transport.Conn
	for c, id := range d.conns {
		if id == deviceID {
			target = c
			break
		}
	}
	d.connsMu.Unlock()
	if target != nil {
		_ = target.Close()
	}
}

// DisconnectDevice closes the active connection for the given remote device.
func (d *Daemon) DisconnectDevice(deviceID string) error {
	d.connsMu.Lock()
	var target *transport.Conn
	for c, id := range d.conns {
		if id == deviceID {
			target = c
			break
		}
	}
	d.connsMu.Unlock()
	if target == nil {
		return fmt.Errorf("no active session for device %q", deviceID)
	}
	return target.Close()
}

// Connect dials a remote daemon and starts the client-side pairing flow.
func (d *Daemon) Connect(host string, port int) {
	go func() {
		dialTimeout := time.Duration(d.cfg.ConnectTimeoutSeconds) * time.Second
		c, err := transport.DialTimeout(host, port, dialTimeout)
		if err != nil {
			d.broadcast(BusEvent{Kind: "error", Msg: "connect: " + err.Error()})
			return
		}
		p2p.DialAndPair(c, d.identity.DeviceID, d.identity.DisplayID, d.identity.Name, d, d.tracker, d.rem, d.emitBus)
	}()
}

// SendPIN forwards a PIN to the pending outbound pairing connection.
func (d *Daemon) SendPIN(pairingID, pin string) error {
	op, ok := d.tracker.Get(pairingID)
	if !ok {
		return fmt.Errorf("no outbound pairing for pairing_id %s", pairingID)
	}
	return op.Conn.Send(buildPairConfirm(pairingID, pin))
}

// RejectOutbound rejects an outbound pairing by closing its connection.
func (d *Daemon) RejectOutbound(pairingID string) {
	op, ok := d.tracker.Get(pairingID)
	if !ok {
		return
	}
	d.tracker.Remove(pairingID)
	_ = op.Conn.Close()
}

// RejectInbound rejects an inbound pairing (server side).
func (d *Daemon) RejectInbound(pairingID string) {
	d.pending.Reject(pairingID)
}

func (d *Daemon) emitBus(ev p2p.BusEvent) {
	d.broadcast(BusEvent(ev))
	switch ev.Kind {
	case "pair_request":
		log.Printf("[daemon] pair_request from %s (pairing_id=%s)", ev.Name, ev.PairingID)
	case "paired":
		log.Printf("[daemon] paired with %s", ev.Name)
	case "session_connected":
		log.Printf("[daemon] session connected: %s (%s) role=%s", ev.Name, ev.DeviceID[:12], ev.Role)
	case "session_disconnected":
		log.Printf("[daemon] session disconnected: %s", ev.Name)
	case "error":
		log.Printf("[daemon] error: %s", ev.Msg)
	}
}

// Subscribe returns a channel that receives every broadcasted BusEvent.
func (d *Daemon) Subscribe() chan BusEvent {
	ch := make(chan BusEvent, 64)
	d.subsMu.Lock()
	d.subs[ch] = struct{}{}
	d.subsMu.Unlock()
	return ch
}

// Unsubscribe removes and closes the channel.
func (d *Daemon) Unsubscribe(ch chan BusEvent) {
	d.subsMu.Lock()
	_, exists := d.subs[ch]
	delete(d.subs, ch)
	d.subsMu.Unlock()
	if exists {
		close(ch)
	}
}

func (d *Daemon) broadcast(ev BusEvent) {
	d.subsMu.RLock()
	subs := make([]chan BusEvent, 0, len(d.subs))
	for ch := range d.subs {
		subs = append(subs, ch)
	}
	d.subsMu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Addr returns the listener address (empty if not listening).
func (d *Daemon) Addr() string {
	d.lnMu.Lock()
	defer d.lnMu.Unlock()
	if d.ln == nil {
		return ""
	}
	return d.ln.Addr().String()
}

// Config returns the daemon config.
func (d *Daemon) Config() *config.Config { return d.cfg }

// DataDir returns the daemon data directory.
func (d *Daemon) DataDir() string { return d.dataDir }

// UpdateHotkeys replaces the daemon hotkeys, persists them, and pushes them to helpers.
func (d *Daemon) UpdateHotkeys(h config.Hotkeys) error {
	d.cfg.Hotkeys = h
	if d.configPath != "" {
		if err := d.cfg.Save(d.configPath); err != nil {
			return err
		}
	}
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
	return nil
}

// Identity returns the device identity.
func (d *Daemon) Identity() device.Identity { return d.identity }

// RememberedList returns all remembered devices as DTOs (no secrets).
func (d *Daemon) RememberedList() []remembered.DTO { return d.rem.List() }

// RememberedForget removes a remembered device by device_id.
func (d *Daemon) RememberedForget(deviceID string) error { return d.rem.Remove(deviceID) }

// RememberedRename updates the alias of a remembered device.
func (d *Daemon) RememberedRename(deviceID, alias string) error { return d.rem.Rename(deviceID, alias) }

// HelperSocketPath returns the daemon-side helper IPC socket path.
func (d *Daemon) HelperSocketPath() string {
	if d.helper == nil {
		return ""
	}
	return d.helper.SocketPath()
}

// LocalHelperStatus returns the current local helper runtime status.
func (d *Daemon) LocalHelperStatus() localhelper.Status {
	if d.localHelper == nil {
		return localhelper.Status{}
	}
	return d.localHelper.Status()
}

// InstallLocalHelper installs or refreshes the helper LaunchAgent.
func (d *Daemon) InstallLocalHelper() error {
	if d.localHelper == nil {
		return fmt.Errorf("local helper runtime is not configured")
	}
	return d.localHelper.Install()
}

// RestartLocalHelper restarts the helper LaunchAgent.
func (d *Daemon) RestartLocalHelper() error {
	if d.localHelper == nil {
		return fmt.Errorf("local helper runtime is not configured")
	}
	return d.localHelper.Restart()
}

// OpenLocalHelperAccessibility opens the macOS Accessibility settings pane.
func (d *Daemon) OpenLocalHelperAccessibility() error {
	if d.localHelper == nil {
		return fmt.Errorf("local helper runtime is not configured")
	}
	return d.localHelper.OpenAccessibility()
}

func (d *Daemon) helperConfigSnapshot() helper.ConfigPushPayload {
	d.sessMu.RLock()
	sessions := make([]helper.SessionInfo, 0, len(d.sessions))
	for _, s := range d.sessions {
		sessions = append(sessions, helper.SessionInfo{
			ConnectionID: s.ConnectionID,
			DeviceID:     s.DeviceID,
			Name:         s.Name,
			Role:         s.Role,
		})
	}
	d.sessMu.RUnlock()

	return helper.ConfigPushPayload{
		Daemon: helper.DaemonInfo{
			DeviceID:  d.identity.DeviceID,
			DisplayID: d.identity.DisplayID,
			Name:      d.identity.Name,
		},
		Sessions: sessions,
		Hotkeys: map[string]string{
			"switch_next":    d.cfg.Hotkeys.SwitchNext,
			"switch_prev":    d.cfg.Hotkeys.SwitchPrev,
			"switch_to_host": d.cfg.Hotkeys.SwitchToHost,
			"disconnect_all": d.cfg.Hotkeys.DisconnectAll,
			"toggle_pause":   d.cfg.Hotkeys.TogglePause,
		},
		EdgeTargets: map[string]string{
			"left":   d.cfg.EdgeTargets.Left,
			"right":  d.cfg.EdgeTargets.Right,
			"top":    d.cfg.EdgeTargets.Top,
			"bottom": d.cfg.EdgeTargets.Bottom,
		},
		ActiveTarget: d.ctrl.ActiveTarget(),
		Paused:       d.Paused(),
	}
}

func (d *Daemon) handleHelperHotkey(ev helper.HotkeyPayload) {
	switch ev.Action {
	case "switch_next":
		d.switchRelative(1)
	case "switch_prev":
		d.switchRelative(-1)
	case "switch_to_host":
		d.ctrl.SwitchBack()
	case "disconnect_all":
		d.disconnectAll()
	case "toggle_pause":
		d.togglePause()
	}
	d.broadcast(BusEvent{
		Kind: "log",
		Msg:  fmt.Sprintf("helper hotkey action=%s combo=%s", ev.Action, ev.Combo),
	})
}

func (d *Daemon) handleHelperEdge(ev helper.EdgePayload) {
	if d.Paused() || d.ctrl.ActiveTarget() != d.identity.DeviceID {
		return
	}

	target := d.edgeTargetFor(ev.Edge)
	if target == "" || !d.hasSessionDevice(target) {
		return
	}

	if d.ctrl.TryEdgeSwitch(target, ev.Edge, ev.Pct) {
		d.broadcast(BusEvent{
			Kind: "log",
			Msg:  fmt.Sprintf("helper edge switch edge=%s target=%s pct=%.3f", ev.Edge, target, ev.Pct),
		})
		if d.helper != nil {
			d.helper.BroadcastConfig()
		}
	}
}

func (d *Daemon) handleHelperInput(input helper.InputPayload) {
	if d.Paused() {
		return
	}

	target := d.ctrl.ActiveTarget()
	if target == "" || target == d.identity.DeviceID {
		return
	}

	if err := d.SendSessionInput(target, input); err != nil {
		d.broadcast(BusEvent{Kind: "error", Msg: "helper input route: " + err.Error()})
	}
}

// PushHelperInput injects a local input event via any connected helper.
func (d *Daemon) PushHelperInput(input helper.InputPayload) {
	if d.helper != nil {
		d.helper.BroadcastInput(input)
	}
}

// SendSessionInput sends one input event to an active remote session.
func (d *Daemon) SendSessionInput(deviceID string, input helper.InputPayload) error {
	msg, err := sessionInputMessage(input)
	if err != nil {
		return err
	}

	d.connsMu.Lock()
	defer d.connsMu.Unlock()
	for c, id := range d.conns {
		if deviceID != "" && id != deviceID {
			continue
		}
		return c.Send(msg)
	}
	return fmt.Errorf("no active session for device %q", deviceID)
}

func helperSocketPath(dataDir string, port int) string {
	sum := sha256.Sum256([]byte(dataDir))
	suffix := hex.EncodeToString(sum[:4])
	return filepath.Join(os.TempDir(), fmt.Sprintf("mb-helper-%d-%s.sock", port, suffix))
}

func sessionInputMessage(input helper.InputPayload) (event.Message, error) {
	base := event.Message{V: 1, Seq: time.Now().UnixMilli(), Ts: time.Now().UnixMilli()}
	switch input.Kind {
	case "mouse_move":
		base.Type = event.TypeMouseMove
		base.Payload = event.MouseMovePayload{DX: input.DX, DY: input.DY, Button: input.Button}
	case "mouse_button":
		base.Type = event.TypeMouseButton
		base.Payload = event.MouseButtonPayload{Button: input.Button, Pressed: input.Pressed}
	case "key_down":
		base.Type = event.TypeKeyDown
		base.Payload = event.KeyDownPayload{Code: int(input.KeyCode), Mods: int(input.Modifiers)}
	case "key_up":
		base.Type = event.TypeKeyUp
		base.Payload = event.KeyUpPayload{Code: int(input.KeyCode), Mods: int(input.Modifiers)}
	case "key_tap":
		base.Type = event.TypeKeyDown
		base.Payload = event.KeyDownPayload{Code: int(input.KeyCode), Mods: int(input.Modifiers)}
	case "scroll":
		base.Type = event.TypeScroll
		base.Payload = event.ScrollPayload{DX: input.DX, DY: input.DY}
	default:
		return event.Message{}, fmt.Errorf("unsupported session input kind %q", input.Kind)
	}
	return base, nil
}

func (d *Daemon) Paused() bool {
	d.pausedMu.RLock()
	defer d.pausedMu.RUnlock()
	return d.paused
}

func (d *Daemon) togglePause() {
	d.pausedMu.Lock()
	d.paused = !d.paused
	paused := d.paused
	d.pausedMu.Unlock()
	d.broadcast(BusEvent{Kind: "log", Msg: fmt.Sprintf("helper pause toggled paused=%t", paused)})
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
}

func (d *Daemon) disconnectAll() {
	d.connsMu.Lock()
	conns := make([]*transport.Conn, 0, len(d.conns))
	for c := range d.conns {
		conns = append(conns, c)
	}
	d.connsMu.Unlock()

	for _, c := range conns {
		_ = c.Close()
	}
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
}

func (d *Daemon) switchRelative(step int) {
	targets := d.switchTargets()
	if len(targets) == 0 {
		d.ctrl.SwitchBack()
		return
	}

	current := d.ctrl.ActiveTarget()
	idx := slices.Index(targets, current)
	if idx < 0 {
		idx = 0
	}
	next := (idx + step + len(targets)) % len(targets)
	d.ctrl.SwitchTo(targets[next])
}

func (d *Daemon) switchTargets() []string {
	d.sessMu.RLock()
	targets := make([]string, 0, len(d.sessions)+1)
	targets = append(targets, d.identity.DeviceID)
	seen := map[string]struct{}{d.identity.DeviceID: {}}
	for _, s := range d.sessions {
		if _, ok := seen[s.DeviceID]; ok {
			continue
		}
		seen[s.DeviceID] = struct{}{}
		targets = append(targets, s.DeviceID)
	}
	d.sessMu.RUnlock()
	slices.Sort(targets[1:])
	return targets
}

func (d *Daemon) edgeTargetFor(edge string) string {
	switch edge {
	case "left":
		return d.cfg.EdgeTargets.Left
	case "right":
		return d.cfg.EdgeTargets.Right
	case "top":
		return d.cfg.EdgeTargets.Top
	case "bottom":
		return d.cfg.EdgeTargets.Bottom
	default:
		return ""
	}
}

func (d *Daemon) hasSessionDevice(deviceID string) bool {
	d.sessMu.RLock()
	defer d.sessMu.RUnlock()
	for _, s := range d.sessions {
		if s.DeviceID == deviceID {
			return true
		}
	}
	return false
}

func (d *Daemon) handleSwitchEvent(ev switch_.SwitchEvent) {
	d.broadcast(BusEvent{
		Kind: "log",
		Msg:  fmt.Sprintf("switch target=%s trigger=%s", ev.TargetID, ev.Trigger),
	})
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
}
