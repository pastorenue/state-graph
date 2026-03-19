# Phase 7 — Python SDK

## Goal

Implement the Python SDK (`sdk/python/kflow/`) that allows user code written in Python to define workflows and services using a decorator-based API. The SDK communicates with the Control Plane over HTTP. Python containers are never co-located with the Go binary; each Python task or service runs in its own container image.

---

## Phase Dependencies

- **Phase 5** must be complete. The Control Plane API (`POST /api/v1/workflows/:name/run`, `POST /api/v1/services`, etc.) must be stable before the Python SDK can submit definitions.
- **Phase 13** must be complete. The runner protocol is now resolved: Python containers use gRPC `RunnerService` (defined in `proto/kflow/v1/runner.proto`). `runner.py` must implement a gRPC client stub for `RunnerService` and optionally a `ServiceRunnerService` server for Deployment-mode services.

---

## Files to Create

| File | Purpose |
|------|---------|
| `sdk/python/kflow/__init__.py` | Public re-exports: `Workflow`, `NewService`, `step`, `run`, `run_service`, `Input`, `Output` |
| `sdk/python/kflow/workflow.py` | `Workflow` class, `TaskDef`, `StepBuilder` — mirrors Go SDK semantics |
| `sdk/python/kflow/service.py` | `ServiceDef`, `ServiceMode` enum |
| `sdk/python/kflow/runner.py` | `run()`, `run_service()`, `run_local()` — entry points and runner-protocol dispatch |
| `sdk/python/pyproject.toml` | Package metadata, dependencies, build config |
| `sdk/python/README.md` | Usage guide (only if explicitly requested by a future task) |

---

## Key Types / Interfaces / Functions

### `sdk/python/kflow/workflow.py`

#### `Input` / `Output`

```python
# Input and Output are plain dicts. All keys must be strings;
# all values must be JSON-serialisable (str, int, float, bool, list, dict, None).
Input = dict[str, any]
Output = dict[str, any]
```

#### `Workflow`

```python
class Workflow:
    def __init__(self, name: str) -> None: ...

    def task(self, name: str):
        """Decorator that registers a function as a Task state.

        Usage:
            @app.task("ValidateOrder")
            def validate_order(input: Input) -> Output:
                ...

        The decorated function receives a plain dict (Input) and must return
        a plain dict (Output). Async functions (async def) are also supported
        — the runner calls asyncio.run() when the handler is async.
        """

    def choice(self, name: str):
        """Decorator that registers a function as a Choice state.

        The decorated function must return a str (the name of the next state
        or a sentinel: "Succeed" or "Fail").

        Usage:
            @app.choice("RouteOrder")
            def route_order(input: Input) -> str:
                return "HighValuePath" if input["amount"] > 1000 else "StandardPath"
        """

    def wait(self, name: str, seconds: int) -> "TaskDef": ...

    def flow(self, *steps: "StepBuilder") -> None:
        """Sets the ordered transition graph. Must be called once before run()."""

    def validate(self) -> None:
        """Raises a ValueError (with a descriptive message) if any invariant is violated.
        Called internally by run() before submitting to the Control Plane.
        """
```

#### `TaskDef`

```python
class TaskDef:
    def invoke_service(self, service_name: str) -> "TaskDef": ...
    def retry(self, max_attempts: int, backoff_seconds: int = 0) -> "TaskDef": ...
    def catch(self, state_name: str) -> "TaskDef": ...
```

#### `StepBuilder`

```python
def step(name: str) -> "StepBuilder":
    """Returns a StepBuilder for the named state. Module-level convenience function."""

class StepBuilder:
    def next(self, state_name: str) -> "StepBuilder": ...
    def catch(self, state_name: str) -> "StepBuilder": ...
    def retry(self, max_attempts: int, backoff_seconds: int = 0) -> "StepBuilder": ...
    def end(self) -> "StepBuilder":
        """Equivalent to next("Succeed")."""
```

Sentinels as module-level constants:
```python
Succeed = "__succeed__"
Fail    = "__fail__"
```

---

### `sdk/python/kflow/service.py`

```python
from enum import IntEnum

class ServiceMode(IntEnum):
    Deployment = 0
    Lambda     = 1

class ServiceDef:
    def __init__(self, name: str) -> None: ...

    def handler(self, fn) -> "ServiceDef": ...
    def mode(self, mode: ServiceMode) -> "ServiceDef": ...
    def port(self, port: int) -> "ServiceDef": ...
    def scale(self, min_: int, max_: int) -> "ServiceDef": ...
    def expose(self, host: str) -> "ServiceDef": ...
    def timeout(self, seconds: float) -> "ServiceDef": ...

def new_service(name: str) -> ServiceDef:
    """Module-level convenience constructor."""
```

