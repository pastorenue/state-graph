# Kubernetes-Native Serverless Workflow Engine

## Project Overview

This project is a **serverless-style workflow orchestration tool** built in Go that runs on top of Kubernetes. It is conceptually similar to AWS Step Functions or AWS Lambda — but self-hosted, cost-optimized, and Kubernetes-native.

Users write only their business logic. The system handles containerization, scheduling, and lifecycle management by spinning up Kubernetes Jobs, Deployments, or Pods as needed.

---

## Core Concept

### Developer Experience

Users write Go functions and register them as states:

```go
package main

import (
    "context"
    "github.com/your-org/kflow"
)

func main() {
    wf := kflow.NewWorkflow("order-pipeline")

    wf.Task("ValidateOrder", func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
        // validation logic
        return kflow.Output{"valid": true}, nil
    })

    wf.Task("ChargePayment", func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
        // payment logic
        return kflow.Output{"charged": true}, nil
    })

    wf.Task("HandleFailure", func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
        // error handling logic
        return kflow.Output{}, nil
    })

    wf.Flow(
        kflow.Step("ValidateOrder").Next("ChargePayment").Catch("HandleFailure"),
        kflow.Step("ChargePayment").Next(kflow.Succeed),
        kflow.Step("HandleFailure").End(),
    )

    kflow.Run(wf)
}
```

The engine:
1. Compiles the workflow graph from registered functions + flow definitions
2. Submits each state as a Kubernetes Job (isolated execution) or runs in-process (fast mode)
3. Passes input/output between states via the state store
4. Handles retries, branching, error catching, and terminal states

### Service API

In addition to workflows, the engine supports **standalone Services** — persistent or on-demand handlers that can be invoked from workflow steps or directly over HTTP.

```go
type ServiceMode int
const (
    Deployment ServiceMode = iota  // K8s Deployment + Service + optional Ingress
    Lambda                         // K8s Job per request; terminates after one invocation
)

kflow.NewService("pricing-service").
    Handler(fn).                       // required; same HandlerFunc signature as Tasks
    Mode(kflow.Deployment).            // default: Deployment
    Port(8080).                        // listen port (ignored for Lambda)
    Scale(min, max).                   // replica bounds (ignored for Lambda; min >= 1)
    Expose("pricing.example.com").     // creates K8s Ingress; omit for cluster-internal only
    Timeout(10 * time.Second)          // per-request deadline (default 30s)

kflow.RunService(svc)  // analogous to kflow.Run(wf); safe to call both in same main()
```

**Workflow integration** — invoking a Service from a workflow step:

```go
// fn must be nil when InvokeService is used; having both or neither is a registration-time panic
wf.Task("GetPrice", nil).InvokeService("pricing-service")
```

`.Retry()` and `.Catch()` on `InvokeService` steps work identically to inline Tasks.

**Full example — Service + Workflow in one binary:**

```go
package main

import (
    "context"
    "time"
    "github.com/your-org/kflow"
)

func main() {
    // Define a persistent pricing service
    svc := kflow.NewService("pricing-service").
        Handler(func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
            price := computePrice(input["item_id"].(string))
            return kflow.Output{"price": price}, nil
        }).
        Mode(kflow.Deployment).
        Port(8080).
        Scale(1, 5).
        Expose("pricing.example.com").
        Timeout(10 * time.Second)

    // Define a workflow that invokes it
    wf := kflow.NewWorkflow("order-pipeline")

    wf.Task("GetPrice", nil).InvokeService("pricing-service")

    wf.Task("ChargePayment", func(ctx context.Context, input kflow.Input) (kflow.Output, error) {
        return kflow.Output{"charged": true}, nil
    })

    wf.Flow(
        kflow.Step("GetPrice").Next("ChargePayment").Catch("HandleFailure"),
        kflow.Step("ChargePayment").Next(kflow.Succeed),
        kflow.Step("HandleFailure").End(),
    )

    kflow.RunService(svc)
    kflow.Run(wf)
}
```

