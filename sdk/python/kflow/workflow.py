"""Workflow, TaskDef, StepBuilder, and sentinel constants."""
from __future__ import annotations

import inspect
from typing import Callable, Optional

# Sentinel constants — must match Go/Rust SDK values exactly.
Succeed = "__succeed__"
Fail    = "__fail__"

# Input/Output are plain dicts; all values must be JSON-serialisable.
Input  = dict
Output = dict


class _RetryPolicy:
    def __init__(self, max_attempts: int, backoff_seconds: int = 0) -> None:
        self.max_attempts    = max_attempts
        self.backoff_seconds = backoff_seconds


class TaskDef:
    def __init__(self, name: str) -> None:
        self._name           = name
        self._handler        = None   # sync or async callable
        self._choice_fn      = None
        self._is_choice      = False
        self._is_wait        = False
        self._wait_seconds   = 0
        self._service_target: Optional[str] = None
        self._retry: Optional[_RetryPolicy]  = None
        self._catch: Optional[str]           = None

    def invoke_service(self, service_name: str) -> "TaskDef":
        self._service_target = service_name
        return self

    def retry(self, max_attempts: int, backoff_seconds: int = 0) -> "TaskDef":
        self._retry = _RetryPolicy(max_attempts, backoff_seconds)
        return self

    def catch(self, state_name: str) -> "TaskDef":
        self._catch = state_name
        return self


class StepBuilder:
    def __init__(self, name: str) -> None:
        self.name              = name
        self._next: Optional[str]          = None
        self._catch: Optional[str]         = None
        self._retry: Optional[_RetryPolicy] = None
        self._is_end           = False

    def next(self, state_name: str) -> "StepBuilder":
        self._next = state_name
        return self

    def catch(self, state_name: str) -> "StepBuilder":
        self._catch = state_name
        return self

    def retry(self, max_attempts: int, backoff_seconds: int = 0) -> "StepBuilder":
        self._retry = _RetryPolicy(max_attempts, backoff_seconds)
        return self

    def end(self) -> "StepBuilder":
        self._next   = Succeed
        self._is_end = True
        return self


def step(name: str) -> StepBuilder:
    """Convenience constructor for StepBuilder."""
    return StepBuilder(name)


class Workflow:
    def __init__(self, name: str) -> None:
        self._name  = name
        self._image = ""
        self._tasks: dict[str, TaskDef] = {}
        self._names: list[str] = []   # insertion-order duplicate tracking
        self._steps: list[StepBuilder] = []

    @property
    def name(self) -> str:
        return self._name

    def with_image(self, image: str) -> "Workflow":
        """Set the container image for K8s Job execution. Empty = in-process."""
        self._image = image
        return self

    def task(self, name: str) -> Callable:
        """Decorator — registers a function as a Task state."""
        def decorator(fn: Callable) -> Callable:
            td = TaskDef(name)
            if inspect.iscoroutinefunction(fn):
                td._handler = fn
            else:
                td._handler = fn
            self._tasks[name] = td
            self._names.append(name)
            return fn
        return decorator

    def choice(self, name: str) -> Callable:
        """Decorator — registers a function as a Choice state."""
        def decorator(fn: Callable) -> Callable:
            td = TaskDef(name)
            td._is_choice = True
            td._choice_fn = fn
            self._tasks[name] = td
            self._names.append(name)
            return fn
        return decorator

    def wait(self, name: str, seconds: int) -> TaskDef:
        td = TaskDef(name)
        td._is_wait      = True
        td._wait_seconds = seconds
        self._tasks[name] = td
        self._names.append(name)
        return td

    def flow(self, *steps: StepBuilder) -> None:
        self._steps = list(steps)

    def validate(self) -> None:
        """Raises ValueError if any invariant is violated."""
        # 1. flow() must have been called with at least one step.
        if not self._steps:
            raise ValueError("flow() has not been called or contains no steps")

        # 2. All state names must be globally unique.
        seen: set[str] = set()
        for n in self._names:
            if n in seen:
                raise ValueError(f"duplicate state name: {n!r}")
            seen.add(n)

        # 3. Each task has exactly one of: handler or invoke_service.
        for name, td in self._tasks.items():
            if td._is_choice or td._is_wait:
                continue
            has_handler = td._handler is not None
            has_service = td._service_target is not None
            if has_handler and has_service:
                raise ValueError(
                    f"state {name!r}: cannot have both a handler and invoke_service"
                )
            if not has_handler and not has_service:
                raise ValueError(
                    f"state {name!r}: must have either a handler or invoke_service"
                )

        # 4. Every next/catch target must resolve to a registered state, Succeed, or Fail.
        valid_targets = set(self._tasks.keys()) | {Succeed, Fail}
        for sb in self._steps:
            if sb._next and sb._next not in valid_targets:
                raise ValueError(
                    f"step {sb.name!r}: next target {sb._next!r} is not a registered state"
                )
            if sb._catch and sb._catch not in valid_targets:
                raise ValueError(
                    f"step {sb.name!r}: catch target {sb._catch!r} is not a registered state"
                )
            # task-level catch
            td = self._tasks.get(sb.name)
            if td and td._catch and td._catch not in valid_targets:
                raise ValueError(
                    f"state {sb.name!r}: catch target {td._catch!r} is not a registered state"
                )
