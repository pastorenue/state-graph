# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

---

## Project

**step-graph** is a Kubernetes-native serverless workflow engine written in Go. Users define state-machine workflows and persistent/on-demand services using a Go SDK; the engine handles containerisation, scheduling, and lifecycle management on Kubernetes. Think self-hosted AWS Step Functions + Lambda.

The repository is currently in the **architectural planning phase**. `AGENTS.md` is the authoritative design document. `docs/phases/` contains 13 phase reference files that drive the implementation roadmap. No source code exists yet.

---

## Build / Test Commands

These commands apply once implementation begins (no source exists yet).

```bash
# Go — build all packages
go build ./...

# Go — run all unit tests (no external deps required)
go test ./...

# Go — run a single test
go test ./internal/store/... -run TestWriteAheadIdempotency

# Go — run MongoDB integration tests (requires a live MongoDB)
KFLOW_TEST_MONGO_URI=mongodb://localhost:27017 go test ./internal/store/...

# Go — run ClickHouse integration tests
KFLOW_TEST_CLICKHOUSE_DSN=clickhouse://localhost:9000 go test ./internal/telemetry/...

# UI — install deps and build
cd ui && npm install && npm run build

# Install the skill for frontend design
cd ui && npx skills add anthropics/claude-code - skill frontend-design

# UI — type-check
cd ui && npm run check

# UI — dev server (proxies API to localhost:8080)
cd ui && npm run dev
```

---

## Architecture

### Projected Package Layout

```
cmd/orchestrator/          # Control Plane binary entrypoint (--state=<name> / --service=<name> dispatch)
internal/
  api/                     # HTTP server, WebSocket hub, auth middleware
  config/                  # Config struct, LoadConfig() from env vars
  controller/              # ServiceDispatcher
  engine/                  # Graph (compiled workflow), Executor (state machine driver)
  gen/                     # buf-generated protobuf + gRPC + grpc-gateway code (never hand-edited)
  grpc/                    # gRPC server, interceptors, grpc-gateway mux
  k8s/                     # Kubernetes client: Jobs, Deployments, Services, Ingress
  runner/                  # RunnerServiceServer, state token security (HMAC-SHA256)
  store/                   # Store interface, MemoryStore, MongoStore, ObjectStore
  telemetry/               # ClickHouse client, EventWriter, MetricsWriter, LogWriter
pkg/kflow/                 # Public Go SDK (TaskDef, ServiceDef, Workflow, RunLocal, RunService)
proto/
  kflow/v1/                # Protobuf definitions (types, runner, workflow, execution, service_mgmt, telemetry)
  buf.yaml                 # buf tool config
  buf.gen.yaml             # Code generation: Go, gRPC, grpc-gateway, OpenAPI
sdk/python/                # Python SDK
sdk/rust/                  # Rust SDK
ui/                        # SvelteKit dashboard
deployments/k8s/           # Helm chart
```

### Two Runtime Contexts

The same Go binary serves two roles, selected by flag at startup:

- **Normal execution** (`kflow.Run(wf)`): registers the workflow, triggers the Executor via the Control Plane.
- **State/Service worker** (`--state=<name>` / `--service=<name>`): runs inside a Kubernetes Job or Deployment; executes exactly one handler function and writes output to the store.

`RunLocal(wf)` is the third path — runs everything in-process using `MemoryStore`, no Kubernetes.

### Execution Flow

```
SDK: kflow.Run(wf)
  → Control Plane API (grpc-gateway): POST /api/v1/workflows/:name/run
  → Executor.Run(ctx, execID, graph, input)
      for each state:
        1. store.WriteAheadState(...)        ← ALWAYS first, no exceptions
        2. store.MarkRunning(...)
        3. Handler(ctx, stateName, input)    ← inline fn / K8s Job / Service dispatch
           ┌── RunLocal (in-process):
           │     HandlerFunc called directly; Executor calls store.CompleteState/FailState
           ├── K8s Job (Lambda/Go):
           │     Container dials KFLOW_GRPC_ENDPOINT
           │     → RunnerService.GetInput(token)   → receives kflow.Input
           │     → HandlerFunc(ctx, input)
           │     → RunnerService.CompleteState(token, output)
           │        OR RunnerService.FailState(token, errMsg)
           │     RunnerServiceServer calls store.CompleteState / store.FailState
           └── Service dispatch (Deployment mode):
                 ServiceDispatcher calls ServiceRunnerService.Invoke via gRPC
        4. [store already written by RunnerServiceServer or inline path]
      → WSHub.Broadcast(event)              ← synchronous, non-blocking
      → EventWriter.RecordStateTransition() ← separate fire-and-forget goroutine
```

