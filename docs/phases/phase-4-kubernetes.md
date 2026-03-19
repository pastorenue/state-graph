# Phase 4 — Kubernetes Integration

## Goal

Implement Kubernetes Job dispatch for workflow states. Replace the in-process `Executor.Handler` with a `K8sExecutor` that spawns a K8s Job per state, waits for completion using the Watch API, and reads output from the store. Implement the `--state=<name>` execution path so the shared binary can run a single task function and exit. Introduce `cmd/orchestrator/main.go` as the Control Plane entry point.

---

## Phase Dependencies

- **Phase 1**: `pkg/kflow` types — `HandlerFunc`, `Input`, `Output`, sentinel errors.
- **Phase 2**: `internal/engine.Executor`, `internal/engine.Graph`, `internal/store.Store` interface.
- **Phase 3**: `internal/store.MongoStore` (production store), `internal/config.Config`.

---

## Files to Create

| File | Purpose |
|------|---------|
| `internal/k8s/client.go` | `Client` — wraps `k8s.io/client-go`, handles kubeconfig |
| `internal/k8s/job.go` | `JobSpec`, `CreateJob`, `DeleteJob`, `jobName`, `WaitForJob`, `JobResult` |
| `internal/engine/k8s_executor.go` | `K8sExecutor` — production executor using K8s Jobs |
| `cmd/orchestrator/main.go` | Control Plane entry point: flag dispatch, store init, K8s client init |

---

## Key Types / Interfaces / Functions

### `internal/k8s/client.go`

```go
// Client wraps the Kubernetes client-go clientset with project-specific helpers.
type Client struct {
    clientset *kubernetes.Clientset
    namespace string
}

// NewClient creates a K8s client. Tries in-cluster config first (KUBERNETES_SERVICE_HOST),
// then falls back to kubeconfig (~/.kube/config or KUBECONFIG env var).
// namespace is the Kubernetes namespace for all resources created by this client.
func NewClient(namespace string) (*Client, error)

// Namespace returns the namespace this client is scoped to.
func (c *Client) Namespace() string
```

Dependencies: `k8s.io/client-go/kubernetes`, `k8s.io/client-go/tools/clientcmd`, `k8s.io/client-go/rest`.

---

### `internal/k8s/job.go`

#### `JobSpec`

```go
// JobSpec describes a Kubernetes Job to be created for a single state execution.
type JobSpec struct {
    // Name is the K8s job name. Must be DNS-safe and <= 63 characters.
    // Use jobName() to generate this from execution ID and state name.
    Name      string

    // Image is the container image to run. Typically the same image as the orchestrator.
    Image     string

    // Args are the command-line arguments for the container (e.g. ["--state=ValidateOrder"]).
    Args      []string

    // Env is the list of environment variables to set in the container.
    Env       []EnvVar

    // Namespace overrides the client's default namespace (optional).
    Namespace string
}

// EnvVar is a name-value pair for a container environment variable.
type EnvVar struct {
    Name  string
    Value string
}
```

#### Job Name Convention

```go
// jobName produces a DNS-safe Kubernetes Job name from an execution ID and state name.
// Format: "kflow-<execID[:16]>-<stateName-kebab>"
// Rules:
//   - execID is truncated to 16 characters (UUID without hyphens recommended as source)
//   - stateName is lowercased and non-alphanumeric characters replaced with "-"
//   - Total length capped at 63 characters (K8s DNS label limit)
//   - Trailing hyphens are stripped
func jobName(execID, stateName string) string
```

Examples:
- `execID = "550e8400e29b41d4"`, `stateName = "ValidateOrder"` → `"kflow-550e8400e29b41d4-validate-order"`
- `execID = "550e8400e29b41d4"`, `stateName = "ChargePayment"` → `"kflow-550e8400e29b41d4-charge-payment"`

#### Job CRUD

