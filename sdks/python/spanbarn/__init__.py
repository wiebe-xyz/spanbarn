"""SpanBarn telemetry SDK for Python."""

from __future__ import annotations

import json
import secrets
import sys
import threading
import time
import urllib.error
import urllib.request
from contextvars import ContextVar
from queue import Empty, Queue
from typing import Any, Callable, Dict, List, Optional, Tuple

_current_span: ContextVar[Optional[Span]] = ContextVar("_current_span", default=None)


# ---------------------------------------------------------------------------
# ID generation
# ---------------------------------------------------------------------------

def generate_trace_id() -> str:
    """Generate a 32 hex-char trace ID."""
    return secrets.token_hex(16)


def generate_span_id() -> str:
    """Generate a 16 hex-char span ID."""
    return secrets.token_hex(8)


# ---------------------------------------------------------------------------
# W3C Trace Context helpers
# ---------------------------------------------------------------------------

def make_traceparent(trace_id: str, span_id: str) -> str:
    """Build a W3C traceparent header value."""
    return f"00-{trace_id}-{span_id}-01"


def parse_traceparent(header: str) -> Optional[Tuple[str, str]]:
    """Parse a W3C traceparent header. Returns (trace_id, span_id) or None."""
    if not header:
        return None
    parts = header.strip().split("-")
    if len(parts) != 4:
        return None
    _version, trace_id, span_id, _flags = parts
    if len(trace_id) != 32 or len(span_id) != 16:
        return None
    try:
        int(trace_id, 16)
        int(span_id, 16)
    except ValueError:
        return None
    return (trace_id, span_id)


# ---------------------------------------------------------------------------
# SpanBarnConfig
# ---------------------------------------------------------------------------

class SpanBarnConfig:
    """Configuration for a SpanBarn client."""

    def __init__(
        self,
        endpoint: str,
        api_key: str,
        service: str,
        environment: str = "",
        flush_interval: float = 5.0,
        max_batch_size: int = 100,
        max_queue_size: int = 1000,
        debug: bool = False,
        disabled: bool = False,
        before_send: Optional[Callable[["SpanData"], Optional["SpanData"]]] = None,
    ) -> None:
        self.endpoint = endpoint.rstrip("/")
        self.api_key = api_key
        self.service = service
        self.environment = environment
        self.flush_interval = flush_interval
        self.max_batch_size = max_batch_size
        self.max_queue_size = max_queue_size
        self.debug = debug
        self.disabled = disabled
        self.before_send = before_send


# ---------------------------------------------------------------------------
# SpanData
# ---------------------------------------------------------------------------

class SpanData:
    """Serializable span data."""

    __slots__ = (
        "trace_id",
        "span_id",
        "parent_span_id",
        "name",
        "service",
        "resource",
        "kind",
        "status",
        "start_time",
        "duration",
        "attributes",
        "events",
        "environment",
    )

    def __init__(
        self,
        trace_id: str = "",
        span_id: str = "",
        parent_span_id: str = "",
        name: str = "",
        service: str = "",
        resource: str = "",
        kind: str = "internal",
        status: str = "unset",
        start_time: int = 0,
        duration: int = 0,
        attributes: Optional[Dict[str, Any]] = None,
        events: Optional[List[Dict[str, Any]]] = None,
        environment: str = "",
    ) -> None:
        self.trace_id = trace_id
        self.span_id = span_id
        self.parent_span_id = parent_span_id
        self.name = name
        self.service = service
        self.resource = resource
        self.kind = kind
        self.status = status
        self.start_time = start_time
        self.duration = duration
        self.attributes = attributes if attributes is not None else {}
        self.events = events if events is not None else []
        self.environment = environment

    def to_dict(self) -> Dict[str, Any]:
        """Serialize to a plain dictionary."""
        return {
            "trace_id": self.trace_id,
            "span_id": self.span_id,
            "parent_span_id": self.parent_span_id,
            "name": self.name,
            "service": self.service,
            "resource": self.resource,
            "kind": self.kind,
            "status": self.status,
            "start_time": self.start_time,
            "duration": self.duration,
            "attributes": self.attributes,
            "events": self.events,
            "environment": self.environment,
        }


# ---------------------------------------------------------------------------
# Span
# ---------------------------------------------------------------------------

