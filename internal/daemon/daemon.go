package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/device"
	"github.com/mousebridge/core/internal/event"
	"github.com/mousebridge/core/internal/helper"
	"github.com/mousebridge/core/internal/localhelper"
	"github.com/mousebridge/core/internal/locallab"
	"github.com/mousebridge/core/internal/localvalidate"
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

	rem                    *remembered.Store
	pending                *pending.Manager
	tracker                *p2p.OutboundTracker
	helper                 *helper.Manager
	localHelper            *localhelper.Runtime
	localLab               *locallab.Runner
	localValidation        *localvalidate.Runner
	ctrl                   *switch_.Controller
	transitionMu           sync.Mutex
	controlMu              sync.RWMutex
	remotePeer             string
	remoteEntryEdge        string
	remoteRequestID        string
	remoteLeasePhase       remoteLeasePhase
	remoteGrantPending     chan struct{}
	remoteReturnPending    bool
	activeLeasePeer        string
	activeLeaseID          string
	edgeSuppressedUntil    time.Time
	localTransitionPending bool
	pendingSwitchMu        sync.Mutex
	pendingSwitches        map[string]*pendingSwitchRequest
	switchRequestCount     uint64

	pausedMu       sync.RWMutex
	paused         bool
	captureMu      sync.RWMutex
	captureEnabled bool

	clipMu     sync.RWMutex
	clipStatus ClipboardStatus

	historyMu      sync.Mutex
	historyEntries []ClipboardHistoryEntry
	historyLoaded  bool
	historyError   string
	historyKey     []byte

	wakeMu     sync.Mutex
	wakeCmd    *exec.Cmd
	wakeStatus KeepAwakeStatus

	fileMu          sync.Mutex
	fileDownloadDir string
	fileTransfers   map[string]*FileTransferStatus
	activeReceives  map[string]*activeFileReceive

	heldKeysMu     sync.Mutex
	heldRemoteKeys map[string]map[int64]bool

	sessMu   sync.RWMutex
	sessions map[string]*sessionEntry // keyed by connectionID

	connsMu      sync.Mutex
	conns        map[*transport.Conn]string // conn → deviceID (empty until authenticated)
	pendingConns map[string]*transport.Conn

	subsMu sync.RWMutex
	subs   map[chan BusEvent]struct{}

	reconnectMu       sync.Mutex
	reconnectTimers   map[string]*time.Timer
	reconnectAttempts map[string]int
	manualDisconnect  map[string]bool
	stopping          bool

	// httpConnCh receives raw HTTP connections from the MUX.
	httpConnCh chan net.Conn

	ln   net.Listener
	lnMu sync.Mutex

	cancel context.CancelFunc
}

type remoteLeasePhase uint8

const (
	remoteLeaseNone remoteLeasePhase = iota
	remoteLeaseGrantPending
	remoteLeaseActive
)