### Store Interface

`internal/store.Store` is the central persistence contract. All code must depend on the interface, not a concrete type. Two implementations:
- `MemoryStore` — for `RunLocal` and unit tests only. Never in production.
- `MongoStore` — production. Has an optional `ObjectStore` field for outputs > 1 MB.

### Key Data Flow Invariants

1. **Write-ahead is never bypassed.** `WriteAheadState` → `MarkRunning` → handler → `CompleteState`/`FailState` — always in this order.
2. **`RunnerServiceServer` is the sole caller of `store.CompleteState`/`store.FailState` for K8s-executed states.** Lambda and Service containers call `RunnerService.CompleteState`/`RunnerService.FailState` via gRPC; the `RunnerServiceServer` on the Control Plane performs the actual store writes. `RunLocal` (in-process) retains direct store calls via the `Executor`. MongoDB is never accessed directly by Lambda Job containers.
3. **Service-to-service calls are forbidden in v1.** `ServiceDispatcher.Dispatch` is called only by the Executor.
4. **WS and telemetry are independent.** `WSHub.Broadcast` is synchronous; `EventWriter` is async. No ordering guarantee between a WebSocket event arriving at a client and the ClickHouse row being committed.
5. **ClickHouse is never read for control-flow.** MongoDB is the sole authority for execution state.
6. **`_error` key**: when routing to a Catch state, the input to the Catch state is the failed state's input merged with `{"_error": "<error.Error() string>"}`.

### Configuration (environment variables)

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `KFLOW_MONGO_URI` | Yes | — | MongoDB connection URI |
| `KFLOW_MONGO_DB` | No | `kflow` | MongoDB database name |
| `KFLOW_NAMESPACE` | No | `kflow` | Kubernetes namespace for all workloads |
| `KFLOW_CLICKHOUSE_DSN` | No | `""` | ClickHouse DSN; empty = telemetry disabled |
| `KFLOW_OBJECT_STORE_URI` | No | `""` | S3-compatible URI; empty = large outputs return `ErrOutputTooLarge` |
| `KFLOW_API_KEY` | No | `""` | Bearer token for API auth; empty = auth disabled (dev mode) |
| `KFLOW_GRPC_PORT` | No | `8080` | gRPC + grpc-gateway public port |
| `KFLOW_RUNNER_GRPC_PORT` | No | `9090` | RunnerService internal port (not exposed outside cluster) |
| `KFLOW_RUNNER_GRPC_ENDPOINT` | No | `kflow-cp.kflow.svc.cluster.local:9090` | RunnerService address injected into Job containers |
| `KFLOW_RUNNER_TOKEN_SECRET` | Yes (prod) | — | HMAC-SHA256 key for state tokens. Min 32 bytes. Never logged. |
| `KFLOW_GRPC_TLS_CERT` | No | `""` | TLS cert file path for gRPC. Empty = no TLS (dev only). |
| `KFLOW_GRPC_TLS_KEY` | No | `""` | TLS key file path for gRPC. |
| `KFLOW_SERVICE_GRPC_PORT` | No | `9091` | Port that Deployment-mode Service pods expose for `ServiceRunnerService` |
| `KFLOW_STATE_TOKEN` | Yes (Lambda) | — | HMAC-SHA256 signed token authorising this state execution; injected into Job containers |
| `KFLOW_EXECUTION_ID` | Yes (Lambda) | — | Execution UUID injected into Lambda Job containers (logging/observability only) |

---

