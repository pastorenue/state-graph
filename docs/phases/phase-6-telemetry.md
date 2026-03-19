# Phase 6 — Telemetry & Observability

## Goal

Add append-only observability storage in ClickHouse for execution events, service metrics, and log lines. Wire telemetry writers into the K8s executor and service dispatcher. Expose REST endpoints for the dashboard to query telemetry data. ClickHouse is never used for control-flow decisions — MongoDB remains the authority for execution state.

---

## Phase Dependencies

- **Phase 1**: `pkg/kflow` types.
- **Phase 2**: `internal/store.Status`, `internal/engine.Executor`.
- **Phase 3**: `internal/config.Config` (for ClickHouse DSN config).
- **Phase 4**: `internal/engine.K8sExecutor` (`Telemetry *telemetry.EventWriter` field).
- **Phase 5**: `internal/controller.ServiceDispatcher` (`Telemetry *telemetry.MetricsWriter` field), `internal/api.Server` (REST telemetry endpoints).

---

## Files to Create

| File | Purpose |
|------|---------|
| `internal/telemetry/clickhouse.go` | `Client`, `InitSchema()` — ClickHouse connection and idempotent DDL |
| `internal/telemetry/events.go` | `EventWriter` — records state transitions to `execution_events` |
| `internal/telemetry/metrics.go` | `MetricsWriter` — records service invocations to `service_metrics` |
| `internal/telemetry/logs.go` | `LogWriter` — writes captured log lines to `logs` |

---

## ClickHouse Table Schemas

All tables use the `MergeTree` engine. `InitSchema()` runs all DDL with `CREATE TABLE IF NOT EXISTS`, making it idempotent on every startup.

### `execution_events`

```sql
CREATE TABLE IF NOT EXISTS execution_events (
    event_id     UUID    DEFAULT generateUUIDv4(),
    execution_id String,
    state_name   String,
    from_status  String,
    to_status    String,
    error        String,
    occurred_at  DateTime64(3) DEFAULT now64()
) ENGINE = MergeTree()
ORDER BY (execution_id, occurred_at)
TTL occurred_at + INTERVAL 90 DAY
SETTINGS index_granularity = 8192;
```

TTL: 90 days. Records are never updated or deleted manually — the TTL merge handles expiry.

### `service_metrics`

```sql
CREATE TABLE IF NOT EXISTS service_metrics (
    metric_id     UUID    DEFAULT generateUUIDv4(),
    service_name  String,
    invocation_id String,
    duration_ms   UInt64,
    status_code   UInt16,
    error         String,
    occurred_at   DateTime64(3) DEFAULT now64()
) ENGINE = MergeTree()
ORDER BY (service_name, occurred_at)
TTL occurred_at + INTERVAL 90 DAY
SETTINGS index_granularity = 8192;
```

TTL: 90 days. `status_code` is the HTTP status code for Deployment-mode invocations; use `200` for successful Lambda invocations and `500` for failures.

### `logs`

```sql
CREATE TABLE IF NOT EXISTS logs (
    log_id       UUID    DEFAULT generateUUIDv4(),
    execution_id String,
    service_name String,
    state_name   String,
    level        String,
    message      String,
    occurred_at  DateTime64(3) DEFAULT now64()
) ENGINE = MergeTree()
ORDER BY (execution_id, occurred_at)
TTL occurred_at + INTERVAL 30 DAY
SETTINGS index_granularity = 8192;
```

TTL: 30 days (shorter than events/metrics — logs are high-volume). `execution_id` and `service_name` are mutually exclusive in practice (a log line is associated with either an execution or a standalone service call), but both fields are present to simplify the schema.

---

## Key Types / Interfaces / Functions

### `internal/telemetry/clickhouse.go`

