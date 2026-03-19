# Phase 5 — Control Plane API & Services

## Goal

Implement the HTTP Control Plane API, the WebSocket hub for real-time events, the Service Registry and Dispatcher, and the Kubernetes resources for Service deployment (Deployments, K8s Services, Ingress). Implement the `--service=<name>` execution path for both Deployment and Lambda service modes. Add server-level name collision enforcement.

---

## Phase Dependencies

- **Phase 1**: `pkg/kflow` types — `ServiceDef`, `ServiceMode`, `HandlerFunc`, `Input`, `Output`.
- **Phase 2**: `internal/store.Store` interface, `internal/engine.Graph`, `internal/engine.Executor`.
- **Phase 3**: `internal/store.MongoStore`, `internal/config.Config`.
- **Phase 4**: `internal/k8s.Client`, `internal/k8s.JobSpec`, `internal/engine.K8sExecutor`.

---

## Files to Create

| File | Purpose |
|------|---------|
| `internal/api/server.go` | `Server` struct, HTTP route registration, middleware |
| `internal/api/ws_handler.go` | `WSHub`, `WSEvent` — WebSocket broadcast hub |
| `internal/api/service_handler.go` | HTTP handlers for service registration, status, deregistration |
| `internal/store/service_store.go` | `ServiceRecord`, service store methods (added to `Store` interface or separate) |
| `internal/controller/service_dispatcher.go` | `ServiceDispatcher` — routes `InvokeService` steps |
| `internal/k8s/deployment.go` | K8s Deployment + K8s Service CRUD, rollout watch |
| `internal/k8s/ingress.go` | K8s Ingress CRUD |

---

## HTTP API Routes

All routes are prefixed with `/api/v1`.

### Health Routes

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/healthz` | Liveness probe — always returns `200 OK`; checks no dependencies |
| `GET` | `/readyz` | Readiness probe — returns `200 OK` after `MongoStore.EnsureIndexes` completes; `503 Service Unavailable` until then |

Both endpoints are defined in `internal/api/server.go` and are **exempt from auth middleware** (Phase 11). They must remain accessible without a Bearer token so that Kubernetes probes can reach them.

### Workflow Routes

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/workflows` | List all registered workflows |
| `POST` | `/api/v1/workflows/:name/run` | Start a new execution; body is JSON `Input`; returns `execution_id` |

### Execution Routes

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/executions` | List executions (query params: `workflow`, `status`, `limit`, `offset`) |
| `GET` | `/api/v1/executions/:id` | Get execution detail (`ExecutionRecord` as JSON) |
| `GET` | `/api/v1/executions/:id/states` | List all `StateRecord`s for an execution |

### Service Routes

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/services` | List all registered services |
| `POST` | `/api/v1/services` | Register a new service; body is `ServiceRecord` JSON |
| `GET` | `/api/v1/services/:name` | Get service detail |
| `DELETE` | `/api/v1/services/:name` | Deregister and teardown a service |

### WebSocket

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/ws` | Upgrade to WebSocket; receives `WSEvent` messages as JSON |

---

## Key Types / Interfaces / Functions

### `internal/api/server.go`

```go
// Server is the HTTP Control Plane server.
type Server struct {
    Store      store.Store
    K8s        *k8s.Client
    WSHub      *WSHub
    Dispatcher *controller.ServiceDispatcher
    // router is the internal HTTP mux (e.g. chi or net/http ServeMux)
    router     http.Handler
}

// NewServer creates a Server, registers all routes, and returns it ready to serve.
func NewServer(store store.Store, k8s *k8s.Client, hub *WSHub, disp *controller.ServiceDispatcher) *Server

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request)
```

Response format: all REST endpoints return JSON. Error responses use:
```json
{"error": "<message>", "code": "<error_code>"}
```

HTTP status codes:
- `200 OK` — successful GET
- `201 Created` — successful POST that creates a resource
- `204 No Content` — successful DELETE
- `400 Bad Request` — invalid request body or parameters
- `404 Not Found` — resource not found
- `409 Conflict` — name collision (service or workflow name already registered)
- `500 Internal Server Error` — unexpected server error

---

### `internal/api/ws_handler.go`

```go
// WSEvent is a message broadcast to all connected WebSocket clients.
type WSEvent struct {
    Type      string    `json:"type"`      // "state_transition" | "service_update"
    Payload   any       `json:"payload"`   // typed by Type (see below)
    Timestamp time.Time `json:"timestamp"`
}