```go
// CreateJob creates a Kubernetes Job from JobSpec and returns the job name.
// The Job uses RestartPolicy=Never. On Job completion (success or failure),
// the container exits and the Job is not restarted.
// Returns the actual job name used (may differ from spec.Name if truncated).
func (c *Client) CreateJob(ctx context.Context, spec JobSpec) (string, error)

// DeleteJob deletes a Kubernetes Job by name (best-effort; non-fatal if not found).
func (c *Client) DeleteJob(ctx context.Context, jobName string) error
```

#### `WaitForJob`

```go
// JobResult holds the outcome of a completed Kubernetes Job.
type JobResult struct {
    Succeeded bool
    Failed    bool
    Message   string // failure reason from K8s Job conditions, or empty on success
}

// WaitForJob watches the Job resource and blocks until the Job reaches a terminal
// condition (Succeeded or Failed). Uses the K8s Watch API — no polling.
//
// Watch implementation:
//   1. Call clientset.BatchV1().Jobs(ns).Watch(ctx, ListOptions{FieldSelector: "metadata.name=<name>"})
//   2. Range over result.ResultChan()
//   3. On MODIFIED events, inspect job.Status.Conditions for "Complete" (type) or "Failed" (type)
//   4. Return JobResult when terminal condition found
//   5. Return error if the Watch channel closes before a terminal condition
//
// The caller's context controls the deadline. A context.DeadlineExceeded means
// the Job did not complete within the allowed time — treat as failure.
func (c *Client) WaitForJob(ctx context.Context, jobName string) (JobResult, error)
```

---

### `internal/engine/k8s_executor.go`

```go
// K8sExecutor implements the production state executor that dispatches states as K8s Jobs.
// It wraps Executor with a Handler that spawns a Job, waits for it, and reads output.
type K8sExecutor struct {
    Store     store.Store
    K8s       *k8s.Client
    Image     string           // container image for the Job (same image as the binary)
    Namespace string
    Telemetry *telemetry.EventWriter // optional; nil disables telemetry (Phase 6)
}

// Run drives a workflow execution using K8s Jobs.
// Creates an Executor with a K8s-backed Handler, then calls executor.Run().
func (e *K8sExecutor) Run(ctx context.Context, execID string, g *engine.Graph, input kflow.Input) error
```

#### K8s Handler (internal to `k8s_executor.go`)

```go
// k8sHandler is the Handler func injected into Executor for K8s Job dispatch.
// It is not exported; it is constructed inline inside K8sExecutor.Run().
func k8sHandler(execID, stateName string, input kflow.Input) func(ctx context.Context, stateName string, input kflow.Input) (kflow.Output, error)
```

Execution sequence per state:

```
1. [Write-ahead is called by Executor before this Handler is invoked]
2. Compute jobName(execID, stateName)
3. client.CreateJob(ctx, JobSpec{
       Name:  jobName,
       Image: e.Image,
       Args:  ["--state=" + stateName],
       Env: [
           {Name: "KFLOW_EXECUTION_ID", Value: execID},
           {Name: "KFLOW_MONGO_URI",    Value: cfg.MongoURI},
           {Name: "KFLOW_MONGO_DB",     Value: cfg.MongoDB},
       ],
   })
4. client.WaitForJob(ctx, jobName)
5. If JobResult.Failed → return error (the state's error is already written to the store by the Job container)
6. store.GetStateOutput(ctx, execID, stateName) → return output
7. client.DeleteJob(ctx, jobName)  [best-effort cleanup; non-fatal]
```

If `Telemetry` is non-nil, call `Telemetry.RecordStateTransition` at steps 3 (→ Running) and after step 4 (→ Completed or Failed).

---

### `--state=<name>` Execution Path

When the binary is invoked with `--state=<stateName>`:

```
1. Parse flags: --state=<name>
2. Read env vars: KFLOW_EXECUTION_ID, KFLOW_MONGO_URI, KFLOW_MONGO_DB
3. Create MongoStore using the env var config
4. Call store.GetStateOutput(ctx, execID, prevStateName) to get Input
   OR store.GetExecution(ctx, execID).Input if this is the first state
5. Look up the HandlerFunc for stateName in the workflow's task registry
6. Call the HandlerFunc(ctx, input)
7. On success: store.CompleteState(ctx, execID, stateName, output)
   On error:   store.FailState(ctx, execID, stateName, errMsg)
8. Exit 0 on success, Exit 1 on error
```

