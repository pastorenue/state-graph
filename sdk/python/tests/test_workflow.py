"""Unit tests for the kflow Python SDK — no network calls required."""
import asyncio
import pytest

from kflow.workflow import Workflow, step, Succeed, Fail
from kflow.service import ServiceDef, ServiceMode, new_service
from kflow.runner import run_local


# ---------------------------------------------------------------------------
# Sentinel constants
# ---------------------------------------------------------------------------

def test_sentinels_match_go_values():
    assert Succeed == "__succeed__"
    assert Fail    == "__fail__"


# ---------------------------------------------------------------------------
# Decorator registration
# ---------------------------------------------------------------------------

def test_task_decorator_registers_handler():
    app = Workflow("test")

    @app.task("ValidateOrder")
    def validate_order(inp):
        return {"valid": True}

    app.flow(step("ValidateOrder").end())
    app.validate()  # must not raise


def test_choice_decorator_registers_choice():
    app = Workflow("test")

    @app.choice("RouteOrder")
    def route(inp):
        return Succeed

    @app.task("NextStep")
    def nxt(inp):
        return {}

    app.flow(step("RouteOrder").next("NextStep"), step("NextStep").end())
    app.validate()


def test_wait_registers_state():
    app = Workflow("test")
    app.wait("Pause", seconds=0)

    @app.task("After")
    def after(inp):
        return {}

    app.flow(step("Pause").next("After"), step("After").end())
    app.validate()


# ---------------------------------------------------------------------------
# Validation rules
# ---------------------------------------------------------------------------

def test_validate_raises_if_flow_not_called():
    app = Workflow("test")

    @app.task("A")
    def a(inp):
        return {}

    with pytest.raises(ValueError, match="flow()"):
        app.validate()


def test_validate_raises_on_duplicate_state_name():
    app = Workflow("test")

    @app.task("A")
    def a1(inp):
        return {}

    @app.task("A")  # duplicate
    def a2(inp):
        return {}

    app.flow(step("A").end())
    with pytest.raises(ValueError, match="duplicate"):
        app.validate()


def test_validate_raises_handler_and_invoke_service():
    app = Workflow("test")

    @app.task("A")
    def a(inp):
        return {}

    app._tasks["A"].invoke_service("my-svc")  # both set → invalid
    app.flow(step("A").end())
    with pytest.raises(ValueError, match="both"):
        app.validate()


def test_validate_raises_no_handler_no_service():
    app = Workflow("test")
    td = app.wait("A", seconds=0)  # wait state is valid
    # Create a plain task with no handler:
    from kflow.workflow import TaskDef
    bad = TaskDef("B")
    app._tasks["B"] = bad
    app._names.append("B")
    app.flow(step("A").next("B"), step("B").end())
    with pytest.raises(ValueError, match="must have"):
        app.validate()


def test_validate_raises_unknown_next_target():
    app = Workflow("test")

    @app.task("A")
    def a(inp):
        return {}

    app.flow(step("A").next("NonExistent"))
    with pytest.raises(ValueError, match="not a registered state"):
        app.validate()


def test_validate_raises_unknown_catch_target():
    app = Workflow("test")

    @app.task("A")
    def a(inp):
        return {}

    app.flow(step("A").catch("Ghost").end())
    with pytest.raises(ValueError, match="not a registered state"):
        app.validate()


# ---------------------------------------------------------------------------
# Service validation
# ---------------------------------------------------------------------------

def test_service_deployment_requires_min_scale():
    svc = new_service("my-svc").mode(ServiceMode.Deployment).scale(0, 5)
    with pytest.raises(ValueError, match="min_scale"):
        svc.validate()


def test_service_lambda_no_min_scale_constraint():
    svc = new_service("my-svc").mode(ServiceMode.Lambda).scale(0, 5)
    svc.validate()  # must not raise


def test_service_deployment_valid_with_min_scale():
    svc = new_service("my-svc").mode(ServiceMode.Deployment).scale(1, 5)
    svc.validate()  # must not raise


# ---------------------------------------------------------------------------
# run_local — basic multi-step workflow
# ---------------------------------------------------------------------------

def test_run_local_single_step():
    app = Workflow("test")

    @app.task("A")
    def a(inp):
        return {"result": inp["x"] * 2}

    app.flow(step("A").end())
    output = run_local(app, {"x": 21})
    assert output == {"result": 42}


