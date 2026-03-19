# Phase 9 — SvelteKit Dashboard

## Goal

Implement the `ui/` SvelteKit web application that provides real-time visibility into workflow executions, services, telemetry, and logs. The dashboard communicates with the Control Plane via WebSocket (for live state push) and REST (for telemetry and log queries). No business logic lives in the dashboard — it is a pure read layer.

---

## Phase Dependencies

- **Phase 5** must be complete. All REST API routes (`/api/v1/executions`, `/api/v1/services`, `/api/v1/ws`) must be stable and serving.
- **Phase 6** must be complete. Telemetry REST endpoints (`/api/v1/executions/:id/events`, `/api/v1/services/:name/metrics`, `/api/v1/logs`) must be stable and backed by ClickHouse.

---

## Files to Create

| File | Purpose |
|------|---------|
| `ui/src/app.html` | SvelteKit root HTML template — sets `<html lang="en">`, `<meta charset="utf-8">`, loads SvelteKit's hydration script via `%sveltekit.head%` and `%sveltekit.body%` placeholders |
| `ui/src/routes/+page.svelte` | Executions overview — live list of all workflow runs |
| `ui/src/routes/executions/[id]/+page.svelte` | Execution detail — step-by-step status, input/output per state |
| `ui/src/routes/services/+page.svelte` | Services overview — all services with mode, replicas, health, last invocation |
| `ui/src/routes/logs/+page.svelte` | Log explorer — full-text search, filter by execution/service/level |
| `ui/src/lib/ws.ts` | WebSocket client with auto-reconnect |
| `ui/src/lib/api.ts` | REST client for all Control Plane endpoints |
| `ui/package.json` | NPM dependencies and scripts |
| `ui/svelte.config.js` | SvelteKit adapter and route configuration |
| `ui/vite.config.ts` | Vite config — dev proxy to Control Plane API |

---

## Key Types / Interfaces / Functions

### `ui/src/lib/api.ts`

All REST calls go through a single typed client. The base URL defaults to the current origin (dashboard served by the Control Plane) and can be overridden via the `VITE_API_BASE` env var for local development.

```typescript
// Mirrors internal/store/store.go ExecutionRecord
export interface Execution {
  id: string;
  workflow: string;
  status: 'Pending' | 'Running' | 'Completed' | 'Failed';
  input: Record<string, unknown>;
  created_at: string; // ISO 8601
  updated_at: string;
}

// Mirrors internal/store/store.go StateRecord
export interface StateRecord {
  execution_id: string;
  state_name: string;
  status: 'Pending' | 'Running' | 'Completed' | 'Failed';
  input: Record<string, unknown>;
  output: Record<string, unknown>;
  error: string;
  attempt: number;
  created_at: string;
  updated_at: string;
}

// Mirrors internal/store/service_store.go ServiceRecord
export interface Service {
  name: string;
  mode: 'Deployment' | 'Lambda';
  port: number;
  min_scale: number;
  max_scale: number;
  ingress_host: string;
  timeout_seconds: number;
  status: 'Pending' | 'Running' | 'Failed' | 'Stopped';
  cluster_ip: string;
  created_at: string;
  updated_at: string;
}

// Mirrors internal/telemetry/events.go row
export interface ExecutionEvent {
  event_id: string;
  execution_id: string;
  state_name: string;
  from_status: string;
  to_status: string;
  error: string;
  occurred_at: string;
}

// Mirrors internal/telemetry/metrics.go row
export interface ServiceMetric {
  metric_id: string;
  service_name: string;
  invocation_id: string;
  duration_ms: number;
  status_code: number;
  error: string;
  occurred_at: string;
}

// Mirrors internal/telemetry/logs.go row
export interface LogLine {
  log_id: string;
  execution_id: string;
  service_name: string;
  state_name: string;
  level: 'INFO' | 'WARN' | 'ERROR' | 'DEBUG';
  message: string;
  occurred_at: string;
}

// ---- API functions ----

export async function listExecutions(params?: {
  workflow?: string;
  status?: string;
  limit?: number;
  offset?: number;
}): Promise<Execution[]>

export async function getExecution(id: string): Promise<Execution>

export async function listExecutionStates(id: string): Promise<StateRecord[]>

export async function listServices(): Promise<Service[]>

export async function getService(name: string): Promise<Service>

export async function getExecutionEvents(
  id: string,
  params?: { since?: string; limit?: number }
): Promise<ExecutionEvent[]>

export async function getServiceMetrics(
  name: string,
  params?: { since?: string; until?: string; limit?: number }
): Promise<ServiceMetric[]>

export async function queryLogs(params: {
  execution_id?: string;
  service_name?: string;
  state_name?: string;
  level?: string;
  since?: string;
  until?: string;
  q?: string;
  limit?: number;
  offset?: number;
}): Promise<{ logs: LogLine[]; total: number }>
```

