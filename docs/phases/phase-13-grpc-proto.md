# Phase 13 — Protocol Buffers & gRPC

## Goal

Define the canonical protobuf schema for all kflow inter-process communication. Replace direct MongoDB access from Lambda containers with a gRPC `RunnerService` running on the Control Plane. Replace HTTP POST service dispatch with gRPC `ServiceRunnerService`. Wire the grpc-gateway to serve all existing REST API routes. Introduce HMAC-SHA256 state tokens for container authentication.

---

## DDD Classification

| DDD Construct | Type(s) in this phase |
|---|---|
| Application Service | `RunnerServiceServer` (`internal/runner/`) — handles container callbacks; sole caller of `store.CompleteState`/`store.FailState` for K8s states |
| Interface Adapter | `internal/grpc/` — gRPC server, interceptors, grpc-gateway mux (replaces manual HTTP route registration) |
| Infrastructure | `internal/gen/` — buf-generated stubs (never hand-edited); `proto/` — canonical schema source |
| Value Object | State token payload (`{exec_id, state, attempt, expires_at}`) |

**Architectural invariant:** `internal/runner/` must NOT import `internal/engine/` or `internal/api/`. It imports only `internal/store/`, `internal/gen/`, and stdlib. `cmd/orchestrator/` wires all layers together.

---

## Phase Dependencies

- **Phase 2**: `internal/store.Store` interface — `RunnerServiceServer` calls `CompleteState`/`FailState`.
- **Phase 3**: `internal/store.MongoStore` and `internal/config.Config` — production store + new config fields.
- **Phase 4**: `internal/engine.K8sExecutor` — updated to inject `KFLOW_STATE_TOKEN`/`KFLOW_GRPC_ENDPOINT` instead of `KFLOW_MONGO_URI`/`KFLOW_INPUT`.
- **Phase 5**: `internal/controller.ServiceDispatcher` — updated to use `ServiceRunnerService` gRPC instead of HTTP POST.

---

## Files to Create

| File | Purpose |
|------|---------|
| `proto/buf.yaml` | buf tool configuration |
| `proto/buf.gen.yaml` | Code generation config: Go, gRPC, grpc-gateway, OpenAPI v2 |
| `proto/kflow/v1/types.proto` | Shared types: `ExecutionRecord`, `StateRecord`, `ServiceRecord`, `WorkflowGraph`, Status enums, event payloads |
| `proto/kflow/v1/runner.proto` | `RunnerService` (container ↔ CP), `ServiceRunnerService` (CP ↔ Service pod) |
| `proto/kflow/v1/workflow.proto` | `WorkflowService` — workflow registration and retrieval (grpc-gateway REST) |
| `proto/kflow/v1/execution.proto` | `ExecutionService` — execution CRUD and state listing (grpc-gateway REST) |
| `proto/kflow/v1/service_mgmt.proto` | `ServiceManagementService` — service registration/deregistration (grpc-gateway REST) |
| `proto/kflow/v1/telemetry.proto` | `TelemetryService` — event/metrics/log query endpoints (grpc-gateway REST) |
| `internal/gen/kflow/v1/` | Generated output of `buf generate` (`.pb.go`, `_grpc.pb.go`, `.pb.gw.go`) — never hand-edited |
| `internal/grpc/server.go` | `GRPCServer`: wires all service implementations, two listeners (`:8080` public, `:9090` internal) |
| `internal/grpc/interceptors.go` | `UnaryAuthInterceptor`, `UnaryLoggingInterceptor`, `UnaryRecoveryInterceptor` |
| `internal/grpc/gateway.go` | grpc-gateway HTTP mux: REST routes + WebSocket + `/healthz` + `/readyz` |
| `internal/grpc/token_auth.go` | Server-side gRPC metadata token extraction and validation for public services |
| `internal/runner/server.go` | `RunnerServiceServer`: `GetInput`, `CompleteState`, `FailState` implementations |
| `internal/runner/token.go` | `GenerateStateToken` / `ValidateStateToken` — HMAC-SHA256, expiry enforcement |