---

## Architecture

```
┌───────────────────────────────────────────────────────────────┐
│      kflow.Run(wf) / kflow.RunService(svc)  (Go binary)       │
│  - Serializes workflow graph OR service definition            │
│  - Submits to Control Plane API                               │
│  --state=<name>   → executes single Task function, exits      │
│  --service=<name> → HTTP listener (Deployment) or one-shot    │
└───────────────────────┬───────────────────────────────────────┘
                        │ HTTP / gRPC
┌───────────────────────▼───────────────────────────────────────┐
│                   Control Plane (Go)                          │
│  - Workflow execution lifecycle (unchanged)                   │
│  - Service Registry: tracks deployed Services                 │
│  - Service Dispatcher: routes InvokeService steps             │
│  - Reconciler: manages Deployment/Job/K8sService/Ingress      │
└──────┬────────────────────────────┬───────────────────────────┘
       │                            │
┌──────▼─────────┐      ┌───────────▼──────────────────────────┐
│  State Store   │      │   Kubernetes Client (Go)             │
│  (MongoDB)     │      │  - Jobs (Tasks + Lambda Services)    │
│  + Service     │      │  - Deployments (long-running Svcs)   │
│    Registry    │      │  - K8s Services (cluster routing)    │
└────────────────┘      │  - Ingress (external exposure)       │
                        └──────────────────────────────────────┘
```
---

## State Types

These are expressed as Go builder methods on the workflow object, not YAML keys.

| Method               | Behaviour                                              |
|----------------------|--------------------------------------------------------|
| `wf.Task(name, fn)`  | Runs fn in a Kubernetes Job; transitions on return     |
| `wf.Choice(name, fn)`| fn returns a branch key; engine routes accordingly     |
| `wf.Wait(name, dur)` | Pauses execution for a fixed duration                  |
| `wf.Parallel(name)`  | Spawns multiple branches concurrently, waits for all   |
| `kflow.Succeed`      | Terminal success state (built-in)                      |
| `kflow.Fail`         | Terminal failure state (built-in)                      |

> **Note:** `wf.Task("name", nil).InvokeService("svc-name")` is still a Task from the state-machine's perspective — it occupies a state node, participates in retries/catches, and transitions normally. The only difference is that execution is delegated to a registered Service rather than an inline function.

### Choice Example

```go
wf.Choice("RouteOrder", func(ctx context.Context, input kflow.Input) (string, error) {
    if input["amount"].(float64) > 1000 {
        return "HighValuePath", nil
    }
    return "StandardPath", nil
})
```

---

## Execution Model

### How Go Functions Become Kubernetes Jobs

Each registered function is resolved at runtime via one of two strategies:

**Strategy A — Shared binary (default, fast)**
The same compiled Go binary is deployed as the orchestrator. When a state needs to run, the control plane spawns a Kubernetes Job using that same image, passing a `--state=<name>` flag. The binary detects the flag, runs only that function, writes output to the state store, and exits.

```
kflow.Run(wf) / kflow.RunService(svc)
  └─ --state=<name>    → execute single Task function, write output to store, exit
  └─ --service=<name>  → Deployment: start HTTP listener loop
                         Lambda:     handle one request (from KFLOW_INPUT env), exit
  └─ (no flag)         → register workflow graph + services with Control Plane, wait/poll
```

**Strategy B — In-process (dev mode)**
For local development or low-latency needs, functions run in the same process. No Kubernetes Jobs are spawned. Retries and state transitions still go through the same engine logic, just without container isolation.

Set via: `kflow.RunLocal(wf)`

### How the Control Plane Dispatches `InvokeService` Steps

1. Look up Service in registry — fail if not found or not Running
2. Serialize `Input` as JSON
3. `Deployment` mode: POST to `http://<clusterIP>:<port>/invoke`, deserialise response as `Output`
4. `Lambda` mode: spawn K8s Job with `--service=<name>` + `KFLOW_INPUT` env var, poll completion, read `Output` from state store
5. Write-ahead guarantee applies: `(execution_id, state_name, status=Pending)` recorded before dispatch

