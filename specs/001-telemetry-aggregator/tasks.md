# Implementation Tasks

## Phase 1: Core Engine

- [ ] T01: Project scaffolding (go.mod, cmd/spanbarn/main.go, Makefile, CI, Docker)
- [ ] T02: SQLite storage layer (schema, goose migrations, sqlc queries)
- [ ] T03: Spool-based ingest pipeline (in-memory queue, NDJSON spool, worker)
- [ ] T04: Native JSON ingest endpoint (POST /api/v1/spans)
- [ ] T05: OTLP/HTTP ingest endpoint (POST /v1/traces, protobuf + JSON)
- [ ] T06: Auth system (API key, user login, bcrypt, HMAC sessions)
- [ ] T07: Aggregation engine (percentile computation, bucketed rollups)
- [ ] T08: Retention worker (aggregate old spans, sample errors, drop uneventful)
- [ ] T09: Query API (service list, operation metrics, trace search, trace detail)
- [ ] T10: Web dashboard — service overview and operation metrics
- [ ] T11: Web dashboard — trace search and waterfall view
- [ ] T12: CLI commands (user, project, apikey management)
- [ ] T13: Rate limiting and security headers

## Phase 2: SDKs

- [ ] T14: JavaScript/TypeScript SDK (browser + Node.js)
- [ ] T15: Go SDK (context propagation, HTTP middleware)
- [ ] T16: Python SDK (context manager, WSGI/ASGI middleware)

## Phase 3: Production Hardening

- [ ] T17: Alerting engine (webhook + email)
- [ ] T18: Dependency tracking dashboard
- [ ] T19: Litestream integration for SQLite backup
- [ ] T20: K8s manifests (testing, staging, production)
- [ ] T21: Binary release pipeline (cross-platform, .deb, Homebrew)
- [ ] T22: E2E test suite (Playwright)

## Phase 4: Rapid-root Integration

- [ ] T23: Instrument rapid-root Fastify backend with SpanBarn
- [ ] T24: Instrument rapid-root Next.js frontends with SpanBarn browser SDK
- [ ] T25: Self-reporting (SpanBarn traces itself)
