@CLAUDE.md

---

## Architecture — Clean Architecture & DDD Principles

> This section supplements the package layout and execution flow in CLAUDE.md with explicit DDD vocabulary and bounded-context definitions. Refer to CLAUDE.md's **Clean Architecture & Domain-Driven Design** section for the full CA layer map, dependency rules, and compile-time assertions.

### Ubiquitous Language Glossary

| Term | Definition |
|------|-----------|
| **workflow** | A named, versioned state machine defined by the user via the Go/Python/Rust SDK. |
| **state** | A single node in the workflow graph; has a type (`task`, `choice`, `wait`, `parallel`) and a handler. |
| **execution** | A single run of a workflow, identified by a UUID. Has lifecycle: `Pending → Running → Completed/Failed`. |
| **handler** | The user-supplied function that processes `Input` and returns `Output` for a given state. |
| **write-ahead** | The protocol invariant: `WriteAheadState` is always called before any handler is invoked — without exception. This is the Execution aggregate's primary consistency boundary. |
| **catch** | An error-routing mechanism: when a state exhausts retries, execution transitions to the named catch state. The catch state's input includes an `_error` key containing the failure message. |
| **service** | A named, persistently deployed or on-demand handler unit (`ServiceDef`), deployed as a K8s Deployment or Lambda Job. |
| **dispatch** | The act of routing an `InvokeService` step to the appropriate service pod via gRPC `ServiceRunnerService.Invoke`. |
| **runner protocol** | The gRPC contract (`RunnerService`) between the Control Plane and Lambda Job containers (Go, Python, Rust). Defined in Phase 13. |

### Three Bounded Contexts

1. **Workflow Execution** — owns the Execution aggregate (`ExecutionRecord`, `StateRecord`), the write-ahead protocol, and the `Executor`/`RunnerServiceServer` application services. Source of truth: MongoDB.
2. **Service Management** — owns the Service aggregate (`ServiceRecord`), K8s Deployment/Ingress lifecycle, and `ServiceDispatcher`. Interacts with the Workflow Execution context only through `InvokeService` dispatch.
3. **Observability (Read Model)** — `internal/telemetry` translates domain state transitions into ClickHouse append-only rows via an Anti-Corruption Layer. Never feeds back into execution decisions.

### Key DDD Construct Mappings

| DDD Construct | kflow Type(s) |
|---|---|
| Aggregate Root | `Workflow`, `ExecutionRecord`, `ServiceRecord` |
| Entity | `ExecutionRecord` (UUID), `StateRecord` (execID+stateName+attempt), `TaskDef` (name within Workflow) |
| Value Object | `Input`/`Output`, `RetryPolicy`, `StepBuilder`, `Status`, `ServiceStatus` |
| Repository | `store.Store` interface |
| Domain Service | `Graph` (compiles Workflow → DAG), `Executor` (drives state machine loop) |
| Application Service | `K8sExecutor`, `ServiceDispatcher`, `RunnerServiceServer` |
| Domain Event | `WSEvent`, `StateTransitionPayload`, `ServiceUpdatePayload` |
| Anti-Corruption Layer | `internal/telemetry` |

### Updated Execution Flow (gRPC Runner Path)

For K8s-dispatched states, the execution sequence is:

```
Executor.executeState(execID, node, input):
  1. store.WriteAheadState(...)          ← domain invariant, never bypassed
  2. store.MarkRunning(...)
  3. K8sExecutor creates Job with env:
       KFLOW_STATE_TOKEN  = HMAC-signed {exec_id, state, attempt, expires_at}
       KFLOW_GRPC_ENDPOINT = kflow-cp.kflow.svc.cluster.local:9090
       KFLOW_EXECUTION_ID  = <uuid>  (observability only)
  4. Job container (Go/Python/Rust SDK):
       → RunnerService.GetInput(token)          → kflow.Input
       → HandlerFunc(ctx, input)
       → RunnerService.CompleteState(token, output)
            OR RunnerService.FailState(token, errMsg)
  5. RunnerServiceServer (Control Plane):
       → validates HMAC token
       → store.CompleteState / store.FailState  ← sole writer for K8s states
  6. Executor: WaitForJob returns; store.GetStateOutput(ctx, execID, stateName)
  7. WSHub.Broadcast(event)              ← synchronous, non-blocking
  8. EventWriter.RecordStateTransition() ← fire-and-forget goroutine
```

`RunLocal` (in-process path): `Executor` calls `store.CompleteState`/`store.FailState` directly — no gRPC involved.

### Updated Service Dispatch (gRPC)

`ServiceDispatcher.Dispatch` for Deployment-mode services (replaces HTTP POST):

```
ServiceDispatcher.Dispatch(ctx, execID, stateName, serviceName, input):
  1. store.GetService(ctx, serviceName) → ServiceRecord{ClusterIP, ...}
  2. dial clusterIP:KFLOW_SERVICE_GRPC_PORT
  3. ServiceRunnerService.Invoke(ctx, InvokeRequest{Input: protoStruct})
  4. return InvokeResponse.Output
```

Service pods (Go/Python/Rust) implement the `ServiceRunnerService` gRPC server defined in `proto/kflow/v1/runner.proto`.
