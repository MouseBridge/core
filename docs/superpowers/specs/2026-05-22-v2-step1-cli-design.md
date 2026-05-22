# MouseBridge v2 — Step 1: CLI Communication Layer Design

Date: 2026-05-22  
Scope: CLI only, no UI, no real input injection  
Goal: Two Macs pair over LAN, forward mouse/keyboard events, verify latency

---

## 1. What This Step Does (and Does Not Do)

**Does:**
- Discover peers via mDNS + manual IP
- Pair with PIN+Accept/Reject flow
- Forward mouse/keyboard events over TCP
- Detect screen-edge crossing and send switch signal
- Switch control target via configurable hotkey
- Log all received events with latency on the receiver side

**Does NOT do:**
- Actually move the mouse or inject keystrokes (Step 3)
- Show any GUI (Step 2)
- Persist layout across devices (Step 2)

---

## 2. Directory Structure

```
cmd/
  mousebridge/
    main.go

internal/
  net/
    conn.go        # single connection read/write (JSON Lines)
    server.go      # TCP listener + Accept loop
    client.go      # TCP Dial

  discovery/
    mdns.go        # mDNS broadcast + scan
    manual.go      # direct IP connect

  pairing/
    manager.go     # pairing state machine
    pin.go         # 6-digit PIN generation + verification

  session/
    manager.go     # tracks paired+connected devices, emits events

  event/
    types.go       # all message/event type definitions
    codec.go       # JSON marshal/unmarshal helpers

  switch/
    edge.go        # screen-edge detection logic (coordinate math)
    hotkey.go      # hotkey listener (configurable, default Ctrl+Alt+Left/Right)
    controller.go  # decides which device is currently "active target"

  ipc/
    interface.go   # reserved: Handler interface for Step 2 UI

  config/
    config.go      # load/save ~/.mousebridge/config.json

  cli/
    serve.go       # `mousebridge serve`
    connect.go     # `mousebridge connect <ip>`
    status.go      # `mousebridge status`
    pair.go        # interactive pairing prompts
```

---

## 3. Wire Protocol

**Transport:** TCP, port 39172 (default, configurable)  
**Framing:** JSON Lines — one JSON object per line, `\n` terminated  
**All messages share this envelope:**

```json
{
  "v": 1,
  "seq": 42,
  "type": "mouse_move",
  "ts": 1716000000123,
  "payload": { ... }
}
```

### Message Types

| type | direction | description |
|---|---|---|
| `handshake` | both | first message after TCP connect |
| `pair_request` | host→slave | initiate pairing |
| `pair_pin` | slave→host | PIN generated, displayed on slave |
| `pair_confirm` | host→slave | host submits PIN |
| `pair_accept` | slave→host | slave accepted (button or PIN match) |
| `pair_reject` | slave→host | slave rejected |
| `ping` | both | latency probe |
| `pong` | both | latency reply, echoes sender ts |
| `mouse_move` | host→slave | delta movement + edge info |
| `mouse_button` | host→slave | button press/release |
| `key_down` | host→slave | key press |
| `key_up` | host→slave | key release |
| `scroll` | host→slave | scroll delta |
| `switch_request` | host→slave | control switching to this slave |
| `switch_ack` | slave→host | slave ready to receive input |
| `switch_back` | slave→host | slave returning control to host |

### Key Payloads

```json
// mouse_move
{"dx": 3, "dy": -2, "abs_x": 1440, "abs_y": 900,
 "screen_w": 2560, "screen_h": 1440,
 "edge": "right", "edge_pct": 0.42}

// switch_request (edge triggered)
{"trigger": "edge", "edge": "right", "entry_pct": 0.42}

// switch_request (hotkey triggered)  
{"trigger": "hotkey", "direction": "right"}

// pong
{"echo_ts": 1716000000200}
```

---

## 4. Pairing State Machine

```
Host                              Slave
  │── handshake ────────────────→  │
  │←─ handshake ──────────────────  │
  │                                 │
  │── pair_request ──────────────→  │  slave generates PIN
  │                                 │  slave prints: PIN=847291 [A]ccept [R]eject
  │←─ pair_pin {pin:"847291"} ─────  │
  │                                 │
  │  Path A: slave presses Accept   │
  │←─ pair_accept ────────────────  │
  │  paired ✓                       │
  │                                 │
  │  Path B: host enters PIN        │
  │── pair_confirm {pin} ────────→  │  PIN matches?
  │←─ pair_accept ────────────────  │
  │  paired ✓                       │
  │                                 │
  │  Path C: slave presses Reject   │
  │←─ pair_reject ────────────────  │
  │  connection closed              │
```