---

## Input / Output Between States

- Each function receives a `kflow.Input` (map[string]any) from the previous state's output
- Return a `kflow.Output` (map[string]any) which becomes the next state's input
- The control plane stores each state's output in Postgres keyed by `(execution_id, state_name)`
- No shared volumes or environment variable passing — state store is the single source of truth

---

## Error Handling

```go
wf.Flow(
    kflow.Step("ChargePayment").
        Retry(kflow.RetryPolicy{MaxAttempts: 3, BackoffSeconds: 5}).
        Catch("HandleFailure").
        Next("FulfillOrder"),
)
```

- `Retry` — retries the same function on error before catching
- `Catch` — routes to a named error-handler state on final failure
- Error handler states receive the original input plus an `_error` key with the error message

---

## File Structure

```
/
├── cmd/
│   └── orchestrator/        # Control plane entrypoint
├── internal/
│   ├── api/
│   │   ├── service_handler.go      # NEW: register/status/deregister Service endpoints
│   │   └── ws_handler.go           # NEW: WebSocket hub
│   ├── controller/
│   │   └── service_dispatcher.go   # NEW: routes InvokeService steps to K8s resources
│   ├── engine/              # State machine graph: parse, validate, resolve next state
│   ├── k8s/
│   │   ├── deployment.go           # NEW: K8s Deployment + K8s Service CRUD + rollout watch
│   │   └── ingress.go              # NEW: K8s Ingress CRUD for Expose()
│   ├── store/
│   │   └── service_store.go        # NEW: ServiceRecord persistence (MongoDB)
│   └── telemetry/
│       ├── clickhouse.go           # NEW: ClickHouse client + schema init
│       ├── events.go               # NEW: execution event writer
│       ├── metrics.go              # NEW: service metrics writer
│       └── logs.go                 # NEW: log line writer
├── pkg/
│   └── kflow/               # Go SDK (unchanged location; Control Plane is Go)
│       ├── service.go              # NEW: ServiceDef, ServiceMode, NewService, RunService
│       ├── workflow.go             # EDIT: TaskDef gains InvokeService method + validation
│       ├── state.go
│       ├── input.go
│       └── runner.go               # EDIT: flag dispatch extended for --service=<name>
├── sdk/
│   ├── python/              # Python SDK
│   │   ├── kflow/
│   │   │   ├── __init__.py
│   │   │   ├── workflow.py
│   │   │   ├── service.py
│   │   │   └── runner.py
│   │   ├── pyproject.toml
│   │   └── README.md
│   └── rust/                # Rust SDK
│       ├── src/
│       │   ├── lib.rs
│       │   ├── workflow.rs
│       │   ├── service.rs
│       │   └── runner.rs
│       ├── Cargo.toml
│       └── README.md
├── ui/                      # NEW: SvelteKit dashboard
│   ├── src/
│   │   ├── routes/
│   │   │   ├── +page.svelte           # Executions overview
│   │   │   ├── executions/[id]/       # Execution detail + live step graph
│   │   │   ├── services/              # Services overview
│   │   │   └── logs/                  # Log explorer
│   │   ├── lib/
│   │   │   ├── ws.ts                  # WebSocket client (auto-reconnect)
│   │   │   └── api.ts                 # REST client for logs/metrics
│   │   └── app.html
│   ├── package.json
│   └── svelte.config.js
├── deployments/
│   └── k8s/                 # Helm chart for the control plane itself
└── AGENTS.md
```

---

## Key Invariants for the Agent