// StateTransitionPayload is the Payload for "state_transition" events.
type StateTransitionPayload struct {
    ExecutionID string `json:"execution_id"`
    StateName   string `json:"state_name"`
    FromStatus  string `json:"from_status"`
    ToStatus    string `json:"to_status"`
    Error       string `json:"error,omitempty"`
}

// ServiceUpdatePayload is the Payload for "service_update" events.
type ServiceUpdatePayload struct {
    ServiceName string `json:"service_name"`
    Status      string `json:"status"`
}

// WSHub manages all active WebSocket connections and broadcasts events.
// Broadcasts are best-effort: slow or disconnected clients are dropped, not retried.
type WSHub struct {
    clients map[*websocket.Conn]struct{}
    mu      sync.RWMutex
}

func NewWSHub() *WSHub

// Register adds a new WebSocket connection to the hub.
func (h *WSHub) Register(conn *websocket.Conn)

// Unregister removes a WebSocket connection from the hub.
func (h *WSHub) Unregister(conn *websocket.Conn)

// Broadcast sends a WSEvent to all registered clients.
// Clients that fail to receive the message are silently unregistered.
func (h *WSHub) Broadcast(event WSEvent)
```

Missed events on disconnect are not replayed. The dashboard must call `GET /api/v1/executions/:id/states` on reconnect to reconcile full state.

---

### `internal/store/service_store.go`

#### `ServiceStatus`

```go
// ServiceStatus tracks the deployment lifecycle of a Service.
type ServiceStatus string