## Files to Modify

| File | Changes |
|------|---------|
| `internal/engine/k8s_executor.go` | Replace `KFLOW_MONGO_URI`/`KFLOW_INPUT` env injection with `KFLOW_STATE_TOKEN`/`KFLOW_GRPC_ENDPOINT` |
| `internal/controller/service_dispatcher.go` | Replace HTTP POST with `ServiceRunnerServiceClient.Invoke` gRPC call |
| `internal/config/config.go` | Add `GRPCPort`, `RunnerGRPCPort`, `RunnerGRPCEndpoint`, `RunnerTokenSecret`, `GRPCTLSCert`, `GRPCTLSKey`, `ServiceGRPCPort` |
| `pkg/kflow/runner.go` | Update `--state=<name>` path to dial `KFLOW_GRPC_ENDPOINT` and use `RunnerService` |
| `cmd/orchestrator/main.go` | Wire `GRPCServer` with both listeners; wire `RunnerServiceServer` |

---

## Proto Definitions

### `proto/buf.yaml`

```yaml
version: v1
name: buf.build/kflow/kflow
deps:
  - buf.build/googleapis/googleapis
  - buf.build/grpc-ecosystem/grpc-gateway
breaking:
  use:
    - FILE
lint:
  use:
    - DEFAULT
```

### `proto/buf.gen.yaml`

```yaml
version: v1
plugins:
  - plugin: go
    out: ../internal/gen
    opt: paths=source_relative
  - plugin: go-grpc
    out: ../internal/gen
    opt: paths=source_relative
  - plugin: grpc-gateway
    out: ../internal/gen
    opt:
      - paths=source_relative
      - generate_unbound_methods=true
  - plugin: openapiv2
    out: ../api/openapi
    opt: logtostderr=true
```

Run `buf generate` from the `proto/` directory to regenerate `internal/gen/`.

---

### `proto/kflow/v1/types.proto`

```protobuf
syntax = "proto3";
package kflow.v1;
option go_package = "github.com/your-org/kflow/internal/gen/kflow/v1;kflowv1";

import "google/protobuf/struct.proto";
import "google/protobuf/timestamp.proto";

enum Status {
  STATUS_UNSPECIFIED = 0;
  STATUS_PENDING     = 1;
  STATUS_RUNNING     = 2;
  STATUS_COMPLETED   = 3;
  STATUS_FAILED      = 4;
}

enum ServiceStatus {
  SERVICE_STATUS_UNSPECIFIED = 0;
  SERVICE_STATUS_PENDING     = 1;
  SERVICE_STATUS_RUNNING     = 2;
  SERVICE_STATUS_FAILED      = 3;
  SERVICE_STATUS_STOPPED     = 4;
}

message RetryPolicy {
  int32 max_attempts     = 1;
  int32 backoff_seconds  = 2;
  int32 max_backoff_seconds = 3;
}

message ExecutionRecord {
  string                    id          = 1;
  string                    workflow    = 2;
  Status                    status      = 3;
  google.protobuf.Struct    input       = 4;
  google.protobuf.Timestamp created_at  = 5;
  google.protobuf.Timestamp updated_at  = 6;
}

message StateRecord {
  string                    execution_id = 1;
  string                    state_name   = 2;
  Status                    status       = 3;
  google.protobuf.Struct    input        = 4;
  google.protobuf.Struct    output       = 5;
  string                    error        = 6;
  int32                     attempt      = 7;
  google.protobuf.Timestamp created_at   = 8;
  google.protobuf.Timestamp updated_at   = 9;
}

message ServiceRecord {
  string                    name         = 1;
  int32                     mode         = 2;  // 0=Deployment, 1=Lambda
  int32                     port         = 3;
  int32                     min_scale    = 4;
  int32                     max_scale    = 5;
  string                    ingress_host = 6;
  int64                     timeout_ms   = 7;
  ServiceStatus             status       = 8;
  string                    cluster_ip   = 9;
  google.protobuf.Timestamp created_at   = 10;
  google.protobuf.Timestamp updated_at   = 11;
}

message StateTransitionEvent {
  string                    execution_id = 1;
  string                    state_name   = 2;
  Status                    from_status  = 3;
  Status                    to_status    = 4;
  string                    error        = 5;
  google.protobuf.Timestamp occurred_at  = 6;
}

message ServiceUpdateEvent {
  string        service_name = 1;
  ServiceStatus status       = 2;
}
```