## Clean Architecture & Domain-Driven Design

kflow follows Clean Architecture and Domain-Driven Design principles. The existing package structure already implements these layers — the vocabulary below makes that structure explicit and guides every future implementation decision.

### Development Instructions
- Always create a new branch for every new phase.
- To install any go package, use docker
- Create `make` commands for easy run
- Use little comments and avoid over-engineering
- After every new feature is added, write tests, run tests, run linters and ensure all looks good and create a commit.
- Always ommit `Co-Authored-By` in commit messages
- After every feature implementation, create a skill for it.
- Run the project in docker - use Dockerfile and docker-compose.yaml files


### CA Layer Map

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  Interface Adapters                                                         │
│    internal/api/          grpc-gateway HTTP mux, WSHub, auth middleware     │
│    internal/grpc/         gRPC server, interceptors, gateway wiring         │
│    cmd/orchestrator/      Binary entry point, composition root              │
├─────────────────────────────────────────────────────────────────────────────┤
│  Application Layer                                                          │
│    internal/engine/       Executor, K8sExecutor (use cases)                 │
│    internal/controller/   ServiceDispatcher (use case)                      │
│    internal/runner/       RunnerServiceServer (use case: container callback)│
├─────────────────────────────────────────────────────────────────────────────┤
│  Domain Layer (no external imports)                                         │
│    pkg/kflow/             Workflow, TaskDef, ServiceDef, Input, Output,     │
│                           RetryPolicy, HandlerFunc — public SDK types       │
│    internal/store/        Store interface (Repository), ExecutionRecord,    │
│                           StateRecord, ServiceRecord, Status                │
│    internal/engine/       Graph, Node (Domain Services)                     │
├─────────────────────────────────────────────────────────────────────────────┤
│  Infrastructure Layer                                                       │
│    internal/store/        MongoStore, MemoryStore, ObjectStore              │
│    internal/k8s/          K8s client, Job/Deployment/Ingress CRUD           │
│    internal/telemetry/    ClickHouse — Anti-Corruption Layer (ACL)          │
│    internal/config/       Environment-variable loader                       │
│    internal/gen/          buf-generated protobuf + gRPC + gateway code      │
└─────────────────────────────────────────────────────────────────────────────┘
```

### DDD Construct Mapping

| DDD Construct | kflow Type(s) |
|---|---|
| Aggregate Root | `Workflow` (pkg/kflow), `ExecutionRecord` (internal/store), `ServiceRecord` (internal/store) |
| Entity | `ExecutionRecord` (identity: UUID), `StateRecord` (identity: execID+stateName+attempt), `TaskDef` (identity: name within Workflow) |
| Value Object | `Input`/`Output`, `RetryPolicy`, `Step`/`StepBuilder`, `Status`, `ServiceStatus` |
| Repository | `store.Store` interface |
| Domain Service | `Graph` (compiles Workflow → DAG), `Executor` (drives state machine) |
| Application Service | `K8sExecutor`, `ServiceDispatcher`, `RunnerServiceServer` |
| Domain Event | `WSEvent`, `StateTransitionPayload`, `ServiceUpdatePayload` |
| Anti-Corruption Layer | `internal/telemetry` (domain transitions → ClickHouse append-only schema) |

### Three Bounded Contexts

1. **Workflow Execution** — `pkg/kflow`, `internal/engine`, `internal/store`; owns the Execution aggregate and the write-ahead protocol.
2. **Service Management** — `internal/controller`, `internal/k8s` (Deployments/Ingress); owns the Service aggregate.
3. **Observability (Read Model)** — `internal/telemetry`, `ui/`; append-only, never controls execution.

### Dependency Rules (enforce in CI)

1. `pkg/kflow/` imports nothing from `internal/`. **Exception**: `pkg/kflow/worker.go` may import `internal/gen/kflow/v1` — proto stubs are byte-encoding infrastructure, not engine/store coupling.
2. `internal/store/` (interface + record types) imports only `pkg/kflow/` and stdlib.
3. `internal/engine/` imports `internal/store/` and `pkg/kflow/`; must NOT import `internal/api/` or `internal/k8s/`.
4. `internal/telemetry/` imports only `internal/store/` Status types + stdlib/ClickHouse driver; must NOT import engine or api.
5. `internal/k8s/` imports only `pkg/kflow/` and stdlib/client-go; must NOT import store or engine.
6. `internal/controller/` imports store, k8s, telemetry; must NOT import api.
7. `internal/runner/` imports store, internal/gen; must NOT import engine or api.
8. `cmd/orchestrator/` is the **composition root** — the only place where all layers wire together.

### Write-Ahead as Domain Invariant

`WriteAheadState` → `MarkRunning` → handler → `CompleteState`/`FailState` is the Execution aggregate's primary consistency boundary. `Executor` is the only code that orchestrates this sequence. For K8s-executed states, `RunnerServiceServer` is the sole entity that calls `store.CompleteState`/`store.FailState` — no container accesses MongoDB directly.

### Compile-Time Interface Assertions

```go
var _ store.Store = (*MemoryStore)(nil)
var _ store.Store = (*MongoStore)(nil)
```

---

## Phase Reference Files

All design decisions are documented in `docs/phases/`. Read the relevant phase file before implementing any package:

| Phase | File | Covers |
|---|---|---|
| 1 | `phase-1-sdk-types.md` | `pkg/kflow` public SDK types |
| 2 | `phase-2-state-machine.md` | `Store` interface, `MemoryStore`, `Graph`, `Executor`, `RunLocal` |
| 3 | `phase-3-mongodb.md` | `MongoStore`, `Config`, `LoadConfig`, index definitions |
| 4 | `phase-4-kubernetes.md` | K8s client, `K8sExecutor`, Job lifecycle |
| 5 | `phase-5-api-services.md` | HTTP API routes, `WSHub`, `ServiceDispatcher`, K8s Deployments/Ingress |
| 6 | `phase-6-telemetry.md` | ClickHouse schema, `EventWriter`, `MetricsWriter`, `LogWriter` |
| 7 | `phase-7-python-sdk.md` | Python SDK |
| 8 | `phase-8-rust-sdk.md` | Rust SDK |
| 9 | `phase-9-dashboard.md` | SvelteKit dashboard, `ui/src/lib/api.ts`, `ws.ts` |
| 10 | `phase-10-helm-chart.md` | Helm chart |
| 11 | `phase-11-auth.md` | Bearer token auth, session tokens, `/healthz`+`/readyz` exemption |
| 12 | `phase-12-graph-protocol.md` | Workflow graph JSON schema, large output handling, `ObjectStore` |
| 13 | `phase-13-grpc-proto.md` | Proto schema definitions, buf toolchain, `RunnerService`, grpc-gateway setup, state token security |

---

## Security Best Practices

Follow these rules when implementing any package in this project.

### Secrets and Credentials

- **Never hardcode secrets.** All credentials (MongoDB URI, ClickHouse DSN, API keys, S3 credentials) are injected via environment variables. No secrets in source code, config files, or Helm `values.yaml` defaults.
- **Never log secrets.** `LoadConfig` must not log the values of `KFLOW_MONGO_URI`, `KFLOW_API_KEY`, or any DSN. Log only whether they are set.
- **`KFLOW_API_KEY` empty = dev mode only.** When auth is disabled, log a prominent warning at startup. Never silently skip auth in production.

### Authentication and Authorization

- **Validate the `Authorization: Bearer <token>` header** using constant-time comparison (`subtle.ConstantTimeCompare`) to prevent timing attacks.
- **`/healthz` and `/readyz` are the only auth-exempt routes.** All other routes must pass through the auth middleware, with no bypass.
- **Reject requests with oversized bodies** before parsing. Set `http.MaxBytesReader` on all API handlers to cap request bodies (e.g., 10 MB).

### Input Validation

- **Validate all user-supplied identifiers** (workflow names, state names, execution IDs) against an allowlist pattern (e.g., `^[a-zA-Z0-9_-]{1,128}$`) before using them in MongoDB queries, Kubernetes resource names, or log messages.
- **Never interpolate user input into Kubernetes resource names or labels** without sanitization. Resource names that fail DNS subdomain rules must be rejected, not silently truncated.
- **Validate `KFLOW_STATE_TOKEN` server-side before any store operation.** The token passed by Lambda Job containers must be verified using HMAC-SHA256 (`internal/runner/token.go`) before `RunnerServiceServer` processes any `CompleteState` or `FailState` call. Token expiry must be checked. Use `subtle.ConstantTimeCompare` for the signature comparison.

### Kubernetes Security

- **Run containers as non-root.** All generated Job and Deployment specs must set `securityContext.runAsNonRoot: true` and `securityContext.runAsUser` to a non-zero UID.
- **Drop all Linux capabilities.** Set `securityContext.capabilities.drop: ["ALL"]` on every container spec.
- **Read-only root filesystem.** Set `securityContext.readOnlyRootFilesystem: true` for Lambda Job containers where possible.
- **No `privileged: true`.** Never generate Kubernetes specs with privileged containers.
- **Use least-privilege RBAC.** The Helm chart's `ClusterRole`/`Role` must grant only the specific verbs and resources the orchestrator needs (Jobs, Deployments, Services, Ingress). No wildcard (`*`) verbs or resources.
- **Pin image tags.** Never use `:latest` in generated Job specs. Require callers to provide an explicit image tag.

### MongoDB

- **Use parameterized queries exclusively.** Never build BSON filter documents by string concatenation. Use `bson.D{{Key: "field", Value: value}}` typed constructors.
- **Enforce TLS in production.** If `KFLOW_MONGO_URI` does not include TLS options in a production deployment, warn at startup.
- **Scope MongoDB credentials.** The application user must have only the minimum required roles (e.g., `readWrite` on the `kflow` database), not `root` or `dbAdmin`.

### HTTP API

- **Set secure response headers** on all API responses: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy` (for any HTML responses).
- **CORS must be explicit.** Do not use `*` for `Access-Control-Allow-Origin` when auth is enabled. Restrict to configured allowed origins.
- **Rate-limit public endpoints.** The `/api/v1/workflows/:name/run` endpoint must have a per-IP or per-token rate limit to prevent abuse.

