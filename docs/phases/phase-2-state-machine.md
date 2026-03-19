# Phase 2 — State Machine & RunLocal

## Goal

Implement the in-process state machine: the `Store` interface and `MemoryStore`, the compiled workflow `Graph`, and the `Executor` that drives state transitions. Wire everything together through `RunLocal` so a workflow can run end-to-end in a single process without Kubernetes. All later phases depend on this runtime contract being stable.

---

## DDD Classification

| DDD Construct | Type(s) in this phase |
|---|---|
| Aggregate Root | `ExecutionRecord` (owns the lifecycle of a single workflow run) |
| Entity | `ExecutionRecord` (identity: UUID), `StateRecord` (identity: execID+stateName+attempt) |
| Value Object | `Status` (string enum), `StateRecord.Input`/`Output` |
| Repository | `store.Store` interface |
| Domain Service | `Graph` (compiles `Workflow` → validated DAG), `Executor` (drives write-ahead → run → transition loop) |
| Application Service | `RunLocal` (orchestrates in-process execution for dev/test) |
| Infrastructure | `MemoryStore` (in-process only; never in production) |

**Write-ahead as aggregate consistency boundary:** The sequence `WriteAheadState` → `MarkRunning` → handler → `CompleteState`/`FailState` is the `ExecutionRecord` aggregate's primary consistency boundary. `Executor` is the only code that orchestrates this sequence.

**Control Plane as sole MongoDB writer (gRPC path):** For K8s-executed states (Phases 4+), `RunnerServiceServer` on the Control Plane calls `store.CompleteState`/`store.FailState` on behalf of containers. Containers never access MongoDB directly. The `MemoryStore`/`RunLocal` path is the only case where `Executor` calls `CompleteState`/`FailState` directly.

---

## Phase Dependencies

- **Phase 1** must be complete. All types in `pkg/kflow/` are assumed stable.

---

## Files to Create

| File | Purpose |
|------|---------|
| `internal/store/store.go` | `Store` interface, `ExecutionRecord`, `StateRecord`, `Status` enum |
| `internal/store/memory_store.go` | `MemoryStore` — mutex-protected in-memory implementation |
| `internal/engine/graph.go` | `Graph`, `Node` — compiled and validated workflow graph |
| `internal/engine/executor.go` | `Executor` — drives the write-ahead → run → transition loop |
| `pkg/kflow/runner.go` | Edit: implement `RunLocal()` using `MemoryStore` + `Executor` |

---

## Key Types / Interfaces / Functions

### `internal/store/store.go`

#### `Status`

```go
// Status represents the lifecycle state of an execution or individual state run.
type Status string

const (
    StatusPending   Status = "Pending"
    StatusRunning   Status = "Running"
    StatusCompleted Status = "Completed"
    StatusFailed    Status = "Failed"
)
```

#### `ExecutionRecord`