PIN is a 6-digit numeric string, generated fresh each connection attempt, expires after 60 seconds or on reject.

After successful pairing, both sides persist `{deviceId, name}` to `~/.mousebridge/trusted.json`. Subsequent connections skip pairing.

---

## 5. Switch Control Flow

### 5a. Edge Switch

1. Host detects mouse reaching screen edge (within 2px of boundary)
2. Host holds for 50ms dwell time (prevents accidental flicker)
3. Host sends `switch_request {trigger:"edge", edge:"right", entry_pct:0.42}`
4. Host stops forwarding local mouse to screen (locks local cursor at edge)
5. Slave receives → logs `[switch] taking control, entry=right@42%`
6. Slave sends `switch_ack`
7. Host begins forwarding all mouse+keyboard events to slave
8. To return: slave detects its cursor hit opposite edge → sends `switch_back`
9. Host releases cursor lock, resumes local control

### 5b. Hotkey Switch

1. User presses `Ctrl+Alt+Right` (configurable)
2. `switch/controller.go` cycles to next device in direction
3. Same `switch_request {trigger:"hotkey"}` flow from step 3 above

### Config file (`~/.mousebridge/config.json`)

```json
{
  "port": 39172,
  "device_name": "MacBook-Pro",
  "hotkeys": {
    "switch_right": "ctrl+alt+right",
    "switch_left": "ctrl+alt+left"
  },
  "trusted_devices_file": "~/.mousebridge/trusted.json"
}
```

---

## 6. CLI Commands

```
mousebridge serve                     # listen, wait for connections
mousebridge connect <ip> [--port N]   # connect to specific IP
mousebridge connect --discover        # mDNS scan, interactive pick
mousebridge status                    # show connections + latency
mousebridge unpair <device-id>        # remove from trusted list
```

### Terminal output format

```
# serve side
[10:00:01] listening on :39172
[10:00:05] pair_request from MacBook-Pro (id=abc123)
[10:00:05] PIN: 847291   [A] Accept  [R] Reject  (expires in 60s)
> A
[10:00:07] paired with MacBook-Pro
[10:00:08] connected — MacBook-Pro (192.168.1.5)
[10:01:00] recv  mouse_move    dx=+3   dy=-2    latency=1.1ms
[10:01:00] recv  key_down      code=65          latency=0.9ms
[10:01:01] recv  switch_request  trigger=edge entry=right@42%
[10:01:01] sent  switch_ack

# connect side
[10:00:05] connecting to 192.168.1.5:39172 ...
[10:00:05] connected — MacBook-Mini
[10:00:05] pair_request sent, waiting...
[10:00:05] remote PIN: 847291 (or wait for remote to Accept)
[10:00:07] paired ✓
[10:00:10] active target: local
[10:01:01] edge reached: right@42% — switching to MacBook-Mini
[10:01:01] active target: MacBook-Mini
[10:01:01] sent  switch_request  trigger=edge entry=right@42%
[10:01:01] recv  switch_ack
[10:01:02] sent  mouse_move    dx=+3 dy=-2   rtt=1.1ms
```

---

## 7. IPC Reserved Interface (Step 2)

`internal/ipc/interface.go` — defines only, no implementation:

```go
type Handler interface {
    OnDeviceDiscovered(d Device)
    OnPairRequest(d Device, pin string)
    OnConnected(d Device)
    OnDisconnected(d Device)
    OnEventSent(e Event, rttMs float64)
    OnEventReceived(e Event, latencyMs float64)
    OnSwitchTarget(deviceID string, trigger string)
}
```

Step 2 UI implements this interface. CLI and network packages have zero awareness of UI.

---

## 8. What "Done" Looks Like for Step 1

- Two Macs can run `mousebridge serve` and `mousebridge connect`
- Pairing works (both Accept-path and PIN-path)
- Moving mouse on host → slave terminal logs `recv mouse_move` with latency
- Pressing keys on host → slave terminal logs `recv key_down/key_up`
- Moving host mouse to screen edge → both sides log the switch event
- Pressing `Ctrl+Alt+Right` → both sides log the switch event
- `mousebridge status` shows connected device + rolling avg latency
- Latency is consistently under 5ms on local LAN
