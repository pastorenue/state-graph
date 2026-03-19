# Phase 3 — MongoDB Persistence

## Goal

Replace `MemoryStore` with a durable `MongoStore` that implements the same `Store` interface. All production execution paths use `MongoStore`. Introduce the `Config` struct and environment-based configuration loading. Provide integration tests gated on a real MongoDB connection.

---

## Phase Dependencies

- **Phase 1**: `pkg/kflow` types (`Input`, `Output`) must be stable.
- **Phase 2**: `internal/store.Store` interface and all sentinel errors must be defined and stable. `MongoStore` must satisfy the same interface as `MemoryStore`.

---

## Files to Create

| File | Purpose |
|------|---------|
| `internal/store/mongo_store.go` | `MongoStore` — MongoDB implementation of `Store` |
| `internal/store/mongo_store_test.go` | Integration tests (require `KFLOW_TEST_MONGO_URI`) |
| `internal/config/config.go` | `Config` struct and `LoadConfig()` |

---

## Key Types / Interfaces / Functions

### `internal/config/config.go`

```go
// Config holds all runtime configuration loaded from environment variables.
type Config struct {
    // MongoURI is the MongoDB connection URI. Required.
    // Source: KFLOW_MONGO_URI
    MongoURI string

    // MongoDB is the database name. Defaults to "kflow".
    // Source: KFLOW_MONGO_DB
    MongoDB string

    // Namespace is the Kubernetes namespace used for all workloads (Jobs, Deployments,
    // Services, Ingress). Defaults to "kflow". Propagated to the K8s client as the
    // target namespace for all resource operations.
    // Source: KFLOW_NAMESPACE
    Namespace string
}

// LoadConfig reads configuration from environment variables.
// Returns an error if any required variable is missing.
func LoadConfig() (*Config, error)
```

Environment variables:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `KFLOW_MONGO_URI` | Yes | — | MongoDB connection URI (e.g. `mongodb://localhost:27017`) |
| `KFLOW_MONGO_DB` | No | `kflow` | MongoDB database name |
| `KFLOW_NAMESPACE` | No | `kflow` | Kubernetes namespace for all workload resources |

---

### `internal/store/mongo_store.go`

#### Collection Names

```go
const (
    collExecutions = "kflow_executions"
    collStates     = "kflow_states"
)
```

#### BSON Document Structs

```go
// executionDoc is the BSON representation of an ExecutionRecord.
type executionDoc struct {
    ID        string    `bson:"_id"`
    Workflow  string    `bson:"workflow"`
    Status    string    `bson:"status"`
    Input     bson.M    `bson:"input"`
    CreatedAt time.Time `bson:"created_at"`
    UpdatedAt time.Time `bson:"updated_at"`
}

// stateDoc is the BSON representation of a StateRecord.
// The _id is a composite key: "<executionID>:<stateName>:<attempt>".
// Using attempt in the key allows concurrent retry records.
// The (execution_id, state_name) unique index enforces only one non-terminal
// record per (execution, state) pair — WriteAheadState is the enforcement point.
type stateDoc struct {
    ID          string    `bson:"_id"`          // "<execID>:<stateName>:<attempt>"
    ExecutionID string    `bson:"execution_id"`
    StateName   string    `bson:"state_name"`
    Status      string    `bson:"status"`
    Input       bson.M    `bson:"input"`
    Output      bson.M    `bson:"output"`
    Error       string    `bson:"error"`
    Attempt     int       `bson:"attempt"`
    CreatedAt   time.Time `bson:"created_at"`
    UpdatedAt   time.Time `bson:"updated_at"`
}
```

#### `MongoStore`

```go
// MongoStore is the production implementation of Store backed by MongoDB.
type MongoStore struct {
    client *mongo.Client
    db     *mongo.Database
}

// NewMongoStore connects to MongoDB using the provided URI and database name,
// ensures all required indexes exist (via EnsureIndexes), and returns a ready store.
func NewMongoStore(ctx context.Context, uri, dbName string) (*MongoStore, error)

// EnsureIndexes creates all required indexes if they do not already exist.
// Safe to call on every startup (CreateMany is idempotent for existing indexes).
func (s *MongoStore) EnsureIndexes(ctx context.Context) error

// Compile-time interface assertion
var _ Store = (*MongoStore)(nil)
```

`MongoStore` must implement all methods of the `Store` interface defined in Phase 2.

---

## Index Definitions

Created via `collection.Indexes().CreateMany(ctx, []mongo.IndexModel{...})` in `EnsureIndexes`.

### `kflow_executions`

| Index | Keys | Options |
|-------|------|---------|
| `executions_status_idx` | `{ "status": 1 }` | background |
| `executions_workflow_created_idx` | `{ "workflow": 1, "created_at": -1 }` | background |

### `kflow_states`

| Index | Keys | Options |
|-------|------|---------|
| `states_exec_state_idx` | `{ "execution_id": 1, "state_name": 1 }` | background |
| `states_exec_status_idx` | `{ "execution_id": 1, "status": 1 }` | background |

Note: `_id` on both collections is the primary unique index (MongoDB default). For `kflow_states`, the composite `_id` string (`<execID>:<stateName>:<attempt>`) serves as the unique key for a specific attempt.