```go
// Client wraps the ClickHouse Go driver connection.
type Client struct {
    conn driver.Conn
}

// NewClient connects to ClickHouse using the provided DSN.
// DSN format: "clickhouse://<host>:<port>?database=<db>&username=<u>&password=<p>"
func NewClient(ctx context.Context, dsn string) (*Client, error)

// InitSchema creates all required tables if they do not already exist.
// Safe to call on every startup — all DDL uses CREATE TABLE IF NOT EXISTS.
func (c *Client) InitSchema(ctx context.Context) error

// Close closes the ClickHouse connection.
func (c *Client) Close() error
```

ClickHouse driver: `github.com/ClickHouse/clickhouse-go/v2`

Config additions to `internal/config/config.go`:

```go
// ClickHouseDSN is the ClickHouse connection DSN. Required when telemetry is enabled.
// Source: KFLOW_CLICKHOUSE_DSN
ClickHouseDSN string
```

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `KFLOW_CLICKHOUSE_DSN` | No | `""` | ClickHouse DSN. If empty, telemetry writers are not initialised (no-op mode). |

When `KFLOW_CLICKHOUSE_DSN` is empty, the Control Plane starts without telemetry. All telemetry writer fields remain `nil`. All `nil`-safety guards (see Injection Pattern) suppress writes silently.

---

### `internal/telemetry/events.go`

```go
// EventWriter records state transition events to the execution_events table.
type EventWriter struct {
    ch *Client
}

func NewEventWriter(ch *Client) *EventWriter

// RecordStateTransition records a state lifecycle event asynchronously.
// This method is fire-and-forget: it spawns a goroutine and returns immediately.
// Errors from ClickHouse are logged but never propagated to the caller.
// This method must NEVER be called in the critical execution path in a blocking manner.
func (w *EventWriter) RecordStateTransition(
    ctx context.Context,
    execID, stateName, fromStatus, toStatus, errMsg string,
)
```

Implementation notes:
- Use a background goroutine for each write: `go func() { ... }()`
- Log write errors at WARN level; do not return them
- The `ctx` passed in should be a background context with a short timeout (e.g. 5 seconds), not the caller's execution context (which may be cancelled before the write completes)

---

### `internal/telemetry/metrics.go`

```go
// MetricsWriter records service invocation metrics to the service_metrics table.
type MetricsWriter struct {
    ch *Client
}

func NewMetricsWriter(ch *Client) *MetricsWriter

// RecordServiceInvocation records a single service invocation metric asynchronously.
// Fire-and-forget; errors are logged but not propagated.
//
// Parameters:
//   serviceName:  the registered service name
//   invocationID: a unique ID for this invocation (e.g. execID + ":" + stateName)
//   durationMs:   wall-clock duration of the invocation in milliseconds
//   statusCode:   HTTP status code (Deployment) or 200/500 (Lambda)
//   errMsg:       error message on failure, empty on success
func (w *MetricsWriter) RecordServiceInvocation(
    ctx context.Context,
    serviceName, invocationID string,
    durationMs uint64,
    statusCode uint16,
    errMsg string,
)
```

---

### `internal/telemetry/logs.go`

```go
// LogWriter writes captured log lines to the logs table.
type LogWriter struct {
    ch *Client
}

func NewLogWriter(ch *Client) *LogWriter

// Write records a single log line asynchronously.
// Fire-and-forget; errors are logged but not propagated.
//
// Parameters:
//   execID:      execution ID (empty for standalone service logs)
//   serviceName: service name (empty for task execution logs)
//   stateName:   state name (empty for service logs)
//   level:       log level string: "INFO" | "WARN" | "ERROR" | "DEBUG"
//   message:     the log line text
func (w *LogWriter) Write(
    ctx context.Context,
    execID, serviceName, stateName, level, message string,
)

// StreamJobLogs reads container logs from the Kubernetes API for a completed Job
// and writes each line to ClickHouse via LogWriter.
// Called by the Control Plane after WaitForJob returns (Phase 4).
//
// Implementation:
//   1. clientset.CoreV1().Pods(ns).List with labelSelector for the Job
//   2. For each pod: clientset.CoreV1().Pods(ns).GetLogs(podName, &PodLogOptions{})
//   3. Read log stream line-by-line; call w.Write for each line
//   4. Close stream; move to next pod
func StreamJobLogs(
    ctx context.Context,
    k8sClientset *kubernetes.Clientset,
    namespace, jobName, execID, stateName string,
    lw *LogWriter,
)
```

