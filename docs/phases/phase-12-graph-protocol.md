# Phase 12 — Graph Serialisation & Large Output Handling

## Goal

Define the canonical JSON schema for workflow graph registration and execution start. Specify how large state outputs (> 1 MB) are stored in an S3-compatible object store instead of MongoDB, with transparent dereferencing at read time.

---

## DDD Classification

| DDD Construct | Type(s) in this phase |
|---|---|
| Aggregate Root Serialisation | Workflow graph JSON = serialised form of the `Workflow` Aggregate Root; the `POST /api/v1/workflows` body is the canonical REST representation transcoded from the protobuf schema |
| Infrastructure Adapter | `ObjectStore` — S3-compatible large-output backend; transparent to domain callers via `store.GetStateOutput` |
| Infrastructure Mapping | `_ref` pointer in `StateRecord.Output` — an infrastructure-level indirection marker; never exposed to domain logic outside `store.GetStateOutput` |

**Proto as canonical schema:** The Workflow graph JSON schema documented in Section A is the grpc-gateway transcoded form of `proto/kflow/v1/workflow.proto` and `proto/kflow/v1/types.proto`. The protobuf definitions (Phase 13) are the canonical schema. When the REST JSON schema and the proto schema conflict, the proto schema wins.

**`buf generate` workflow:** Run `buf generate` in `proto/` to regenerate `internal/gen/`. The REST JSON schema is derived from `internal/gen/*.pb.gw.go`. Never hand-edit files in `internal/gen/`.

**`handler_ref` is now resolved:** `handler_ref = ""` for Go in-process states. For Python/Rust states, `handler_ref` identifies the container runner language. The gRPC RunnerService protocol (Phase 13) is the mechanism — `handler_ref` is informational metadata in the graph, not a routing key.

---

## Phase Dependencies

- **Phase 3** must be complete. `MongoStore` and `Config` must be stable.
- **Phase 5** must be complete. Workflow registration (`POST /api/v1/workflows`) and execution start (`POST /api/v1/workflows/:name/run`) routes must exist.

---

## Files to Create

| File | Purpose |
|------|---------|
| `internal/store/object_store.go` | Thin S3-compatible client wrapper for large output storage |

---

## Section A — Workflow Graph JSON Schema

### `POST /api/v1/workflows` — Register Workflow

Registers a compiled workflow graph. The body is the canonical graph JSON:

```json
{
  "name": "order-fulfillment",
  "states": [
    {
      "name": "ValidateOrder",
      "type": "task",
      "handler_ref": "",
      "service_target": "",
      "retry": {
        "max_attempts": 3,
        "backoff_seconds": 2,
        "max_backoff_seconds": 30
      },
      "catch": "HandleValidationError"
    },
    {
      "name": "ChargePayment",
      "type": "task",
      "handler_ref": "",
      "service_target": "payment-service",
      "retry": null,
      "catch": ""
    },
    {
      "name": "HandleValidationError",
      "type": "task",
      "handler_ref": "",
      "service_target": "",
      "retry": null,
      "catch": ""
    }
  ],
  "flow": [
    { "name": "ValidateOrder",       "next": "ChargePayment",         "catch": "HandleValidationError", "is_end": false, "retry": null },
    { "name": "ChargePayment",       "next": "",                      "catch": "",                       "is_end": true,  "retry": null },
    { "name": "HandleValidationError", "next": "",                    "catch": "",                       "is_end": true,  "retry": null }
  ]
}
```

#### Schema Fields

**Top-level:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Unique workflow name. Must match `[a-z0-9-]+`. |
| `states` | array | Yes | State definitions. |
| `flow` | array | Yes | Ordered execution flow (edges). |

**`states[]` items:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | State name. Unique within the workflow. |
| `type` | string | Yes | State type. Enum: `"task"`, `"choice"`, `"parallel"`, `"wait"`. |
| `handler_ref` | string | No | For Go states resolved in-process: `""` (empty). For Python/Rust containers: informational only (e.g. `"python"`, `"rust"`); routing is via gRPC `RunnerService` token, not this field. See Phase 13. |
| `service_target` | string | No | Name of a registered `Service` to invoke via `InvokeService`. Empty for direct task states. |
| `retry` | object\|null | No | Retry policy. `null` means no retry. |
| `catch` | string | No | Name of the error-handler state. Empty if none. |

**`retry` object:**

| Field | Type | Description |
|-------|------|-------------|
| `max_attempts` | integer | Maximum total attempts (including the first). |
| `backoff_seconds` | integer | Base backoff interval in seconds. |
| `max_backoff_seconds` | integer | Backoff cap in seconds. |