The state name to `HandlerFunc` lookup uses the `Workflow.tasks` map (same process, same binary image). Step 4 determines input by reading the predecessor state's output from the store. The predecessor state name is determined by the graph (also reconstructed in-process).

**Env vars consumed by `--state` path:**

| Variable | Required | Description |
|----------|----------|-------------|
| `KFLOW_EXECUTION_ID` | Yes | The UUID of the current workflow execution |
| `KFLOW_MONGO_URI` | Yes | MongoDB connection URI |
| `KFLOW_MONGO_DB` | No | MongoDB database name (default: `kflow`) |

---

### `cmd/orchestrator/main.go`

```go
// main is the Control Plane entry point. It handles three execution modes:
//   --state=<name>   → Task execution path (see above)
//   --service=<name> → Service execution path (Phase 5)
//   (no flag)        → Control Plane server mode: register workflows, serve API
func main()
```

Startup sequence for server mode (no flag):

```
1. LoadConfig()
2. NewMongoStore(ctx, cfg.MongoURI, cfg.MongoDB)
3. MongoStore.EnsureIndexes(ctx)
4. k8s.NewClient(cfg.Namespace)
5. (Phase 5) Start HTTP API server
6. Block on signal (SIGTERM / SIGINT)
```

---

## Design Invariants

1. `WaitForJob` uses the K8s Watch API, never polling with `time.Sleep`. Polling is explicitly forbidden — it creates unnecessary load on the K8s API server and introduces latency.
2. `CreateJob` sets `RestartPolicy: Never`. The orchestrator manages retries, not Kubernetes.
3. `jobName()` output is deterministic for a given `(execID, stateName)` pair. This allows idempotent Job creation checks.
4. `DeleteJob` is always best-effort: the executor must continue even if deletion fails (log the error, do not return it).
5. Write-ahead is performed by `Executor` before calling the K8s Handler. The Handler never calls `WriteAheadState` or `MarkRunning` — these are the Executor's responsibility.
6. The `--state=<name>` binary writes exactly one `CompleteState` or `FailState` and then exits. It never loops.
7. `K8sExecutor.Telemetry` field is `nil`-safe. All calls are guarded with `if e.Telemetry != nil`.
8. The orchestrator binary and the task-execution binary are the **same image**. Flag dispatch is the only mechanism that selects the execution path.
9. `KFLOW_EXECUTION_ID` and `KFLOW_MONGO_URI` are required env vars for the `--state=<name>` path; the binary must exit with a clear error message if they are absent.
10. In-cluster and out-of-cluster kubeconfig are tried in that order. The orchestrator must work both inside the cluster (production) and outside (local development with a kubeconfig).

---

## Acceptance Criteria / Verification

- [ ] `go build ./internal/k8s/... ./internal/engine/... ./cmd/orchestrator/...` with zero errors.
- [ ] `jobName("550e8400e29b41d4a4b2", "ValidateOrder")` → `"kflow-550e8400e29b41d4-validate-order"` (verify truncation and kebab-case).
- [ ] `jobName` output is always <= 63 characters for any valid input.
- [ ] Unit test: `jobName` with a 100-character state name is truncated to <= 63 characters.
- [ ] Unit test: `k8sHandler` calls `CreateJob`, then `WaitForJob`, then `GetStateOutput`, then `DeleteJob` in that order (use a mock K8s client and mock store).
- [ ] Integration test (requires `KFLOW_TEST_K8S`): end-to-end workflow execution via K8s Jobs completes successfully.
- [ ] `--state=<name>` path with missing `KFLOW_EXECUTION_ID` exits non-zero with a readable error.
- [ ] `--state=<name>` path with missing `KFLOW_MONGO_URI` exits non-zero with a readable error.
- [ ] `WaitForJob` uses Watch, not polling (verify: no `time.Sleep` in `job.go`).
- [ ] `K8sExecutor` with `Telemetry: nil` does not panic during execution.