`StreamJobLogs` is a free function (not a method on LogWriter) to keep `logs.go` testable independently of the K8s client.

---

## REST Telemetry Endpoints

These endpoints are added to `internal/api/server.go` (Phase 5 Server):

### `GET /api/v1/executions/:id/events`

Returns state transition events for an execution, ordered by `occurred_at` ascending.

Query parameters:
- `since` (optional): ISO 8601 timestamp; returns events after this time
- `limit` (optional): integer; default 100, max 1000

Response:
```json
{
  "events": [
    {
      "event_id": "uuid",
      "execution_id": "uuid",
      "state_name": "ValidateOrder",
      "from_status": "Pending",
      "to_status": "Running",
      "error": "",
      "occurred_at": "2026-03-19T10:00:00.000Z"
    }
  ]
}
```

### `GET /api/v1/services/:name/metrics`

Returns invocation metrics for a service.

Query parameters:
- `since` (optional): ISO 8601 timestamp
- `until` (optional): ISO 8601 timestamp
- `limit` (optional): integer; default 100, max 1000

Response:
```json
{
  "metrics": [
    {
      "metric_id": "uuid",
      "service_name": "pricing-service",
      "invocation_id": "execID:stateName",
      "duration_ms": 42,
      "status_code": 200,
      "error": "",
      "occurred_at": "2026-03-19T10:00:00.000Z"
    }
  ]
}
```

### `GET /api/v1/logs`

Full-text searchable log query endpoint.

Query parameters:
- `execution_id` (optional): filter by execution ID
- `service_name` (optional): filter by service name
- `state_name` (optional): filter by state name
- `level` (optional): filter by log level (`INFO`, `WARN`, `ERROR`, `DEBUG`)
- `since` (optional): ISO 8601 timestamp
- `until` (optional): ISO 8601 timestamp
- `q` (optional): full-text search string (matched against `message` using `LIKE '%<q>%'`)
- `limit` (optional): integer; default 100, max 1000
- `offset` (optional): integer for pagination; default 0

Response:
```json
{
  "logs": [
    {
      "log_id": "uuid",
      "execution_id": "uuid",
      "service_name": "",
      "state_name": "ChargePayment",
      "level": "INFO",
      "message": "Payment processed successfully",
      "occurred_at": "2026-03-19T10:00:00.000Z"
    }
  ],
  "total": 1
}
```

---

## Injection Pattern

Telemetry writers are injected as optional fields. All injection points must be `nil`-safe.

### `K8sExecutor` (Phase 4)

```go
type K8sExecutor struct {
    // ... existing fields ...
    Telemetry *telemetry.EventWriter // nil = no telemetry
}
```

Usage in `k8s_executor.go`:
```go
if e.Telemetry != nil {
    e.Telemetry.RecordStateTransition(ctx, execID, stateName, "Pending", "Running", "")
}
```

### `ServiceDispatcher` (Phase 5)

```go
type ServiceDispatcher struct {
    // ... existing fields ...
    Telemetry *telemetry.MetricsWriter // nil = no telemetry
}
```

### Log Capture

`StreamJobLogs` is called from the K8s executor after `WaitForJob` returns. It requires a `*telemetry.LogWriter` parameter. If `nil`, the call is skipped:

```go
if lw != nil {
    telemetry.StreamJobLogs(ctx, c.clientset, namespace, jobName, execID, stateName, lw)
}
```

---

## Design Invariants

