"""ServiceDef and ServiceMode — mirror of Go pkg/kflow/service.go."""
from __future__ import annotations

from enum import IntEnum
from typing import Callable, Optional


class ServiceMode(IntEnum):
    Deployment = 0
    Lambda     = 1


class ServiceDef:
    def __init__(self, name: str) -> None:
        self._name         = name
        self._handler: Optional[Callable] = None
        self._mode         = ServiceMode.Deployment
        self._port         = 8080
        self._min_scale    = 0
        self._max_scale    = 0
        self._ingress_host: Optional[str] = None
        self._timeout      = 30.0  # seconds

    def handler(self, fn: Callable) -> "ServiceDef":
        self._handler = fn
        return self

    def mode(self, mode: ServiceMode) -> "ServiceDef":
        self._mode = mode
        return self

    def port(self, port: int) -> "ServiceDef":
        self._port = port
        return self

    def scale(self, min_: int, max_: int) -> "ServiceDef":
        self._min_scale = min_
        self._max_scale = max_
        return self

    def expose(self, host: str) -> "ServiceDef":
        self._ingress_host = host
        return self

    def timeout(self, seconds: float) -> "ServiceDef":
        self._timeout = seconds
        return self

    def validate(self) -> None:
        """Raises ValueError if any invariant is violated."""
        if self._mode == ServiceMode.Deployment and self._min_scale < 1:
            raise ValueError(
                f"service {self._name!r}: Deployment mode requires min_scale >= 1"
            )


def new_service(name: str) -> ServiceDef:
    """Module-level convenience constructor."""
    return ServiceDef(name)
