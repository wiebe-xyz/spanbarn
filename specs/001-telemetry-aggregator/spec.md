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
- Full spans retained for configurable window (default: 72 hours)
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

---

## Extended User Stories

### US-11: Ingest-only mode

**As** an operator deploying at scale,
**I want** a separate ingest pod that accepts spans and forwards to a writer,
**So that** ingest capacity can be scaled independently and writes are serialised through one pod.

**Acceptance criteria:**
- `SPANBARN_MODE=ingest` starts the pod in ingest-only mode (no DB writes locally)
- `SPANBARN_WRITER_URL` points to the writer's internal endpoint
- Ingest pod spools received spans and forwards via internal API (`/internal/v1/ingest`)
- Dashboard and mutation APIs remain on the writer pod only

### US-12: Live tail

**As** a developer debugging in real time,
**I want** a stream of spans as they arrive,
**So that** I can watch traffic without polling the database.

**Acceptance criteria:**
- `GET /api/v1/spans/live` returns an SSE stream
- Each event is a JSON span record
- Stream is filtered per session/project
- Requires session auth

### US-13: Client error tracking

**As** a developer instrumenting a browser application,
**I want** to capture JS errors and send them to SpanBarn,
**So that** frontend errors are co-located with the trace context they occurred in.

**Acceptance criteria:**
- `POST /api/v1/client-errors` accepts error payloads from the browser SDK
- Errors are enriched with trace context (traceId, spanId) if present
- Forwarded to BugBarn if `SPANBARN_BUGBARN_ENDPOINT` is configured

### US-14: Saved queries

**As** a user who runs the same dashboard filters frequently,
**I want** to save and recall named query presets,
**So that** I don't have to re-enter filters on every session.

**Acceptance criteria:**
- `GET/POST/DELETE /api/v1/saved-queries` for CRUD
- Saved per project, visible to all users of that project
- Loaded into the dashboard filter UI

### US-15: Web vitals tracking

**As** a frontend developer,
**I want** Core Web Vitals (CLS, LCP, INP, TTFB) collected alongside traces,
**So that** I can correlate user-perceived performance with backend latency.

**Acceptance criteria:**
- Browser SDK collects Web Vitals and sends them as span attributes
- `GET /api/v1/web-vitals` returns aggregated vitals per page
- `GET /api/v1/web-vitals/timeseries` returns time-bucketed vitals
- Dashboard shows vitals per page/route

### US-16: Database query analysis

**As** a backend developer,
**I want** slow database queries grouped and ranked,
**So that** I can find N+1s and missing indexes without manually digging through traces.

**Acceptance criteria:**
- `GET /api/v1/database` returns grouped query fingerprints with p50/p95/p99, call count, error rate
- `GET /api/v1/database/detail` returns example spans for a query fingerprint
- Recognises `db.statement`, `db.system`, `db.operation` OpenTelemetry attributes

### US-17: LLM prompt tracking

**As** a developer building AI-powered features,
**I want** LLM calls tracked as first-class spans with model, token usage, and latency,
**So that** I can optimise cost and latency of AI features alongside other dependencies.

**Acceptance criteria:**
- `GET /api/v1/prompts` returns grouped prompts with latency, token counts, error rate
- `GET /api/v1/prompts/detail` returns example spans for a prompt fingerprint
- Recognises `gen_ai.system`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`

### US-18: FunnelBarn analytics integration

**As** an operator running FunnelBarn alongside SpanBarn,
**I want** pageviews and events forwarded to FunnelBarn,
**So that** analytics and traces are correlated without a separate instrumentation call.

**Acceptance criteria:**
- Configure via `SPANBARN_FUNNELBARN_ENDPOINT`, `SPANBARN_FUNNELBARN_API_KEY`, `SPANBARN_FUNNELBARN_PROJECT`
- Pageview/event spans forwarded asynchronously; failure does not affect ingest

### US-19: BugBarn error tracking integration

**As** an operator running BugBarn alongside SpanBarn,
**I want** captured errors forwarded to BugBarn automatically,
**So that** error tracking and distributed tracing are correlated.

**Acceptance criteria:**
- Configure via `SPANBARN_BUGBARN_ENDPOINT`, `SPANBARN_BUGBARN_API_KEY`
- Error spans and client errors forwarded asynchronously; failure does not affect ingest

### US-20: Redis-backed caching and rate limiting

**As** an operator running multiple SpanBarn instances,
**I want** rate limits and response caches backed by Redis,
**So that** limits are enforced consistently across all pods.

**Acceptance criteria:**
- Configure via `SPANBARN_REDIS_URL`; falls back to in-process when not set
- `SPANBARN_CACHE_TTL_SECONDS` controls dashboard query cache lifetime
- Per-minute limits: `SPANBARN_API_RATE_PER_MINUTE`, `SPANBARN_INGEST_RATE_PER_MINUTE`, `SPANBARN_LOGIN_RATE_PER_MINUTE`

### US-21: Service map

**As** an operator,
**I want** a visual graph of which services call which,
**So that** I can understand propagation paths and identify unexpected dependencies.

**Acceptance criteria:**
- `GET /api/v1/service-map` returns nodes (services) and edges (call relationships) with call counts and error rates
- Dashboard renders as a connected graph
- Edges weighted by call volume
