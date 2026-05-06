"""Tests for the SpanBarn Python SDK."""

from __future__ import annotations

import time

import pytest

from spanbarn import (
    SpanBarn,
    SpanBarnConfig,
    SpanBarnWSGIMiddleware,
    Span,
    SpanData,
    generate_trace_id,
    generate_span_id,
    make_traceparent,
    parse_traceparent,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _make_client(**kwargs) -> SpanBarn:
    """Create a disabled client for unit tests (no background thread)."""
    defaults = {
        "endpoint": "http://localhost:9999",
        "api_key": "test-key",
        "service": "test-svc",
        "disabled": True,
    }
    defaults.update(kwargs)
    return SpanBarn(**defaults)


# ---------------------------------------------------------------------------
# ID Generation
# ---------------------------------------------------------------------------

class TestIDGeneration:
    def test_trace_id_length(self):
        tid = generate_trace_id()
        assert len(tid) == 32
        int(tid, 16)  # must be valid hex

    def test_span_id_length(self):
        sid = generate_span_id()
        assert len(sid) == 16
        int(sid, 16)

    def test_uniqueness(self):
        trace_ids = {generate_trace_id() for _ in range(100)}
        assert len(trace_ids) == 100

        span_ids = {generate_span_id() for _ in range(100)}
        assert len(span_ids) == 100


# ---------------------------------------------------------------------------
# Traceparent
# ---------------------------------------------------------------------------

class TestTraceparent:
    def test_make_traceparent(self):
        tid = "a" * 32
        sid = "b" * 16
        tp = make_traceparent(tid, sid)
        assert tp == f"00-{tid}-{sid}-01"

    def test_parse_traceparent_valid(self):
        tid = "a" * 32
        sid = "b" * 16
        result = parse_traceparent(f"00-{tid}-{sid}-01")
        assert result == (tid, sid)

    def test_parse_traceparent_invalid_empty(self):
        assert parse_traceparent("") is None

    def test_parse_traceparent_invalid_parts(self):
        assert parse_traceparent("00-abc-def") is None

    def test_parse_traceparent_invalid_lengths(self):
        assert parse_traceparent("00-short-short-01") is None

    def test_parse_traceparent_invalid_hex(self):
        # valid lengths but non-hex
        tid = "g" * 32
        sid = "h" * 16
        assert parse_traceparent(f"00-{tid}-{sid}-01") is None


# ---------------------------------------------------------------------------
# SpanData
# ---------------------------------------------------------------------------

class TestSpanData:
    def test_to_dict(self):
        sd = SpanData(
            trace_id="t1",
            span_id="s1",
            parent_span_id="p1",
            name="test-span",
            service="svc",
            resource="/api",
            kind="server",
            status="ok",
            start_time=1000,
            duration=500,
            attributes={"key": "val"},
            events=[{"name": "ev1", "timestamp": 1100, "attributes": {}}],
            environment="prod",
        )
        d = sd.to_dict()
        assert d["trace_id"] == "t1"
        assert d["span_id"] == "s1"
        assert d["parent_span_id"] == "p1"
        assert d["name"] == "test-span"
        assert d["service"] == "svc"
        assert d["resource"] == "/api"
        assert d["kind"] == "server"
        assert d["status"] == "ok"
        assert d["start_time"] == 1000
        assert d["duration"] == 500
        assert d["attributes"] == {"key": "val"}
        assert len(d["events"]) == 1
        assert d["environment"] == "prod"

    def test_defaults(self):
        sd = SpanData()
        d = sd.to_dict()
        assert d["status"] == "unset"
        assert d["kind"] == "internal"
        assert d["attributes"] == {}
        assert d["events"] == []


# ---------------------------------------------------------------------------
# Span
# ---------------------------------------------------------------------------

class TestSpan:
    def test_set_attribute(self):
        client = _make_client()
        span = Span(client, "test")
        span.set_attribute("k", "v")
        assert span.data.attributes["k"] == "v"

    def test_set_attributes(self):
        client = _make_client()
        span = Span(client, "test")
        span.set_attributes({"a": 1, "b": 2})
        assert span.data.attributes == {"a": 1, "b": 2}

    def test_set_status(self):
        client = _make_client()
        span = Span(client, "test")
        span.set_status("ok")
        assert span.data.status == "ok"

    def test_add_event(self):
        client = _make_client()
        span = Span(client, "test")
        span.add_event("ev1", {"x": 1})
        assert len(span.data.events) == 1
        assert span.data.events[0]["name"] == "ev1"
        assert span.data.events[0]["attributes"] == {"x": 1}
        assert "timestamp" in span.data.events[0]

    def test_error_with_exception(self):
        client = _make_client()
        span = Span(client, "test")
        span.error(ValueError("boom"))
        assert span.data.status == "error"
        assert span.data.attributes["error.type"] == "ValueError"
        assert span.data.attributes["error.message"] == "boom"

    def test_error_without_exception(self):
        client = _make_client()
        span = Span(client, "test")
        span.error()
        assert span.data.status == "error"
        assert "error.type" not in span.data.attributes

    def test_end_calculates_duration(self):
        client = _make_client()
        span = Span(client, "test")
        # Manually set start_time so we can verify duration
        span._data.start_time = int(time.time() * 1_000_000) - 1000
        span.end()
        assert span.data.duration >= 1000

    def test_end_twice_noop(self):
        client = _make_client()
        span = Span(client, "test")
        span.end()
        first_duration = span.data.duration
        span.end()
        assert span.data.duration == first_duration

    def test_chaining(self):
        client = _make_client()
        span = Span(client, "test")
        result = span.set_attribute("a", 1).set_status("ok").add_event("ev")
        assert result is span
        assert span.data.attributes["a"] == 1
        assert span.data.status == "ok"

    def test_context_manager(self):
        client = _make_client()
        span = client.span("test")
        with span as s:
            s.set_attribute("inside", True)
        assert s.data.attributes["inside"] is True
        assert s.data.status == "ok"
        assert s.data.duration > 0

    def test_context_manager_error(self):
        client = _make_client()
        span = client.span("test")
        with pytest.raises(RuntimeError):
            with span as s:
                raise RuntimeError("fail")
        assert s.data.status == "error"
        assert s.data.attributes["error.type"] == "RuntimeError"
        assert s.data.attributes["error.message"] == "fail"

    def test_mutations_after_end_ignored(self):
        client = _make_client()
        span = Span(client, "test")
        span.end()
        span.set_attribute("late", True)
        assert "late" not in span.data.attributes

    def test_ok_helper(self):
        client = _make_client()
        span = Span(client, "test")
        result = span.ok()
        assert result is span
        assert span.data.status == "ok"

    def test_span_ids_populated(self):
        client = _make_client()
        span = Span(client, "test")
        assert len(span.trace_id) == 32
        assert len(span.span_id) == 16


# ---------------------------------------------------------------------------
# Client
# ---------------------------------------------------------------------------

class TestClient:
    def test_init(self):
        client = _make_client()
        assert client._config.endpoint == "http://localhost:9999"
        assert client._config.api_key == "test-key"
        assert client._config.service == "test-svc"

    def test_init_with_config(self):
        config = SpanBarnConfig(
            endpoint="http://example.com",
            api_key="k",
            service="s",
        )
        client = SpanBarn(config)
        assert client._config.endpoint == "http://example.com"

    def test_singleton(self):
        # Reset singleton
        SpanBarn._instance = None
        sb = SpanBarn.init(
            endpoint="http://localhost:9999",
            api_key="test-key",
            service="test-svc",
            disabled=True,
        )
        assert SpanBarn.get_instance() is sb
        # Clean up
        SpanBarn._instance = None

    def test_span_creation(self):
        client = _make_client()
        span = client.span("my-span", kind="server")
        assert span.data.name == "my-span"
        assert span.data.kind == "server"
        assert span.data.service == "test-svc"

    def test_start_span(self):
        client = _make_client()
        span = client.start_span("my-span")
        assert span.data.name == "my-span"

    def test_enqueue(self):
        client = _make_client()
        sd = SpanData(name="test")
        # Disabled client silently drops
        client.enqueue(sd)
        assert client._queue.empty()

    def test_enqueue_enabled(self):
        client = SpanBarn(
            endpoint="http://localhost:9999",
            api_key="test-key",
            service="test-svc",
            disabled=False,
        )
        try:
            sd = SpanData(name="test")
            client.enqueue(sd)
            assert not client._queue.empty()
        finally:
            client.shutdown()

    def test_disabled(self):
        client = _make_client(disabled=True)
        assert client._config.disabled is True
        assert client._worker is None

    def test_max_queue_size(self):
        client = SpanBarn(
            endpoint="http://localhost:9999",
            api_key="test-key",
            service="test-svc",
            disabled=False,
            max_queue_size=2,
        )
        try:
            # Fill the queue
            client.enqueue(SpanData(name="s1"))
            client.enqueue(SpanData(name="s2"))
            # This should be dropped silently (queue full)
            client.enqueue(SpanData(name="s3"))
            assert client._queue.qsize() <= 2
        finally:
            client.shutdown()

    def test_before_send(self):
        def add_tag(sd: SpanData) -> SpanData:
            sd.attributes["extra"] = "injected"
            return sd

        client = SpanBarn(
            endpoint="http://localhost:9999",
            api_key="test-key",
            service="test-svc",
            disabled=False,
            before_send=add_tag,
        )
        try:
            sd = SpanData(name="test")
            client.enqueue(sd)
            item = client._queue.get_nowait()
            assert item.attributes["extra"] == "injected"
        finally:
            client.shutdown()

    def test_before_send_none(self):
        """Returning None from before_send filters the span out."""
        def drop(_sd: SpanData) -> None:
            return None

        client = SpanBarn(
            endpoint="http://localhost:9999",
            api_key="test-key",
            service="test-svc",
            disabled=False,
            before_send=drop,
        )
        try:
            sd = SpanData(name="test")
            client.enqueue(sd)
            assert client._queue.empty()
        finally:
            client.shutdown()

    def test_flush(self):
        client = _make_client()
        # flush on empty queue should not hang
        client.flush()

    def test_shutdown(self):
        client = SpanBarn(
            endpoint="http://localhost:9999",
            api_key="test-key",
            service="test-svc",
            disabled=False,
        )
        client.shutdown()
        assert client._shutdown_event.is_set()

    def test_endpoint_trailing_slash_stripped(self):
        client = _make_client(endpoint="http://localhost:9999/")
        assert client._config.endpoint == "http://localhost:9999"

    def test_parent_propagation(self):
        """Child spans inherit trace_id and set parent_span_id."""
        client = _make_client()
        parent = client.span("parent")
        with parent as p:
            child = client.span("child")
            assert child.data.trace_id == p.trace_id
            assert child.data.parent_span_id == p.span_id
            child.end()


# ---------------------------------------------------------------------------
# WSGI Middleware
# ---------------------------------------------------------------------------

class TestWSGIMiddleware:
    def _make_environ(self, method="GET", path="/test", traceparent=""):
        environ = {
            "REQUEST_METHOD": method,
            "PATH_INFO": path,
        }
        if traceparent:
            environ["HTTP_TRACEPARENT"] = traceparent
        return environ

    def test_basic_request(self):
        spans_collected: list[SpanData] = []
        client = _make_client()
        client.enqueue = lambda sd: spans_collected.append(sd)

        def app(environ, start_response):
            start_response("200 OK", [])
            return [b"ok"]

        middleware = SpanBarnWSGIMiddleware(app, client=client)
        environ = self._make_environ()

        collected_status = []
        def mock_start_response(status, headers, exc_info=None):
            collected_status.append(status)

        result = middleware(environ, mock_start_response)
        assert result == [b"ok"]
        assert len(spans_collected) == 1
        sd = spans_collected[0]
        assert sd.kind == "server"
        assert sd.attributes["http.method"] == "GET"
        assert sd.attributes["http.route"] == "/test"
        assert sd.attributes["http.status_code"] == 200
        assert sd.status == "ok"

    def test_error_status(self):
        spans_collected: list[SpanData] = []
        client = _make_client()
        client.enqueue = lambda sd: spans_collected.append(sd)

        def app(environ, start_response):
            start_response("500 Internal Server Error", [])
            return [b"error"]

        middleware = SpanBarnWSGIMiddleware(app, client=client)
        environ = self._make_environ()

        middleware(environ, lambda s, h, e=None: None)
        assert spans_collected[0].status == "error"
        assert spans_collected[0].attributes["http.status_code"] == 500

    def test_traceparent_propagation(self):
        spans_collected: list[SpanData] = []
        client = _make_client()
        client.enqueue = lambda sd: spans_collected.append(sd)

        def app(environ, start_response):
            start_response("200 OK", [])
            return [b"ok"]

        tid = "a" * 32
        sid = "b" * 16
        tp = make_traceparent(tid, sid)
        middleware = SpanBarnWSGIMiddleware(app, client=client)
        environ = self._make_environ(traceparent=tp)

        middleware(environ, lambda s, h, e=None: None)
        sd = spans_collected[0]
        assert sd.trace_id == tid
        assert sd.parent_span_id == sid

    def test_no_client(self):
        """Without a client, middleware passes through."""
        SpanBarn._instance = None

        def app(environ, start_response):
            start_response("200 OK", [])
            return [b"ok"]

        middleware = SpanBarnWSGIMiddleware(app, client=None)
        environ = self._make_environ()

        result = middleware(environ, lambda s, h, e=None: None)
        assert result == [b"ok"]
        SpanBarn._instance = None