1. **Append-only**: ClickHouse tables are never updated or deleted by the engine. The TTL merge process handles expiry. Corrections are new rows, not updates.
2. **Non-blocking writes**: All telemetry writes are fire-and-forget goroutines. Write errors are logged at WARN level and never propagated to callers.
3. **No control-flow decisions**: The Control Plane never reads from ClickHouse to make execution decisions. MongoDB is the sole authority. ClickHouse is read-only from the dashboard's perspective.
4. **No-op when unconfigured**: If `KFLOW_CLICKHOUSE_DSN` is empty, all telemetry writers are `nil`. No calls to ClickHouse are made. The engine operates normally.
5. **Control Plane owns log capture**: Individual language SDKs (Python, Rust) never write to ClickHouse directly. The Control Plane captures container stdout/stderr via the K8s API (`StreamJobLogs`) and writes to ClickHouse.
6. **Context isolation for writes**: Telemetry goroutines must use a fresh `context.Background()` with a short timeout (not the caller's context), to prevent cancellation of in-flight writes when the parent execution context ends.
7. **`InitSchema` is idempotent**: Safe to call on every Control Plane startup. All DDL uses `CREATE TABLE IF NOT EXISTS`.
8. **Log volume**: The `logs` table has a 30-day TTL (shorter than events/metrics). The dashboard must not use logs for anything other than display — no log-based alerting or control-flow.
9. **`StreamJobLogs` is best-effort**: Log capture failures (e.g. pod logs unavailable after deletion) are logged at WARN level and do not affect execution correctness.
10. **WS and telemetry are decoupled.** `WSHub.Broadcast` is called **synchronously** from the Executor immediately after each state transition (non-blocking; the Executor does not wait for slow clients — they are dropped). `EventWriter.RecordStateTransition` is a **separate, independent fire-and-forget goroutine** spawned by the `EventWriter`. The two calls share no transaction, no channel, and have **no guaranteed ordering**: a WebSocket event may arrive at dashboard clients before or after the ClickHouse row is committed. Code must never assume that a WebSocket event implies the ClickHouse row already exists. The Executor triggers both calls sequentially in source, but their effects are observed independently.

---

## Config Additions (to `internal/config/config.go`)

```go
// ClickHouseDSN is the ClickHouse connection string.
// Source: KFLOW_CLICKHOUSE_DSN
// If empty, telemetry is disabled (no-op mode).
ClickHouseDSN string
```

`LoadConfig()` does not error if `KFLOW_CLICKHOUSE_DSN` is empty — telemetry is optional.

---

## Acceptance Criteria / Verification

- [ ] `go build ./internal/telemetry/...` with zero errors.
- [ ] `InitSchema()` called twice on the same database does not error (idempotency).
- [ ] `EventWriter.RecordStateTransition` with a nil `*Client` (or nil writer) does not panic.
- [ ] Unit test: `RecordStateTransition` goroutine writes exactly one row to `execution_events`.
- [ ] Unit test: `RecordServiceInvocation` writes exactly one row to `service_metrics`.
- [ ] Unit test: `LogWriter.Write` writes exactly one row to `logs`.
- [ ] `GET /api/v1/executions/:id/events` returns all transition events for a completed execution in chronological order.
- [ ] `GET /api/v1/services/:name/metrics` returns invocation records with correct `duration_ms` and `status_code`.
- [ ] `GET /api/v1/logs?execution_id=<id>` returns only log lines for that execution.
- [ ] `GET /api/v1/logs?q=Payment` returns only log lines containing "Payment".
- [ ] Control Plane starts normally when `KFLOW_CLICKHOUSE_DSN` is unset (telemetry disabled, no errors).
- [ ] Integration test (requires `KFLOW_TEST_CLICKHOUSE_DSN`): full workflow run produces expected rows in all three tables.
- [ ] `go test -run TestTelemetry ./internal/telemetry/...` skips gracefully when env var is absent.