class Span:
    """Active span that can be modified and ended."""

    def __init__(
        self,
        client: "SpanBarn",
        name: str,
        kind: str = "internal",
        parent_span_id: str = "",
        trace_id: str = "",
        attributes: Optional[Dict[str, Any]] = None,
        resource: str = "",
    ) -> None:
        self._client = client
        self._ended = False
        self._token: Any = None

        config = client._config
        self._data = SpanData(
            trace_id=trace_id or generate_trace_id(),
            span_id=generate_span_id(),
            parent_span_id=parent_span_id,
            name=name,
            service=config.service,
            resource=resource,
            kind=kind,
            status="unset",
            start_time=_now_us(),
            attributes=dict(attributes) if attributes else {},
            environment=config.environment,
        )

    # -- Mutators (return self for chaining) --------------------------------

    def set_attribute(self, key: str, value: Any) -> "Span":
        if not self._ended:
            self._data.attributes[key] = value
        return self

    def set_attributes(self, attrs: Dict[str, Any]) -> "Span":
        if not self._ended:
            self._data.attributes.update(attrs)
        return self

    def set_status(self, status: str) -> "Span":
        if not self._ended:
            self._data.status = status
        return self

    def add_event(self, name: str, attributes: Optional[Dict[str, Any]] = None) -> "Span":
        if not self._ended:
            self._data.events.append({
                "name": name,
                "timestamp": _now_us(),
                "attributes": attributes or {},
            })
        return self

    def ok(self) -> "Span":
        return self.set_status("ok")

    def error(self, exc: Optional[Exception] = None) -> "Span":
        self.set_status("error")
        if exc is not None and not self._ended:
            self._data.attributes["error.type"] = type(exc).__name__
            self._data.attributes["error.message"] = str(exc)
        return self

    # -- Lifecycle ----------------------------------------------------------

    def end(self) -> None:
        if self._ended:
            return
        self._ended = True
        self._data.duration = _now_us() - self._data.start_time

        # Restore parent context
        if self._token is not None:
            _current_span.reset(self._token)
            self._token = None

        self._client.enqueue(self._data)

    # -- Context manager ----------------------------------------------------

    def __enter__(self) -> "Span":
        self._token = _current_span.set(self)
        return self

    def __exit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        if exc_val is not None and self._data.status == "unset":
            self.error(exc_val)
        if self._data.status == "unset":
            self.ok()
        self.end()

    # -- Read access --------------------------------------------------------

    @property
    def trace_id(self) -> str:
        return self._data.trace_id

    @property
    def span_id(self) -> str:
        return self._data.span_id

    @property
    def data(self) -> SpanData:
        return self._data


# ---------------------------------------------------------------------------
# SpanBarn client
# ---------------------------------------------------------------------------

