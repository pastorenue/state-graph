# Phase 8 — Rust SDK

## Goal

Implement the Rust SDK (`sdk/rust/`) that allows user code written in Rust to define workflows and services using a builder and proc-macro API. The SDK communicates with the Control Plane over HTTP. Rust containers are never co-located with the Go binary; each Rust task or service runs in its own container image built from the user's compiled binary.

---

## Phase Dependencies

- **Phase 5** must be complete. The Control Plane API must be stable before the Rust SDK can submit definitions.
- **Phase 13** must be complete. The runner protocol is now resolved: Rust containers use gRPC `RunnerService` (defined in `proto/kflow/v1/runner.proto`). `runner.rs` must implement a tonic-based `RunnerServiceClient` and optionally a `ServiceRunnerServiceServer` for Deployment-mode services.

---

## Files to Create

| File | Purpose |
|------|---------|
| `sdk/rust/src/lib.rs` | Crate root: public re-exports, `Input`, `Output`, `Error` types |
| `sdk/rust/src/workflow.rs` | `Workflow`, `TaskDef`, `StepBuilder`, sentinels |
| `sdk/rust/src/service.rs` | `ServiceDef`, `ServiceMode` |
| `sdk/rust/src/runner.rs` | `run()`, `run_service()`, `run_local()` — entry points and runner-protocol dispatch |
| `sdk/rust/Cargo.toml` | Crate manifest and dependencies |

---

## Key Types / Interfaces / Functions

### `sdk/rust/src/lib.rs`

```rust
pub mod workflow;
pub mod service;
pub mod runner;

// Re-exports for convenient top-level use
pub use workflow::{Workflow, StepBuilder, step, SUCCEED, FAIL};
pub use service::{ServiceDef, ServiceMode};
pub use runner::{run, run_service, run_local};

/// Input is the data received by a state handler. Must be JSON-serialisable.
pub type Input = std::collections::HashMap<String, serde_json::Value>;

/// Output is the data returned by a state handler. Must be JSON-serialisable.
pub type Output = std::collections::HashMap<String, serde_json::Value>;

/// Error wraps a user-facing error message from a handler.
#[derive(Debug, thiserror::Error)]
#[error("{0}")]
pub struct Error(pub String);

impl From<&str> for Error {
    fn from(s: &str) -> Self { Error(s.to_string()) }
}
impl From<String> for Error {
    fn from(s: String) -> Self { Error(s) }
}
```

---

### `sdk/rust/src/workflow.rs`

#### Sentinels

```rust
/// Terminal success sentinel. Use as the argument to .next() to end a workflow successfully.
pub const SUCCEED: &str = "__succeed__";

/// Terminal failure sentinel. Use as the argument to .next() or .catch() to end a workflow as failed.
pub const FAIL: &str = "__fail__";
```

#### `HandlerFn` / `ChoiceFn`

```rust
use std::future::Future;
use std::pin::Pin;

/// The signature for Task and Service handler functions.
/// Handlers are async: they return a boxed future to support both sync and async implementations.
pub type HandlerFn = Box<
    dyn Fn(Input) -> Pin<Box<dyn Future<Output = Result<Output, crate::Error>> + Send>>
        + Send
        + Sync,
>;

/// The signature for Choice state handlers.
/// Returns the name of the next state or a sentinel.
pub type ChoiceFn = Box<
    dyn Fn(Input) -> Pin<Box<dyn Future<Output = Result<String, crate::Error>> + Send>>
        + Send
        + Sync,
>;
```

#### `RetryPolicy`

```rust
#[derive(Debug, Clone, Default)]
pub struct RetryPolicy {
    pub max_attempts: u32,     // must be >= 1
    pub backoff_seconds: u64,  // seconds between attempts; 0 = no delay
}
```

#### `Workflow`

```rust
pub struct Workflow {
    name: String,
    tasks: HashMap<String, TaskDef>,
    steps: Vec<StepBuilder>,
}

impl Workflow {
    /// Creates a new workflow with the given name.
    pub fn new(name: impl Into<String>) -> Self;

    /// Registers an async function as a Task state.
    /// handler may be None only if invoke_service is subsequently called on the returned TaskDef.
    pub fn task(
        &mut self,
        name: impl Into<String>,
        handler: Option<HandlerFn>,
    ) -> &mut TaskDef;

    /// Registers an async function as a Choice state.
    /// handler must not be None.
    pub fn choice(
        &mut self,
        name: impl Into<String>,
        handler: ChoiceFn,
    ) -> &mut TaskDef;

    /// Registers a timed pause state.
    pub fn wait(&mut self, name: impl Into<String>, duration: std::time::Duration) -> &mut TaskDef;

    /// Sets the ordered list of state transitions. Must be called before run().
    pub fn flow(&mut self, steps: Vec<StepBuilder>) -> &mut Self;

    /// Validates all registration and flow invariants.
    /// Returns Err with a descriptive message if any invariant is violated.
    pub fn validate(&self) -> Result<(), String>;
}
```

