"""Entry points: run(), run_service(), run_local()."""
from __future__ import annotations

import asyncio
import inspect
import os
import sys
import time
from typing import Optional

from kflow.workflow import (
    Input,
    Output,
    Succeed,
    Fail,
    StepBuilder,
    Workflow,
    _RetryPolicy,
)
from kflow.service import ServiceDef


def run(wf: Workflow) -> None:
    """Entry point for workflow execution.

    Dispatch priority:
    1. --state=<name>   → execute one state via RunnerService gRPC, then exit.
    2. --service=<name> → enter service execution path.
    3. (no flag)        → validate, serialise graph as JSON, POST to Control Plane.

    gRPC runner protocol is implemented in Phase 13.
    """
    state_name   = _flag("state")
    service_name = _flag("service")

    if state_name:
        _run_state(wf, state_name)
        return

    if service_name:
        # Workflow-context service execution is handled by run_service().
        sys.stderr.write(
            f"kflow: --service flag passed to run(); use run_service() for service dispatch\n"
        )
        sys.exit(1)

    # Normal path: validate then POST to Control Plane.
    wf.validate()
    _post_workflow(wf)


def run_service(svc: ServiceDef) -> None:
    """Entry point for service registration and execution.

    Dispatch priority:
    1. --service=<name> matches svc → execute service (Lambda or Deployment gRPC server).
    2. (no match) → validate svc, POST service definition to Control Plane.

    gRPC runner protocol is implemented in Phase 13.
    """
    service_name = _flag("service")

    if service_name and service_name == svc._name:
        _run_service_worker(svc)
        return

    # Registration path.
    svc.validate()
    _post_service(svc)


def run_local(wf: Workflow, input: Input) -> Output:
    """Runs a workflow entirely in-process without any Control Plane or Kubernetes.

    WARNING: for local development and testing only. Never use in production.

    Implements the same write-ahead state transition logic as the Go MemoryStore,
    using an in-memory dict. Retries and Catch routing are fully supported.
    Returns the final Output when the workflow reaches a terminal state.
    Raises RuntimeError if the workflow fails without a Catch handler.
    """
    wf.validate()
    graph = _build_graph(wf)
    entry = wf._steps[0].name if wf._steps else None
    if not entry:
        raise ValueError("run_local: no steps defined")

    current: dict = dict(input)
    node_name: Optional[str] = entry

    while node_name not in (Succeed, Fail, None):
        node = graph[node_name]
        td   = wf._tasks[node_name]

        output, err = _execute_state_local(td, node, dict(current))

        if err is not None:
            catch_name = node.get("catch")
            if catch_name and catch_name not in (Succeed, Fail):
                current   = {**current, "_error": str(err)}
                node_name = catch_name
                continue
            raise RuntimeError(str(err))

        if td._is_choice:
            node_name = output.get("__choice__", Fail)
        else:
            node_name = node["next"]
            if node_name in (Succeed, Fail):
                return output

        current = output

    return current


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------

def _flag(name: str) -> Optional[str]:
    """Parse --<name>=<value> or --<name> <value> from sys.argv."""
    prefix = f"--{name}="
    for i, arg in enumerate(sys.argv[1:], 1):
        if arg.startswith(prefix):
            return arg[len(prefix):]
        if arg == f"--{name}" and i < len(sys.argv) - 1:
            return sys.argv[i + 1]
    return None


def _call_handler(fn, input: dict):
    """Invoke handler, supporting both sync and async functions."""
    if inspect.iscoroutinefunction(fn):
        return asyncio.run(fn(input))
    return fn(input)


def _build_graph(wf: Workflow) -> dict:
    """Build a name→node dict from the workflow steps."""
    graph: dict = {}
    for sb in wf._steps:
        td   = wf._tasks.get(sb.name)
        catch = sb._catch or (td._catch if td else None)
        retry = sb._retry or (td._retry if td else None)
        next_ = sb._next or ""
        graph[sb.name] = {
            "next":  next_,
            "catch": catch,
            "retry": retry,
        }
    return graph