- **Never parse YAML for state definitions.** States are Go functions only.
- **The public SDK lives in `pkg/kflow`** — this is what user code imports.
- **Write-ahead persistence** — state must be written to the store before a Job is created, not after.
- **The `--state` flag pattern** is how the shared binary knows to run a single function vs. orchestrate.
- **`kflow.Input` and `kflow.Output` are `map[string]any`** — keep them JSON-serialisable.
- **RunLocal is for dev only** — never default to it in production paths.
- **A `wf.Task` step must have exactly one of: inline `fn` or `InvokeService` name.** Both or neither is a registration-time panic.
- **Service names share the same namespace as state names within a binary.** Collision is a registration error.
- **`--service=<name>` is the only way a Deployment-mode binary enters its serve loop.** `RunService` alone registers and submits; it does not start the server.
- **Service handlers must not write to the state store directly.** Output is returned via HTTP response (Deployment) or read from the store by the Control Plane after Job completion (Lambda). The Control Plane is the only writer.
- **Write-ahead still applies to `InvokeService` steps**, identical to Job-based Tasks.
- **Lambda-mode Services are stateless between invocations.** The Job-per-request lifecycle enforces this.
- **`RunService` and `kflow.Run` are safe to call in the same `main()`.** Flag dispatch selects exactly one execution path.
- **Scale min >= 1 for Deployment mode.** Scale-to-zero is a future feature (see TODOs).
- **ClickHouse is append-only from the engine's perspective.** No updates or deletes. Corrections are new rows.
- **The Control Plane never reads from ClickHouse for control-flow decisions.** MongoDB is the authority for execution state; ClickHouse is for read/query only by the dashboard.
- **WebSocket broadcasts are best-effort.** Missed events due to client disconnects do not affect execution correctness. The dashboard reconciles full state on reconnect via the REST API.
- **Log lines are emitted by the Control Plane on behalf of Job/Deployment containers.** Container stdout/stderr is captured by the Kubernetes Client and forwarded to `internal/telemetry/logs.go`; individual language SDKs do not write to ClickHouse directly.
- **Go**: shared binary + `--state=<name>` / `--service=<name>` strategy (unchanged).
- **Python/Rust**: container-per-language strategy; the compiled binary or interpreter is baked into the container image at deployment time; no cross-language binary sharing.
- **All SDKs must produce identical `Input`/`Output` semantics** (`map[string]any` equivalent): JSON-serialisable key-value maps.
- **The Control Plane is written in Go only and never imports Python or Rust SDKs**; it communicates with non-Go runtimes only through the runner protocol.

---

## Multi-Language Support

The engine supports Go, Python, and Rust. All SDKs live in this repo (monorepo). The Control Plane is Go-only; the SDKs are language-specific libraries that user code imports.

### Python SDK — decorator-based API

```python
import kflow

app = kflow.Workflow("order-pipeline")

@app.task("ValidateOrder")
def validate_order(input: dict) -> dict:
    return {"valid": True, "item_id": input["item_id"]}

@app.task("HandleFailure")
def handle_failure(input: dict) -> dict:
    return {}

app.flow(
    kflow.step("ValidateOrder").next("ChargePayment").catch("HandleFailure"),
    kflow.step("HandleFailure").end(),
)

kflow.run(app)
```

### Rust SDK — builder/macro-based API

```rust
use kflow::{workflow, task, step, Input, Output};

#[task]
async fn validate_order(input: Input) -> Result<Output, kflow::Error> {
    Ok(Output::from([("valid", true.into())]))
}

fn main() {
    let wf = workflow!("order-pipeline")
        .task("ValidateOrder", validate_order)
        .flow(vec![
            step("ValidateOrder").next("ChargePayment").catch("HandleFailure"),
        ]);

    kflow::run(wf);
}
```

### Execution Model for Non-Go Languages

The Go shared-binary `--state=<name>` strategy does not apply to Python and Rust containers. Instead, each language runtime uses its own container image. The **runner protocol** — how the Control Plane communicates with non-Go containers at runtime (e.g. HTTP JSON, stdin/stdout, gRPC) — is deferred as an open question.