class SpanBarn:
    """SpanBarn client. Thread-safe."""

    _instance: Optional["SpanBarn"] = None

    def __init__(
        self,
        config_or_endpoint: Optional[Any] = None,
        api_key: Optional[str] = None,
        service: Optional[str] = None,
        **kwargs: Any,
    ) -> None:
        if isinstance(config_or_endpoint, SpanBarnConfig):
            self._config = config_or_endpoint
        else:
            # Allow endpoint= as a kwarg (e.g. SpanBarn(endpoint="..."))
            endpoint = kwargs.pop("endpoint", None) or config_or_endpoint or ""
            api_key = kwargs.pop("api_key", None) or api_key or ""
            service = kwargs.pop("service", None) or service or ""
            self._config = SpanBarnConfig(
                endpoint=endpoint,
                api_key=api_key,
                service=service,
                **kwargs,
            )

        self._queue: Queue[SpanData] = Queue(maxsize=self._config.max_queue_size)
        self._shutdown_event = threading.Event()
        self._worker: Optional[threading.Thread] = None

        if not self._config.disabled:
            self._worker = threading.Thread(target=self._flush_loop, daemon=True)
            self._worker.start()

    # -- Class-level singleton helpers --------------------------------------

    @classmethod
    def init(cls, **kwargs: Any) -> "SpanBarn":
        """Create (or replace) the global singleton."""
        cls._instance = cls(**kwargs)
        return cls._instance

    @classmethod
    def get_instance(cls) -> Optional["SpanBarn"]:
        """Return the global singleton, or None."""
        return cls._instance

    # -- Public API ---------------------------------------------------------

    def span(self, name: str, kind: str = "internal", **kwargs: Any) -> Span:
        """Create a new Span (also usable as a context manager)."""
        parent = _current_span.get()
        parent_span_id = kwargs.pop("parent_span_id", "")
        trace_id = kwargs.pop("trace_id", "")
        if parent is not None and not parent_span_id:
            parent_span_id = parent.span_id
            trace_id = trace_id or parent.trace_id
        return Span(
            self,
            name,
            kind=kind,
            parent_span_id=parent_span_id,
            trace_id=trace_id,
            **kwargs,
        )

    def start_span(self, name: str, kind: str = "internal", **kwargs: Any) -> Span:
        """Create and return a new Span (caller must call .end())."""
        return self.span(name, kind=kind, **kwargs)

    def enqueue(self, span_data: SpanData) -> None:
        """Add finished span data to the send queue."""
        if self._config.disabled:
            return
        if self._config.before_send is not None:
            span_data = self._config.before_send(span_data)
            if span_data is None:
                return
        try:
            self._queue.put_nowait(span_data)
        except Exception:
            # Queue full — drop the span (best effort).
            if self._config.debug:
                print("[spanbarn] queue full, dropping span", file=sys.stderr)

    def flush(self) -> None:
        """Block until all currently queued spans are sent."""
        deadline = time.monotonic() + 10.0
        while not self._queue.empty() and time.monotonic() < deadline:
            time.sleep(0.05)

    def shutdown(self) -> None:
        """Flush remaining spans and stop the background worker."""
        self.flush()
        self._shutdown_event.set()
        if self._worker is not None:
            self._worker.join(timeout=5.0)

    # -- Internal -----------------------------------------------------------

    def _flush_loop(self) -> None:
        """Background daemon that drains the queue in batches."""
        while not self._shutdown_event.is_set():
            batch: List[SpanData] = []
            # Block until at least one item arrives or interval elapses.
            try:
                item = self._queue.get(timeout=self._config.flush_interval)
                batch.append(item)
                self._queue.task_done()
            except Empty:
                continue

            # Drain up to max_batch_size.
            while len(batch) < self._config.max_batch_size:
                try:
                    item = self._queue.get_nowait()
                    batch.append(item)
                    self._queue.task_done()
                except Empty:
                    break

            if batch:
                self._send_batch(batch)

        # Drain on shutdown.
        batch: List[SpanData] = []
        while True:
            try:
                item = self._queue.get_nowait()
                batch.append(item)
                self._queue.task_done()
            except Empty:
                break
        if batch:
            self._send_batch(batch)

    def _send_batch(self, batch: List[SpanData]) -> bool:
        """POST a batch of spans to the server. Returns True on success."""
        url = f"{self._config.endpoint}/api/v1/spans"
        payload = json.dumps([sd.to_dict() for sd in batch]).encode()
        headers = {
            "Content-Type": "application/json",
            "x-spanbarn-api-key": self._config.api_key,
        }
        req = urllib.request.Request(url, data=payload, headers=headers, method="POST")
        try:
            with urllib.request.urlopen(req, timeout=5.0) as resp:
                _ = resp.read()
            if self._config.debug:
                print(f"[spanbarn] sent {len(batch)} span(s)", file=sys.stderr)
            return True
        except Exception as exc:
            if self._config.debug:
                print(f"[spanbarn] send failed: {exc}", file=sys.stderr)
            return False


# ---------------------------------------------------------------------------
# WSGI Middleware
# ---------------------------------------------------------------------------

class SpanBarnWSGIMiddleware:
    """WSGI middleware for automatic request tracing."""

    def __init__(self, app: Any, client: Optional[SpanBarn] = None) -> None:
        self.app = app
        self.client = client or SpanBarn.get_instance()

    def __call__(self, environ: Dict[str, Any], start_response: Any) -> Any:
        if self.client is None:
            return self.app(environ, start_response)

        method = environ.get("REQUEST_METHOD", "")
        path = environ.get("PATH_INFO", "/")
        traceparent = environ.get("HTTP_TRACEPARENT", "")

        parent_span_id = ""
        trace_id = ""
        parsed = parse_traceparent(traceparent)
        if parsed:
            trace_id, parent_span_id = parsed

        span = self.client.span(
            f"{method} {path}",
            kind="server",
            parent_span_id=parent_span_id,
            trace_id=trace_id,
            attributes={
                "http.method": method,
                "http.route": path,
            },
        )

        status_code = 500  # default in case start_response is never called

        def _start_response(status: str, response_headers: Any, exc_info: Any = None) -> Any:
            nonlocal status_code
            try:
                status_code = int(status.split(" ", 1)[0])
            except (ValueError, IndexError):
                status_code = 500
            return start_response(status, response_headers, exc_info)

        try:
            with span:
                response = self.app(environ, _start_response)
                span.set_attribute("http.status_code", status_code)
                if status_code >= 400:
                    span.set_status("error")
                else:
                    span.set_status("ok")
                return response
        except Exception:
            span.set_attribute("http.status_code", status_code)
            raise


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _now_us() -> int:
    """Current time in microseconds since epoch."""
    return int(time.time() * 1_000_000)


__all__ = [
    "SpanBarn",
    "SpanBarnConfig",
    "SpanData",
    "Span",
    "SpanBarnWSGIMiddleware",
    "generate_trace_id",
    "generate_span_id",
    "make_traceparent",
    "parse_traceparent",
]