def _execute_state_local(td, node: dict, input: dict):
    """Execute a state with retry. Returns (output, error)."""
    retry: Optional[_RetryPolicy] = node.get("retry")
    max_attempts    = retry.max_attempts if retry else 1
    backoff_seconds = retry.backoff_seconds if retry else 0

    last_err = None
    for attempt in range(max_attempts):
        if attempt > 0 and backoff_seconds > 0:
            time.sleep(backoff_seconds)
        try:
            if td._is_wait:
                time.sleep(td._wait_seconds)
                return {}, None
            if td._is_choice:
                choice = _call_handler(td._choice_fn, input)
                return {"__choice__": choice}, None
            if td._service_target:
                raise RuntimeError(
                    f"run_local: service dispatch not available for {td._name!r}; "
                    "use run() with a live Control Plane"
                )
            output = _call_handler(td._handler, input)
            return output, None
        except Exception as exc:
            last_err = exc

    return None, last_err


def _post_workflow(wf: Workflow) -> None:
    """POST the workflow definition to the Control Plane HTTP API."""
    try:
        import httpx
    except ImportError:
        sys.stderr.write("kflow: httpx is required to submit workflows (pip install httpx)\n")
        sys.exit(1)

    endpoint = os.environ.get("KFLOW_CONTROL_PLANE_URL", "http://localhost:8080")
    graph_json = _serialise_workflow(wf)

    resp = httpx.post(
        f"{endpoint}/api/v1/workflows/{wf._name}/run",
        json=graph_json,
        timeout=30,
    )
    if resp.status_code not in (200, 201):
        sys.stderr.write(f"kflow: Control Plane returned {resp.status_code}: {resp.text}\n")
        sys.exit(1)


def _post_service(svc: ServiceDef) -> None:
    """POST the service definition to the Control Plane HTTP API."""
    try:
        import httpx
    except ImportError:
        sys.stderr.write("kflow: httpx is required to register services (pip install httpx)\n")
        sys.exit(1)

    endpoint = os.environ.get("KFLOW_CONTROL_PLANE_URL", "http://localhost:8080")
    resp = httpx.post(
        f"{endpoint}/api/v1/services",
        json={
            "name":         svc._name,
            "mode":         int(svc._mode),
            "port":         svc._port,
            "min_scale":    svc._min_scale,
            "max_scale":    svc._max_scale,
            "ingress_host": svc._ingress_host or "",
            "timeout_ms":   int(svc._timeout * 1000),
        },
        timeout=30,
    )
    if resp.status_code not in (200, 201):
        sys.stderr.write(f"kflow: Control Plane returned {resp.status_code}: {resp.text}\n")
        sys.exit(1)


def _serialise_workflow(wf: Workflow) -> dict:
    """Serialise the workflow graph to a JSON-compatible dict."""
    steps = []
    for sb in wf._steps:
        td = wf._tasks.get(sb.name)
        retry = sb._retry or (td._retry if td else None)
        steps.append({
            "name":           sb.name,
            "next":           sb._next or "",
            "catch":          sb._catch or (td._catch if td else "") or "",
            "is_end":         sb._is_end,
            "service_target": (td._service_target if td else "") or "",
            "retry": {
                "max_attempts":    retry.max_attempts,
                "backoff_seconds": retry.backoff_seconds,
            } if retry else None,
        })
    return {"steps": steps}


def _run_state(wf: Workflow, state_name: str) -> None:
    """Execute a single state via the RunnerService gRPC protocol (Phase 13).

    TODO(Phase 13): implement gRPC client using generated stubs from
    proto/kflow/v1/runner.proto. Steps:
    1. dial os.environ["KFLOW_GRPC_ENDPOINT"]
    2. RunnerService.GetInput(state_token) → Input
    3. call handler
    4. RunnerService.CompleteState(token, output) or FailState(token, errMsg)
    5. sys.exit(0) on success, sys.exit(1) on error
    """
    sys.stderr.write(
        f"kflow: --state mode requires gRPC RunnerService (Phase 13 not yet implemented)\n"
    )
    sys.exit(1)


def _run_service_worker(svc: ServiceDef) -> None:
    """Execute a service worker via the gRPC runner protocol (Phase 13).

    TODO(Phase 13): implement gRPC client/server stubs.
    - Deployment mode: start tonic gRPC ServiceRunnerService server.
    - Lambda mode: dial KFLOW_GRPC_ENDPOINT, GetInput, run, CompleteState/FailState, exit.
    """
    sys.stderr.write(
        f"kflow: service worker mode requires gRPC RunnerService (Phase 13 not yet implemented)\n"
    )
    sys.exit(1)