### WebSocket

- **Authenticate WebSocket upgrade requests** using the same bearer token middleware as HTTP. Unauthenticated upgrades must be rejected with HTTP 401.
- **Validate and sanitize all data broadcast over WebSocket.** Never relay raw MongoDB documents or internal error messages directly to clients.

### Telemetry

- **Do not write PII or secrets to ClickHouse.** State inputs/outputs may contain user data; apply field-level redaction before writing to `EventWriter` or `LogWriter`.
- **ClickHouse credentials** follow the same rules as MongoDB: TLS encouraged, least-privilege DB user, never logged.

### Go Code Patterns

- **Check all errors.** Never use `_` to discard an error from a function that can fail in a security-relevant way (e.g., writes, closes, auth checks).
- **Use `context.Context` cancellation.** All blocking operations (DB queries, K8s API calls, HTTP requests) must respect context cancellation to prevent goroutine leaks.
- **Avoid `fmt.Sprintf` for constructing queries or commands.** Use typed constructors or the relevant SDK's parameter binding.
- **Dependency supply chain.** Pin all Go module dependencies with a `go.sum` file committed to the repository. Run `govulncheck ./...` in CI.

---

## Important Notes

- **MongoDB, not Postgres.** AGENTS.md line 229 contains a stray "Postgres" reference — this is an error. MongoDB is the canonical state store throughout.
- **`/healthz` and `/readyz` are always auth-exempt.** Kubernetes probes must reach them without credentials.
- **`handler_ref = ""`** for all Go in-process states. Non-empty values are reserved for the multi-language runner protocol; Python/Rust containers communicate with the Control Plane via gRPC `RunnerService` (Phase 13).
- The `docs/phases/` files are the specification. AGENTS.md is the higher-level design narrative. When they conflict, the phase file is more authoritative (it reflects later, more detailed decisions).
