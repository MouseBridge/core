# ui-web + Shortcuts Implementation Design

## Goal

Build `ui-web` — a Vite + React SPA served at `http://127.0.0.1:39173` — and extend `core` with a shortcuts API and `ShortcutManager` stub, enabling users to configure global hotkeys that are immediately pushed to helper upon save or helper connection.

## Architecture

### Repos involved

| Repo | Change |
|------|--------|
| `core` | Extend `Hotkeys` config, add `/api/shortcuts` GET+PUT, add `ShortcutManager` stub |
| `ui-types` | Add `Hotkeys` type, add `getShortcuts`/`setShortcuts` to `DaemonClient` |
| `ui-web` | New Vite+React SPA, 4 tabs, consumes `@mousebridge/components` |

### Dependency chain

```
ui-web
  → @mousebridge/components  (file:../ui-components)
  → @mousebridge/provider    (file:../ui-provider)
  → @mousebridge/types       (file:../ui-types)
  → core daemon HTTP API + SSE
```

### Role derivation (frontend only, no core API needed)

```
lastEvent.event === 'listening'  → role = 'host'
lastEvent.event === 'connected'  → role = 'client'
otherwise                        → role = 'idle'
```

`useRole()` hook in `ui-web/src/hooks/useRole.ts` reads `lastEvent` from `useDaemon()` and returns `'host' | 'client' | 'idle'`.

---

## core Changes

### 1. Extend `Hotkeys` in `internal/config/config.go`

Replace existing two-field struct:

```go
type Hotkeys struct {
    SwitchNext    string `json:"switch_next"`
    SwitchPrev    string `json:"switch_prev"`
    SwitchToHost  string `json:"switch_to_host"`
    DisconnectAll string `json:"disconnect_all"`
    TogglePause   string `json:"toggle_pause"`
}

func defaultHotkeys() Hotkeys {
    return Hotkeys{
        SwitchNext:   "ctrl+alt+right",
        SwitchPrev:   "ctrl+alt+left",
        SwitchToHost: "ctrl+alt+home",
    }
}
```

Old fields `switch_right` / `switch_left` are removed; existing config files with those fields are silently ignored by JSON unmarshalling.

### 2. `ShortcutManager` stub — `internal/shortcuts/manager.go`

```go
package shortcuts

import "github.com/mousebridge/core/internal/config"

// Manager pushes hotkey config to connected helpers.
// Push is a no-op until helper integration is implemented.
type Manager struct{}

func New() *Manager { return &Manager{} }

// Push sends the current hotkeys to all connected helpers immediately.
// Called on every PUT /api/shortcuts and on each new helper connection.
func (m *Manager) Push(h config.Hotkeys) error { return nil }
```

When helper is implemented, `Manager` will hold a list of active helper connections and broadcast to all of them.

### 3. New HTTP handlers in `internal/httpapi/`

Add to `routes()`:
```go
mux.HandleFunc("/api/shortcuts", s.handleShortcuts)
```

Handler:
- `GET` → return `s.d.Config().Hotkeys` as JSON
- `PUT` → decode body into `config.Hotkeys`, save config, call `s.shortcuts.Push(hotkeys)`

`daemon.Daemon` needs a new exported method `Config() *config.Config` (currently `cfg` is private). Add it to `internal/daemon/daemon.go`.

`Server` gains a `shortcuts *shortcuts.Manager` field, initialized in `New()`.

### 4. Tests for new handlers (`internal/httpapi/handlers_test.go`)

- `GET /api/shortcuts` returns default hotkeys JSON
- `PUT /api/shortcuts` with valid body → 200, config updated
- `PUT /api/shortcuts` with empty body → 400

---

## ui-types Changes

### New type in `src/types.ts`

```typescript
export interface Hotkeys {
    switch_next: string
    switch_prev: string
    switch_to_host: string
    disconnect_all: string
    toggle_pause: string
}
```

### New methods in `src/client.ts`

```typescript
async getShortcuts(): Promise<Hotkeys> {
    const res = await fetch(`${this.baseURL}/api/shortcuts`)
    return res.json()
}

async setShortcuts(h: Hotkeys): Promise<void> {
    await fetch(`${this.baseURL}/api/shortcuts`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(h),
    })
}
```

---

## ui-web Structure

```
ui-web/
  src/
    App.tsx                    # DaemonProvider + TabBar + page routing + PairDialog
    hooks/
      useRole.ts               # derive 'host'|'client'|'idle' from lastEvent
    pages/
      OverviewPage.tsx         # summary dashboard
      DevicesPage.tsx          # device management
      LogPage.tsx              # full event log
      SettingsPage.tsx         # shortcut key configuration
    components/
      TabBar.tsx               # tab navigation (Overview/Devices/Log/Settings)
      DeviceSummary.tsx        # read-only device count + names for Overview
      RecentLog.tsx            # last 3 log entries for Overview
      ShortcutRow.tsx          # single shortcut row with record button
  index.html
  index.css                    # Tailwind directives
  package.json
  vite.config.ts
  tsconfig.json
  tailwind.config.js
  postcss.config.js
```

### Page details

**OverviewPage**
- `StatusBar` (connection status + port)
- `ServerControls` (role-aware: idle shows both buttons, host shows Stop+Connect input, client shows Disconnect All)
- `DeviceSummary`: device count + name list, click → navigate to Devices tab
- `RecentLog`: last 3 events, click "View All" → navigate to Log tab

**DevicesPage**
- `DeviceCard` list for all connected devices
- host role: each card shows Disconnect button
- client role: read-only cards + "Disconnect All" button
- host/idle role: IP input + Connect button (calls `client.connect(ip)`)

**LogPage**
- `EventLog` (full history, accumulates via useState in the component)

**SettingsPage**
- Loads hotkeys via `client.getShortcuts()` on mount
- 5 `ShortcutRow` entries (one per Hotkeys field)
- Record mode: click row's Record button → capture next keydown → fill field
- Save button → `client.setShortcuts(hotkeys)` → inline success/error feedback

**ShortcutRow** props:
```typescript
interface Props {
    label: string
    value: string
    onRecord: () => void   // enters capture mode for this row
    recording: boolean
}
```

**App.tsx** layout:
```typescript
<DaemonProvider>
    <div className="min-h-screen bg-gray-50">
        <TabBar activeTab={tab} onTabChange={setTab} />
        <main className="p-4">
            {tab === 'overview' && <OverviewPage onNavigate={setTab} />}
            {tab === 'devices' && <DevicesPage />}
            {tab === 'log' && <LogPage />}
            {tab === 'settings' && <SettingsPage />}
        </main>
    </div>
    <PairDialog />
</DaemonProvider>
```

---

## Helper Push Contract

All config changes must be pushed to helper immediately on save — not just persisted. When helper connects to daemon, daemon pushes the current full config immediately.

`ShortcutManager.Push()` is currently a stub (no-op). When helper is implemented:
- `Manager` maintains a list of active helper connections
- `Push()` broadcasts to all connected helpers
- On new helper connection, daemon calls `Push(currentConfig.Hotkeys)` immediately

This contract applies to all future config types, not just hotkeys.

---

## What is NOT in scope

- Helper implementation (CGEventTap, global hotkey capture)
- Database storage (JSON files only, open-source version)
- Authentication / SSO (commercial version, separate repo)
- `ui-web` unit tests (visual verification via browser)