#### `TaskDef`

```rust
pub struct TaskDef {
    pub(crate) name: String,
    pub(crate) handler: Option<HandlerFn>,
    pub(crate) choice_handler: Option<ChoiceFn>,
    pub(crate) service_target: Option<String>,
    pub(crate) retry: Option<RetryPolicy>,
    pub(crate) catch: Option<String>,
}

impl TaskDef {
    /// Delegates this task to a named registered service.
    /// Mutually exclusive with an inline handler.
    pub fn invoke_service(&mut self, service_name: impl Into<String>) -> &mut Self;

    /// Attaches a RetryPolicy to this task.
    pub fn retry(&mut self, policy: RetryPolicy) -> &mut Self;

    /// Sets the error-handler state name for this task.
    pub fn catch(&mut self, state_name: impl Into<String>) -> &mut Self;
}
```

#### `StepBuilder`

```rust
/// Returns a new StepBuilder for the named state. Free function for ergonomic use in flow().
pub fn step(name: impl Into<String>) -> StepBuilder;

pub struct StepBuilder {
    pub(crate) name: String,
    pub(crate) next: Option<String>,
    pub(crate) catch: Option<String>,
    pub(crate) retry: Option<RetryPolicy>,
    pub(crate) is_end: bool,
}

impl StepBuilder {
    pub fn next(mut self, state_name: impl Into<String>) -> Self;
    pub fn catch(mut self, state_name: impl Into<String>) -> Self;
    pub fn retry(mut self, policy: RetryPolicy) -> Self;
    /// Equivalent to .next(SUCCEED).
    pub fn end(mut self) -> Self;
}
```

#### `#[task]` Proc Macro

```rust
// Defined in a separate crate: kflow-macros (sdk/rust/kflow-macros/)
// Allows ergonomic handler definition:
//
// #[kflow::task]
// async fn validate_order(input: Input) -> Result<Output, kflow::Error> {
//     Ok(Output::from([("valid".to_string(), true.into())]))
// }
//
// Expands to a function with the HandlerFn signature, wrapping the body in an async block
// and boxing the future. Does not change runtime semantics.
```

If the `kflow-macros` crate proves complex to implement, it is optional. The builder API alone (`workflow.task("Name", Some(Box::new(|input| Box::pin(async move { ... }))))`) is the canonical form.

---

### `sdk/rust/src/service.rs`

```rust
#[repr(u8)]
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ServiceMode {
    Deployment = 0,
    Lambda     = 1,
}

impl Default for ServiceMode {
    fn default() -> Self { ServiceMode::Deployment }
}

pub struct ServiceDef {
    pub(crate) name: String,
    pub(crate) handler: Option<HandlerFn>,
    pub(crate) mode: ServiceMode,
    pub(crate) port: u16,
    pub(crate) min_scale: u32,
    pub(crate) max_scale: u32,
    pub(crate) ingress_host: Option<String>,
    pub(crate) timeout: std::time::Duration,
}

impl ServiceDef {
    pub fn new(name: impl Into<String>) -> Self;
    pub fn handler(mut self, fn_: HandlerFn) -> Self;
    pub fn mode(mut self, mode: ServiceMode) -> Self;
    pub fn port(mut self, port: u16) -> Self;
    pub fn scale(mut self, min: u32, max: u32) -> Self;
    pub fn expose(mut self, host: impl Into<String>) -> Self;
    pub fn timeout(mut self, d: std::time::Duration) -> Self;
}

/// Module-level convenience constructor.
pub fn new_service(name: impl Into<String>) -> ServiceDef {
    ServiceDef::new(name)
}
```

---

### `sdk/rust/src/runner.rs`