**`flow[]` items:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | State name this flow entry describes. |
| `next` | string | No | Successor state name. Empty when `is_end: true`. |
| `catch` | string | No | Error-handler state name. May differ from the `states[].catch` field (flow-level catch overrides state-level catch if both present; implementation must document which wins). |
| `is_end` | boolean | No | `true` if this state has no successor (terminal). |
| `retry` | object\|null | No | Flow-level retry policy. Takes precedence over `states[].retry` if both present. |

#### Response

- `201 Created` — workflow registered. Body: `{ "name": "<workflow-name>" }`.
- `409 Conflict` — workflow name already registered.
- `400 Bad Request` — schema validation failure. Body: `{ "error": "<reason>", "code": "invalid_graph" }`.

---

### `GET /api/v1/workflows/:name` — Retrieve Workflow

Returns the registered graph JSON for the named workflow.

- `200 OK` — body is the same schema as `POST /api/v1/workflows` body.
- `404 Not Found` — no workflow with that name.

---

### `POST /api/v1/workflows/:name/run` — Start Execution

```
POST /api/v1/workflows/:name/run
Content-Type: application/json

{ "input": { "order_id": "abc-123", "amount": 99.99 } }
```

Response `202 Accepted`:

```json
{ "execution_id": "550e8400-e29b-41d4-a716-446655440000" }
```

- `404 Not Found` — workflow not registered.
- `400 Bad Request` — malformed body.

The `input` field is the `kflow.Input` passed to the entry state. It may be an empty object (`{}`).

---

### `handler_ref` Semantics

| Runtime | Value | Meaning |
|---------|-------|---------|
| Go (in-process) | `""` (empty string) | Handler is resolved by state name within the compiled binary. |
| Python SDK | `"python"` (informational) | Container communicates via gRPC `RunnerService` (Phase 13). `handler_ref` is metadata only — routing is via token, not this field. |
| Rust SDK | `"rust"` (informational) | Container communicates via gRPC `RunnerService` (Phase 13). Same as Python. |

> **Resolved in Phase 13:** The multi-language runner protocol is gRPC `RunnerService`. `handler_ref` is an informational field in the graph schema and does not affect execution routing. The Control Plane dispatches all non-Go states via the same `RunnerService` gRPC contract regardless of `handler_ref` value.

---

## Section B — Large Output Handling

### Inline Threshold

State outputs larger than **1 MB** (1,048,576 bytes, measured as the JSON-serialised size of `kflow.Output`) are stored in an S3-compatible object store rather than MongoDB.

### `_ref` Pointer

When an output is offloaded to object storage, `StateRecord.Output` in MongoDB stores a pointer document instead of the actual output:

```json
{ "_ref": "s3://kflow-outputs/executions/550e8400.../states/ChargePayment/attempt-1.json" }
```

Key format: `executions/<executionID>/states/<stateName>/attempt-<N>.json`

The raw output JSON is stored at the referenced key in the object store.

### Transparent Dereferencing

`store.GetStateOutput` must transparently dereference `_ref` markers before returning:

```
1. Fetch StateRecord.Output from MongoDB.
2. If Output contains only the key "_ref":
   a. Parse the URI from Output["_ref"].
   b. Fetch the object from the object store.
   c. JSON-decode the fetched bytes as kflow.Output.
   d. Return the decoded output.
3. Otherwise: return Output directly (inline, no dereference).
```

Callers of `store.GetStateOutput` are unaware of whether the output was stored inline or in object storage — the interface is identical.

### `internal/store/object_store.go`

```go
// ObjectStore is a thin wrapper around an S3-compatible object storage client.
type ObjectStore struct {
    client     *s3.Client // AWS SDK v2 S3 client
    bucketName string
}

// NewObjectStore creates an ObjectStore using the provided URI.
// URI format: "s3://<bucket-name>" or an endpoint-overridden URI for MinIO/GCS/etc.
// Credentials are sourced from the standard AWS credential chain
// (env vars, instance profile, etc.).
func NewObjectStore(ctx context.Context, uri string) (*ObjectStore, error)

// Put stores data at the given key. The key must be a path (no leading slash).
// Overwrites existing objects silently.
func (s *ObjectStore) Put(ctx context.Context, key string, data []byte) error

// Get retrieves the object at the given key.
// Returns ErrObjectNotFound if the key does not exist.
func (s *ObjectStore) Get(ctx context.Context, key string) ([]byte, error)

// ErrObjectNotFound is returned by Get when the key does not exist.
var ErrObjectNotFound = errors.New("object store: key not found")
```

### Configuration

