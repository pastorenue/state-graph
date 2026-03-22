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


def run(wf: Workflow, input: Input = None) -> None:
    """Entry point for workflow execution.

    Dispatch priority:
    1. KFLOW_STATE_TOKEN set → worker mode (K8s Job executing one state).
    2. --state=<name>        → same worker mode via CLI flag.
    3. --local flag          → run_local in-process.
    4. (no flag)             → validate, POST graph + trigger to Control Plane.
    """
    token = os.environ.get("KFLOW_STATE_TOKEN", "")
    if token:
        state_name = _flag("state")
        if not state_name:
            sys.stderr.write("kflow: KFLOW_STATE_TOKEN set but --state=<name> missing\n")
            sys.exit(1)
        _run_state(wf, state_name, token)
        return

    state_name = _flag("state")
    service_name = _flag("service")

    if state_name:
        # Token not in env; may be injected another way — attempt worker dispatch.
        token = os.environ.get("KFLOW_STATE_TOKEN", "")
        _run_state(wf, state_name, token)
        return

    if service_name:
        sys.stderr.write(
            "kflow: --service flag passed to run(); use run_service() for service dispatch\n"
        )
        sys.exit(1)

    if "--local" in sys.argv[1:]:
        run_local(wf, input or {})
        return

    # Normal path: validate then POST to Control Plane.
    wf.validate()
    _post_workflow(wf, input)


def run_service(svc: ServiceDef) -> None:
    """Entry point for service registration and execution."""
    service_name = _flag("service")

    if service_name and service_name == svc._name:
        _run_service_worker(svc)
        return

    svc.validate()
    _post_service(svc)


def run_local(wf: Workflow, input: Input) -> Output:
    """Runs a workflow entirely in-process without any Control Plane or Kubernetes.

    WARNING: for local development and testing only. Never use in production.
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
                return dict(input), None
            if td._is_choice:
                choice = _call_handler(td._choice_fn, input)
                return {**input, "__choice__": choice}, None
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


def _serialise_graph(wf: Workflow) -> dict:
    """Serialise the workflow graph to the RegisterWorkflow JSON format."""
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
    result: dict = {"steps": steps}
    if wf._image:
        result["image"] = wf._image
    return result


def _post_workflow(wf: Workflow, input: Input = None) -> None:
    """Register + trigger the workflow on the Control Plane HTTP API."""
    try:
        import httpx
    except ImportError:
        sys.stderr.write("kflow: httpx is required to submit workflows (pip install httpx)\n")
        sys.exit(1)

    endpoint = os.environ.get("KFLOW_SERVER", "http://localhost:8080")
    api_key  = os.environ.get("KFLOW_API_KEY", "")
    headers  = {"Authorization": f"Bearer {api_key}"} if api_key else {}

    # 1. Register (409 = already registered = OK)
    graph_json = _serialise_graph(wf)
    reg_resp = httpx.post(
        f"{endpoint}/api/v1/workflows",
        json={"graph": {"name": wf._name, **graph_json}},
        headers=headers,
        timeout=30,
    )
    if reg_resp.status_code not in (200, 201, 409):
        sys.stderr.write(f"kflow: register failed {reg_resp.status_code}: {reg_resp.text}\n")
        sys.exit(1)

    # 2. Trigger
    run_resp = httpx.post(
        f"{endpoint}/api/v1/workflows/{wf._name}/run",
        json={"input": input or {}},
        headers=headers,
        timeout=30,
    )
    if run_resp.status_code not in (200, 201, 202):
        sys.stderr.write(f"kflow: run failed {run_resp.status_code}: {run_resp.text}\n")
        sys.exit(1)


def _post_service(svc: ServiceDef) -> None:
    """POST the service definition to the Control Plane HTTP API."""
    try:
        import httpx
    except ImportError:
        sys.stderr.write("kflow: httpx is required to register services (pip install httpx)\n")
        sys.exit(1)

    endpoint = os.environ.get("KFLOW_SERVER", "http://localhost:8080")
    api_key  = os.environ.get("KFLOW_API_KEY", "")
    headers  = {"Authorization": f"Bearer {api_key}"} if api_key else {}
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
        headers=headers,
        timeout=30,
    )
    if resp.status_code not in (200, 201):
        sys.stderr.write(f"kflow: Control Plane returned {resp.status_code}: {resp.text}\n")
        sys.exit(1)


def _run_state(wf: Workflow, state_name: str, token: str) -> None:
    """Execute a single state via the RunnerService gRPC protocol (worker mode)."""
    import grpc
    from kflow.proto.runner_pb2 import GetInputRequest, CompleteStateRequest, FailStateRequest
    from kflow.proto.runner_pb2_grpc import RunnerServiceStub
    from google.protobuf.struct_pb2 import Struct

    endpoint = os.environ.get(
        "KFLOW_GRPC_ENDPOINT", "kflow-cp.kflow.svc.cluster.local:9090"
    )
    channel = grpc.insecure_channel(endpoint)
    stub    = RunnerServiceStub(channel)

    try:
        resp  = stub.GetInput(GetInputRequest(token=token))
        input = dict(resp.payload) if resp.payload else {}
    except grpc.RpcError as exc:
        sys.stderr.write(f"kflow: GetInput failed: {exc}\n")
        sys.exit(1)

    td = wf._tasks.get(state_name)
    if td is None:
        stub.FailState(FailStateRequest(token=token, error_message=f"unknown state: {state_name}"))
        sys.exit(1)

    try:
        output = _call_handler(td._handler, input)
        s = Struct()
        s.update(output or {})
        stub.CompleteState(CompleteStateRequest(token=token, output=s))
        sys.exit(0)
    except Exception as exc:
        stub.FailState(FailStateRequest(token=token, error_message=str(exc)))
        sys.exit(1)


def _run_service_worker(svc: ServiceDef) -> None:
    """Execute a service worker via the gRPC runner protocol (Phase 13)."""
    sys.stderr.write(
        "kflow: service worker mode requires gRPC RunnerService (Phase 13 not yet implemented)\n"
    )
    sys.exit(1)