Each language's `kflow.run()` / `kflow::run()` entry point:
1. Detects a runtime invocation signal (flag, env var, or socket — TBD per runner protocol decision)
2. Executes the named state/service handler
3. Returns output via the agreed protocol

---

## Observability Dashboard

A SvelteKit web dashboard provides real-time visibility into all workflows, services, telemetry, and logs. ClickHouse is the backend for telemetry and log storage.

**What the dashboard shows:**
- **Executions**: Live and historical workflow runs — step-by-step status, input/output per state, duration, errors
- **Services**: All deployed Services with mode (Deployment/Lambda), replica count, health status, last invocation, endpoint URL
- **Logs**: Per-execution and per-service log streams, full-text searchable via ClickHouse
- **Telemetry**: Invocation count, error rate, p50/p95/p99 latency, CPU/memory per execution or service, charted over time from ClickHouse

### Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                  SvelteKit Dashboard (ui/)                   │
│  - Real-time execution + service status via WebSocket        │
│  - Logs + telemetry queries via REST API → ClickHouse        │
└───────────────────────┬──────────────────────────────────────┘
                        │ WebSocket + HTTP REST
┌───────────────────────▼──────────────────────────────────────┐
│                  Control Plane (Go)                          │
│  - WebSocket hub: broadcasts state transitions to clients    │
│  - REST endpoints: query logs + telemetry from ClickHouse    │
│  - Telemetry writer: writes events + logs to ClickHouse      │
└──────┬──────────────────────────────┬────────────────────────┘
       │                              │
┌──────▼─────────┐        ┌───────────▼────────────────────────┐
│  State Store   │        │  ClickHouse                        │
│  (MongoDB)     │        │  - execution_events (transitions)  │
│  execution +   │        │  - service_metrics (CPU/mem/rate)  │
│  service state │        │  - logs (per execution, service)   │
└────────────────┘        └────────────────────────────────────┘
```

### Data Split Between MongoDB and ClickHouse

- **MongoDB**: Mutable state (execution status, current step, service registry) — the engine's source of truth for control flow
- **ClickHouse**: Append-only time-series (event history, metrics, logs) — the dashboard's source of truth for observability. Never used for control-flow decisions.

### Control Plane Changes

- New `internal/telemetry/` package: writes `execution_event`, `service_metric`, and `log` rows to ClickHouse at every state transition, invocation, and log line
- `internal/api/ws_handler.go`: WebSocket hub that broadcasts state-change events to connected dashboard clients in real time
- Existing REST API gains `/api/v1/executions`, `/api/v1/services`, `/api/v1/logs`, `/api/v1/metrics` endpoints backed by ClickHouse queries

---

## Open Questions / TODOs

- [ ] How to handle large outputs between states (blob storage vs inline in Postgres)
- [ ] Auth model for the control plane submission API
- [ ] OpenTelemetry spans per state transition for observability
- [ ] Cost accounting: CPU/memory per execution
- [ ] Workflow versioning: what happens when a function signature changes mid-execution
- [ ] Scale-to-zero for Deployment-mode Services (needs a cold-start proxy)
- [ ] Service versioning: blue/green or canary rollout when the handler function changes
- [ ] Service-to-Service invocation: can a Service's handler call `InvokeService` on another Service, or only Workflow steps can? (Circular dispatch risk)
- [ ] Runner protocol: define how the Control Plane sends input to and receives output from non-Go containers (HTTP JSON `POST /run`, stdin/stdout, or gRPC)
- [ ] Container image strategy for Python and Rust: does the user supply a Dockerfile, or does the engine generate one from the SDK?
- [ ] Python async support: should `kflow` support `async def` handlers (asyncio)?
- [ ] ClickHouse schema: define `execution_events`, `service_metrics`, and `logs` table schemas and TTL/retention policy
- [ ] Authentication for the dashboard: same auth model as the Control Plane API, or a separate read-only token?
- [ ] Dashboard deployment: is the SvelteKit app served by the Control Plane binary (embedded static assets) or as a separate container in the Helm chart?