def test_run_local_multi_step_chain():
    app = Workflow("test")

    @app.task("Double")
    def double(inp):
        return {"v": inp["v"] * 2}

    @app.task("AddOne")
    def add_one(inp):
        return {"v": inp["v"] + 1}

    app.flow(step("Double").next("AddOne"), step("AddOne").end())
    output = run_local(app, {"v": 5})
    assert output == {"v": 11}  # 5*2=10, 10+1=11


def test_run_local_retry_on_transient_error():
    call_count = [0]
    app = Workflow("test")

    @app.task("Flaky")
    def flaky(inp):
        call_count[0] += 1
        if call_count[0] < 3:
            raise RuntimeError("transient")
        return {"ok": True}

    app.flow(step("Flaky").retry(3).end())
    output = run_local(app, {})
    assert output["ok"] is True
    assert call_count[0] == 3


def test_run_local_catch_routing():
    app = Workflow("test")

    @app.task("Risky")
    def risky(inp):
        raise RuntimeError("boom")

    @app.task("ErrorHandler")
    def handler(inp):
        return {"handled": inp["_error"]}

    app.flow(
        step("Risky").catch("ErrorHandler").end(),
        step("ErrorHandler").end(),
    )
    output = run_local(app, {})
    assert "boom" in output["handled"]


def test_run_local_raises_without_catch():
    app = Workflow("test")

    @app.task("Boom")
    def boom(inp):
        raise RuntimeError("uncaught")

    app.flow(step("Boom").end())
    with pytest.raises(RuntimeError, match="uncaught"):
        run_local(app, {})


def test_run_local_choice_routing():
    app = Workflow("test")

    @app.choice("Route")
    def route(inp):
        return "High" if inp["amount"] > 1000 else "Low"

    @app.task("High")
    def high(inp):
        return {"path": "high"}

    @app.task("Low")
    def low(inp):
        return {"path": "low"}

    app.flow(
        step("Route").next("High"),
        step("Route").next("Low"),   # duplicate step names are handled by first-wins in graph
        step("High").end(),
        step("Low").end(),
    )
    # Rebuild properly — each state appears once in flow
    app._steps = []
    app.flow(
        step("Route").next("High"),
        step("High").end(),
        step("Low").end(),
    )
    output = run_local(app, {"amount": 2000})
    assert output["path"] == "high"


def test_run_local_choice_low_path():
    app = Workflow("test")

    @app.choice("Route")
    def route(inp):
        return "Low" if inp["amount"] <= 1000 else "High"

    @app.task("High")
    def high(inp):
        return {"path": "high"}

    @app.task("Low")
    def low(inp):
        return {"path": "low"}

    app.flow(
        step("Route").next("High"),
        step("High").end(),
        step("Low").end(),
    )
    output = run_local(app, {"amount": 500})
    assert output["path"] == "low"


# ---------------------------------------------------------------------------
# Async handler support
# ---------------------------------------------------------------------------

def test_run_local_async_handler():
    app = Workflow("test")

    @app.task("Async")
    async def async_handler(inp):
        await asyncio.sleep(0)
        return {"async": True}

    app.flow(step("Async").end())
    output = run_local(app, {})
    assert output["async"] is True


# ---------------------------------------------------------------------------
# with_image
# ---------------------------------------------------------------------------

def test_with_image_sets_image():
    wf = Workflow("test")
    wf.with_image("my-image:latest")
    assert wf._image == "my-image:latest"


def test_with_image_default_empty():
    wf = Workflow("test")
    assert wf._image == ""


def test_with_image_included_in_serialised_graph():
    from kflow.runner import _serialise_graph

    wf = Workflow("test")

    @wf.task("A")
    def handler(inp):
        return inp

    wf.flow(step("A").end())
    wf.with_image("kflow-example:dev")

    graph = _serialise_graph(wf)
    assert graph["image"] == "kflow-example:dev"


def test_no_image_not_in_serialised_graph():
    from kflow.runner import _serialise_graph

    wf = Workflow("test")

    @wf.task("A")
    def handler(inp):
        return inp

    wf.flow(step("A").end())

    graph = _serialise_graph(wf)
    assert "image" not in graph