---

### `proto/kflow/v1/runner.proto`

```protobuf
syntax = "proto3";
package kflow.v1;
option go_package = "github.com/your-org/kflow/internal/gen/kflow/v1;kflowv1";

import "google/protobuf/struct.proto";

// RunnerService is served on the Control Plane's internal port (:9090).
// It is called by Lambda Job containers (Go, Python, Rust) to exchange
// input/output with the Control Plane without direct MongoDB access.
// All RPCs require a valid KFLOW_STATE_TOKEN in gRPC metadata key "x-kflow-state-token".
service RunnerService {
  // GetInput returns the Input for this state execution, sourced from the store.
  rpc GetInput(GetInputRequest) returns (GetInputResponse);

  // CompleteState marks the state as Completed and stores the output.
  // Equivalent to store.CompleteState — RunnerServiceServer is the sole caller.
  rpc CompleteState(CompleteStateRequest) returns (CompleteStateResponse);

  // FailState marks the state as Failed and records the error message.
  // Equivalent to store.FailState — RunnerServiceServer is the sole caller.
  rpc FailState(FailStateRequest) returns (FailStateResponse);
}

message GetInputRequest {
  string state_token = 1;  // HMAC-SHA256 signed token (also in metadata)
}

message GetInputResponse {
  google.protobuf.Struct input = 1;
}

message CompleteStateRequest {
  string                 state_token = 1;
  google.protobuf.Struct output      = 2;
}

message CompleteStateResponse {}

message FailStateRequest {
  string state_token = 1;
  string error_msg   = 2;
}

message FailStateResponse {}

// ServiceRunnerService is implemented by Deployment-mode Service pods.
// It is called by ServiceDispatcher on the Control Plane to invoke a service handler.
// Replaces the previous HTTP POST /invoke mechanism.
service ServiceRunnerService {
  rpc Invoke(InvokeRequest) returns (InvokeResponse);
}

message InvokeRequest {
  google.protobuf.Struct input = 1;
}

message InvokeResponse {
  google.protobuf.Struct output = 1;
}
```

---

### `proto/kflow/v1/workflow.proto`

```protobuf
syntax = "proto3";
package kflow.v1;
option go_package = "github.com/your-org/kflow/internal/gen/kflow/v1;kflowv1";

import "google/api/annotations.proto";
import "kflow/v1/types.proto";

service WorkflowService {
  rpc RegisterWorkflow(RegisterWorkflowRequest) returns (RegisterWorkflowResponse) {
    option (google.api.http) = {
      post: "/api/v1/workflows"
      body: "*"
    };
  }

  rpc GetWorkflow(GetWorkflowRequest) returns (GetWorkflowResponse) {
    option (google.api.http) = {
      get: "/api/v1/workflows/{name}"
    };
  }

  rpc ListWorkflows(ListWorkflowsRequest) returns (ListWorkflowsResponse) {
    option (google.api.http) = {
      get: "/api/v1/workflows"
    };
  }

  rpc RunWorkflow(RunWorkflowRequest) returns (RunWorkflowResponse) {
    option (google.api.http) = {
      post: "/api/v1/workflows/{name}/run"
      body: "*"
    };
  }
}

// WorkflowGraph is the canonical protobuf representation of a registered workflow.
// The REST JSON form is the grpc-gateway transcoded representation of this message.
message WorkflowGraph {
  string          name   = 1;
  repeated State  states = 2;
  repeated Step   flow   = 3;
}

message State {
  string      name           = 1;
  string      type           = 2;  // "task", "choice", "parallel", "wait"
  string      handler_ref    = 3;  // "" for Go in-process; "python"/"rust" for SDK containers
  string      service_target = 4;
  RetryPolicy retry          = 5;
  string      catch          = 6;
}

message Step {
  string      name    = 1;
  string      next    = 2;
  string      catch   = 3;
  bool        is_end  = 4;
  RetryPolicy retry   = 5;
}

message RegisterWorkflowRequest  { WorkflowGraph graph = 1; }
message RegisterWorkflowResponse { string name = 1; }
message GetWorkflowRequest       { string name = 1; }
message GetWorkflowResponse      { WorkflowGraph graph = 1; }
message ListWorkflowsRequest     {}
message ListWorkflowsResponse    { repeated WorkflowGraph workflows = 1; }

message RunWorkflowRequest {
  string                       name  = 1;
  google.protobuf.Struct       input = 2;
}
message RunWorkflowResponse    { string execution_id = 1; }
```

