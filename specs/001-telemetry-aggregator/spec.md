# SpanBarn — Self-hosted Telemetry Aggregator

## Vision

A lightweight, self-hosted telemetry aggregator that accepts distributed traces (OTLP-compatible), stores full-fidelity spans for a short window, then intelligently aggregates into long-term performance metrics. Error and slow spans are preserved in detail. Setup is trivial, performance is rock-solid, and the instrumented application is never blocked.

## User Stories

### US-1: Ingest spans without blocking

**As** a developer instrumenting my application,
**I want** span submission to be fire-and-forget with sub-millisecond overhead,
**So that** tracing never degrades my application's performance.

**Acceptance criteria:**
- SDK `span.end()` returns immediately (async batch flush)
- Server ingest endpoint responds in <5ms at p99 under load
- Backpressure: if the server is down, the SDK drops spans silently (no retries by default, configurable)
- In-memory queue (32k capacity) → NDJSON spool → background worker

### US-2: OTLP-compatible ingest

**As** a developer using OpenTelemetry SDKs,
**I want** to export traces to SpanBarn via standard OTLP/HTTP,
**So that** I can use existing OTel instrumentation without custom SDKs.

**Acceptance criteria:**
- `POST /v1/traces` accepts OTLP/HTTP protobuf and JSON
- Standard `x-spanbarn-api-key` header for auth (also supports OTel `Authorization: Bearer`)
- Maps OTLP resource/scope/span attributes to SpanBarn's data model
- Supports batch span export

### US-3: Native lightweight ingest

**As** a developer wanting minimal overhead,
**I want** a simpler JSON ingest endpoint,
**So that** I can send spans without the full OTLP protobuf dependency.

**Acceptance criteria:**
- `POST /api/v1/spans` accepts JSON array of spans
- Minimal required fields: traceId, spanId, name, service, startTime, duration
- Optional: parentSpanId, status, kind, attributes, events, resource

### US-4: Smart span retention

**As** an operator,
**I want** full spans kept for recent debugging and aggregated metrics kept long-term,
**So that** storage doesn't grow unbounded while I retain useful performance data.

**Acceptance criteria:**
- Full spans retained for configurable window (default: 4 hours)
- After retention window, spans are aggregated into per-minute buckets per (service, operation, resource)
- Aggregates include: count, error_count, p50, p95, p99, max, sum (for mean)
- Error spans (status=error) are sampled and kept for `RETENTION_ERROR_DAYS`
- Slow spans (duration > threshold) are sampled and kept for `RETENTION_ERROR_DAYS`
- Uneventful spans are dropped after aggregation
- Retention worker runs periodically (every 5 minutes)

### US-5: Performance dashboard

**As** an operator,
**I want** a web dashboard showing latency, throughput, and error rates,
**So that** I can monitor application performance at a glance.

**Acceptance criteria:**
- Service map: list of services with key metrics
- Per-service view: operations/routes with p50/p95/p99 latency, throughput, error rate
- Per-operation drill-down: latency distribution, recent traces, dependency calls
- Time range selector (last 1h, 4h, 24h, 7d, 30d)
- Auto-refresh (configurable interval)
- Recent traces table with filtering (service, operation, status, min duration)
- Trace waterfall view showing span hierarchy and timing

### US-6: Dependency tracking

**As** a developer,
**I want** to see performance of external dependencies (DB, S3, HTTP, gRPC),
**So that** I can identify which dependency is causing slowdowns.

**Acceptance criteria:**
- Client spans (kind=client) are grouped by target system
- Shows: call count, latency percentiles, error rate per dependency
- Recognizes common attributes: `db.system`, `http.url`, `rpc.service`, `aws.service`
- Dashboard section showing top slow dependencies

### US-7: Alerting on performance regression

**As** an operator,
**I want** to be notified when a route's latency or error rate degrades significantly,
**So that** I can investigate before users are impacted.

**Acceptance criteria:**
- Configurable alerts per (service, operation): latency threshold, error rate threshold
- Comparison: current bucket vs rolling average of previous N buckets
- Notification channels: webhook (generic), email (SMTP)
- Alert cooldown to prevent spam

### US-8: Multi-language SDKs

**As** a developer working in Go, Node.js, or Python,
**I want** a lightweight SDK in my language,
**So that** I can instrument my application with minimal effort.

**Acceptance criteria:**
- SDKs for: JavaScript/TypeScript (browser + Node.js), Go, Python
- Each SDK supports: manual spans, context propagation, automatic HTTP instrumentation
- Node.js SDK: auto-instrumentation for http, fetch, pg, mysql, redis
- Go SDK: context-based span propagation, HTTP middleware
- Python SDK: context manager spans, WSGI/ASGI middleware
- All SDKs: async batch flush, configurable flush interval, graceful shutdown
- All SDKs: W3C Trace Context propagation (traceparent header)

### US-9: Trivial setup

**As** an operator,
**I want** setup to be one Docker command or one binary,
**So that** I can be collecting traces in under 5 minutes.

**Acceptance criteria:**
- Single binary with embedded SQLite (no external DB required)
- `docker compose up` for full stack
- First-run setup wizard in web UI (create admin user, first project, first API key)
- CLI commands for headless setup: `spanbarn user create`, `spanbarn project create`, `spanbarn apikey create`
- Litestream integration for optional S3 backup of SQLite

### US-10: Secure by default

**As** an operator,
**I want** the dashboard and API protected with authentication,
**So that** trace data is not exposed to unauthorized users.

**Acceptance criteria:**
- Dashboard protected by username/password login (bcrypt)
- HMAC-signed session tokens with configurable TTL
- Ingest API protected by API key (SHA256 hashed, timing-safe comparison)
- Per-project API keys with scope (ingest-only vs full)
- Rate limiting on login, ingest, and API endpoints
- Security headers (X-Frame-Options, CSP, HSTS)
- Ingest endpoint has wildcard CORS (needed for browser SDKs)

## Non-functional Requirements

### Performance
- Ingest: sustain 10,000 spans/second on a single core
- Dashboard queries: <100ms for aggregated metrics, <500ms for recent span search
- Memory: <100MB baseline, <500MB under sustained load
- Storage: ~100 bytes per aggregated bucket, ~1KB per retained span

### Reliability
- Spool-based ingest survives process restarts (no span loss)
- Graceful shutdown: flush in-memory queue before exit
- Dead-letter handling: malformed spans logged and skipped, never block pipeline

### Compatibility
- OTLP/HTTP v1 (protobuf + JSON)
- W3C Trace Context (traceparent/tracestate propagation)
- OpenTelemetry semantic conventions for common attributes