---

## Idempotency Guard — `WriteAheadState`

`WriteAheadState` must enforce that a terminal state record cannot be overwritten:

```
1. Query kflow_states for documents where:
      execution_id == record.ExecutionID
   AND state_name  == record.StateName
   AND status IN ("Completed", "Failed")

2. If any terminal document exists → return ErrStateAlreadyTerminal

3. Otherwise → InsertOne the new stateDoc with status = "Pending"
```

The `InsertOne` uses the composite `_id` `"<execID>:<stateName>:<attempt>"`. If two concurrent callers race to write the same attempt, MongoDB's `_id` uniqueness guarantees exactly one succeeds; the loser receives a duplicate key error, which the store wraps as `ErrStateAlreadyTerminal`.

The terminal-record check in step 1 must use a session with `ReadConcern: majority` to avoid stale reads in replica set environments.

---

## MongoDB Driver Usage

Use the official Go driver: `go.mongodb.org/mongo-driver/mongo`.

Key driver conventions:

- All operations accept a `context.Context`; timeouts are the caller's responsibility.
- `UpdateOne` uses `$set` for partial field updates (never full document replacement).
- Use `bson.M` for ad-hoc filter/update documents; use typed structs for full document insert/decode.
- `mongo.IsDuplicateKeyError(err)` detects `_id` collision on `InsertOne`.
- All connections are pooled by the `*mongo.Client`; do not create per-request clients.

---

## Integration Test Gates

Integration tests in `mongo_store_test.go` are skipped unless `KFLOW_TEST_MONGO_URI` is set:

```go
func requireMongo(t *testing.T) string {
    uri := os.Getenv("KFLOW_TEST_MONGO_URI")
    if uri == "" {
        t.Skip("KFLOW_TEST_MONGO_URI not set; skipping MongoDB integration tests")
    }
    return uri
}
```

Each test creates a uniquely named database (e.g. `kflow_test_<uuid>`) and drops it in `t.Cleanup`. Tests must not share state across test cases.

Integration test cases:

| Test | Scenario |
|------|----------|
| `TestCreateAndGetExecution` | Round-trip `CreateExecution` → `GetExecution` |
| `TestUpdateExecution` | Status transitions `Pending → Running → Completed` |
| `TestWriteAheadAndComplete` | Full write-ahead → MarkRunning → CompleteState cycle |
| `TestWriteAheadIdempotency` | Second `WriteAheadState` for a Completed state returns `ErrStateAlreadyTerminal` |
| `TestRetryAttempts` | Two failed attempts followed by a successful third (three stateDoc records) |
| `TestGetStateOutput_NotFound` | `GetStateOutput` on missing state returns `ErrStateNotFound` |
| `TestGetStateOutput_NotCompleted` | `GetStateOutput` on Running state returns `ErrStateNotCompleted` |
| `TestEnsureIndexes_Idempotent` | `EnsureIndexes` called twice does not error |

---

> **Note: AGENTS.md Discrepancy**
> AGENTS.md line 229 references "Postgres" as the state store. This is an error in AGENTS.md.
> MongoDB is the canonical state store as specified throughout the rest of AGENTS.md and all phase docs.
> All implementation must target MongoDB. No Postgres driver should be introduced.

---

## Design Invariants

1. `MongoStore` must be a drop-in replacement for `MemoryStore` — all `Store` interface methods must have identical semantics.
2. `WriteAheadState` uses `InsertOne`, not `Upsert`, to leverage MongoDB's duplicate key detection.
3. `EnsureIndexes` is called at startup. The Control Plane must not serve requests until indexes exist.
4. No raw BSON queries outside of `mongo_store.go`. All store logic is encapsulated in `MongoStore` methods.
5. `LoadConfig()` must not read from files or command-line flags — environment variables only.
6. The `collExecutions` and `collStates` constants must not be referenced outside of `mongo_store.go`.
7. BSON document structs (`executionDoc`, `stateDoc`) are unexported. Only the `Store` interface types are exposed.

---

## Acceptance Criteria / Verification

- [ ] `go build ./internal/store/... ./internal/config/...` compiles with zero errors.
- [ ] `var _ store.Store = (*store.MongoStore)(nil)` compile-time assertion passes.
- [ ] `LoadConfig()` returns an error when `KFLOW_MONGO_URI` is unset.
- [ ] `LoadConfig()` returns `MongoDB = "kflow"` when `KFLOW_MONGO_DB` is unset.
- [ ] `LoadConfig()` returns `Namespace = "kflow"` when `KFLOW_NAMESPACE` is unset.
- [ ] Unit test (no Mongo needed): `LoadConfig()` reads correct values from env vars.
- [ ] All integration tests pass when `KFLOW_TEST_MONGO_URI` points to a live MongoDB.
- [ ] Integration test confirms `EnsureIndexes` creates expected indexes (verify via `listIndexes`).
- [ ] `WriteAheadState` idempotency test: second call for a Completed state returns `ErrStateAlreadyTerminal`.
- [ ] `go test -run TestMongo ./internal/store/...` skips gracefully when env var is absent.