```go
// ObjectStoreURI is the S3-compatible object store URI for large output storage.
// Source: KFLOW_OBJECT_STORE_URI
// If empty, large output offload is disabled.
ObjectStoreURI string
```

Added to `internal/config/config.go`.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `KFLOW_OBJECT_STORE_URI` | No | `""` | S3-compatible URI (e.g. `s3://kflow-outputs`). If empty, large output offload is disabled. |

### Behaviour When `KFLOW_OBJECT_STORE_URI` Is Empty

If `KFLOW_OBJECT_STORE_URI` is not configured and a state output exceeds 1 MB:

- `store.CompleteState` returns `ErrOutputTooLarge`.
- The executor treats `ErrOutputTooLarge` as a terminal failure for that state (calls `FailState` with the error message).
- The execution continues to the Catch handler if one is defined; otherwise the execution fails.

```go
// ErrOutputTooLarge is returned by CompleteState when the output exceeds 1 MB
// and no object store is configured.
var ErrOutputTooLarge = errors.New("store: output exceeds 1 MB and no object store is configured")
```

### MongoStore Changes

`MongoStore.CompleteState` must be updated to:

1. JSON-encode the output and measure its size.
2. If size > 1 MB and `ObjectStore != nil`: call `ObjectStore.Put`, store the `_ref` pointer in MongoDB.
3. If size > 1 MB and `ObjectStore == nil`: return `ErrOutputTooLarge`.
4. If size <= 1 MB: store inline as before.

`MongoStore.GetStateOutput` must be updated to dereference `_ref` markers (see above).

`MongoStore` gains an optional `ObjectStore *ObjectStore` field:

```go
type MongoStore struct {
    client      *mongo.Client
    db          *mongo.Database
    ObjectStore *ObjectStore // nil = large output offload disabled
}
```

---

## Design Invariants

1. The `_ref` pointer format is always `{ "_ref": "s3://..." }` — a single-key JSON object. No other keys are present alongside `_ref`.
2. `store.GetStateOutput` is the **only** place where `_ref` dereferencing happens. Callers must never inspect raw `StateRecord.Output` for `_ref` markers directly.
3. Object store keys are deterministic: `executions/<execID>/states/<stateName>/attempt-<N>.json`. No random suffixes.
4. Large output offload does not affect the write-ahead protocol. `WriteAheadState` always writes `StatusPending` with empty output; offload happens only in `CompleteState`.
5. The 1 MB threshold is measured on the **JSON-serialised** output, not the in-memory Go map size.
6. `handler_ref = ""` is the only valid value for Go in-process states in v1. Non-empty `handler_ref` is informational metadata for multi-language containers (e.g. `"python"`, `"rust"`); all non-Go states communicate with the Control Plane via gRPC `RunnerService` (Phase 13) regardless of `handler_ref` value.

---

## Open Questions (Deferred)

The following items are explicitly deferred and not addressed in this phase:

| Topic | Status |
|-------|--------|
| Python/Rust runner protocol | **Resolved in Phase 13** — gRPC `RunnerService` |
| Workflow versioning (multiple versions of the same workflow name) | Deferred |
| Service versioning | Deferred |
| Cost accounting (execution cost per workflow / service) | Deferred |
| OpenTelemetry integration (distributed tracing across Go, Python, Rust) | Deferred |
| Scale-to-zero for Deployment-mode services | Deferred |

---

## Acceptance Criteria / Verification

- [ ] `go build ./internal/store/...` compiles with `object_store.go` added.
- [ ] `POST /api/v1/workflows` with valid graph JSON returns `201`.
- [ ] `POST /api/v1/workflows` with duplicate name returns `409`.
- [ ] `GET /api/v1/workflows/:name` returns the registered graph JSON.
- [ ] `POST /api/v1/workflows/:name/run` with `{ "input": {} }` returns `202` with a valid UUID `execution_id`.
- [ ] `ObjectStore.Put` followed by `ObjectStore.Get` round-trips the data correctly.
- [ ] `CompleteState` with output > 1 MB and a configured `ObjectStore` stores a `_ref` pointer in MongoDB.
- [ ] `GetStateOutput` transparently dereferences a `_ref` pointer and returns the original output.
- [ ] `CompleteState` with output > 1 MB and no `ObjectStore` returns `ErrOutputTooLarge`.
- [ ] `GetStateOutput` on an inline output (< 1 MB) returns it directly without any object store call.
- [ ] Integration test (requires `KFLOW_OBJECT_STORE_URI`): full execution with a > 1 MB state output completes successfully and the output is retrievable via `GetStateOutput`.
- [ ] `go test -run TestObjectStore ./internal/store/...` skips gracefully when `KFLOW_OBJECT_STORE_URI` is absent.
