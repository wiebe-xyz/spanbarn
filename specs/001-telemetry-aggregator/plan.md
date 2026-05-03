# Implementation Plan

## Phase 1: Core Engine (MVP)

Foundation: Go binary, SQLite storage, spool-based ingest, basic dashboard.

1. **Project scaffolding** — Go module, Makefile, CI, Docker
2. **SQLite storage** — Schema, migrations (goose), repository layer (sqlc)
3. **Spool ingest** — In-memory queue → NDJSON spool → background worker
4. **Native JSON ingest** — `POST /api/v1/spans` endpoint
5. **OTLP ingest** — `POST /v1/traces` (protobuf + JSON)
6. **Auth** — API key validation, user login, session management
7. **Retention worker** — Aggregate old spans, keep error samples, drop uneventful
8. **Web dashboard** — React + Vite, service overview, operation drill-down, trace waterfall
9. **CLI** — `spanbarn user/project/apikey create`

## Phase 2: SDKs

10. **JavaScript SDK** — Browser + Node.js, auto-instrumentation, batch flush
11. **Go SDK** — Context propagation, HTTP middleware
12. **Python SDK** — Context manager, WSGI/ASGI middleware

## Phase 3: Production Hardening

13. **Alerting** — Webhook + email notifications on regression
14. **Dependency tracking** — Client span grouping, dependency dashboard
15. **Litestream** — Optional S3 backup of SQLite
16. **K8s deployment** — Kustomize manifests, testing/staging/production
17. **Binary releases** — Cross-platform builds, .deb, Homebrew

## Phase 4: Rapid-root Integration

18. **Rapid-root backend SDK integration** — Instrument Fastify API with SpanBarn
19. **Rapid-root frontend integration** — Instrument Next.js sites with browser SDK
20. **Self-reporting** — SpanBarn reports its own traces to itself

## Technical Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Database | SQLite + Litestream | Same as BugBarn/FunnelBarn. Single-file, zero-ops, proven |
| Query builder | sqlc | Type-safe, no ORM overhead |
| Migrations | goose | Proven in FunnelBarn |
| HTTP framework | net/http stdlib | Minimal dependencies, fast |
| Frontend | React + Vite | Same as FunnelBarn, good DX |
| Protobuf | google.golang.org/protobuf | Required for OTLP ingest |
| Aggregation | T-digest or DDSketch | Mergeable percentile sketches for bucketed aggregation |
| Spool format | NDJSON | Human-debuggable, same as BugBarn/FunnelBarn |