All functions `throw` on non-2xx responses with an `{ error: string; code: string }` payload.

---

### `ui/src/lib/ws.ts`

```typescript
// Mirrors internal/api/ws_handler.go WSEvent
export interface WSEvent {
  type: 'state_transition' | 'service_update';
  payload: StateTransitionPayload | ServiceUpdatePayload;
  timestamp: string;
}

export interface StateTransitionPayload {
  execution_id: string;
  state_name: string;
  from_status: string;
  to_status: string;
  error?: string;
}

export interface ServiceUpdatePayload {
  service_name: string;
  status: string;
}

export type EventHandler = (event: WSEvent) => void;

/**
 * createWSClient returns a WebSocket client with automatic reconnection.
 *
 * Reconnect behaviour:
 * - On close or error: wait reconnectDelayMs (default 2000ms), then reconnect.
 * - Reconnect attempts are unlimited.
 * - The caller reconciles full state on reconnect by re-fetching via REST.
 *   Missed events during the disconnected window are not replayed.
 *
 * Usage:
 *   const ws = createWSClient('/api/v1/ws', handler);
 *   ws.connect();
 *   // ...
 *   ws.disconnect(); // on component teardown
 */
export function createWSClient(
  path: string,
  onEvent: EventHandler,
  reconnectDelayMs?: number,
): {
  connect: () => void;
  disconnect: () => void;
  isConnected: () => boolean;
}
```

The WebSocket client is created once per SvelteKit layout (not per route). It is stored in a Svelte store so all routes can subscribe to live events without maintaining multiple connections.

---

## Page Specifications

### `/` — Executions Overview (`+page.svelte`)

**Data sources:**
- Initial load: `listExecutions({ limit: 50 })`
- Live updates: `WSEvent{ type: "state_transition" }` updates the matching row in-place

**What it shows:**
- Table: execution ID (truncated), workflow name, status badge, start time, duration, input summary
- Status badge colours: Pending=grey, Running=blue, Completed=green, Failed=red
- Clicking a row navigates to `/executions/[id]`
- "Refresh" button re-fetches the list (for executions started before the current WebSocket connection)
- Filter controls: workflow name, status

**Live update behaviour:**
- On `state_transition` event: find the matching execution row by `execution_id`, update its `status` field in the local store. If the execution is not yet in the list (started before page load), append it.

---

### `/executions/[id]` — Execution Detail (`executions/[id]/+page.svelte`)

**Data sources:**
- Initial load: `getExecution(id)` + `listExecutionStates(id)` + `getExecutionEvents(id)`
- Live updates: `WSEvent{ type: "state_transition" }` where `payload.execution_id == id`

**What it shows:**
- Execution metadata: ID, workflow, overall status, start time, duration
- Step-by-step status table: state name, status badge, attempt count, duration, error (if Failed)
- Expandable row per state: shows full `input` and `output` as formatted JSON
- Event timeline: ordered list of state transition events from ClickHouse
- Reconnect notice: if WebSocket reconnects, re-fetches `listExecutionStates(id)` to reconcile missed events

---

### `/services` — Services Overview (`services/+page.svelte`)

**Data sources:**
- Initial load: `listServices()`
- Live updates: `WSEvent{ type: "service_update" }` updates the matching row in-place

**What it shows:**
- Table: service name, mode (Deployment/Lambda), status badge, replica count (Deployment only), last invocation time, endpoint URL (Ingress host if exposed)
- Clicking a row expands inline detail: timeout, scale bounds, cluster IP, full invocation history (link to `/logs?service_name=<name>`)
- Status badge colours: Pending=grey, Running=green, Failed=red, Stopped=grey

---

### `/logs` — Log Explorer (`logs/+page.svelte`)

**Data sources:**
- On demand: `queryLogs(params)` — called when search form is submitted or filters change
- No live WebSocket updates (logs are append-only; polling on demand is sufficient)

**What it shows:**
- Filter form: execution ID, service name, state name, log level, time range (since/until), free-text search (`q`)
- Results table: timestamp, level badge, source (execution or service), state name, message
- Pagination: next/previous page via `offset` param; total count displayed
- Level badge colours: INFO=blue, WARN=yellow, ERROR=red, DEBUG=grey
- Empty state: "No logs found for the selected filters."

---

### `ui/svelte.config.js`

