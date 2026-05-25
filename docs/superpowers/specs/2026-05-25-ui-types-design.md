# ui-types Design Spec

**Repo:** `MouseBridge/ui-types`
**Package name:** `@mousebridge/types`
**Purpose:** TypeScript type definitions, REST API client, and SSE hook for communicating with the MouseBridge daemon HTTP API. Used by `ui-components`, `ui-web`, and `ui-electron`.

---

## Architecture

Single npm package built with Vite library mode. Zero runtime dependencies. React is a peer dependency (required only for the SSE hook). Outputs ESM + CJS so both Vite-based web apps and Electron/Node consumers can import it.

```
ui-types/
  package.json        ← name: "@mousebridge/types", exports ESM + CJS
  tsconfig.json
  vite.config.ts      ← library mode, entry: src/index.ts
  src/
    index.ts          ← re-exports everything
    types.ts          ← DaemonEvent, DeviceStatus
    client.ts         ← DaemonClient class
    sse.ts            ← useDaemonEvents React hook
  dist/               ← build output (gitignored)
```

---

## Types (`src/types.ts`)

Mirrors the Go structs in `core/internal/daemon/events.go` exactly.

```typescript
export interface DeviceStatus {
  id: string
  name: string
  ip: string
  avg_latency_ms: number
}

export type DaemonEvent =
  | { event: 'listening'; port: number }
  | { event: 'pair_request'; device_id: string; name: string; pin: string }
  | { event: 'paired'; device_id: string; name: string }
  | { event: 'connected'; device_id: string; name: string; ip: string }
  | { event: 'disconnected'; device_id: string; name: string }
  | { event: 'status'; devices: DeviceStatus[] }
  | { event: 'log'; msg: string }
  | { event: 'error'; msg: string }
```

The discriminated union on `event` lets consumers use exhaustive switch statements with full type narrowing.

---

## API Client (`src/client.ts`)

`DaemonClient` wraps every REST endpoint exposed by `core/internal/httpapi`. All methods throw on non-2xx responses.

```typescript
export class DaemonClient {
  constructor(baseURL = 'http://127.0.0.1:39173')

  serve(port?: number): Promise<void>
  connect(ip: string, port?: number): Promise<void>
  stopServe(): Promise<void>
  disconnect(deviceId: string): Promise<void>
  pairAccept(): Promise<void>
  pairReject(): Promise<void>
  pairPIN(pin: string): Promise<void>
  status(): Promise<{ devices: DeviceStatus[] }>
}
```

Each method maps to one HTTP endpoint:

| Method | HTTP |
|--------|------|
| `serve` | `POST /api/serve` |
| `connect` | `POST /api/connect` |
| `stopServe` | `POST /api/stop-serve` |
| `disconnect` | `POST /api/disconnect` |
| `pairAccept` | `POST /api/pair/accept` |
| `pairReject` | `POST /api/pair/reject` |
| `pairPIN` | `POST /api/pair/pin` |
| `status` | `GET /api/status` |

---

## SSE Hook (`src/sse.ts`)

`useDaemonEvents` subscribes to `GET /api/events` and returns the latest event plus connection state.

```typescript
export function useDaemonEvents(baseURL = 'http://127.0.0.1:39173'): {
  lastEvent: DaemonEvent | null  // most recent event from daemon
  connected: boolean             // SSE connection is open
}
```

- Uses the browser-native `EventSource` API — no third-party dependency.
- `EventSource` automatically reconnects on disconnect; `connected` reflects the current state.
- The hook closes the `EventSource` when the component unmounts (cleanup in `useEffect`).
- `lastEvent` is replaced on every new event; consumers that need history should accumulate in their own state.

---

## Package Config

**`package.json`:**
```json
{
  "name": "@mousebridge/types",
  "version": "0.1.0",
  "type": "module",
  "exports": {
    ".": {
      "import": "./dist/index.js",
      "require": "./dist/index.cjs"
    }
  },
  "peerDependencies": {
    "react": ">=18"
  },
  "devDependencies": {
    "typescript": "^5",
    "vite": "^6",
    "@types/react": "^18",
    "vitest": "^2"
  }
}
```

**`vite.config.ts`:**
```typescript
import { defineConfig } from 'vite'
import { resolve } from 'path'

export default defineConfig({
  build: {
    lib: {
      entry: resolve(__dirname, 'src/index.ts'),
      formats: ['es', 'cjs'],
      fileName: (format) => format === 'es' ? 'index.js' : 'index.cjs',
    },
    rollupOptions: {
      external: ['react'],
    },
  },
})
```

---

## Testing

Vitest for unit tests. No DOM required for `types.ts` and `client.ts`. SSE hook tested with `@testing-library/react` and a mock `EventSource`.

Test files:
- `src/client.test.ts` — mocks `fetch`, verifies correct URL/body for each method
- `src/sse.test.ts` — mocks `EventSource`, verifies `lastEvent` updates and `connected` state

---

## Error Handling

- `DaemonClient` methods reject with an `Error` containing the HTTP status and response body if the server returns non-2xx.
- `useDaemonEvents` sets `connected: false` when `EventSource` fires `onerror`; recovers to `true` on `onopen`.
- No retry logic in `DaemonClient` — callers decide whether to retry.