```go
// ExecutionRecord tracks the lifecycle of a single workflow execution.
type ExecutionRecord struct {
    ID        string       // globally unique execution ID (UUID)
    Workflow  string       // name of the workflow
    Status    Status
    Input     kflow.Input  // initial input passed to the entry state
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

#### `StateRecord`

```go
// StateRecord tracks the lifecycle of a single state run within an execution.
type StateRecord struct {
    ExecutionID string
    StateName   string
    Status      Status
    Input       kflow.Input  // input received by this state
    Output      kflow.Output // output produced (set on Completed)
    Error       string       // error message (set on Failed)
    Attempt     int          // 1-based attempt number
    ResumeAt    *time.Time   // non-nil for Wait states; reconciler unblocks when now() >= ResumeAt
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

#### `Store` interface

```go
// Store is the persistence interface for all execution and state lifecycle data.
// All implementations must satisfy the write-ahead protocol.
type Store interface {
    // CreateExecution persists a new execution record with StatusPending.
    // Returns an error if the execution ID already exists.
    CreateExecution(ctx context.Context, record ExecutionRecord) error

    // GetExecution retrieves an execution record by ID.
    GetExecution(ctx context.Context, execID string) (ExecutionRecord, error)

    // UpdateExecution updates only the Status and UpdatedAt fields of an execution.
    UpdateExecution(ctx context.Context, execID string, status Status) error

    // WriteAheadState persists a new StateRecord with StatusPending before any Job is created.
    // MUST be called before MarkRunning. Must return an error if a terminal record for
    // (executionID, stateName) already exists — idempotency guard.
    WriteAheadState(ctx context.Context, record StateRecord) error

    // MarkRunning transitions a state record from Pending to Running.
    // Called immediately before invoking the handler.
    MarkRunning(ctx context.Context, execID, stateName string) error

    // CompleteState transitions a state record to Completed and records its output.
    CompleteState(ctx context.Context, execID, stateName string, output kflow.Output) error

    // FailState transitions a state record to Failed and records the error message.
    FailState(ctx context.Context, execID, stateName string, errMsg string) error

    // GetStateOutput retrieves the Output of a Completed state.
    // Used by the executor to pass output from one state to the next.
    GetStateOutput(ctx context.Context, execID, stateName string) (kflow.Output, error)
}
```

---

### `internal/store/memory_store.go`

```go
// MemoryStore is a thread-safe, in-memory implementation of Store.
// All data is lost when the process exits. For RunLocal / testing only.
type MemoryStore struct {
    mu         sync.RWMutex
    executions map[string]*ExecutionRecord  // keyed by ExecutionID
    states     map[string]*StateRecord      // keyed by execID + ":" + stateName
}

func NewMemoryStore() *MemoryStore
```

Implementation notes:
- `WriteAheadState` checks for an existing terminal record (`StatusCompleted` or `StatusFailed`). If found, returns `ErrStateAlreadyTerminal` (sentinel defined in `store.go`). If a non-terminal record exists (e.g. `StatusPending` from a previous attempt), it may be overwritten — this supports retry with incremented `Attempt`.
- All mutations hold the write lock; all reads hold the read lock.
- `GetStateOutput` returns `ErrStateNotFound` if the key is absent, `ErrStateNotCompleted` if status is not `StatusCompleted`.

Sentinel errors in `store.go`:
```go
var (
    ErrExecutionNotFound   = errors.New("store: execution not found")
    ErrStateNotFound       = errors.New("store: state record not found")
    ErrStateNotCompleted   = errors.New("store: state is not in Completed status")
    ErrStateAlreadyTerminal = errors.New("store: state record already in terminal status")
)
```

---

### `internal/engine/graph.go`

```go
// Node represents a single state in the compiled workflow graph.
type Node struct {
    Name     string
    TaskDef  *kflow.TaskDef // nil for sentinel terminals
    Next     string         // successor state name or sentinel
    Catch    string         // error-handler state name (empty if none)
    Retry    *kflow.RetryPolicy
    Terminal bool           // true if Next is Succeed or Fail, or End() was called
}

// Graph is the compiled, validated representation of a Workflow's state machine.
type Graph struct {
    nodes map[string]*Node
    entry string // name of the entry state (first in Flow())
}

// Build compiles a *kflow.Workflow into a *Graph.
// Calls wf.Validate() internally; returns the same errors on failure.
// The entry state is the first step passed to wf.Flow().
func Build(wf *kflow.Workflow) (*Graph, error)

// EntryNode returns the first Node in the flow.
func (g *Graph) EntryNode() *Node

// Node returns the Node for the given state name, or nil if not found.
func (g *Graph) Node(name string) *Node

// Next returns the successor Node given the current node and an optional choice key.
// For Choice states, key is the string returned by ChoiceFunc.
// For all other states, key is ignored and the static Next field is used.
// Returns nil if the successor is a terminal sentinel (Succeed/Fail).
func (g *Graph) Next(node *Node, key string) (*Node, error)

// IsTerminal reports whether the node has no successor (terminal state).
func (n *Node) IsTerminal() bool
```

---

### `internal/engine/executor.go`

```go
// Executor drives the write-ahead → run → transition loop for a workflow execution.
// It is transport-agnostic: the Handler func may call an in-process function (RunLocal)
// or dispatch to Kubernetes (Phase 4 K8sExecutor).
type Executor struct {
    Store   store.Store
    // Handler resolves a state name + input to an output.
    // For RunLocal: calls the HandlerFunc directly.
    // For K8sExecutor: spawns a Kubernetes Job and waits.
    Handler func(ctx context.Context, stateName string, input kflow.Input) (kflow.Output, error)
}

// Run drives execution from the entry state to a terminal state.
// execID must correspond to a pre-existing ExecutionRecord in StatusPending.
func (e *Executor) Run(ctx context.Context, execID string, g *graph.Graph, input kflow.Input) error

// executeState performs the write-ahead → run → complete/fail cycle for a single state.
// It does NOT recurse; Run() calls it in a loop advancing through the graph.
func (e *Executor) executeState(ctx context.Context, execID string, node *graph.Node, input kflow.Input) (kflow.Output, error)

// executeParallel runs all branches of a Parallel state concurrently.
// Each branch gets its own write-ahead → run → complete cycle in a separate goroutine.
// All goroutines are joined via sync.WaitGroup before returning.
//
// Output merging: each branch's output is stored under the branch name as a top-level key.
// Example result for branches "branch_A" and "branch_B":
//
//   kflow.Output{
//       "branch_A": map[string]any{...branch A output...},
//       "branch_B": map[string]any{...branch B output...},
//   }
//
// If any branch fails (after exhausting retries / routing to its Catch), the parallel
// state itself is marked Failed. All other branches still run to completion — branches
// are not cancelled on sibling failure.
func (e *Executor) executeParallel(ctx context.Context, execID string, node *graph.Node, input kflow.Input) (kflow.Output, error)

// applyRetry wraps fn with retry logic according to policy.
// On each failed attempt, it calls FailState (to record the attempt) then WriteAheadState
// for the next attempt with Attempt incremented.
// Returns the last error if all attempts are exhausted.
func (e *Executor) applyRetry(ctx context.Context, execID string, node *graph.Node, fn func() (kflow.Output, error)) (kflow.Output, error)
```

#### Write-Ahead Protocol

The write-ahead protocol is **never bypassed**. Every state execution follows this exact sequence:

```
1. store.WriteAheadState(ctx, StateRecord{Status: Pending, Attempt: N})
        ↓ [must succeed before proceeding]
2. store.MarkRunning(ctx, execID, stateName)
        ↓
3. Call Handler (inline fn, K8s Job, or Service dispatch)
        ↓
4a. store.CompleteState(ctx, execID, stateName, output)   [on success]
4b. store.FailState(ctx, execID, stateName, errMsg)        [on error]
```

If step 1 fails (e.g. `ErrStateAlreadyTerminal`), the executor must not proceed to step 2. This is the idempotency guard — it prevents double-execution of completed states after a restart.

If step 3 panics, the state record remains in `StatusRunning`. A future recovery pass can detect orphaned `Running` records and mark them `Failed`.

#### Wait State Execution

Wait states delay execution for a specified duration before proceeding. The `StateRecord.ResumeAt` field is set to the target wake-up time when the write-ahead record is created.

**In production (`MongoStore` + K8s):**
A reconciler goroutine polls MongoDB for records where `status == "Pending"` and `resume_at <= now()`. When found, it calls `MarkRunning` then `CompleteState` (with empty output), allowing the Executor to proceed to the next state.

**In `RunLocal` (`MemoryStore`):**
`executeState` detects the `ResumeAt` field and calls `time.Sleep(time.Until(*node.ResumeAt))` inline before proceeding. No reconciler goroutine is used.

#### `_error` Key Convention

When a state fails and routes to a Catch handler, the Catch state receives its input as the original state's input merged with an `_error` key:

```json
{
  "_error": "payment gateway timeout",
  "order_id": "abc-123",
  "amount": 99.99
}
```

Rules:
- The value of `_error` is always a plain `string` — the `error.Error()` message from the failed handler.
- All original input keys from the failed state are preserved alongside `_error`.
- If the original input already contains an `_error` key, it is overwritten.
- The `_error` key is never present in the output of a successfully completed state.

---

### `pkg/kflow/runner.go` — `RunLocal` Implementation

```go
// RunLocalWorkflow creates a MemoryStore, compiles the workflow graph, generates a
// UUID execution ID, creates the ExecutionRecord, then runs the Executor with an
// inline Handler that calls each TaskDef's HandlerFunc directly.
// Blocks until the workflow reaches a terminal state or returns an error.
func RunLocalWorkflow(wf *Workflow, input Input) error
```

`RunLocal(wf, input)` is a convenience wrapper that calls `RunLocalWorkflow` and log-fatals on error.

The inline Handler for RunLocal:
```go
handler := func(ctx context.Context, stateName string, input kflow.Input) (kflow.Output, error) {
    node := g.Node(stateName)
    if node.TaskDef.ChoiceFn != nil {
        key, err := node.TaskDef.ChoiceFn(ctx, input)
        // Choice states return the branch key as a single-key Output
        return kflow.Output{"__choice__": key}, err
    }
    return node.TaskDef.Fn(ctx, input)
}
```

---

## Design Invariants

1. `WriteAheadState` is always called before any handler invocation — without exception.
2. `MemoryStore` is only used by `RunLocal` and tests. It must never be used in production execution paths (Phases 3–6 use `MongoStore`). In the `MemoryStore`/`RunLocal` path, `Executor` calls `store.CompleteState`/`store.FailState` directly. In the K8s path (Phase 4+), `RunnerServiceServer` is the sole caller of these methods — no container accesses MongoDB directly.
3. `Graph.Build()` calls `wf.Validate()` — callers do not need to call it separately.
4. `Executor` has no knowledge of Kubernetes. Transport is injected via the `Handler` func.
5. The entry state is always the first step in `wf.Flow()`. If `Flow()` was not called, `Build()` returns `ErrNoEntryPoint`.
6. Choice state output is opaque to the store — only the branch key is used for graph traversal. The store receives the full `Output` map (which may contain `__choice__`), but the executor uses the `__choice__` key internally.
7. A `Catch` target is only visited if all retry attempts fail. After a successful retry, execution continues normally.
8. Terminal nodes (`Succeed`/`Fail`) are not stored as `StateRecord`s — only real states are recorded.
9. `executeParallel` uses one goroutine per branch. Each branch has its own independent write-ahead → run → complete cycle. Branch results are merged into a top-level map keyed by branch name before being written to the store.
10. `StateRecord.ResumeAt` is only set for Wait states. Non-wait states must always have `ResumeAt == nil`.
11. The `_error` key in Catch state input is always a plain `string`. Catch handlers must not assume any other type for this key.

---

## Acceptance Criteria / Verification

- [ ] `go build ./internal/store/...` and `go build ./internal/engine/...` compile with zero errors.
- [ ] `MemoryStore` satisfies the `Store` interface (compile-time assertion: `var _ store.Store = (*store.MemoryStore)(nil)`).
- [ ] Unit test: simple linear workflow (A → B → Succeed) runs to completion with correct output threading.
- [ ] Unit test: task with `RetryPolicy{MaxAttempts: 3}` fails twice then succeeds on the third attempt.
- [ ] Unit test: task exhausts all retries, routes to Catch state, Catch state runs and terminates.
- [ ] Unit test: `Choice` state with two branches — each branch routes correctly based on ChoiceFunc return value.
- [ ] Unit test: `WriteAheadState` called with an existing terminal record returns `ErrStateAlreadyTerminal` and the executor does not re-run the state.
- [ ] `go test ./internal/store/... ./internal/engine/...` passes (no external dependencies).
- [ ] `RunLocal` drives a real workflow end-to-end in a `go test` process.