---

### `sdk/python/kflow/runner.py`

#### `run(wf: Workflow)`

```python
def run(wf: Workflow) -> None:
    """Entry point for workflow execution.

    Dispatch logic (in priority order):
    1. If --state=<name> flag is present → execute that single state, write output, exit.
    2. If --service=<name> flag is present → enter service execution path.
    3. Otherwise → validate wf, serialise graph as JSON, POST to Control Plane API, block.

    Environment variables used in --state execution path:
        KFLOW_STATE_TOKEN   — required; HMAC-SHA256 signed token for this state execution
        KFLOW_GRPC_ENDPOINT — required; RunnerService address (e.g. kflow-cp.kflow.svc.cluster.local:9090)
        KFLOW_EXECUTION_ID  — required; execution UUID (logging/observability only)

    Runner protocol for state execution (gRPC):
        1. dial KFLOW_GRPC_ENDPOINT
        2. RunnerService.GetInput(token) → kflow.Input
        3. HandlerFunc(ctx, input)
        4. RunnerService.CompleteState(token, output)
               OR RunnerService.FailState(token, errMsg)
        5. exit 0 on success, exit 1 on error

    gRPC stub: generated from proto/kflow/v1/runner.proto via grpcio-tools.
    See Phase 13 for proto definitions and token format.
    """
```

#### `run_service(svc: ServiceDef)`

```python
def run_service(svc: ServiceDef) -> None:
    """Entry point for service registration and execution.

    Dispatch logic:
    1. If --service=<name> flag matches svc.name:
       - Deployment mode: start gRPC server implementing ServiceRunnerService on svc.port;
         route Invoke RPC to handler. (Replaces HTTP POST /invoke.)
       - Lambda mode: dial KFLOW_GRPC_ENDPOINT; call RunnerService.GetInput(token),
         run handler, call RunnerService.CompleteState/FailState, exit.
    2. Otherwise → validate svc, POST service definition to Control Plane API, return.

    Safe to call alongside run() in the same __main__. Flag dispatch selects one path.

    gRPC stubs: generated from proto/kflow/v1/runner.proto via grpcio-tools.
    ServiceRunnerService server: implement the Invoke RPC for Deployment-mode services.
    """
```

#### `run_local(wf: Workflow, input: Input)`

```python
def run_local(wf: Workflow, input: Input) -> Output:
    """Runs a workflow entirely in-process without any Control Plane or Kubernetes.

    For local development and testing only. Never use in production.
    Implements the same write-ahead state transition logic as the Go MemoryStore,
    using an in-memory dict. Retries and Catch routing are fully supported.
    Returns the final Output when the workflow reaches a terminal state.
    """
```

---

### `sdk/python/pyproject.toml`

```toml
[build-system]
requires = ["setuptools>=68", "wheel"]
build-backend = "setuptools.backends.legacy:build"

[project]
name = "kflow"
version = "0.1.0"
description = "Python SDK for the kflow workflow engine"
requires-python = ">=3.11"
dependencies = [
    "httpx>=0.27",       # Control Plane HTTP client
    "uvicorn>=0.29",     # ASGI server for Deployment-mode services
    "starlette>=0.37",   # Lightweight ASGI framework for POST /invoke route
]

[project.optional-dependencies]
dev = [
    "pytest>=8",
    "pytest-asyncio>=0.23",
]

[tool.setuptools.packages.find]
where = ["."]
include = ["kflow*"]
```

---

## Runner Protocol (Resolved — gRPC)

The runner protocol is **gRPC**, defined in `proto/kflow/v1/runner.proto` (Phase 13). Python containers use `grpcio` and `grpcio-tools` to generate stubs from the proto file.

### `RunnerService` (container → Control Plane)

```python
# Generated stub usage in runner.py
import grpc
from kflow.gen import runner_pb2, runner_pb2_grpc

channel = grpc.insecure_channel(os.environ["KFLOW_GRPC_ENDPOINT"])
stub = runner_pb2_grpc.RunnerServiceStub(channel)

# Get input for this state
resp = stub.GetInput(runner_pb2.GetInputRequest(state_token=token))
input_dict = dict(resp.input.fields)   # google.protobuf.Struct → dict

# Report output
stub.CompleteState(runner_pb2.CompleteStateRequest(
    state_token=token,
    output=dict_to_proto_struct(output),
))
```

### `ServiceRunnerService` (Control Plane → Deployment-mode service)

Deployment-mode Python services must implement a gRPC server for `ServiceRunnerService`:

```python
from kflow.gen import runner_pb2, runner_pb2_grpc

class ServiceRunnerServicer(runner_pb2_grpc.ServiceRunnerServiceServicer):
    def Invoke(self, request, context):
        input_dict = proto_struct_to_dict(request.input)
        output = handler_fn(input_dict)
        return runner_pb2.InvokeResponse(output=dict_to_proto_struct(output))
```

### pyproject.toml additions

```toml
dependencies = [
    "httpx>=0.27",
    "grpcio>=1.64",
    "grpcio-tools>=1.64",
    "protobuf>=5.0",
    "uvicorn>=0.29",  # retained for potential HTTP fallback / healthz
]
```

### Impact on K8s Job containers

Python Job containers receive:
- `KFLOW_STATE_TOKEN` (HMAC-signed token)
- `KFLOW_GRPC_ENDPOINT` (RunnerService address)
- `KFLOW_EXECUTION_ID` (observability only)

`KFLOW_INPUT`, `KFLOW_MONGO_URI`, and `KFLOW_MONGO_DB` are NOT injected into Python containers.

---

## Validation Rules

`Workflow.validate()` in Python must enforce the same invariants as `Workflow.Validate()` in Go:

1. `flow()` must have been called with at least one step.
2. All state names (tasks + choices) and service names registered in the same binary are globally unique.
3. Each task has exactly one of: decorated handler function or `invoke_service` target.
4. Every `next()` and `catch()` target resolves to a registered state name, `Succeed`, or `Fail`.
5. Every `ServiceDef` with `mode == Deployment` has `min_scale >= 1`.

Validation failures raise `ValueError` with a descriptive message (not a generic error).

---

## Async Handler Support

Python handlers may be either sync or async:

```python
@app.task("FetchData")
async def fetch_data(input: Input) -> Output:
    async with httpx.AsyncClient() as client:
        resp = await client.get(input["url"])
    return {"body": resp.text}
```

`runner.py` detects `asyncio.iscoroutinefunction(fn)` and calls `asyncio.run(fn(input))` for async handlers. The top-level `run()` and `run_service()` entry points are always synchronous — they never expose an async interface.

---

## Execution Model for Python Containers

Python containers use the **container-per-language strategy** — not the shared binary strategy used by Go. Each Python task or Lambda service runs in its own container image that includes the Python interpreter and the user's code.

```
Container image = Python interpreter + user code + kflow SDK + dependencies
```

The binary shares no image with the Go orchestrator. The Control Plane communicates with Python containers only through the runner protocol.

`kflow.run(wf)` in a Python `__main__`:
- Without any flag: serialises the workflow definition and POSTs to the Control Plane.
- With `--state=<name>`: dials `KFLOW_GRPC_ENDPOINT`, calls `RunnerService.GetInput(token)`, executes the named state handler, reports output via `RunnerService.CompleteState`/`FailState` (gRPC — see Phase 13).
- With `--service=<name>`: enters the service execution path.

---

## Design Invariants

1. `Input` and `Output` are plain `dict[str, any]`. Never introduce custom classes — JSON round-trip must be lossless.
2. The Python SDK must never import any Go binary or call any Go code. It communicates with the Control Plane only over HTTP.
3. `run_local()` is dev-only. It must carry a docstring warning against production use.
4. `run()` and `run_service()` are safe to call together in the same `if __name__ == "__main__"` block.
5. `Succeed` and `Fail` constants are strings matching the Go SDK sentinels exactly (`"__succeed__"`, `"__fail__"`).
6. Async handlers are fully supported but the SDK entry points are always sync.
7. The SDK never writes to MongoDB directly. Output is returned via `RunnerService.CompleteState`/`FailState` gRPC calls; `RunnerServiceServer` on the Control Plane performs the actual store writes.
8. `validate()` must be called by `run()` before any network call. A validation failure must not reach the Control Plane.

---

## Acceptance Criteria / Verification

- [ ] `pip install -e sdk/python` succeeds in a clean Python 3.11+ environment.
- [ ] `python -m pytest sdk/python/` passes all unit tests with no network calls.
- [ ] Unit test: decorator `@app.task("A")` registers the function; `validate()` passes; `flow(step("A").end())` compiles.
- [ ] Unit test: task with both a handler and `invoke_service` raises `ValueError`.
- [ ] Unit test: task with neither handler nor `invoke_service` raises `ValueError`.
- [ ] Unit test: `step("A").next("B")` where "B" is unregistered raises `ValueError`.
- [ ] Unit test: `Deployment` mode service with `scale(0, 5)` raises `ValueError`.
- [ ] `run_local()` drives a multi-step workflow in-process including retry and Catch routing.
- [ ] Async handler is called correctly via `asyncio.run()`.
- [ ] `Succeed` and `Fail` constants match Go SDK values (`"__succeed__"`, `"__fail__"`).