const (
    ServiceStatusPending  ServiceStatus = "Pending"   // registered, not yet deployed
    ServiceStatusRunning  ServiceStatus = "Running"   // Deployment/Job active and healthy
    ServiceStatusFailed   ServiceStatus = "Failed"    // deployment error
    ServiceStatusStopped  ServiceStatus = "Stopped"   // deregistered and torn down
)
```

#### `ServiceRecord`

```go
// ServiceRecord is the persisted representation of a registered Service.
type ServiceRecord struct {
    Name        string            // unique; shares namespace with state names
    Mode        kflow.ServiceMode
    Port        int
    MinScale    int
    MaxScale    int
    IngressHost string            // empty if not exposed
    Timeout     time.Duration
    Status      ServiceStatus
    ClusterIP   string            // set after K8s Service is created (Deployment mode)
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

#### Service Store Methods

These methods are added to the `Store` interface (or defined on a separate `ServiceStore` interface that `MongoStore` also satisfies):

```go
// CreateService persists a new ServiceRecord with StatusPending.
// Returns ErrDuplicateServiceName if the name is already registered.
CreateService(ctx context.Context, record ServiceRecord) error

// GetService retrieves a ServiceRecord by name.
GetService(ctx context.Context, name string) (ServiceRecord, error)

// ListServices returns all ServiceRecords (optionally filtered by status).
ListServices(ctx context.Context) ([]ServiceRecord, error)

// UpdateServiceStatus updates Status, ClusterIP, and UpdatedAt fields.
UpdateServiceStatus(ctx context.Context, name string, status ServiceStatus, clusterIP string) error

// DeleteService removes the ServiceRecord (hard delete; only after K8s teardown).
DeleteService(ctx context.Context, name string) error
```

MongoDB collection for services: `kflow_services`

Indexes on `kflow_services`:
- `{ "status": 1 }` — filter by status

---

### `internal/controller/service_dispatcher.go`

```go
// ServiceDispatcher routes InvokeService steps to the appropriate K8s resource.
type ServiceDispatcher struct {
    Store     store.Store
    K8s       *k8s.Client
    Telemetry *telemetry.MetricsWriter // optional; nil = no-op (Phase 6)
}

// Dispatch executes an InvokeService step for the given (execID, stateName) pair.
// It enforces write-ahead before dispatch (identical protocol to Task steps).
//
// Dispatch algorithm:
//   1. Look up serviceName in the service registry (store.GetService)
//      → 404-equivalent error if not found
//      → error if Status != Running
//   2. [Write-ahead is caller's responsibility (Executor), not Dispatcher's]
//   3. Deployment mode:
//      a. POST http://<clusterIP>:<port>/invoke with JSON-encoded input as body
//      b. Decode response body as kflow.Output
//      c. Return output (Executor writes to store)
//   4. Lambda mode:
//      a. CreateJob with args ["--service=" + serviceName] and env KFLOW_INPUT=<JSON input>
//      b. WaitForJob(ctx, jobName)
//      c. store.GetStateOutput(ctx, execID, stateName) → return output
//      d. DeleteJob (best-effort)
func (d *ServiceDispatcher) Dispatch(ctx context.Context, execID, stateName, serviceName string, input kflow.Input) (kflow.Output, error)
```

The Executor is responsible for write-ahead before calling Dispatch. Dispatch must not call `WriteAheadState` or `MarkRunning` directly.

---

### `internal/k8s/deployment.go`

```go
// DeploymentSpec describes a K8s Deployment + K8s Service pair for a Service.
type DeploymentSpec struct {
    Name      string          // K8s Deployment and K8s Service name
    Image     string          // container image
    Args      []string        // args passed to the container (e.g. ["--service=pricing-service"])
    Port      int32           // container port and K8s Service targetPort
    MinScale  int32
    MaxScale  int32           // used for HPA in future; initial Deployment replica count = MinScale
    Namespace string
}

// CreateDeployment creates a K8s Deployment and a ClusterIP K8s Service.
// Returns when the Deployment is accepted by the API server (not when rollout completes).
// Use WatchDeploymentRollout for rollout completion.
func (c *Client) CreateDeployment(ctx context.Context, spec DeploymentSpec) error

// UpdateDeploymentReplicas scales the Deployment to the specified replica count.
func (c *Client) UpdateDeploymentReplicas(ctx context.Context, name string, replicas int32) error

// DeleteDeployment deletes the K8s Deployment and its associated K8s Service.
func (c *Client) DeleteDeployment(ctx context.Context, name string) error

// GetDeploymentClusterIP returns the ClusterIP of the K8s Service associated with name.
// Called after CreateDeployment to get the IP for service dispatch.
func (c *Client) GetDeploymentClusterIP(ctx context.Context, name string) (string, error)

// WatchDeploymentRollout blocks until the named Deployment has at least minReady
// available replicas or the context deadline is exceeded.
func (c *Client) WatchDeploymentRollout(ctx context.Context, name string, minReady int32) error
```

K8s resource naming convention for services:
- Deployment name: `kflow-svc-<service-name-kebab>`
- K8s Service name: `kflow-svc-<service-name-kebab>` (same as Deployment)

---

### `internal/k8s/ingress.go`

```go
// IngressSpec describes a K8s Ingress resource for external exposure of a Service.
type IngressSpec struct {
    Name        string   // K8s Ingress name (same as K8s Service name)
    ServiceName string   // K8s Service name to route to
    Port        int32    // K8s Service port
    Host        string   // Ingress hostname (from ServiceDef.Expose())
    Namespace   string
}

// CreateIngress creates a K8s Ingress resource.
// Uses the nginx ingress class by default (configurable in future).
func (c *Client) CreateIngress(ctx context.Context, spec IngressSpec) error

// DeleteIngress deletes a K8s Ingress resource by name.
func (c *Client) DeleteIngress(ctx context.Context, name string) error
```

Ingress is only created when `ServiceDef.Expose()` was called (non-empty `IngressHost`).

---

### `--service=<name>` Execution Path

When the binary is invoked with `--service=<serviceName>`:

#### Deployment Mode

```
1. Parse --service=<name>, look up ServiceDef in the workflow/service registry
2. Read mode from ServiceDef → Deployment
3. Start HTTP server on ServiceDef.Port
4. Route POST /invoke:
   a. Decode request body as kflow.Input
   b. Apply ServiceDef.Timeout as request deadline
   c. Call ServiceDef.HandlerFunc(ctx, input)
   d. On success: return JSON-encoded kflow.Output with 200 OK
   e. On error:   return {"error": "<msg>"} with 500 status
5. Block indefinitely (Deployment serve loop)
```

#### Lambda Mode

```
1. Parse --service=<name>, look up ServiceDef in the workflow/service registry
2. Read mode from ServiceDef → Lambda
3. Read KFLOW_INPUT env var → JSON-decode as kflow.Input
4. Apply ServiceDef.Timeout as execution deadline
5. Call ServiceDef.HandlerFunc(ctx, input)
6. On success: store.CompleteState(ctx, execID, stateName, output); exit 0
7. On error:   store.FailState(ctx, execID, stateName, errMsg); exit 1
```

**Lambda Output Writer Clarification:**
The Lambda Job container calls `store.CompleteState` / `store.FailState` directly (steps 6–7 above) — the same write path used by `--state=<name>` Task containers. The `K8sExecutor` reads output via `store.GetStateOutput(ctx, execID, stateName)` **after** `WaitForJob` returns. The Control Plane does not write to the store on behalf of the container. The container is the sole writer of its own output.

Env vars for Lambda mode:

| Variable | Required | Description |
|----------|----------|-------------|
| `KFLOW_INPUT` | Yes | JSON-encoded `kflow.Input` |
| `KFLOW_EXECUTION_ID` | Yes | Execution UUID |
| `KFLOW_MONGO_URI` | Yes | MongoDB connection URI |
| `KFLOW_MONGO_DB` | No | MongoDB database name |

---

## Service Name Collision Enforcement

Name collision is enforced at two levels:

### SDK Level (Phase 1)

`Workflow.Validate()` checks that no service name matches any state name within the same binary. Returns `ErrDuplicateName`.

### Server Level

`POST /api/v1/services` checks the service registry for an existing record with the same name before persisting:

```
1. store.GetService(ctx, name)
2. If found AND Status != Stopped → return 409 Conflict
   Response body: {"error": "service name already registered", "code": "name_collision"}
3. Otherwise → store.CreateService(ctx, record)
```

The 409 response contract:
- Status code: `409 Conflict`
- Body: `{"error": "service name already registered", "code": "name_collision"}`
- This applies to both duplicate service names and service names that conflict with a known workflow state name (checked by the server against its workflow registry).

---

## Design Invariants

1. `ServiceDispatcher.Dispatch` never calls `WriteAheadState` — the Executor always performs write-ahead before calling Dispatch.
2. Deployment-mode dispatch uses the `ClusterIP` stored in `ServiceRecord.ClusterIP` — it never does DNS resolution at dispatch time.
3. Lambda-mode dispatch follows the same Job naming convention as Task dispatch: `jobName(execID, stateName)`.
4. `CreateDeployment` creates both the K8s Deployment and the K8s Service atomically (best-effort: if K8s Service creation fails after Deployment success, the Control Plane must clean up the orphaned Deployment).
5. `--service=<name>` is the **only** way a Deployment-mode binary enters its serve loop. `RunService` alone (without the flag) registers and submits the service definition to the Control Plane; it does not start an HTTP server.
6. Service handlers must never write to the state store directly. Lambda-mode output is written by the container exit path, not the handler.
7. WebSocket broadcasts are best-effort. Missed events on disconnect do not affect execution correctness.
8. The `WSHub` must not block on slow clients. Use non-blocking sends with a deadline; drop and unregister clients that can't keep up.
9. `RunService` and `kflow.Run` are safe to call in the same `main()`. The flag dispatch in `cmd/orchestrator/main.go` selects exactly one execution path.
10. Scale min >= 1 for Deployment mode is enforced both in `Validate()` (Phase 1) and in `CreateDeployment` (defensive check).
11. **Service-to-service invocation is forbidden in v1.** Service handlers must not call `InvokeService` on other services. `ServiceDispatcher.Dispatch` is called exclusively by the `Executor` as part of normal state machine execution. This is an architectural invariant enforced by convention (not runtime enforcement); it must be documented in the Service SDK and any code review checklist.
12. **WS and telemetry are independent.** `WSHub.Broadcast` is called **synchronously** from the Executor (non-blocking; slow clients are dropped). `EventWriter.RecordStateTransition` is a **separate fire-and-forget goroutine**. The two calls share no transaction and have no ordering guarantee — a WebSocket event may arrive at a client before or after the ClickHouse row is committed.

---

## Acceptance Criteria / Verification

- [ ] `go build ./internal/api/... ./internal/controller/... ./internal/k8s/... ./internal/store/...` with zero errors.
- [ ] `POST /api/v1/services` with a duplicate service name returns `409 Conflict` with correct JSON body.
- [ ] `POST /api/v1/workflows/:name/run` creates an `ExecutionRecord` and returns a valid `execution_id`.
- [ ] `GET /api/v1/executions/:id/states` returns all `StateRecord`s for a completed execution.
- [ ] `GET /api/v1/ws` upgrades to WebSocket; state transitions broadcast `WSEvent{Type: "state_transition"}`.
- [ ] `ServiceDispatcher.Dispatch` in Deployment mode POSTs to `http://<clusterIP>:<port>/invoke`.
- [ ] `ServiceDispatcher.Dispatch` in Lambda mode calls `CreateJob` with `--service=<name>` and `KFLOW_INPUT` env.
- [ ] `--service=<name>` Deployment path: binary starts HTTP server on configured port and handles `POST /invoke`.
- [ ] `--service=<name>` Lambda path: binary reads `KFLOW_INPUT`, runs handler, writes to store, exits.
- [ ] `CreateDeployment` followed by `GetDeploymentClusterIP` returns a non-empty IP.
- [ ] `DeleteDeployment` removes both the K8s Deployment and its K8s Service.
- [ ] `CreateIngress` creates a K8s Ingress resource with the specified host.
- [ ] Integration test (end-to-end): workflow with `InvokeService` step completes successfully with a Deployment-mode service.