type pendingSwitchRequest struct {
	deviceID string
	ack      chan event.SwitchAckPayload
	canceled bool
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
	if err := ensureConfigFile(opts.ConfigPath, cfg); err != nil {
		return nil, fmt.Errorf("daemon: ensure config file: %w", err)
	}

	rem, err := remembered.New(opts.DataDir + "/remembered.json")
	if err != nil {
		log.Warnf("[daemon] remembered store load failed: %v — starting empty", err)
		rem, _ = remembered.New("")
	}

	ttl := time.Duration(cfg.PairingPINTTLSeconds) * time.Second
	pm := pending.New(ttl, cfg.PairingPINMaxAttempts)

	d := &Daemon{
		configPath:        opts.ConfigPath,
		dataDir:           opts.DataDir,
		cfg:               cfg,
		identity:          identity,
		rem:               rem,
		pending:           pm,
		tracker:           p2p.NewOutboundTracker(),
		sessions:          make(map[string]*sessionEntry),
		conns:             make(map[*transport.Conn]string),
		pendingConns:      make(map[string]*transport.Conn),
		subs:              make(map[chan BusEvent]struct{}),
		reconnectTimers:   make(map[string]*time.Timer),
		pendingSwitches:   make(map[string]*pendingSwitchRequest),
		reconnectAttempts: make(map[string]int),
		manualDisconnect:  make(map[string]bool),
		httpConnCh:        make(chan net.Conn, 64),
		captureEnabled:    true,
		clipStatus: ClipboardStatus{
			Enabled:         cfg.ClipboardSyncEnabled,
			AllowServerSync: cfg.ClipboardAllowServerSync,
		},
		fileDownloadDir: expandHomePath("~/Downloads/MouseBridge"),
		fileTransfers:   make(map[string]*FileTransferStatus),
		activeReceives:  make(map[string]*activeFileReceive),
		heldRemoteKeys:  make(map[string]map[int64]bool),
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
	d.helper.SetClipboardHandler(d.HandleLocalClipboard)
	d.localHelper = localhelper.NewRuntime(opts.DataDir, d.helper.ClientCount)
	d.localLab = locallab.NewRunner(opts.DataDir)
	d.localValidation = localvalidate.NewRunner(opts.DataDir)
	d.loadFileDownloadDir()
	return d, nil
}

func ensureConfigFile(configPath string, cfg *config.Config) error {
	if configPath == "" || cfg == nil {
		return nil
	}
	_, err := os.Stat(configPath)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return cfg.Save(configPath)
}

// Start begins listening on listen_host:port and starts background goroutines.
func (d *Daemon) Start() error {
	d.reconnectMu.Lock()
	d.stopping = false
	d.reconnectMu.Unlock()
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
	if d.cfg.KeepAwakeEnabled {
		if _, err := d.applyKeepAwake(true); err != nil {
			log.Warnf("[daemon] keep-awake request unavailable: %v", err)
		}
	}

	d.broadcast(BusEvent{Kind: "listening"})
	d.helper.BroadcastConfig()
	// Reconnect trusted remembered devices immediately after the daemon is
	// ready. Previously reconnect was only scheduled after a disconnect event,
	// so restarting the app left all remembered devices disconnected forever.
	if d.cfg.RememberedAutoConnectEnabled {
		for _, device := range d.rem.List() {
			// Trust is owned by the receiver. The controller still needs to
			// attempt remembered devices so a receiver-side trust decision can
			// authenticate it without a PIN.
			if device.Endpoint != "" {
				go d.scheduleReconnectWithDelay(device.DeviceID, 0)
			}
		}
	}
	return nil
}

// Stop shuts down the daemon.
func (d *Daemon) Stop() {
	d.reconnectMu.Lock()
	d.stopping = true
	for deviceID := range d.reconnectTimers {
		d.reconnectTimers[deviceID].Stop()
		delete(d.reconnectTimers, deviceID)
	}
	d.sessMu.RLock()
	for _, session := range d.sessions {
		d.manualDisconnect[session.DeviceID] = true
	}
	d.sessMu.RUnlock()
	d.reconnectMu.Unlock()
	if d.cancel != nil {
		d.cancel()
	}
	d.stopKeepAwake()
	if d.helper != nil {
		d.helper.Stop()
	}
	if d.localValidation != nil {
		_ = d.localValidation.Stop()
	}
	if d.localLab != nil {
		_ = d.localLab.Stop()
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
func (d *Daemon) RememberedAutoConnectEnabled() bool {
	return d.cfg.RememberedAutoConnectEnabled
}

// p2p.SessionHost interface
func (d *Daemon) TrackConn(c *transport.Conn, deviceID string) {
	d.connsMu.Lock()
	d.conns[c] = deviceID
	d.connsMu.Unlock()
}

func (d *Daemon) TrackPendingConn(connectionID string, c *transport.Conn) {
	d.connsMu.Lock()
	d.pendingConns[connectionID] = c
	d.connsMu.Unlock()
}

func (d *Daemon) UntrackPendingConn(connectionID string) {
	d.connsMu.Lock()
	delete(d.pendingConns, connectionID)
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
	entry := d.sessions[connectionID]
	delete(d.sessions, connectionID)
	d.sessMu.Unlock()
	if entry != nil {
		d.cancelPendingSwitchRequests(entry.DeviceID)
	}
	d.transitionMu.Lock()
	defer d.transitionMu.Unlock()
	if entry != nil {
		activeTarget := d.ctrl.ActiveTarget()
		activeLeasePeer := d.activeLeasePeerSnapshot()
		if d.ctrl.ActiveTarget() == entry.DeviceID {
			d.ctrl.SwitchBack()
			d.releaseRemoteKeys(entry.DeviceID)
		}
		d.clearRemoteControl(entry.DeviceID)
		if activeTarget == entry.DeviceID || activeLeasePeer == entry.DeviceID {
			d.clearActiveLease(entry.DeviceID, d.activeLeaseIDSnapshot())
		}
		d.suppressEdgesFor(controlTransitionCooldown)
	}
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
	d.markManualDisconnect(deviceID)
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

// ApproveInbound sends a local receiver approval to the waiting peer. The
// caller is protected by the local-only API route; the peer cannot approve
// itself over the network.
func (d *Daemon) ApproveInbound(pairingID string, trusted bool) error {
	entry, ok := d.pending.Get(pairingID)
	if !ok {
		return fmt.Errorf("no inbound pairing for pairing_id %s", pairingID)
	}
	d.connsMu.Lock()
	target := d.pendingConns[entry.ConnectionID]
	if target == nil {
		for c, id := range d.conns {
			if id == entry.ConnectionID {
				target = c
				break
			}
		}
	}
	d.connsMu.Unlock()
	if target == nil {
		return fmt.Errorf("pairing connection is no longer active")
	}
	return target.Send(event.Message{V: 1, Seq: time.Now().UnixNano(), Type: event.TypePairApprove, Ts: time.Now().UnixMilli(), Payload: event.PairConfirmPayload{PairingID: pairingID, Trusted: trusted}})
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
		if ev.DisplayPIN != "" {
			log.Printf("[daemon] pair_request from %s (pairing_id=%s pin=%s)", ev.Name, ev.PairingID, ev.DisplayPIN)
		} else {
			log.Printf("[daemon] pair_request from %s (pairing_id=%s)", ev.Name, ev.PairingID)
		}
	case "paired":
		log.Printf("[daemon] paired with %s", ev.Name)
	case "session_connected":
		d.clearReconnect(ev.DeviceID)
		log.Printf("[daemon] session connected: %s (%s) role=%s", ev.Name, ev.DeviceID[:12], ev.Role)
	case "session_disconnected":
		log.Printf("[daemon] session disconnected: %s", ev.Name)
		if ev.Role == "client" && !d.consumeManualDisconnect(ev.DeviceID) {
			d.scheduleReconnect(ev.DeviceID)
		}
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
	for ch := range d.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	d.subsMu.RUnlock()
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
	hotkeys := map[string]string{
		"switch_next":       h.SwitchNext,
		"switch_prev":       h.SwitchPrev,
		"switch_to_host":    h.SwitchToHost,
		"disconnect_all":    h.DisconnectAll,
		"toggle_pause":      h.TogglePause,
		"clipboard_history": h.ClipboardHistory,
	}
	used := make(map[string]string, len(hotkeys)+len(d.cfg.QuickTexts))
	for name, combo := range hotkeys {
		if strings.TrimSpace(combo) == "" {
			continue
		}
		sig, err := normalizeHotkey(combo)
		if err != nil {
			return fmt.Errorf("shortcut %s: %w", name, err)
		}
		if owner, exists := used[sig]; exists {
			return fmt.Errorf("shortcut %s conflicts with %s", name, owner)
		}
		used[sig] = name
	}
	for _, entry := range d.cfg.QuickTexts {
		if strings.TrimSpace(entry.Hotkey) == "" {
			continue
		}
		sig, err := normalizeHotkey(entry.Hotkey)
		if err != nil {
			return fmt.Errorf("quick-text %q: %w", entry.Name, err)
		}
		if owner, exists := used[sig]; exists {
			return fmt.Errorf("quick-text %q hotkey conflicts with %s", entry.Name, owner)
		}
		used[sig] = entry.Name
	}
	previous := d.cfg.Hotkeys
	d.cfg.Hotkeys = h
	if d.configPath != "" {
		if err := d.cfg.Save(d.configPath); err != nil {
			d.cfg.Hotkeys = previous
			return err
		}
	}
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
	return nil
}

// UpdateEdgeTargets replaces the controller's screen-side layout, persists it,
// and pushes the new mapping to the local helper.
func (d *Daemon) UpdateEdgeTargets(targets config.EdgeTargets) error {
	d.cfg.EdgeTargets = targets
	d.cfg.EdgeLayoutConfigured = true
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

// PointerSpeeds returns the configured mouse movement and scroll amplification multipliers.
func (d *Daemon) PointerSpeeds() (float64, float64) {
	d.pausedMu.RLock()
	defer d.pausedMu.RUnlock()
	mouseSpeed := d.cfg.MouseSpeed
	scrollSpeed := d.cfg.ScrollSpeed
	if mouseSpeed <= 0 {
		mouseSpeed = config.Default().MouseSpeed
	}
	if scrollSpeed <= 0 {
		scrollSpeed = config.Default().ScrollSpeed
	}
	return mouseSpeed, scrollSpeed
}

// UpdatePointerSpeeds updates mouse movement and/or scroll amplification multipliers,
// persists them to config.json, and pushes the updated snapshot to helpers and UI.
func (d *Daemon) UpdatePointerSpeeds(mouseSpeed, scrollSpeed *float64) (float64, float64, error) {
	d.pausedMu.Lock()
	nextMouse := d.cfg.MouseSpeed
	if nextMouse <= 0 {
		nextMouse = config.Default().MouseSpeed
	}
	nextScroll := d.cfg.ScrollSpeed
	if nextScroll <= 0 {
		nextScroll = config.Default().ScrollSpeed
	}
	if mouseSpeed != nil {
		v := *mouseSpeed
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0.2 || v > 6.0 {
			d.pausedMu.Unlock()
			return nextMouse, nextScroll, fmt.Errorf("mouse_speed must be between 0.2 and 6.0")
		}
		nextMouse = math.Round(v*100) / 100
	}
	if scrollSpeed != nil {
		v := *scrollSpeed
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0.2 || v > 10.0 {
			d.pausedMu.Unlock()
			return nextMouse, nextScroll, fmt.Errorf("scroll_speed must be between 0.2 and 10.0")
		}
		nextScroll = math.Round(v*100) / 100
	}
	d.cfg.MouseSpeed = nextMouse
	d.cfg.ScrollSpeed = nextScroll
	if d.configPath != "" {
		if err := d.cfg.Save(d.configPath); err != nil {
			d.pausedMu.Unlock()
			return nextMouse, nextScroll, err
		}
	}
	d.pausedMu.Unlock()

	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
	d.broadcast(BusEvent{
		Kind: "log",
		Msg:  fmt.Sprintf("pointer speeds updated mouse_speed=%.2f scroll_speed=%.2f", nextMouse, nextScroll),
	})
	return nextMouse, nextScroll, nil
}

// ShakeToLocateConfig returns the configured shake to locate enabled state and sensitivity.
func (d *Daemon) ShakeToLocateConfig() (bool, string) {
	d.pausedMu.RLock()
	defer d.pausedMu.RUnlock()
	sens := d.cfg.ShakeSensitivity
	if sens == "" || sens == "medium" || sens == "standard" {
		sens = "4"
	} else if sens == "lowest" || sens == "very_low" {
		sens = "1"
	} else if sens == "low" {
		sens = "3"
	} else if sens == "high" {
		sens = "6"
	} else if sens == "highest" || sens == "ultra_high" {
		sens = "8"
	} else if sens == "disabled" || sens == "off" {
		sens = "0"
	}
	return d.cfg.ShakeToLocateEnabled, sens
}

// UpdateShakeToLocate updates shake to locate enabled state and/or sensitivity.
func (d *Daemon) UpdateShakeToLocate(enabled *bool, sensitivity *string) (bool, string, error) {
	d.pausedMu.Lock()
	if enabled != nil {
		d.cfg.ShakeToLocateEnabled = *enabled
		if *enabled && (d.cfg.ShakeSensitivity == "0" || d.cfg.ShakeSensitivity == "disabled") {
			d.cfg.ShakeSensitivity = "4"
		} else if !*enabled {
			d.cfg.ShakeSensitivity = "0"
		}
	}
	if sensitivity != nil {
		s := strings.ToLower(strings.TrimSpace(*sensitivity))
		if num, err := strconv.ParseFloat(s, 64); err == nil {
			if num < 0 {
				num = 0
			} else if num > 8 {
				num = 8
			}
			if num < 0.15 {
				d.cfg.ShakeSensitivity = "0"
				d.cfg.ShakeToLocateEnabled = false
			} else {
				formatted := fmt.Sprintf("%.2f", num)
				formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
				d.cfg.ShakeSensitivity = formatted
				d.cfg.ShakeToLocateEnabled = true
			}
		} else {
			switch s {
			case "0", "disabled", "off":
				d.cfg.ShakeSensitivity = "0"
				d.cfg.ShakeToLocateEnabled = false
			case "1", "lowest", "very_low", "minimal":
				d.cfg.ShakeSensitivity = "1"
				d.cfg.ShakeToLocateEnabled = true
			case "2":
				d.cfg.ShakeSensitivity = "2"
				d.cfg.ShakeToLocateEnabled = true
			case "3", "low":
				d.cfg.ShakeSensitivity = "3"
				d.cfg.ShakeToLocateEnabled = true
			case "4", "medium", "standard", "normal":
				d.cfg.ShakeSensitivity = "4"
				d.cfg.ShakeToLocateEnabled = true
			case "5":
				d.cfg.ShakeSensitivity = "5"
				d.cfg.ShakeToLocateEnabled = true
			case "6", "high":
				d.cfg.ShakeSensitivity = "6"
				d.cfg.ShakeToLocateEnabled = true
			case "7":
				d.cfg.ShakeSensitivity = "7"
				d.cfg.ShakeToLocateEnabled = true
			case "8", "highest", "ultra_high", "very_high":
				d.cfg.ShakeSensitivity = "8"
				d.cfg.ShakeToLocateEnabled = true
			default:
				d.pausedMu.Unlock()
				return d.cfg.ShakeToLocateEnabled, d.cfg.ShakeSensitivity, fmt.Errorf("invalid shake_sensitivity: %q (must be 0-8 or recognized preset)", s)
			}
		}
	}
	curEnabled := d.cfg.ShakeToLocateEnabled
	curSens := d.cfg.ShakeSensitivity
	if curSens == "" {
		curSens = "4"
	}
	if d.configPath != "" {
		_ = d.cfg.Save(d.configPath)
	}
	d.pausedMu.Unlock()

	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
	d.broadcast(BusEvent{
		Kind: "log",
		Msg:  fmt.Sprintf("shake to locate updated enabled=%v sensitivity=%s", curEnabled, curSens),
	})
	return curEnabled, curSens, nil
}

// Identity returns the device identity.
func (d *Daemon) Identity() device.Identity { return d.identity }

// RememberedList returns all remembered devices as DTOs (no secrets).
func (d *Daemon) RememberedList() []remembered.DTO { return d.rem.List() }

// RememberedForget removes a remembered device by device_id.
func (d *Daemon) RememberedForget(deviceID string) error { return d.rem.Remove(deviceID) }

// RememberedRename updates the alias of a remembered device.
func (d *Daemon) RememberedRename(deviceID, alias string) error { return d.rem.Rename(deviceID, alias) }

// RememberedPatch updates selected fields of a remembered device.
func (d *Daemon) RememberedPatch(deviceID string, patch remembered.Patch) error {
	return d.rem.Patch(deviceID, patch)
}

// UpdateRememberedAutoConnectEnabled toggles trusted reconnect for this daemon.
func (d *Daemon) UpdateRememberedAutoConnectEnabled(enabled bool) error {
	d.cfg.RememberedAutoConnectEnabled = enabled
	if d.configPath != "" {
		if err := d.cfg.Save(d.configPath); err != nil {
			return err
		}
	}
	return nil
}

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

// LocalValidationStatus returns the current local validation runner status.
func (d *Daemon) LocalValidationStatus() localvalidate.Status {
	if d.localValidation == nil {
		return localvalidate.Status{}
	}
	return d.localValidation.Status()
}

// LocalLabStatus returns the current local two-node lab status.
func (d *Daemon) LocalLabStatus() locallab.Status {
	if d.localLab == nil {
		return locallab.Status{}
	}
	return d.localLab.Status()
}

// StartLocalLab starts the long-lived local two-node lab.
func (d *Daemon) StartLocalLab() error {
	if d.localLab == nil {
		return fmt.Errorf("local lab runner is not configured")
	}
	if err := d.localLab.Start(); err != nil {
		return err
	}
	d.broadcast(BusEvent{Kind: "log", Msg: "local lab started"})
	return nil
}

// StopLocalLab stops the local two-node lab.
func (d *Daemon) StopLocalLab() error {
	if d.localLab == nil {
		return fmt.Errorf("local lab runner is not configured")
	}
	if err := d.localLab.Stop(); err != nil {
		return err
	}
	d.broadcast(BusEvent{Kind: "log", Msg: "local lab stop requested"})
	return nil
}

// StartLocalValidation starts the local validation suite.
func (d *Daemon) StartLocalValidation(req localvalidate.RunRequest) error {
	if d.localValidation == nil {
		return fmt.Errorf("local validation runner is not configured")
	}
	if err := d.localValidation.Start(req); err != nil {
		return err
	}
	d.broadcast(BusEvent{Kind: "log", Msg: "local validation started"})
	return nil
}

// StopLocalValidation stops the local validation suite if it is running.
func (d *Daemon) StopLocalValidation() error {
	if d.localValidation == nil {
		return fmt.Errorf("local validation runner is not configured")
	}
	if err := d.localValidation.Stop(); err != nil {
		return err
	}
	d.broadcast(BusEvent{Kind: "log", Msg: "local validation stop requested"})
	return nil
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
	mouseSpeed, scrollSpeed := d.PointerSpeeds()
	shakeEnabled, shakeSens := d.ShakeToLocateConfig()
	activeTarget := d.ctrl.ActiveTarget()
	hotkeys := map[string]string{
		"switch_next":       d.cfg.Hotkeys.SwitchNext,
		"switch_prev":       d.cfg.Hotkeys.SwitchPrev,
		"switch_to_host":    d.cfg.Hotkeys.SwitchToHost,
		"disconnect_all":    d.cfg.Hotkeys.DisconnectAll,
		"toggle_pause":      d.cfg.Hotkeys.TogglePause,
		"clipboard_history": d.cfg.Hotkeys.ClipboardHistory,
	}
	for _, entry := range d.cfg.QuickTexts {
		if entry.ID != "" && entry.Hotkey != "" {
			hotkeys["quick_text_"+entry.ID] = entry.Hotkey
		}
	}
	d.controlMu.RLock()
	controlledByRemote := d.remotePeer != ""
	d.controlMu.RUnlock()

	return helper.ConfigPushPayload{
		Daemon: helper.DaemonInfo{
			DeviceID:  d.identity.DeviceID,
			DisplayID: d.identity.DisplayID,
			Name:      d.identity.Name,
		},
		Sessions: sessions,
		Hotkeys:  hotkeys,
		EdgeTargets: map[string]string{
			"left":   d.cfg.EdgeTargets.Left,
			"right":  d.cfg.EdgeTargets.Right,
			"top":    d.cfg.EdgeTargets.Top,
			"bottom": d.cfg.EdgeTargets.Bottom,
		},
		EdgeLayoutConfigured:     d.cfg.EdgeLayoutConfigured,
		ActiveTarget:             activeTarget,
		ControlledByRemote:       controlledByRemote,
		Paused:                   d.Paused(),
		CaptureEnabled:           d.CaptureEnabled(),
		MouseSpeed:               mouseSpeed,
		ScrollSpeed:              scrollSpeed,
		ShakeToLocateEnabled:     shakeEnabled,
		ShakeToLocateSensitivity: shakeSens,
		ClipboardEnabled:         d.ClipboardStatusSnapshot().Enabled,
		ClipboardAllowServerSync: d.ClipboardStatusSnapshot().AllowServerSync,
		ClipboardHistoryEnabled:  d.cfg.ClipboardHistoryEnabled,
	}
}

func (d *Daemon) handleHelperHotkey(ev helper.HotkeyPayload) {
	switch ev.Action {
	case "switch_next":
		d.switchRelative(1)
	case "switch_prev":
		d.switchRelative(-1)
	case "switch_to_host":
		d.SwitchToHost()
	case "disconnect_all":
		d.disconnectAll()
	case "toggle_pause":
		d.togglePause()
	case "clipboard_history":
		d.broadcast(BusEvent{Kind: "clipboard_history_requested"})
	default:
		if strings.HasPrefix(ev.Action, "quick_text_") {
			d.triggerQuickText(strings.TrimPrefix(ev.Action, "quick_text_"))
		}
	}
	d.broadcast(BusEvent{
		Kind: "log",
		Msg:  fmt.Sprintf("helper hotkey action=%s combo=%s", ev.Action, ev.Combo),
	})
}

func (d *Daemon) handleHelperEdge(ev helper.EdgePayload) {
	d.transitionMu.Lock()
	defer d.transitionMu.Unlock()
	if d.Paused() || d.edgeEventsSuppressed() {
		return
	}
	d.controlMu.RLock()
	remotePeer := d.remotePeer
	d.controlMu.RUnlock()
	if remotePeer != "" {
		d.handleRemoteReturnEdge(remotePeer, ev)
		return
	}
	activeTarget := d.ctrl.ActiveTarget()
	if activeTarget != d.identity.DeviceID {
		return
	}

	target := d.edgeTargetFor(ev.Edge)
	if target == "" && ev.Edge == "right" && !d.cfg.EdgeLayoutConfigured && !d.hasConfiguredEdgeTargets() {
		// The first usable setup is a single remote: right enters it automatically.
		targets := d.switchTargets()
		if len(targets) == 2 {
			target = targets[1]
		}
	}
	if target == "" || !d.hasSessionDevice(target) {
		return
	}

	if d.ctrl.EdgeReady(target) {
		if !d.beginLocalTransition() {
			return
		}
		defer d.endLocalTransition()
		granted, leaseID, err := d.requestSwitch(target, "edge", ev.Edge, ev.Pct)
		if err != nil || !granted {
			d.suppressEdgesFor(controlTransitionCooldown)
			if err != nil {
				d.broadcast(BusEvent{Kind: "error", Msg: "edge switch request: " + err.Error()})
			}
			return
		}
		if !d.hasSessionDevice(target) {
			_ = d.sendSwitchBack(target, leaseID)
			d.suppressEdgesFor(controlTransitionCooldown)
			return
		}
		d.ctrl.SwitchTo(target)
		d.setActiveLease(target, leaseID)
		d.suppressEdgesFor(controlTransitionCooldown)
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
	if target == "" || target == d.identity.DeviceID || !d.hasSessionDevice(target) {
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
	msgs, err := sessionInputMessages(input)
	if err != nil {
		return err
	}

	var matchedPeer string
	d.connsMu.Lock()
	for c, id := range d.conns {
		if deviceID != "" && id != deviceID {
			continue
		}
		for _, msg := range msgs {
			if err := c.Send(msg); err != nil {
				d.connsMu.Unlock()
				return err
			}
		}
		matchedPeer = id
		d.connsMu.Unlock()
		if matchedPeer == "" {
			matchedPeer = deviceID
		}
		switch input.Kind {
		case "key_down":
			d.recordRemoteKeyDown(matchedPeer, input.KeyCode)
		case "key_up", "key_tap":
			d.recordRemoteKeyUp(matchedPeer, input.KeyCode)
		}
		return nil
	}
	d.connsMu.Unlock()
	return fmt.Errorf("no active session for device %q", deviceID)
}

func (d *Daemon) recordRemoteKeyDown(deviceID string, keyCode int64) {
	if deviceID == "" {
		return
	}
	d.heldKeysMu.Lock()
	defer d.heldKeysMu.Unlock()
	if d.heldRemoteKeys[deviceID] == nil {
		d.heldRemoteKeys[deviceID] = make(map[int64]bool)
	}
	d.heldRemoteKeys[deviceID][keyCode] = true
}

func (d *Daemon) recordRemoteKeyUp(deviceID string, keyCode int64) {
	if deviceID == "" {
		return
	}
	d.heldKeysMu.Lock()
	defer d.heldKeysMu.Unlock()
	if set := d.heldRemoteKeys[deviceID]; set != nil {
		delete(set, keyCode)
		if len(set) == 0 {
			delete(d.heldRemoteKeys, deviceID)
		}
	}
}

func (d *Daemon) releaseRemoteKeys(deviceID string) {
	d.heldKeysMu.Lock()
	var targets map[string][]int64
	if deviceID != "" {
		if set := d.heldRemoteKeys[deviceID]; len(set) > 0 {
			codes := make([]int64, 0, len(set))
			for code := range set {
				codes = append(codes, code)
			}
			slices.Sort(codes)
			targets = map[string][]int64{deviceID: codes}
		}
		delete(d.heldRemoteKeys, deviceID)
	} else {
		for peerID, set := range d.heldRemoteKeys {
			if len(set) == 0 {
				continue
			}
			if targets == nil {
				targets = make(map[string][]int64)
			}
			codes := make([]int64, 0, len(set))
			for code := range set {
				codes = append(codes, code)
			}
			slices.Sort(codes)
			targets[peerID] = codes
		}
		d.heldRemoteKeys = make(map[string]map[int64]bool)
	}
	d.heldKeysMu.Unlock()

	for peerID, codes := range targets {
		for _, code := range codes {
			_ = d.SendSessionInput(peerID, helper.InputPayload{
				Kind:      "key_up",
				KeyCode:   code,
				Modifiers: 0,
			})
		}
	}
}

func helperSocketPath(dataDir string, port int) string {
	canonical := dataDir
	if abs, err := filepath.Abs(dataDir); err == nil {
		canonical = filepath.Clean(abs)
	}
	sum := sha256.Sum256([]byte(canonical))
	suffix := hex.EncodeToString(sum[:4])
	return filepath.Join(os.TempDir(), fmt.Sprintf("mb-helper-%d-%s.sock", port, suffix))
}

func sessionInputMessages(input helper.InputPayload) ([]event.Message, error) {
	base := event.Message{V: 1, Seq: time.Now().UnixMilli(), Ts: time.Now().UnixMilli()}
	switch input.Kind {
	case "mouse_move":
		base.Type = event.TypeMouseMove
		base.Payload = event.MouseMovePayload{DX: input.DX, DY: input.DY, Button: input.Button}
		return []event.Message{base}, nil
	case "mouse_move_abs":
		if input.X == nil || input.Y == nil {
			return nil, fmt.Errorf("mouse_move_abs requires x and y")
		}
		base.Type = event.TypeMouseMoveAbs
		base.Payload = event.MouseMoveAbsPayload{X: *input.X, Y: *input.Y, Button: input.Button}
		return []event.Message{base}, nil
	case "mouse_button":
		base.Type = event.TypeMouseButton
		base.Payload = event.MouseButtonPayload{Button: input.Button, Pressed: input.Pressed, ClickCount: input.ClickCount}
		return []event.Message{base}, nil
	case "key_down":
		base.Type = event.TypeKeyDown
		base.Payload = event.KeyDownPayload{Code: int(input.KeyCode), Mods: int(input.Modifiers)}
		return []event.Message{base}, nil
	case "key_up":
		base.Type = event.TypeKeyUp
		base.Payload = event.KeyUpPayload{Code: int(input.KeyCode), Mods: int(input.Modifiers)}
		return []event.Message{base}, nil
	case "key_tap":
		base.Type = event.TypeKeyDown
		base.Payload = event.KeyDownPayload{Code: int(input.KeyCode), Mods: int(input.Modifiers)}
		up := event.Message{
			V:       1,
			Seq:     time.Now().UnixMilli() + 1,
			Ts:      time.Now().UnixMilli(),
			Type:    event.TypeKeyUp,
			Payload: event.KeyUpPayload{Code: int(input.KeyCode), Mods: int(input.Modifiers)},
		}
		return []event.Message{base, up}, nil
	case "text":
		base.Type = event.TypeText
		base.Payload = event.TextPayload{Text: input.Text}
		return []event.Message{base}, nil
	case "scroll":
		base.Type = event.TypeScroll
		base.Payload = event.ScrollPayload{DX: input.DX, DY: input.DY}
		return []event.Message{base}, nil
	case "locate_cursor":
		base.Type = event.TypeLocateCursor
		base.Payload = map[string]interface{}{}
		return []event.Message{base}, nil
	default:
		return nil, fmt.Errorf("unsupported session input kind %q", input.Kind)
	}
}

func (d *Daemon) Paused() bool {
	d.pausedMu.RLock()
	defer d.pausedMu.RUnlock()
	return d.paused
}

func (d *Daemon) CaptureEnabled() bool {
	d.captureMu.RLock()
	defer d.captureMu.RUnlock()
	return d.captureEnabled
}

func (d *Daemon) SwitchToHost() {
	d.switchToHost()
}

func (d *Daemon) SwitchNext() {
	d.switchRelative(1)
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
}

func (d *Daemon) SwitchPrev() {
	d.switchRelative(-1)
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
}

func (d *Daemon) TogglePause() bool {
	d.pausedMu.Lock()
	d.paused = !d.paused
	paused := d.paused
	d.pausedMu.Unlock()
	if paused {
		d.releaseRemoteKeys("")
	}
	d.broadcast(BusEvent{Kind: "log", Msg: fmt.Sprintf("helper pause toggled paused=%t", paused)})
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
	return paused
}

func (d *Daemon) DisconnectAll() {
	d.disconnectAll()
}

func (d *Daemon) SetCaptureEnabled(enabled bool) bool {
	d.captureMu.Lock()
	changed := d.captureEnabled != enabled
	d.captureEnabled = enabled
	d.captureMu.Unlock()
	if !enabled {
		d.releaseRemoteKeys("")
	}
	if changed {
		d.broadcast(BusEvent{Kind: "log", Msg: fmt.Sprintf("helper capture enabled=%t", enabled)})
		if d.helper != nil {
			d.helper.BroadcastConfig()
		}
	}
	return enabled
}

func (d *Daemon) togglePause() {
	d.TogglePause()
}

func (d *Daemon) disconnectAll() {
	d.releaseRemoteKeys("")
	d.connsMu.Lock()
	conns := make([]*transport.Conn, 0, len(d.conns))
	devices := make([]string, 0, len(d.conns))
	for c, deviceID := range d.conns {
		conns = append(conns, c)
		if deviceID != "" {
			devices = append(devices, deviceID)
		}
	}
	d.connsMu.Unlock()
	for _, deviceID := range devices {
		d.markManualDisconnect(deviceID)
	}

	for _, c := range conns {
		_ = c.Close()
	}
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
}

const maxReconnectBackoffAttempt = 5

func (d *Daemon) markManualDisconnect(deviceID string) {
	d.reconnectMu.Lock()
	d.manualDisconnect[deviceID] = true
	d.reconnectMu.Unlock()
}

func (d *Daemon) consumeManualDisconnect(deviceID string) bool {
	d.reconnectMu.Lock()
	defer d.reconnectMu.Unlock()
	manual := d.manualDisconnect[deviceID]
	delete(d.manualDisconnect, deviceID)
	return manual
}

func (d *Daemon) clearReconnect(deviceID string) {
	d.reconnectMu.Lock()
	if timer := d.reconnectTimers[deviceID]; timer != nil {
		timer.Stop()
		delete(d.reconnectTimers, deviceID)
	}
	delete(d.reconnectAttempts, deviceID)
	delete(d.manualDisconnect, deviceID)
	d.reconnectMu.Unlock()
}

func (d *Daemon) hasSession(deviceID string) bool {
	d.sessMu.RLock()
	defer d.sessMu.RUnlock()
	for _, session := range d.sessions {
		if session.DeviceID == deviceID {
			return true
		}
	}
	return false
}

func (d *Daemon) hasPendingPair(displayID string) bool {
	for _, pairing := range d.tracker.Snapshot() {
		if pairing.RemoteDisplayID == displayID {
			return true
		}
	}
	return false
}

// scheduleReconnect retries only trusted client sessions. If the remote side
// no longer trusts this device, DialAndPair leaves an outbound PIN request in
// the tracker and this loop stops so the normal PIN fallback UI can be used.
func (d *Daemon) scheduleReconnect(deviceID string) {
	d.scheduleReconnectWithDelay(deviceID, time.Second)
}

func (d *Daemon) scheduleReconnectWithDelay(deviceID string, initialDelay time.Duration) {
	rec, ok := d.rem.Get(deviceID)
	if !ok || !d.cfg.RememberedAutoConnectEnabled || rec.Endpoint == "" {
		return
	}
	host, port, err := net.SplitHostPort(rec.Endpoint)
	if err != nil || host == "" || port == "" {
		return
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return
	}

	d.reconnectMu.Lock()
	if d.stopping || d.reconnectTimers[deviceID] != nil || d.hasSession(deviceID) {
		d.reconnectMu.Unlock()
		return
	}
	attempt := d.reconnectAttempts[deviceID]
	// Keep retrying indefinitely. A remote daemon may start later or a
	// network can be temporarily unavailable; stopping after five attempts
	// made a trusted device stay disconnected until the user clicked Reconnect.
	if attempt < maxReconnectBackoffAttempt+1 {
		d.reconnectAttempts[deviceID] = attempt + 1
	}
	delay := initialDelay
	if delay < 0 {
		delay = 0
	}
	if attempt > 0 {
		backoffAttempt := attempt
		if backoffAttempt > maxReconnectBackoffAttempt {
			backoffAttempt = maxReconnectBackoffAttempt
		}
		delay = time.Duration(1<<backoffAttempt) * time.Second
	}
	d.reconnectTimers[deviceID] = time.AfterFunc(delay, func() {
		d.reconnectMu.Lock()
		delete(d.reconnectTimers, deviceID)
		stopping := d.stopping
		d.reconnectMu.Unlock()
		if stopping || d.hasSession(deviceID) {
			return
		}
		c, dialErr := transport.DialTimeout(host, portNumber, time.Duration(d.cfg.ConnectTimeoutSeconds)*time.Second)
		if dialErr != nil {
			d.broadcast(BusEvent{Kind: "error", DeviceID: deviceID, Msg: "trusted reconnect: " + dialErr.Error()})
			d.scheduleReconnect(deviceID)
			return
		}
		p2p.DialAndPair(c, d.identity.DeviceID, d.identity.DisplayID, d.identity.Name, d, d.tracker, d.rem, d.emitBus)
		if d.hasSession(deviceID) || d.hasPendingPair(rec.DisplayID) {
			return
		}
		d.scheduleReconnect(deviceID)
	})
	d.reconnectMu.Unlock()
}

func (d *Daemon) switchRelative(step int) {
	d.transitionMu.Lock()
	defer d.transitionMu.Unlock()
	targets := d.switchTargets()
	if len(targets) == 0 {
		d.switchToHostLocked()
		return
	}

	current := d.ctrl.ActiveTarget()
	idx := slices.Index(targets, current)
	if idx < 0 {
		idx = 0
	}
	next := (idx + step + len(targets)) % len(targets)
	nextTarget := targets[next]
	if nextTarget == current {
		return
	}
	if !d.beginLocalTransition() {
		return
	}
	defer d.endLocalTransition()
	if !d.waitForRemoteGrantConfirmation() {
		d.broadcast(BusEvent{Kind: "error", Msg: "switch aborted while grant acknowledgement was pending"})
		return
	}
	// Stop the old route before releasing its lease or requesting a new one.
	d.ctrl.SwitchBack()
	d.controlMu.RLock()
	remotePeer := d.remotePeer
	remoteLeaseID := d.remoteRequestID
	activeLeasePeer := d.activeLeasePeer
	activeLeaseID := d.activeLeaseID
	d.controlMu.RUnlock()
	toRelease := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, peer := range []string{current, activeLeasePeer, remotePeer} {
		if peer == "" || peer == d.identity.DeviceID {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		toRelease = append(toRelease, peer)
	}
	for _, peer := range toRelease {
		leaseID := ""
		if peer == activeLeasePeer {
			leaseID = activeLeaseID
		}
		if peer == remotePeer {
			leaseID = remoteLeaseID
		}
		if err := d.releasePeerAndWait(peer, leaseID); err != nil {
			d.broadcast(BusEvent{Kind: "error", Msg: "switch back: " + err.Error()})
			return
		}
		d.clearRemoteControl(peer)
		d.clearActiveLease(peer, leaseID)
	}
	if nextTarget != d.identity.DeviceID {
		granted, leaseID, err := d.requestSwitch(nextTarget, "hotkey", "", 0)
		if err != nil || !granted {
			d.suppressEdgesFor(controlTransitionCooldown)
			if err != nil {
				d.broadcast(BusEvent{Kind: "error", Msg: "switch request: " + err.Error()})
			}
			return
		}
		if !d.hasSessionDevice(nextTarget) {
			_ = d.sendSwitchBack(nextTarget, leaseID)
			d.suppressEdgesFor(controlTransitionCooldown)
			return
		}
		d.ctrl.SwitchTo(nextTarget)
		d.setActiveLease(nextTarget, leaseID)
	}
	d.suppressEdgesFor(controlTransitionCooldown)
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

func (d *Daemon) hasConfiguredEdgeTargets() bool {
	targets := d.cfg.EdgeTargets
	return targets.Left != "" || targets.Right != "" || targets.Top != "" || targets.Bottom != ""
}

func (d *Daemon) handleRemoteReturnEdge(activeTarget string, ev helper.EdgePayload) {
	d.controlMu.RLock()
	peer := d.remotePeer
	entryEdge := d.remoteEntryEdge
	d.controlMu.RUnlock()
	if peer == "" || peer != activeTarget {
		return
	}
	returnEdge := oppositeEdge(entryEdge)
	if returnEdge == "" {
		returnEdge = "left"
	}
	if ev.Edge != returnEdge {
		return
	}
	d.controlMu.Lock()
	if d.remotePeer != peer || d.remoteReturnPending {
		d.controlMu.Unlock()
		return
	}
	d.remoteReturnPending = true
	leaseID := d.remoteRequestID
	d.controlMu.Unlock()
	if !d.ctrl.TryEdgeSwitch(d.identity.DeviceID, ev.Edge, ev.Pct) {
		d.controlMu.Lock()
		if d.remotePeer == peer {
			d.remoteReturnPending = false
		}
		d.controlMu.Unlock()
		return
	}
	d.clearRemoteControl(peer)
	if err := d.sendSwitchBack(peer, leaseID); err != nil {
		d.broadcast(BusEvent{Kind: "error", Msg: "edge switch back: " + err.Error()})
	}
	d.suppressEdgesFor(controlTransitionCooldown)
	d.broadcast(BusEvent{
		Kind: "log",
		Msg:  fmt.Sprintf("helper edge switch back edge=%s target=%s pct=%.3f", ev.Edge, peer, ev.Pct),
	})
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
}

func oppositeEdge(edge string) string {
	switch edge {
	case "left":
		return "right"
	case "right":
		return "left"
	case "top":
		return "bottom"
	case "bottom":
		return "top"
	default:
		return ""
	}
}

func (d *Daemon) switchToHost() {
	d.transitionMu.Lock()
	defer d.transitionMu.Unlock()
	d.switchToHostLocked()
}

func (d *Daemon) switchToHostLocked() {
	if !d.beginLocalTransition() {
		return
	}
	defer d.endLocalTransition()
	if !d.waitForRemoteGrantConfirmation() {
		d.broadcast(BusEvent{Kind: "error", Msg: "switch to host aborted while grant acknowledgement was pending"})
		return
	}
	previous := d.ctrl.ActiveTarget()
	d.controlMu.RLock()
	remotePeer := d.remotePeer
	remoteLeaseID := d.remoteRequestID
	activeLeasePeer := d.activeLeasePeer
	activeLeaseID := d.activeLeaseID
	d.controlMu.RUnlock()
	// Pause outbound routing and revoke the inbound input gate before notifying
	// peers. The lease IDs remain in the local snapshot for the release messages.
	d.ctrl.SwitchBack()
	seen := make(map[string]struct{}, 2)
	for _, peer := range []string{previous, activeLeasePeer, remotePeer} {
		if peer == "" || peer == d.identity.DeviceID {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		if peer == remotePeer {
			d.clearRemoteControl(peer)
		}
		leaseID := ""
		if peer == activeLeasePeer {
			leaseID = activeLeaseID
		}
		if peer == remotePeer {
			leaseID = remoteLeaseID
		}
		if err := d.sendSwitchBack(peer, leaseID); err != nil {
			d.broadcast(BusEvent{Kind: "error", Msg: "switch back: " + err.Error()})
		}
		d.clearActiveLease(peer, leaseID)
	}
	d.controlMu.Lock()
	d.activeLeasePeer = ""
	d.activeLeaseID = ""
	d.controlMu.Unlock()
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
}

func (d *Daemon) clearRemoteControl(peer string) {
	changed := false
	d.controlMu.Lock()
	if peer == "" || d.remotePeer == peer {
		changed = d.remotePeer != "" || d.remoteEntryEdge != "" || d.remoteRequestID != "" ||
			d.remoteLeasePhase != remoteLeaseNone || d.remoteReturnPending
		d.remotePeer = ""
		d.remoteEntryEdge = ""
		d.remoteRequestID = ""
		d.remoteLeasePhase = remoteLeaseNone
		d.remoteReturnPending = false
		d.settleRemoteGrantLocked()
		d.suppressEdgesUntilLocked(controlTransitionCooldown)
	}
	d.controlMu.Unlock()
	if changed && d.helper != nil {
		d.helper.BroadcastConfig()
	}
}

const controlTransitionCooldown = 1500 * time.Millisecond

func (d *Daemon) suppressEdgesFor(duration time.Duration) {
	d.controlMu.Lock()
	d.suppressEdgesUntilLocked(duration)
	d.controlMu.Unlock()
}

func (d *Daemon) suppressEdgesUntilLocked(duration time.Duration) {
	until := time.Now().Add(duration)
	if until.After(d.edgeSuppressedUntil) {
		d.edgeSuppressedUntil = until
	}
}

func (d *Daemon) edgeEventsSuppressed() bool {
	d.controlMu.RLock()
	suppressed := time.Now().Before(d.edgeSuppressedUntil)
	d.controlMu.RUnlock()
	return suppressed
}

func (d *Daemon) beginLocalTransition() bool {
	d.controlMu.Lock()
	defer d.controlMu.Unlock()
	if d.localTransitionPending {
		return false
	}
	d.localTransitionPending = true
	return true
}

func (d *Daemon) waitForRemoteGrantConfirmation() bool {
	timer := time.NewTimer(switchRequestTimeout)
	defer timer.Stop()
	for {
		d.controlMu.RLock()
		pending := d.remoteGrantPending
		peer := d.remotePeer
		ready := d.remoteLeasePhase == remoteLeaseActive
		d.controlMu.RUnlock()
		if pending == nil {
			return true
		}
		select {
		case <-pending:
			d.controlMu.RLock()
			stillPending := d.remoteGrantPending
			stillOwned := d.remotePeer == peer
			ready = d.remoteLeasePhase == remoteLeaseActive
			d.controlMu.RUnlock()
			if !stillOwned || ready || stillPending == nil {
				return true
			}
		case <-timer.C:
			return false
		}
	}
}

func (d *Daemon) settleRemoteGrantLocked() {
	if d.remoteGrantPending != nil {
		close(d.remoteGrantPending)
		d.remoteGrantPending = nil
	}
}

func (d *Daemon) remoteLeaseID(peer string) string {
	d.controlMu.RLock()
	defer d.controlMu.RUnlock()
	if d.remotePeer == peer {
		return d.remoteRequestID
	}
	if d.activeLeasePeer == peer {
		return d.activeLeaseID
	}
	return ""
}

func (d *Daemon) activeLeaseIDSnapshot() string {
	d.controlMu.RLock()
	defer d.controlMu.RUnlock()
	return d.activeLeaseID
}

func (d *Daemon) activeLeasePeerSnapshot() string {
	d.controlMu.RLock()
	defer d.controlMu.RUnlock()
	return d.activeLeasePeer
}

func (d *Daemon) setActiveLease(peer, leaseID string) {
	d.controlMu.Lock()
	if d.ctrl.ActiveTarget() == peer {
		d.activeLeasePeer = peer
		d.activeLeaseID = leaseID
	}
	d.controlMu.Unlock()
}

func (d *Daemon) clearActiveLease(peer, leaseID string) {
	d.controlMu.Lock()
	if d.activeLeasePeer == peer && d.activeLeaseID == leaseID && d.ctrl.ActiveTarget() != peer {
		d.activeLeasePeer = ""
		d.activeLeaseID = ""
	}
	d.controlMu.Unlock()
}

func (d *Daemon) endLocalTransition() {
	d.controlMu.Lock()
	d.localTransitionPending = false
	d.controlMu.Unlock()
}

const switchRequestTimeout = 2 * time.Second

func (d *Daemon) requestSwitch(deviceID, trigger, edge string, entryPct float64) (bool, string, error) {
	requestID, pending := d.newPendingSwitchRequest(deviceID)

	payload := event.SwitchRequestPayload{RequestID: requestID, Trigger: trigger, Edge: edge, EntryPct: entryPct}
	if err := d.sendPeerSwitchMessage(deviceID, event.TypeSwitchRequest, payload); err != nil {
		d.removePendingSwitch(requestID)
		return false, requestID, err
	}

	timer := time.NewTimer(switchRequestTimeout)
	defer timer.Stop()
	select {
	case ack := <-pending.ack:
		d.pendingSwitchMu.Lock()
		canceled := pending.canceled
		delete(d.pendingSwitches, requestID)
		d.pendingSwitchMu.Unlock()
		if canceled {
			return false, requestID, fmt.Errorf("switch lease was released before activation")
		}
		if !ack.Granted {
			return false, requestID, fmt.Errorf("switch request rejected: %s", ack.Reason)
		}
		return true, requestID, nil
	case <-timer.C:
		d.removePendingSwitch(requestID)
		_ = d.sendSwitchBack(deviceID, requestID)
		return false, requestID, fmt.Errorf("switch request timed out")
	}
}

func (d *Daemon) releasePeerAndWait(deviceID, leaseID string) error {
	requestID, pending := d.newPendingSwitchRequest(deviceID)
	payload := event.SwitchBackPayload{RequestID: requestID, LeaseID: leaseID}
	if err := d.sendPeerSwitchMessage(deviceID, event.TypeSwitchBack, payload); err != nil {
		d.removePendingSwitch(requestID)
		return err
	}
	timer := time.NewTimer(switchRequestTimeout)
	defer timer.Stop()
	select {
	case ack := <-pending.ack:
		d.removePendingSwitch(requestID)
		if !ack.Released {
			return fmt.Errorf("switch release rejected: %s", ack.Reason)
		}
		return nil
	case <-timer.C:
		d.removePendingSwitch(requestID)
		// A bounded, uncorrelated cleanup handles a lost ACK without activating
		// another target. Releasing an already-cleared lease is idempotent.
		_ = d.sendSwitchBack(deviceID, leaseID)
		d.clearRemoteControl(deviceID)
		d.clearActiveLease(deviceID, leaseID)
		return fmt.Errorf("switch release timed out")
	}
}

func (d *Daemon) newPendingSwitchRequest(deviceID string) (string, *pendingSwitchRequest) {
	d.pendingSwitchMu.Lock()
	d.switchRequestCount++
	requestID := fmt.Sprintf("%s-%d", d.identity.DeviceID, d.switchRequestCount)
	pending := &pendingSwitchRequest{deviceID: deviceID, ack: make(chan event.SwitchAckPayload, 1)}
	d.pendingSwitches[requestID] = pending
	d.pendingSwitchMu.Unlock()
	return requestID, pending
}

func (d *Daemon) removePendingSwitch(requestID string) {
	d.pendingSwitchMu.Lock()
	delete(d.pendingSwitches, requestID)
	d.pendingSwitchMu.Unlock()
}

func (d *Daemon) cancelPendingSwitchRequests(deviceID string) {
	d.pendingSwitchMu.Lock()
	defer d.pendingSwitchMu.Unlock()
	for _, pending := range d.pendingSwitches {
		if pending.deviceID != deviceID {
			continue
		}
		select {
		case pending.ack <- event.SwitchAckPayload{Granted: false, Reason: "session_disconnected"}:
		default:
		}
	}
}

// HandleRemoteSwitchAck completes only the pending request for this peer and ID.
func (d *Daemon) HandleRemoteSwitchAck(deviceID string, ack event.SwitchAckPayload) {
	if ack.RequestID == "" {
		return
	}
	d.pendingSwitchMu.Lock()
	pending := d.pendingSwitches[ack.RequestID]
	if pending == nil || pending.deviceID != deviceID {
		d.pendingSwitchMu.Unlock()
		return
	}
	select {
	case pending.ack <- ack:
	default:
	}
	d.pendingSwitchMu.Unlock()
}

func (d *Daemon) sendSwitchBack(deviceID, leaseID string) error {
	return d.sendPeerSwitchMessage(deviceID, event.TypeSwitchBack, event.SwitchBackPayload{LeaseID: leaseID})
}

func (d *Daemon) sendPeerSwitchMessage(deviceID, messageType string, payload interface{}) error {
	d.connsMu.Lock()
	var target *transport.Conn
	for conn, id := range d.conns {
		if id == deviceID {
			target = conn
			break
		}
	}
	d.connsMu.Unlock()
	if target == nil {
		return fmt.Errorf("no active session for device %q", deviceID)
	}
	now := time.Now().UnixMilli()
	return target.Send(event.Message{V: 1, Seq: now, Type: messageType, Ts: now, Payload: payload})
}

// HandleRemoteSwitchRequest applies a control handoff initiated by a connected peer.
func (d *Daemon) HandleRemoteSwitchRequest(deviceID string, pay event.SwitchRequestPayload) event.SwitchAckPayload {
	ack := event.SwitchAckPayload{RequestID: pay.RequestID}
	if !d.hasSessionDevice(deviceID) {
		ack.Reason = "no_active_session"
		return ack
	}
	if pay.RequestID == "" {
		ack.Reason = "request_id_required"
		return ack
	}
	d.controlMu.Lock()
	if d.localTransitionPending {
		d.controlMu.Unlock()
		ack.Reason = "local_switch_transition_pending"
		return ack
	}
	if d.remotePeer == deviceID && d.remoteRequestID == pay.RequestID {
		d.controlMu.Unlock()
		ack.Granted = true
		return ack
	}
	if d.remotePeer != "" {
		d.controlMu.Unlock()
		ack.Reason = "control_owned_by_another_peer"
		return ack
	}
	if d.ctrl.ActiveTarget() != d.identity.DeviceID || d.activeLeasePeer != "" {
		d.controlMu.Unlock()
		ack.Reason = "local_daemon_is_controlling_another_peer"
		return ack
	}
	d.remotePeer = deviceID
	d.remoteEntryEdge = pay.Edge
	d.remoteRequestID = pay.RequestID
	d.remoteLeasePhase = remoteLeaseGrantPending
	d.remoteGrantPending = make(chan struct{})
	d.remoteReturnPending = false
	d.suppressEdgesUntilLocked(controlTransitionCooldown)
	d.controlMu.Unlock()
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
	ack.Granted = true
	return ack
}

// CanReceiveInput permits data only from the peer holding the current switch grant.
func (d *Daemon) CanReceiveInput(deviceID string) bool {
	d.controlMu.RLock()
	granted := d.remotePeer == deviceID && d.remoteRequestID != "" && d.remoteLeasePhase == remoteLeaseActive
	d.controlMu.RUnlock()
	return granted && d.hasSessionDevice(deviceID)
}

// ConfirmRemoteSwitchGrant marks the lease active only after its grant ACK was written.
func (d *Daemon) ConfirmRemoteSwitchGrant(deviceID, requestID string) {
	d.controlMu.Lock()
	if d.remotePeer == deviceID && d.remoteRequestID == requestID && requestID != "" {
		d.remoteLeasePhase = remoteLeaseActive
		d.settleRemoteGrantLocked()
	}
	d.controlMu.Unlock()
}

// HandleRemoteSwitchBack applies a peer's return notification without creating a new handoff.
func (d *Daemon) HandleRemoteSwitchBack(deviceID string, pay event.SwitchBackPayload) event.SwitchAckPayload {
	ack := event.SwitchAckPayload{RequestID: pay.RequestID}
	if pay.LeaseID == "" {
		ack.Reason = "lease_id_required"
		return ack
	}
	d.pendingSwitchMu.Lock()
	if pending := d.pendingSwitches[pay.LeaseID]; pending != nil && pending.deviceID == deviceID {
		pending.canceled = true
	}
	d.pendingSwitchMu.Unlock()
	d.transitionMu.Lock()
	defer d.transitionMu.Unlock()
	activeTarget := d.ctrl.ActiveTarget()
	d.controlMu.Lock()
	controlledByPeer := d.remotePeer == deviceID
	hasRemoteOwner := d.remotePeer != ""
	activeLeaseMatches := activeTarget == deviceID && d.activeLeasePeer == deviceID && d.activeLeaseID == pay.LeaseID
	remoteLeaseMatches := controlledByPeer && d.remoteRequestID == pay.LeaseID
	if !activeLeaseMatches && !remoteLeaseMatches {
		d.controlMu.Unlock()
		if activeTarget == d.identity.DeviceID && !hasRemoteOwner {
			ack.Released = true // Idempotent release after a lost ACK.
		} else {
			ack.Reason = "lease_not_owned"
		}
		return ack
	}
	if remoteLeaseMatches {
		d.remotePeer = ""
		d.remoteEntryEdge = ""
		d.remoteRequestID = ""
		d.remoteLeasePhase = remoteLeaseNone
		d.remoteReturnPending = false
		d.settleRemoteGrantLocked()
		d.suppressEdgesUntilLocked(controlTransitionCooldown)
	}
	if activeLeaseMatches {
		d.activeLeasePeer = ""
		d.activeLeaseID = ""
	}
	d.controlMu.Unlock()
	if activeLeaseMatches {
		d.ctrl.SwitchBack()
		d.releaseRemoteKeys(deviceID)
	}
	d.suppressEdgesFor(controlTransitionCooldown)
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
	ack.Released = true
	return ack
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
	if ev.PreviousTargetID != "" && ev.PreviousTargetID != d.identity.DeviceID && ev.PreviousTargetID != ev.TargetID {
		d.releaseRemoteKeys(ev.PreviousTargetID)
	}
	d.broadcast(BusEvent{
		Kind: "log",
		Msg:  fmt.Sprintf("switch target=%s trigger=%s", ev.TargetID, ev.Trigger),
	})
	if d.helper != nil {
		d.helper.BroadcastConfig()
	}
}
