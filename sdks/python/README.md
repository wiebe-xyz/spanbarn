# SpanBarn Python SDK

Lightweight telemetry SDK for [SpanBarn](https://spanbarn.com), a self-hosted telemetry aggregator. Zero external dependencies.

## Installation

```bash
pip install spanbarn
```

## Quick Start

```python
from spanbarn import SpanBarn

sb = SpanBarn.init(
    endpoint="https://spanbarn.example.com",
    api_key="your-api-key",
    service="my-service",
    environment="production",
)

# Create a span using a context manager
with sb.span("handle-request", kind="server") as s:
    s.set_attribute("http.method", "GET")
    s.set_attribute("http.route", "/api/users")
    # ... do work ...
    s.ok()

# Manual span management
span = sb.start_span("db-query", kind="client")
span.set_attribute("db.system", "postgresql")
span.set_attribute("db.statement", "SELECT * FROM users")
span.ok()
span.end()

# Flush and shut down
sb.shutdown()
```

## W3C Trace Context

```python
from spanbarn import make_traceparent, parse_traceparent

# Create a traceparent header
header = make_traceparent(trace_id, span_id)
# "00-<trace_id>-<span_id>-01"

# Parse an incoming traceparent header
result = parse_traceparent(header)
if result:
    trace_id, span_id = result
```

## WSGI Middleware

```python
from spanbarn import SpanBarn, SpanBarnWSGIMiddleware

sb = SpanBarn.init(endpoint="...", api_key="...", service="my-app")

app = SpanBarnWSGIMiddleware(your_wsgi_app, client=sb)
```

The middleware automatically creates server spans for each request with `http.method`, `http.route`, and `http.status_code` attributes, and propagates W3C `traceparent` headers.

## Configuration

| Parameter | Default | Description |
|---|---|---|
| `endpoint` | required | SpanBarn server URL |
| `api_key` | required | API key for authentication |
| `service` | required | Service name |
| `environment` | `""` | Deployment environment |
| `flush_interval` | `5.0` | Seconds between flushes |
| `max_batch_size` | `100` | Max spans per batch |
| `max_queue_size` | `1000` | Max queued spans before dropping |
| `debug` | `False` | Print debug info to stderr |
| `disabled` | `False` | Disable sending (spans are no-ops) |
| `before_send` | `None` | Callback to modify/filter spans |