```javascript
import adapter from '@sveltejs/adapter-static';

export default {
  kit: {
    adapter: adapter({
      // Output to ui/build/ — Control Plane embeds this as static assets,
      // OR a separate container serves it (deployment decision TBD; see Phase 10).
      pages: 'build',
      assets: 'build',
      fallback: 'index.html',   // SPA fallback for client-side routing
    }),
    paths: {
      // If served at a sub-path by the Control Plane (e.g. /ui), set base here.
      // Default: served at root.
      base: '',
    },
  },
};
```

---

### `ui/vite.config.ts`

```typescript
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [sveltekit()],
  server: {
    proxy: {
      // In local dev, proxy API calls to the Control Plane
      '/api': {
        target: process.env.VITE_API_TARGET ?? 'http://localhost:8080',
        changeOrigin: true,
      },
      '/api/v1/ws': {
        target: process.env.VITE_API_TARGET ?? 'ws://localhost:8080',
        ws: true,
        changeOrigin: true,
      },
    },
  },
});
```

---

### `ui/package.json`

```json
{
  "name": "kflow-ui",
  "version": "0.1.0",
  "private": true,
  "scripts": {
    "dev":   "vite dev",
    "build": "vite build",
    "check": "svelte-kit sync && svelte-check --tsconfig ./tsconfig.json",
    "lint":  "prettier --check . && eslint ."
  },
  "devDependencies": {
    "@sveltejs/adapter-static": "^3.0.0",
    "@sveltejs/kit":            "^2.0.0",
    "svelte":                   "^5.0.0",
    "svelte-check":             "^3.6.0",
    "typescript":               "^5.0.0",
    "vite":                     "^5.0.0",
    "prettier":                 "^3.0.0",
    "eslint":                   "^9.0.0",
    "@typescript-eslint/eslint-plugin": "^7.0.0"
  }
}
```

No runtime npm dependencies. All data fetching uses the native `fetch` API. The WebSocket client uses the native `WebSocket` API.

---

## Dashboard Deployment (Open Question)

**This is an explicitly unresolved design decision in `AGENTS.md`.** Two options:

| Option | Description | Tradeoffs |
|--------|-------------|-----------|
| Embedded in Control Plane binary | `go:embed ui/build` in `cmd/orchestrator/main.go`; served at `/` | Single binary to deploy; Control Plane binary grows in size; rebuild required to update UI |
| Separate container in Helm chart | `ui/` built into its own nginx container; served independently | Independent deployments; separate scaling; extra Helm resource |

**Until resolved, the dashboard is built as a static SPA (`adapter-static`) and the deployment method is deferred to Phase 10.** `ui/build/` is the artifact; how it is served is a Phase 10 concern.

---

## Design Invariants

1. The dashboard is a **pure read layer**. It never writes to the Control Plane API (no workflow start, no service registration). All mutation APIs are out of scope for v1.
2. WebSocket events update local Svelte stores in-place. The dashboard never reads from ClickHouse via WebSocket — only REST.
3. On WebSocket reconnect, the affected page re-fetches its full state via REST to reconcile missed events.
4. All REST calls use the native `fetch` API. No third-party HTTP client libraries.
5. The WebSocket client is created once per app lifecycle (in the root layout), not per page.
6. No server-side rendering (SSR). The dashboard is a pure SPA (`adapter-static`). All data is fetched client-side.
7. Log queries are on-demand (user-initiated). No background polling for new log lines.
8. The dashboard must be fully functional with the WebSocket disconnected — it degrades gracefully to polling-free REST-only mode. Users can manually refresh pages.
9. TypeScript strict mode (`"strict": true` in `tsconfig.json`). No `any` types except in JSON payload fields typed as `Record<string, unknown>`.
10. `npm run check` must pass with zero type errors before the phase is considered complete.

---

## Acceptance Criteria / Verification

- [ ] `npm install && npm run build` in `ui/` succeeds with zero errors.
- [ ] `npm run check` passes with zero TypeScript type errors.
- [ ] `npm run lint` passes with zero linting or formatting errors.
- [ ] Executions overview: running a workflow from the CLI causes a new row to appear in the UI within one WebSocket event.
- [ ] Execution detail: each state transition updates the state's status badge in real time.
- [ ] Execution detail: clicking a completed state row shows its input and output JSON.
- [ ] Services overview: a newly registered service appears after page load or on `service_update` WebSocket event.
- [ ] Log explorer: querying `?q=Payment` returns only log lines containing "Payment".
- [ ] Log explorer: pagination navigates correctly (`offset` increments by `limit`).
- [ ] WebSocket disconnect: the reconnect indicator appears; on reconnect, execution detail refreshes.
- [ ] Dashboard loads and renders correctly with no WebSocket connection (REST-only fallback).
- [ ] `ui/build/` is produced and contains `index.html` suitable for static serving.
