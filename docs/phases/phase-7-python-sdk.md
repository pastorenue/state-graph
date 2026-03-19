# Phase 7 — Python SDK

## Goal

Implement the Python SDK (`sdk/python/kflow/`) that allows user code written in Python to define workflows and services using a decorator-based API. The SDK communicates with the Control Plane over HTTP. Python containers are never co-located with the Go binary; each Python task or service runs in its own container image.

---

## Phase Dependencies

- **Phase 5** must be complete. The Control Plane API (`POST /api/v1/workflows/:name/run`, `POST /api/v1/services`, etc.) must be stable before the Python SDK can submit definitions.
- **Runner Protocol decision** (currently an open question in `AGENTS.md`) must be resolved before `runner.py` can fully implement the execution path. The reference file documents the known contract and flags the TBD decision point.

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

    Environment variables used in state execution path:
        KFLOW_EXECUTION_ID  — required
        KFLOW_CONTROL_PLANE — Control Plane base URL (e.g. http://kflow-cp:8080)

    Runner protocol for state execution: **TBD** (see Open Questions below).
    """
```

#### `run_service(svc: ServiceDef)`

```python
def run_service(svc: ServiceDef) -> None:
    """Entry point for service registration and execution.

    Dispatch logic:
    1. If --service=<name> flag matches svc.name:
       - Deployment mode: start HTTP server on svc.port; route POST /invoke to handler.
       - Lambda mode: read KFLOW_INPUT env var, call handler, return output via runner protocol, exit.
    2. Otherwise → validate svc, POST service definition to Control Plane API, return.

    Safe to call alongside run() in the same __main__. Flag dispatch selects one path.
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

## Runner Protocol (Open Question)

**This is an explicitly unresolved design decision in `AGENTS.md`.** The runner protocol defines how the Control Plane sends `Input` to a Python container and receives `Output` back. Until this decision is made, `runner.py` cannot fully implement the `--state=<name>` path for production.

The three candidate approaches from `AGENTS.md`:

| Approach | Description | Tradeoffs |
|----------|-------------|-----------|
| HTTP JSON `POST /run` | Container starts a minimal HTTP server; Control Plane POSTs input, reads response | Clean boundary; adds startup latency per Lambda invocation |
| stdin/stdout | Control Plane writes JSON to stdin, reads JSON from stdout | No server overhead; harder to debug; coupling to process lifecycle |
| gRPC | Protobuf contract; bidirectional streaming possible | Strongest contract; most infrastructure overhead |

**Until the runner protocol is decided, `runner.py` must implement the `--state=<name>` path using a placeholder that reads input from `KFLOW_INPUT` env var (same as Lambda mode) and writes output to stdout as JSON.** This allows local testing and defers the production protocol.

The runner protocol decision impacts:
- How `internal/k8s/job.go` spawns Python containers (what args/env vars it passes)
- How `K8sExecutor` retrieves output from Python Job containers
- Whether `GetStateOutput` reads from the store (Lambda-style) or from the Job's stdout

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
- With `--state=<name>`: executes the named state handler (runner protocol TBD).
- With `--service=<name>`: enters the service execution path.

---

## Design Invariants

1. `Input` and `Output` are plain `dict[str, any]`. Never introduce custom classes — JSON round-trip must be lossless.
2. The Python SDK must never import any Go binary or call any Go code. It communicates with the Control Plane only over HTTP.
3. `run_local()` is dev-only. It must carry a docstring warning against production use.
4. `run()` and `run_service()` are safe to call together in the same `if __name__ == "__main__"` block.
5. `Succeed` and `Fail` constants are strings matching the Go SDK sentinels exactly (`"__succeed__"`, `"__fail__"`).
6. Async handlers are fully supported but the SDK entry points are always sync.
7. The SDK never writes to a database directly. Output is returned via the runner protocol or HTTP response; the Control Plane is the sole state store writer.
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
