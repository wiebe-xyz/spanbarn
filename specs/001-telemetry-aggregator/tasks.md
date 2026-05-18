# Implementation Tasks

## Phase 1: Core Engine

- [x] T01: Project scaffolding (go.mod, cmd/spanbarn/main.go, Makefile, CI, Docker)
- [x] T02: SQLite storage layer (schema, goose migrations, sqlc queries)
- [x] T03: Spool-based ingest pipeline (in-memory queue, NDJSON spool, worker)
- [x] T04: Native JSON ingest endpoint (POST /api/v1/spans)
- [x] T05: OTLP/HTTP ingest endpoint (POST /v1/traces, protobuf + JSON)
- [x] T06: Auth system (API key, user login, bcrypt, HMAC sessions)
- [x] T07: Aggregation engine (percentile computation, bucketed rollups)
- [x] T08: Retention worker (aggregate old spans, sample errors, drop uneventful)
- [x] T09: Query API (service list, operation metrics, trace search, trace detail)
- [x] T10: Web dashboard — service overview and operation metrics
- [x] T11: Web dashboard — trace search and waterfall view
- [x] T12: CLI commands (user, project, apikey management)
- [x] T13: Rate limiting and security headers

## Phase 2: SDKs

- [x] T14: JavaScript/TypeScript SDK (browser + Node.js)
- [x] T15: Go SDK (context propagation, HTTP middleware)
- [x] T16: Python SDK (context manager, WSGI/ASGI middleware)

## Phase 3: Production Hardening

- [x] T17: Alerting engine (webhook + email)
- [x] T18: Dependency tracking dashboard
- [x] T19: Litestream integration for SQLite backup
- [x] T20: K8s manifests (testing, staging, production)
- [x] T21: Binary release pipeline (cross-platform, .deb, Homebrew)
- [x] T22: E2E test suite (Playwright)

## Phase 4: Rapid-root Integration

- [x] T23: Instrument rapid-root Fastify backend with SpanBarn
- [x] T24: Instrument rapid-root Next.js frontends with SpanBarn browser SDK
- [x] T25: Self-reporting (SpanBarn traces itself)

## Phase 5: Extended Features

- [x] T26: Ingest-only mode — separate ingest pod forwards to writer via internal API (SPANBARN_MODE, SPANBARN_WRITER_URL)
- [x] T27: Live tail — SSE stream of spans as they arrive (/api/v1/spans/live)
- [x] T28: Client error tracking — browser JS error ingest (/api/v1/client-errors)
- [x] T29: Saved queries — persist and recall dashboard filter sets
- [x] T30: Web vitals tracking — CLS, LCP, FID/INP, TTFB per page
- [x] T31: Database query analysis — slow query grouping and drill-down
- [x] T32: LLM/prompt span tracking — token usage, model, latency per prompt
- [x] T33: FunnelBarn analytics integration — forward pageview/event data
- [x] T34: BugBarn error tracking integration — forward captured errors
- [x] T35: Redis-backed distributed rate limiting and response cache
- [x] T36: Service map — visual graph of inter-service call relationships
- [x] T37: Export API — download trace data
