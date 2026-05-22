# MouseBridge Daemon + IPC Architecture Design

Date: 2026-05-22  
Scope: Refactor Step 1 CLI into daemon + thin CLI client  
Goal: Separate long-running state from user interaction so UI can plug in later

---

## 1. Problem with Current Architecture

Current `serve` and `connect` commands hold all state in the process that the user runs interactively. This means:
- Pairing requires stdin to be a real terminal
- UI (Step 2) has no way to attach to a running session
- Restarting the CLI kills all connections

---

## 2. Target Architecture

```
┌─────────────────────────────────────┐
│  mousebridge daemon                 │  (always running, no stdin)
│                                     │
│  ┌─────────┐   ┌──────────────────┐ │
│  │ TCP     │   │ Unix Socket      │ │
│  │ server  │   │ /tmp/mb.sock     │ │
│  │ :39172  │   │                  │ │
│  └────┬────┘   └────────┬─────────┘ │
│       │                 │           │
│  ┌────▼─────────────────▼─────────┐ │
│  │         Core state             │ │
│  │  session / pairing / switch    │ │
│  └────────────────────────────────┘ │
└─────────────────────────────────────┘
         ▲                  ▲
         │                  │
   mousebridge          mousebridge
   connect/serve         UI (Step 2)
   (thin CLI client)     (Step 2)
```

---

## 3. Unix Socket Protocol

**Transport:** Unix domain socket at `~/.mousebridge/mb.sock`  
**Framing:** JSON Lines (same as TCP wire protocol)

### Client → Daemon (commands)

```json
{"cmd": "serve"}
{"cmd": "connect", "ip": "192.168.1.5", "port": 39172}
{"cmd": "pair_accept"}
{"cmd": "pair_reject"}
{"cmd": "pair_pin", "pin": "847291"}
{"cmd": "status"}
{"cmd": "disconnect", "device_id": "abc123"}
```

### Daemon → Client (events, streamed)

```json
{"event": "listening", "port": 39172}
{"event": "pair_request", "device_id": "abc", "name": "MacBook-Pro", "pin": "847291"}
{"event": "paired", "device_id": "abc", "name": "MacBook-Pro"}
{"event": "connected", "device_id": "abc", "name": "MacBook-Pro", "ip": "127.0.0.1"}
{"event": "disconnected", "device_id": "abc", "name": "MacBook-Pro"}
{"event": "log", "msg": "[10:00:01] recv mouse_move latency=1.1ms"}
{"event": "status", "devices": [{"id":"abc","name":"MacBook-Pro","avg_latency_ms":1.2}]}
{"event": "error", "msg": "connect failed: ..."}
```

The client subscribes to the event stream for as long as it stays connected. Multiple clients can connect simultaneously (e.g., CLI + UI both watching).

---

## 4. File Structure Changes

```
cmd/mousebridge/main.go          # unchanged — cobra root

internal/daemon/
  daemon.go                      # starts TCP + Unix Socket, owns core state
  ipc_server.go                  # Unix Socket listener, broadcasts events to clients
  ipc_client.go                  # one connected IPC client (read commands, write events)
  events.go                      # event/command struct definitions (JSON)

internal/cli/
  serve.go      → thin: sends {"cmd":"serve"} to daemon, streams events
  connect.go    → thin: sends {"cmd":"connect","ip":...} to daemon, streams events
  status.go     → thin: sends {"cmd":"status"}, prints result
  daemon.go     → NEW: `mousebridge daemon` — starts the daemon process
  pair.go       → NEW: `mousebridge pair accept/reject/pin <pin>`

internal/ipc/
  interface.go  → keep Handler interface (unchanged)
  client.go     → NEW: Go client for connecting to daemon socket
```

---

## 5. CLI Commands After Refactor

```
mousebridge daemon              # start the background daemon (blocks, or use & )
mousebridge serve               # tell daemon to listen for TCP connections
mousebridge connect <ip>        # tell daemon to connect to remote
mousebridge status              # query daemon for current connections + latency
mousebridge pair accept         # tell daemon to accept pending pair request
mousebridge pair reject         # tell daemon to reject pending pair request  
mousebridge pair pin <PIN>      # tell daemon to submit PIN to remote
```

---

## 6. Daemon Lifecycle

- Daemon writes its PID and socket path to `~/.mousebridge/daemon.pid`
- CLI commands check if socket exists before connecting; if not, print "daemon not running, start with: mousebridge daemon"
- Daemon handles SIGTERM/SIGINT cleanly: close all TCP connections, remove socket file
- Multiple CLI clients can connect simultaneously — all receive the same event stream

---

## 7. What Does NOT Change

- `internal/net/` — TCP conn/server/client unchanged
- `internal/pairing/` — state machine unchanged  
- `internal/session/` — unchanged
- `internal/switch/` — unchanged
- `internal/event/` — unchanged
- `internal/config/` — unchanged
- Wire protocol between two MouseBridge instances — unchanged

Only the CLI layer and the new daemon layer change.

---

## 8. Step 2 UI Integration

Step 2 UI connects to `~/.mousebridge/mb.sock` as another IPC client. It sends commands and receives the same event stream. No changes needed to daemon or core packages — UI is just another client.