---

### `proto/kflow/v1/execution.proto`

```protobuf
syntax = "proto3";
package kflow.v1;
option go_package = "github.com/your-org/kflow/internal/gen/kflow/v1;kflowv1";

import "google/api/annotations.proto";
import "kflow/v1/types.proto";

service ExecutionService {
  rpc ListExecutions(ListExecutionsRequest) returns (ListExecutionsResponse) {
    option (google.api.http) = { get: "/api/v1/executions" };
  }
  rpc GetExecution(GetExecutionRequest) returns (GetExecutionResponse) {
    option (google.api.http) = { get: "/api/v1/executions/{id}" };
  }
  rpc ListExecutionStates(ListExecutionStatesRequest) returns (ListExecutionStatesResponse) {
    option (google.api.http) = { get: "/api/v1/executions/{id}/states" };
  }
}

message ListExecutionsRequest {
  string workflow = 1;
  string status   = 2;
  int32  limit    = 3;
  int32  offset   = 4;
}
message ListExecutionsResponse  { repeated ExecutionRecord executions = 1; }
message GetExecutionRequest     { string id = 1; }
message GetExecutionResponse    { ExecutionRecord execution = 1; }
message ListExecutionStatesRequest  { string id = 1; }
message ListExecutionStatesResponse { repeated StateRecord states = 1; }
```

---

### `internal/grpc/server.go`

```go
// GRPCServer wires all service implementations and manages two listeners.
type GRPCServer struct {
    // publicServer serves WorkflowService, ExecutionService, ServiceManagementService,
    // TelemetryService, and the grpc-gateway HTTP mux on GRPCPort (:8080).
    publicServer *grpc.Server

    // runnerServer serves RunnerService only on RunnerGRPCPort (:9090).
    // Never exposed via a Kubernetes Service to external traffic.
    runnerServer *grpc.Server

    cfg *config.Config
}

// NewGRPCServer constructs GRPCServer with all interceptors and service bindings.
func NewGRPCServer(
    cfg *config.Config,
    store store.Store,
    k8s *k8s.Client,
    hub *api.WSHub,
    disp *controller.ServiceDispatcher,
    runnerSrv *runner.RunnerServiceServer,
) *GRPCServer

// Serve starts both gRPC listeners. Blocks until ctx is cancelled.
func (s *GRPCServer) Serve(ctx context.Context) error
```

**Two listeners:**
- `:8080` — grpc-gateway HTTP mux (REST) + gRPC for `WorkflowService`, `ExecutionService`, `ServiceManagementService`, `TelemetryService`
- `:9090` — `RunnerService` only; internal port; not exposed via a Kubernetes `Service` to traffic outside the cluster

---

### `internal/grpc/interceptors.go`

