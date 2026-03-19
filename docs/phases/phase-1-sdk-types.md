# Phase 1 — SDK Types & Skeleton

## Goal

Define every public type, interface, constructor, sentinel error, and validation rule that lives in `pkg/kflow/`. This phase produces a compilable Go package with no runtime logic — stubs only. All later phases depend on these types being stable and correct.

---

## Phase Dependencies

None. This is the foundation layer.

---

## Files to Create

| File | Purpose |
|------|---------|
| `pkg/kflow/input.go` | `Input`, `Output` type aliases |
| `pkg/kflow/state.go` | `Succeed`/`Fail` sentinels, `HandlerFunc`, `ChoiceFunc`, `RetryPolicy` |
| `pkg/kflow/workflow.go` | `Workflow`, `TaskDef`, `StepBuilder` |
| `pkg/kflow/service.go` | `ServiceDef`, `ServiceMode` |
| `pkg/kflow/runner.go` | `Run()`, `RunService()`, `RunLocal()` stubs + sentinel errors |
| `pkg/kflow/validate.go` | `Validate()` logic |

---

## Key Types / Interfaces / Functions

### `pkg/kflow/input.go`

```go
// Input is the data passed into a state handler from the previous state's output.
// Must be JSON-serialisable at all times.
type Input = map[string]any

// Output is the data returned by a state handler; becomes the next state's Input.
// Must be JSON-serialisable at all times.
type Output = map[string]any
```

### `pkg/kflow/state.go`

```go
// HandlerFunc is the signature for all inline Task and Service handler functions.
type HandlerFunc func(ctx context.Context, input Input) (Output, error)

// ChoiceFunc is the signature for Choice state handlers.
// The returned string must match a registered state name or a sentinel.
type ChoiceFunc func(ctx context.Context, input Input) (string, error)

// RetryPolicy controls retry behaviour for a Task or InvokeService step.
type RetryPolicy struct {
    MaxAttempts    int // must be >= 1
    BackoffSeconds int // seconds between attempts; 0 means no delay
}

// Succeed is the built-in terminal success sentinel.
// Use as the target of Next() to end a workflow successfully.
const Succeed = "__succeed__"

// Fail is the built-in terminal failure sentinel.
// Use as the target of Next() or Catch() to end a workflow as failed.
const Fail = "__fail__"
```

### `pkg/kflow/workflow.go`

```go
// Workflow is the top-level object that holds state registrations and the flow graph.
type Workflow struct {
    name  string
    tasks map[string]*TaskDef
    steps []*StepBuilder
}

// NewWorkflow creates a new Workflow with the given name.
func NewWorkflow(name string) *Workflow

// Task registers an inline Task state. fn may be nil if InvokeService is called on the
// returned TaskDef. Having both fn and InvokeService, or neither, is a validation error.
func (w *Workflow) Task(name string, fn HandlerFunc) *TaskDef

// Choice registers a Choice state. fn must not be nil.
func (w *Workflow) Choice(name string, fn ChoiceFunc) *TaskDef

// Wait registers a timed pause state for the given duration.
func (w *Workflow) Wait(name string, dur time.Duration) *TaskDef

// Parallel registers a parallel branch state (details TBD in Phase 2).
func (w *Workflow) Parallel(name string) *TaskDef

// Flow sets the ordered list of step transitions. Must be called once before Run/RunLocal.
func (w *Workflow) Flow(steps ...*StepBuilder)

// Validate checks all registration and flow invariants. Returns nil if valid.
// See Validation Rules section below.
func (w *Workflow) Validate() error

// TaskDef is the definition of a single task registration.
type TaskDef struct {
    name          string
    fn            HandlerFunc
    choiceFn      ChoiceFunc
    serviceTarget string // set by InvokeService
}

// InvokeService marks this Task as delegating to a named Service.
// Calling this when fn is non-nil is a validation error.
func (t *TaskDef) InvokeService(serviceName string) *TaskDef

// Retry attaches a RetryPolicy to this task.
func (t *TaskDef) Retry(policy RetryPolicy) *TaskDef

// Catch sets the error-handler state name for this task.
// The catch state receives the original Input plus an "_error" key.
func (t *TaskDef) Catch(stateName string) *TaskDef

// StepBuilder defines a single state's transition rules in the flow graph.
type StepBuilder struct {
    name        string
    next        string
    catch       string
    retry       *RetryPolicy
    isEnd       bool
}

// Step returns a new StepBuilder for the named state.
func Step(name string) *StepBuilder

// Next sets the successor state (or Succeed/Fail sentinel).
func (s *StepBuilder) Next(stateName string) *StepBuilder

// Catch sets the error-handler state for this step.
func (s *StepBuilder) Catch(stateName string) *StepBuilder

// Retry attaches a retry policy to this step (overrides TaskDef.Retry if both set).
func (s *StepBuilder) Retry(policy RetryPolicy) *StepBuilder

// End marks this step as a terminal state (equivalent to Next(Succeed)).
func (s *StepBuilder) End() *StepBuilder
```

### `pkg/kflow/service.go`