```rust
/// Entry point for workflow execution.
///
/// Dispatch logic (in priority order):
/// 1. If --state=<name> flag is present → execute that state handler, write output, exit.
/// 2. If --service=<name> flag is present → enter service execution path.
/// 3. Otherwise → validate wf, serialise as JSON, POST to Control Plane, block.
///
/// Environment variables for --state path:
///     KFLOW_STATE_TOKEN   — required; HMAC-SHA256 signed token for this state execution
///     KFLOW_GRPC_ENDPOINT — required; RunnerService address (e.g. kflow-cp.kflow.svc.cluster.local:9090)
///     KFLOW_EXECUTION_ID  — required; execution UUID (logging/observability only)
///
/// Runner protocol (gRPC via tonic):
///     1. Connect to KFLOW_GRPC_ENDPOINT
///     2. RunnerServiceClient::get_input(token) → Input
///     3. HandlerFn(input).await
///     4. RunnerServiceClient::complete_state(token, output)
///            OR RunnerServiceClient::fail_state(token, err_msg)
///     5. std::process::exit(0) on success, exit(1) on error
///
/// See Phase 13 for proto definitions. Tonic stubs generated by tonic-build in build.rs.
pub fn run(wf: Workflow);

/// Entry point for service registration and execution.
///
/// Dispatch logic:
/// 1. If --service=<name> matches svc.name:
///    - Deployment: start tonic gRPC server implementing ServiceRunnerService on svc.port;
///      route Invoke RPC to handler. (Replaces HTTP POST /invoke.)
///    - Lambda: dial KFLOW_GRPC_ENDPOINT, call RunnerService::get_input(token),
///      run handler, call RunnerService::complete_state/fail_state, exit.
/// 2. Otherwise → validate svc, POST definition to Control Plane, return.
///
/// Safe to call alongside run() in the same fn main(). Flag dispatch selects one path.
///
/// gRPC stubs: generated by tonic-build from proto/kflow/v1/runner.proto (build.rs).
pub fn run_service(svc: ServiceDef);

/// Runs a workflow entirely in-process without any Control Plane or Kubernetes.
///
/// For local development and unit testing only. Never use in production.
/// Returns the final Output when the workflow reaches a terminal state, or
/// an Err if the workflow fails without a Catch handler.
pub fn run_local(wf: Workflow, input: Input) -> Result<Output, String>;
```

---

### `sdk/rust/Cargo.toml`

```toml
[package]
name    = "kflow"
version = "0.1.0"
edition = "2021"

[dependencies]
serde       = { version = "1",    features = ["derive"] }
serde_json  = "1"
tokio       = { version = "1",    features = ["full"] }
reqwest     = { version = "0.12", features = ["json"] }   # Control Plane HTTP client
axum        = "0.7"                                        # HTTP server for Deployment-mode services
thiserror   = "1"

[dev-dependencies]
tokio-test = "0.4"

# Optional proc-macro crate (implement after core SDK is working)
# kflow-macros = { path = "kflow-macros", optional = true }
# [features]
# macros = ["kflow-macros"]
```

---

## Runner Protocol (Resolved — gRPC via Tonic)

The runner protocol is **gRPC**, defined in `proto/kflow/v1/runner.proto` (Phase 13). Rust containers use **tonic** (the idiomatic Rust gRPC library) with stubs generated by **tonic-build** in `build.rs`.

### `RunnerService` (container → Control Plane)

```rust
// In build.rs:
tonic_build::compile_protos("../../proto/kflow/v1/runner.proto")?;

// In runner.rs:
use tonic::transport::Channel;
use kflow_proto::runner_service_client::RunnerServiceClient;
use kflow_proto::{GetInputRequest, CompleteStateRequest, FailStateRequest};

let endpoint = std::env::var("KFLOW_GRPC_ENDPOINT")?;
let mut client = RunnerServiceClient::connect(format!("http://{}", endpoint)).await?;

// Get input
let resp = client.get_input(GetInputRequest { state_token: token.clone() }).await?;
let input: Input = proto_struct_to_hashmap(resp.into_inner().input);

// Report completion
client.complete_state(CompleteStateRequest {
    state_token: token,
    output: Some(hashmap_to_proto_struct(output)),
}).await?;
```

### `ServiceRunnerService` (Control Plane → Deployment-mode service)

Deployment-mode Rust services implement a tonic server for `ServiceRunnerService`:

```rust
#[tonic::async_trait]
impl ServiceRunnerService for MyServiceHandler {
    async fn invoke(
        &self,
        request: Request<InvokeRequest>,
    ) -> Result<Response<InvokeResponse>, Status> {
        let input = proto_struct_to_hashmap(request.into_inner().input);
        let output = (self.handler_fn)(input).await.map_err(|e| Status::internal(e.to_string()))?;
        Ok(Response::new(InvokeResponse { output: Some(hashmap_to_proto_struct(output)) }))
    }
}
```