```go
// UnaryAuthInterceptor validates the Authorization: Bearer <token> header
// for all public gRPC calls. Uses subtle.ConstantTimeCompare.
// /healthz and /readyz bypass paths are handled in gateway.go, not here.
func UnaryAuthInterceptor(apiKey string) grpc.UnaryServerInterceptor

// UnaryLoggingInterceptor logs each RPC call with method, duration, and status code.
func UnaryLoggingInterceptor() grpc.UnaryServerInterceptor

// UnaryRecoveryInterceptor catches panics and returns them as gRPC INTERNAL errors.
// Logs the panic with a stack trace at ERROR level.
func UnaryRecoveryInterceptor() grpc.UnaryServerInterceptor
```

---

### `internal/grpc/gateway.go`

```go
// NewGatewayMux creates a grpc-gateway ServeMux that transcodes HTTP/JSON to gRPC.
// The mux is registered with all public services (Workflow, Execution, ServiceMgmt, Telemetry).
// It also mounts:
//   - GET /api/v1/ws        → WSHub WebSocket handler
//   - GET /healthz          → liveness probe (auth-exempt)
//   - GET /readyz           → readiness probe (auth-exempt)
func NewGatewayMux(
    ctx context.Context,
    grpcAddr string,  // e.g. "localhost:8080"
    hub *api.WSHub,
) (http.Handler, error)
```

The grpc-gateway mux replaces the manual HTTP route registration from Phase 5. Auth middleware is applied to all routes except `/healthz` and `/readyz`.

---

### `internal/grpc/token_auth.go`

```go
// ExtractBearerToken extracts the Bearer token from gRPC metadata key "authorization".
// Returns an empty string if absent or malformed.
func ExtractBearerToken(ctx context.Context) string

// ValidateBearerToken compares the provided token against the configured API key
// using subtle.ConstantTimeCompare. Returns nil if valid.
func ValidateBearerToken(provided, expected string) error
```

---

### `internal/runner/token.go`

```go
// TokenPayload is the claims embedded in a KFLOW_STATE_TOKEN.
type TokenPayload struct {
    ExecID    string    `json:"exec_id"`
    State     string    `json:"state"`
    Attempt   int       `json:"attempt"`
    ExpiresAt time.Time `json:"expires_at"`
}

// GenerateStateToken creates a signed state token for the given execution context.
// Token format: base64url(json_payload) + "." + base64url(hmac_sha256(payload_bytes, secret))
// Expiry is set to 24 hours from now (sufficient for any realistic state execution).
// secret must be at least 32 bytes; an error is returned otherwise.
func GenerateStateToken(execID, stateName string, attempt int, secret []byte) (string, error)

// ValidateStateToken parses and verifies a state token.
// Returns the decoded TokenPayload if valid.
// Returns an error if: the token is malformed, the HMAC is invalid, or the token has expired.
// Uses subtle.ConstantTimeCompare for the HMAC comparison.
func ValidateStateToken(token string, secret []byte) (TokenPayload, error)
```

---

### `internal/runner/server.go`

```go
// RunnerServiceServer implements the RunnerService gRPC interface.
// It is the sole caller of store.CompleteState and store.FailState for K8s-executed states.
type RunnerServiceServer struct {
    store  store.Store
    secret []byte  // KFLOW_RUNNER_TOKEN_SECRET, min 32 bytes
}

func NewRunnerServiceServer(store store.Store, secret []byte) *RunnerServiceServer

// GetInput validates the token, retrieves the state's Input from the store,
// and returns it as a google.protobuf.Struct.
// The Input is sourced from: store.GetExecution(execID).Input for the first state,
// or store.GetStateOutput(execID, prevStateName) for subsequent states.
func (s *RunnerServiceServer) GetInput(
    ctx context.Context, req *kflowv1.GetInputRequest,
) (*kflowv1.GetInputResponse, error)

// CompleteState validates the token and calls store.CompleteState.
// This is the sole production path for writing Completed state records
// for K8s-executed states. Returns gRPC INTERNAL on store errors.
func (s *RunnerServiceServer) CompleteState(
    ctx context.Context, req *kflowv1.CompleteStateRequest,
) (*kflowv1.CompleteStateResponse, error)

// FailState validates the token and calls store.FailState.
// Returns gRPC INTERNAL on store errors.
func (s *RunnerServiceServer) FailState(
    ctx context.Context, req *kflowv1.FailStateRequest,
) (*kflowv1.FailStateResponse, error)
```