```go
// ServiceMode controls how the Service is deployed in Kubernetes.
type ServiceMode int

const (
    // Deployment creates a K8s Deployment + K8s Service (persistent, long-running).
    // Scale min >= 1 is enforced.
    Deployment ServiceMode = iota

    // Lambda creates a K8s Job per invocation; terminates after one request.
    // Scale and Port are ignored.
    Lambda
)

// ServiceDef is the definition of a standalone Service.
type ServiceDef struct {
    name        string
    fn          HandlerFunc
    mode        ServiceMode
    port        int
    minScale    int
    maxScale    int
    ingressHost string
    timeout     time.Duration
}

// NewService creates a new ServiceDef with the given name.
func NewService(name string) *ServiceDef

// Handler sets the handler function. Required.
func (s *ServiceDef) Handler(fn HandlerFunc) *ServiceDef

// Mode sets the deployment mode (default: Deployment).
func (s *ServiceDef) Mode(mode ServiceMode) *ServiceDef

// Port sets the listen port for Deployment-mode services (default: 8080).
// Ignored for Lambda mode.
func (s *ServiceDef) Port(port int) *ServiceDef

// Scale sets the min/max replica bounds for Deployment mode.
// min must be >= 1. Ignored for Lambda mode.
func (s *ServiceDef) Scale(min, max int) *ServiceDef

// Expose sets the Ingress hostname, creating a K8s Ingress resource.
// Omit to keep the Service cluster-internal only.
func (s *ServiceDef) Expose(host string) *ServiceDef

// Timeout sets the per-request deadline (default: 30s).
func (s *ServiceDef) Timeout(d time.Duration) *ServiceDef
```

### `pkg/kflow/runner.go`

```go
// Run registers the workflow with the Control Plane and begins orchestration.
// If the binary is invoked with --state=<name>, runs only that task function and exits.
// If the binary is invoked with --service=<name>, enters the service execution path.
// Otherwise, serialises the workflow graph and submits it to the Control Plane API.
// Safe to call alongside RunService in the same main().
func Run(wf *Workflow)

// RunService registers and deploys the service with the Control Plane.
// If the binary is invoked with --service=<name>, enters the service execution path:
//   - Deployment mode: starts an HTTP listener on the configured port.
//   - Lambda mode: reads KFLOW_INPUT env, calls the handler once, writes output, exits.
// Safe to call alongside Run in the same main().
func RunService(svc *ServiceDef)

// RunLocal runs the workflow entirely in-process without Kubernetes Jobs.
// Intended for local development and testing only. Never use in production.
// Uses MemoryStore from internal/store. Not safe to call with Run in the same main().
func RunLocal(wf *Workflow, input Input)
```

### `pkg/kflow/validate.go` — Sentinel Errors

```go
var (
    // ErrDuplicateName is returned when two states or services share the same name.
    // Service names and state names share the same namespace within a binary.
    ErrDuplicateName = errors.New("kflow: duplicate state or service name")

    // ErrMissingHandler is returned when a Task has neither an inline fn nor an InvokeService target.
    ErrMissingHandler = errors.New("kflow: task has no handler and no InvokeService target")

    // ErrAmbiguousHandler is returned when a Task has both an inline fn and an InvokeService target.
    ErrAmbiguousHandler = errors.New("kflow: task has both inline handler and InvokeService target")

    // ErrUnknownState is returned when a Next or Catch reference names a state that is not registered.
    ErrUnknownState = errors.New("kflow: reference to unknown state name")

    // ErrNoEntryPoint is returned when Flow() has not been called or the flow is empty.
    ErrNoEntryPoint = errors.New("kflow: workflow has no entry point (call Flow first)")

    // ErrScaleMin is returned when a Deployment-mode service has min scale < 1.
    ErrScaleMin = errors.New("kflow: Deployment-mode service must have min scale >= 1")
)
```

---

## Validation Rules

`Workflow.Validate()` enforces the following in order:

1. **No-entry-point check**: `Flow()` must have been called with at least one step.
2. **Duplicate name check**: Every registered state name and every registered service name must be globally unique within the binary (shared namespace).
3. **Handler ambiguity check (per task)**: Each `Task` must have exactly one of: inline `fn` or `InvokeService` target. Both is `ErrAmbiguousHandler`; neither is `ErrMissingHandler`.
4. **Unknown state reference check**: Every `Next()` and `Catch()` target in all StepBuilders must resolve to either a registered state name, `Succeed`, or `Fail`.
5. **Scale min check (per service)**: Every `ServiceDef` with `mode == Deployment` must have `minScale >= 1`.

`Run()` and `RunService()` call `Validate()` internally before any network activity and panic or log-fatal on error.

---

## Design Invariants

- `Input` and `Output` are `map[string]any`. Never change this — JSON serialisability is a hard requirement for state store round-trips.
- `Succeed` and `Fail` are untyped string constants, not states. They must never be registered as task names.
- `StepBuilder.End()` is syntactic sugar for `Next(Succeed)`.
- `TaskDef.Catch()` and `StepBuilder.Catch()` are equivalent; the step-level value wins if both are set on the same state.
- `HandlerFunc` and `ChoiceFunc` are distinct types to prevent misuse in `wf.Choice()`.
- `RetryPolicy.MaxAttempts` of 0 means "use the default" (implementation-defined in Phase 2); negative values are invalid.
- `RunLocal` is in this package but is a dev-only path — it must be clearly documented as not for production use.

---

## Acceptance Criteria / Verification

- [ ] `pkg/kflow` compiles with `go build ./pkg/kflow/...` with zero errors.
- [ ] `go vet ./pkg/kflow/...` reports no issues.
- [ ] `Workflow.Validate()` unit tests cover all six sentinel errors.
- [ ] A workflow with `wf.Task("A", nil).InvokeService("pricing")` + `wf.Task("A", fn)` returns `ErrAmbiguousHandler`.
- [ ] A workflow with `wf.Task("A", nil)` (no InvokeService) returns `ErrMissingHandler`.
- [ ] A `StepBuilder` referencing an unregistered state name returns `ErrUnknownState`.
- [ ] A `ServiceDef` with `Mode(Deployment).Scale(0, 5)` returns `ErrScaleMin`.
- [ ] Two states or services with the same name return `ErrDuplicateName`.
- [ ] A workflow with no `Flow()` call returns `ErrNoEntryPoint`.
- [ ] `go test ./pkg/kflow/...` passes (unit tests only; no external dependencies).