### Cargo.toml additions

```toml
[dependencies]
tonic       = "0.12"
prost       = "0.13"
prost-types = "0.13"    # for google.protobuf.Struct

[build-dependencies]
tonic-build = "0.12"
```

The `axum` dependency is retained for optional HTTP healthz endpoint on Deployment-mode services; it is no longer used for the primary `/invoke` route.

### Impact on K8s Job containers

Rust Job containers receive:
- `KFLOW_STATE_TOKEN` (HMAC-signed token)
- `KFLOW_GRPC_ENDPOINT` (RunnerService address)
- `KFLOW_EXECUTION_ID` (observability only)

`KFLOW_INPUT`, `KFLOW_MONGO_URI`, and `KFLOW_MONGO_DB` are NOT injected into Rust containers.

---

## Validation Rules

`Workflow::validate()` enforces the same invariants as Go's `Workflow.Validate()`:

1. `flow()` must have been called with at least one step.
2. All task/choice names and service names within the same binary are globally unique.
3. Each task has exactly one of: inline handler function or `invoke_service` target.
4. Every `next()` and `catch()` target resolves to a registered state name, `SUCCEED`, or `FAIL`.
5. Every `ServiceDef` with `mode == Deployment` has `min_scale >= 1`.

Validation failures return `Err(String)` with a descriptive message. `run()` calls `validate()` and `panic!`s on error (consistent with Go's `log.Fatal` behaviour).

---

## Execution Model for Rust Containers

Rust containers use the **container-per-language strategy** — not the shared binary strategy used by Go. The Rust SDK produces a binary that is compiled once and baked into a container image.

```
Container image = Rust binary (user code + kflow SDK statically linked)
```

```
kflow::run(wf)
  └─ --state=<name>    → dial KFLOW_GRPC_ENDPOINT, GetInput(token), run handler,
                          CompleteState/FailState via RunnerService, exit
  └─ --service=<name>  → Deployment: start tonic gRPC server (ServiceRunnerService)
                         Lambda:     dial KFLOW_GRPC_ENDPOINT, GetInput(token),
                                     run handler, CompleteState/FailState, exit
  └─ (no flag)         → serialise workflow, POST to Control Plane, block
```

The Control Plane communicates with Rust containers only through the runner protocol. No Go code is linked into the Rust binary.

---

## Design Invariants

1. `Input` and `Output` are `HashMap<String, serde_json::Value>`. Never introduce custom types that break JSON round-trip.
2. All handler functions are `async`. The runtime is Tokio (`tokio::main` in the binary entry point).
3. `run()` and `run_service()` are safe to call together in the same `fn main()`. Flag dispatch selects one path.
4. `run_local()` is dev-only. Its doc comment must warn against production use.
5. `SUCCEED` and `FAIL` constants match Go and Python SDK values exactly (`"__succeed__"`, `"__fail__"`).
6. The SDK never writes to MongoDB directly. Output is returned via `RunnerServiceClient::complete_state`/`fail_state` gRPC calls; `RunnerServiceServer` on the Control Plane performs the actual store writes.
7. `validate()` is always called before any network operation. A validation failure panics with a clear message.
8. The `kflow-macros` proc-macro crate is optional. The core builder API must work without it.
9. The SDK must compile with `cargo build --release` with no `unsafe` blocks outside of `std` or well-audited dependencies.

---

## Acceptance Criteria / Verification

- [ ] `cargo build --release` in `sdk/rust/` succeeds with zero errors and zero warnings.
- [ ] `cargo test` in `sdk/rust/` passes all unit tests with no network calls.
- [ ] Unit test: `wf.task("A", Some(handler)).invoke_service("svc")` causes `validate()` to return `Err`.
- [ ] Unit test: `wf.task("A", None)` (no `invoke_service`) causes `validate()` to return `Err`.
- [ ] Unit test: `step("A").next("B")` where "B" is unregistered causes `validate()` to return `Err`.
- [ ] Unit test: `ServiceDef::new("svc").mode(ServiceMode::Deployment).scale(0, 5)` causes `validate()` to return `Err`.
- [ ] `run_local()` drives a multi-step workflow including retry and Catch routing.
- [ ] `SUCCEED` and `FAIL` values match Go/Python SDK constants.
- [ ] `cargo clippy -- -D warnings` passes with zero lints.
- [ ] `cargo doc --no-deps` generates documentation without errors.