---

## Configuration Additions (`internal/config/config.go`)

```go
// GRPCPort is the combined gRPC + grpc-gateway public port.
// Source: KFLOW_GRPC_PORT. Default: "8080".
GRPCPort string

// RunnerGRPCPort is the RunnerService internal-only port.
// Source: KFLOW_RUNNER_GRPC_PORT. Default: "9090".
RunnerGRPCPort string

// RunnerGRPCEndpoint is the RunnerService address injected into Job containers.
// Source: KFLOW_RUNNER_GRPC_ENDPOINT.
// Default: "kflow-cp.kflow.svc.cluster.local:9090".
RunnerGRPCEndpoint string

// RunnerTokenSecret is the HMAC-SHA256 key for state tokens. Min 32 bytes. Required in production.
// Source: KFLOW_RUNNER_TOKEN_SECRET. Never logged.
RunnerTokenSecret []byte

// GRPCTLSCert is the TLS certificate file path for gRPC. Empty = no TLS (dev only).
// Source: KFLOW_GRPC_TLS_CERT.
GRPCTLSCert string

// GRPCTLSKey is the TLS key file path for gRPC.
// Source: KFLOW_GRPC_TLS_KEY.
GRPCTLSKey string

// ServiceGRPCPort is the port that Deployment-mode Service pods expose for ServiceRunnerService.
// Source: KFLOW_SERVICE_GRPC_PORT. Default: "9091".
ServiceGRPCPort string
```

`LoadConfig` must:
- Return an error if `KFLOW_RUNNER_TOKEN_SECRET` is set but shorter than 32 bytes.
- Log a prominent warning if `KFLOW_RUNNER_TOKEN_SECRET` is empty (dev mode only).
- Never log the value of `KFLOW_RUNNER_TOKEN_SECRET`.

---

## `pkg/kflow/runner.go` — Updated `--state=<name>` Path

```go
// --state=<name> execution path:
//
// 1. Read KFLOW_STATE_TOKEN, KFLOW_GRPC_ENDPOINT, KFLOW_EXECUTION_ID from env.
//    Exit with clear error if any required var is missing.
// 2. Dial KFLOW_GRPC_ENDPOINT (insecure for dev; TLS when KFLOW_GRPC_TLS_CERT is set).
// 3. RunnerServiceClient.GetInput(ctx, &GetInputRequest{StateToken: token})
//    → decode google.protobuf.Struct → kflow.Input
// 4. Look up HandlerFunc for stateName in the workflow's task registry.
// 5. Call HandlerFunc(ctx, input).
// 6. On success: RunnerServiceClient.CompleteState(ctx, token, protoStructOutput)
//    On error:   RunnerServiceClient.FailState(ctx, token, err.Error())
// 7. os.Exit(0) on success, os.Exit(1) on error.
//
// RunLocal path: UNCHANGED — uses MemoryStore in-process, no gRPC.
```

---

## Security Rules for gRPC / Tokens

1. **Token validation is mandatory.** `RunnerServiceServer` must validate the HMAC on every RPC call before performing any store operation. Never skip validation.
2. **Token expiry is enforced.** `ValidateStateToken` must check `expires_at` and return an error if the token is expired.
3. **`subtle.ConstantTimeCompare` for HMAC comparison.** Never use `==` or `bytes.Equal` for comparing the token signature.
4. **RunnerService port is internal-only.** Port `9090` must not be exposed via a Kubernetes `Service` to external traffic. Use a headless or `ClusterIP` service restricted to cluster-internal traffic only.
5. **TLS for production.** When `KFLOW_GRPC_TLS_CERT` and `KFLOW_GRPC_TLS_KEY` are set, all gRPC listeners must use TLS. Empty = dev mode only; log a prominent warning.
6. **Token secret minimum length.** `KFLOW_RUNNER_TOKEN_SECRET` must be at least 32 bytes (256 bits). `LoadConfig` must reject shorter values.
7. **Token payload is canonical.** The `exec_id`, `state`, and `attempt` in the token are the authoritative identifiers for a state execution. `RunnerServiceServer` must use these values — not values from the request body — when calling `store.CompleteState`/`store.FailState`.

---

## Design Invariants

1. `internal/gen/` is never hand-edited. All changes to the wire format go through `proto/` and `buf generate`.
2. `RunnerServiceServer` is the only caller of `store.CompleteState`/`store.FailState` for K8s-executed states. `Executor.executeState` (for `RunLocal`) retains direct store calls.
3. The grpc-gateway mux (`internal/grpc/gateway.go`) is the authoritative HTTP router. The Phase 5 `internal/api/server.go` manual route registration is superseded.
4. `RunnerService` runs on a dedicated internal port (`:9090`) separate from the public gRPC port (`:8080`). No auth-exempt health probe routes are served on the runner port.
5. `google.protobuf.Struct` is used for `kflow.Input`/`Output` to preserve `map[string]any` semantics. Conversion between `Struct` and Go maps must be lossless.
6. `ServiceRunnerService` replaces HTTP POST `/invoke` for Deployment-mode services. Existing HTTP-based test tooling should be updated to use gRPC clients or the grpc-gateway REST transcoding.
7. The `RunLocal` path uses `MemoryStore` in-process and is never modified by this phase. No gRPC is used in `RunLocal`.
8. All gRPC unary interceptors apply to both the public server and the runner server, with the exception of `UnaryAuthInterceptor` (which checks the Bearer API token) — this applies only to the public server. The runner server uses `token_auth.go` for per-call token validation, not the session Bearer token.

---

## Acceptance Criteria / Verification

- [ ] `buf generate` in `proto/` completes without errors and populates `internal/gen/kflow/v1/`.
- [ ] `go build ./internal/grpc/... ./internal/runner/... ./internal/gen/...` with zero errors.
- [ ] `GenerateStateToken` + `ValidateStateToken` round-trip: generated token validates correctly.
- [ ] `ValidateStateToken` rejects a token with a tampered payload (HMAC mismatch).
- [ ] `ValidateStateToken` rejects an expired token (set `ExpiresAt` to `time.Now().Add(-1*time.Second)`).
- [ ] `RunnerServiceServer.GetInput` returns the correct input for a state in a running execution.
- [ ] `RunnerServiceServer.CompleteState` transitions the state to `StatusCompleted` in the store.
- [ ] `RunnerServiceServer.FailState` transitions the state to `StatusFailed` in the store.
- [ ] `GRPCServer.Serve` starts both listeners (`:8080`, `:9090`) without error.
- [ ] `GET /healthz` returns `200 OK` without an Authorization header.
- [ ] `GET /readyz` returns `200 OK` without an Authorization header.
- [ ] `POST /api/v1/workflows` via grpc-gateway returns `201` for a valid graph.
- [ ] `POST /api/v1/workflows/:name/run` via grpc-gateway returns `202` with `execution_id`.
- [ ] `--state=<name>` binary path: missing `KFLOW_STATE_TOKEN` exits non-zero with a readable error.
- [ ] `--state=<name>` binary path: missing `KFLOW_GRPC_ENDPOINT` exits non-zero with a readable error.
- [ ] K8s Job spec does NOT contain `KFLOW_MONGO_URI`, `KFLOW_MONGO_DB`, or `KFLOW_INPUT` env vars.
- [ ] `LoadConfig` returns an error when `KFLOW_RUNNER_TOKEN_SECRET` is set but shorter than 32 bytes.
- [ ] `LoadConfig` logs a warning (not an error) when `KFLOW_RUNNER_TOKEN_SECRET` is empty.
- [ ] Integration test: full workflow execution via K8s Jobs using `RunnerService` completes successfully.
- [ ] `go test ./internal/runner/... ./internal/grpc/...` passes (unit tests; no external dependencies).
