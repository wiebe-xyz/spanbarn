# Feature Specification: Telemetry aggregator

**Spec**: `specs/001-telemetry-aggregator/`
**Status**: Partial
**Last verified against the tree**: 2026-10-17 at `41a73f7`
**Issues**: [#1](https://github.com/wiebe-xyz/spanbarn/issues/1) to [#25](https://github.com/wiebe-xyz/spanbarn/issues/25) (the T01 to T25 scaffold), [#194](https://github.com/wiebe-xyz/spanbarn/issues/194)
**Principles touched**: I, III, IV, V, VI

## Purpose

SpanBarn is a self-hosted telemetry aggregator. It accepts distributed traces (OTLP and a native JSON endpoint), keeps full spans for a short window, and aggregates them into long-term performance metrics. Error and slow spans are kept in detail. The instrumented application is never blocked, and setup is one binary with an embedded SQLite database. The plan is in [plan.md](./plan.md), the schema in [data-model.md](./data-model.md), the API contract in [contracts/](./contracts/), and the original task list in [tasks.md](./tasks.md). Sharding and the split into several database files live in [002 Temporal sharding](../002-temporal-sharding/spec.md).

## Users

- **Developer**: instruments an application and reads traces, dependencies, database queries, prompts and web vitals.
- **Operator**: deploys SpanBarn, sets retention and alerts, and watches the service map and SLOs.

## User Stories

### US1 - Ingest spans without blocking (Priority: P1)

Spans go to an in-memory queue, then an NDJSON spool, then a background worker.

**Acceptance Scenarios**:

1. **Given** a running server, **When** an SDK posts a batch, **Then** the request is answered once the batch is in the queue and spool, without waiting for the database.
2. **Given** a stopped worker, **When** spans arrive, **Then** they stay in the spool and are processed after restart.

### US2 - OTLP-compatible ingest (Priority: P1)

**Acceptance Scenarios**:

1. **Given** an OpenTelemetry exporter pointed at `POST /v1/traces`, **When** it sends protobuf or JSON with the API key header or a bearer token, **Then** the spans are stored with resource, scope and span attributes mapped.

### US3 - Native lightweight ingest (Priority: P1)

**Acceptance Scenarios**:

1. **Given** a JSON array with `traceId`, `spanId`, `name`, `service`, `startTime` and `duration`, **When** it is posted to `POST /api/v1/spans`, **Then** the spans are accepted.

### US4 - Smart span retention (Priority: P1)

**Acceptance Scenarios**:

1. **Given** spans older than the retention window, **When** the retention worker runs, **Then** they are aggregated into per-minute buckets and the uneventful ones are deleted.
2. **Given** an error span or a slow span, **When** retention runs, **Then** a sample is kept for the error window.

### US5 - Performance dashboard (Priority: P1)

**Acceptance Scenarios**:

1. **Given** ingested traces, **When** the operator opens the dashboard, **Then** services show latency percentiles, throughput and error rate for the chosen time range, with a recent-traces table and a trace waterfall.

### US6 - Dependency tracking (Priority: P2)

**Acceptance Scenarios**:

1. **Given** client spans with `db.system`, `http.url`, `rpc.service` or `aws.service`, **When** the operator opens dependencies, **Then** calls are grouped per target with call count, percentiles and error rate.

### US7 - Alerting on performance regression (Priority: P2)

**Acceptance Scenarios**:

1. **Given** an alert on a service and operation, **When** the current bucket crosses the threshold against the rolling average, **Then** a webhook or email is sent once per cooldown.

### US8 - Multi-language SDKs (Priority: P2)

**Acceptance Scenarios**:

1. **Given** the Go, JavaScript or Python SDK, **When** an application creates spans, **Then** they are flushed in batches and carry W3C `traceparent` context.

### US9 - Trivial setup (Priority: P1)

**Acceptance Scenarios**:

1. **Given** an empty data directory, **When** the binary starts, **Then** the setup flow creates the admin user, the first project and the first API key, and the CLI offers `spanbarn user|project|apikey create`.

### US10 - Secure by default (Priority: P1)

**Acceptance Scenarios**:

1. **Given** no session, **When** a dashboard API is called, **Then** it answers unauthorized. **Given** an ingest-only key, **When** a read API is called, **Then** it is refused.

### US11 - Ingest-only mode (Priority: P3)

**Acceptance Scenarios**:

1. **Given** `SPANBARN_MODE=ingest`, **When** spans arrive, **Then** they are spooled and forwarded to the writer through `/internal/v1/ingest`.

### US12 - Live tail (Priority: P3)

**Acceptance Scenarios**:

1. **Given** a session, **When** the client opens `GET /api/v1/spans/live`, **Then** it receives an SSE stream of span records for its project.

### US13 - Client error tracking (Priority: P3)

**Acceptance Scenarios**:

1. **Given** the browser SDK, **When** a JS error occurs, **Then** the payload is accepted by `POST /api/v1/client-errors`.

### US14 - Saved queries (Priority: P3)

**Acceptance Scenarios**:

1. **Given** a filter, **When** the user saves it, **Then** every user of the project can list and delete it.

### US15 - Web vitals (Priority: P3)

**Acceptance Scenarios**:

1. **Given** browser spans with vitals attributes, **When** the user opens web vitals, **Then** aggregated and time-bucketed values per page appear.

### US16 - Database query analysis (Priority: P3)

**Acceptance Scenarios**:

1. **Given** spans with `db.statement`, **When** the user opens database queries, **Then** statements are grouped by fingerprint with percentiles and error rate, and example spans are available.

### US17 - LLM prompt tracking (Priority: P3)

**Acceptance Scenarios**:

1. **Given** spans with `gen_ai.*` attributes, **When** the user opens prompts, **Then** prompts are grouped with latency, token counts and error rate.

### US18 - FunnelBarn analytics integration (Priority: P3)

Shipped as SpanBarn's own web UI reporting to FunnelBarn. See Drift.

### US19 - BugBarn error tracking integration (Priority: P3)

**Acceptance Scenarios**:

1. **Given** `SPANBARN_BUGBARN_ENDPOINT`, **When** SpanBarn logs at error level, **Then** the record is captured in BugBarn.

### US20 - Redis-backed caching and rate limiting (Priority: P3)

**Acceptance Scenarios**:

1. **Given** `SPANBARN_REDIS_URL`, **When** several pods run, **Then** limits and caches are shared. **Given** no Redis URL, **Then** the process falls back to in-process state.

### US21 - Service map (Priority: P3)

**Acceptance Scenarios**:

1. **Given** traces across services, **When** the user opens the service map, **Then** nodes and weighted edges with error rates are returned.

## Functional Requirements

Each requirement names the issue that asked for it and the test that proves it. A requirement with neither is marked `[inferred]`.

- **FR-001**: The system MUST accept spans into an in-memory queue of 32,768 spans and an NDJSON spool, and answer the SDK without waiting for the database. Issues: [#3](https://github.com/wiebe-xyz/spanbarn/issues/3). Tests: `internal/ingest/handler_test.go::TestHandlerFlushesToSpool`, `internal/spool/spool_test.go::TestSpoolWriteAndRead`.
- **FR-002**: The system MUST move buffered traces to the spool when the process stops. Issues: [#331](https://github.com/wiebe-xyz/spanbarn/issues/331). Tests: `cmd/spanbarn/shutdown_test.go::TestShutdownMovesBufferedTracesToTheSpool`.
- **FR-003**: The system MUST accept OTLP/HTTP traces as protobuf and JSON at `POST /v1/traces`. Issues: [#5](https://github.com/wiebe-xyz/spanbarn/issues/5). Tests: `internal/api/otlp_test.go::TestOTLPIngestProtobuf`, `::TestOTLPIngestJSON`.
- **FR-004**: The system MUST accept the API key in `x-spanbarn-api-key` or as an `Authorization: Bearer` token on OTLP ingest. Issues: [#5](https://github.com/wiebe-xyz/spanbarn/issues/5). Tests: `internal/api/otlp_test.go::TestOTLPBearerAuth`.
- **FR-005**: The system MUST accept a JSON batch of spans at `POST /api/v1/spans`. Issues: [#4](https://github.com/wiebe-xyz/spanbarn/issues/4). Tests: `internal/api/ingest_test.go::TestIngestValidBatch`.
- **FR-006**: The system MUST aggregate expired spans into per-minute buckets with count, error count, p50, p95, p99, max and sum. Issues: [#7](https://github.com/wiebe-xyz/spanbarn/issues/7), [#8](https://github.com/wiebe-xyz/spanbarn/issues/8). Tests: `internal/aggregation/aggregator_test.go::TestAggregateSpansPercentiles`, `::TestAggregateSpansErrorCount`.
- **FR-007**: The retention worker MUST delete uneventful spans and keep a sample of slow spans and an error exemplar per error operation per day. Issues: [#8](https://github.com/wiebe-xyz/spanbarn/issues/8), [#171](https://github.com/wiebe-xyz/spanbarn/issues/171), [#320](https://github.com/wiebe-xyz/spanbarn/issues/320). Tests: `internal/retention/retention_test.go::TestRunOnceDeletesBoringSpans`, `::TestRunOnceSamplesSlowSpans`, `internal/repository/repo_error_exemplars_test.go::TestInsertErrorSamplesRecordsOneExemplarPerOperationPerDay`.
- **FR-008**: Retention MUST run every 5 minutes, and every 30 seconds while the volume is under disk pressure. Issues: [#8](https://github.com/wiebe-xyz/spanbarn/issues/8). Tests: `internal/retention/reclaim_test.go::TestPressuredTickIsShorter`.
- **FR-009**: Retention MUST run apart from span aggregation, so a slow cleanup does not delay aggregates. Issues: [#50](https://github.com/wiebe-xyz/spanbarn/issues/50). [inferred]
- **FR-010**: The query API MUST return services, operations, traces and metrics, and a trace detail by id. Issues: [#9](https://github.com/wiebe-xyz/spanbarn/issues/9), [#169](https://github.com/wiebe-xyz/spanbarn/issues/169). Tests: `internal/api/query_test.go::TestTraceDetailEndpoint`, `::TestTraceDetailEndpointNotFound`.
- **FR-011**: The web dashboard MUST show services, operations, recent traces and a trace waterfall. Issues: [#10](https://github.com/wiebe-xyz/spanbarn/issues/10), [#11](https://github.com/wiebe-xyz/spanbarn/issues/11). [inferred]
- **FR-012**: The system MUST group client spans by dependency target from `db.system`, `http.url`, `rpc.service` and `aws.service`. Issues: [#18](https://github.com/wiebe-xyz/spanbarn/issues/18). Tests: `internal/service/query_dependencies_test.go::TestExtractDependencyTargetRules`, `::TestDependencyAggregator`.
- **FR-013**: The alert evaluator MUST respect a cooldown between notifications and MUST send webhooks. Issues: [#17](https://github.com/wiebe-xyz/spanbarn/issues/17). Tests: `internal/alert/evaluator_test.go::TestEvaluateCooldown`, `internal/alert/notifier_test.go::TestSendWebhook`.
- **FR-014**: The system MUST evaluate SLO burn-rate alerts. Issues: [#222](https://github.com/wiebe-xyz/spanbarn/issues/222), [#225](https://github.com/wiebe-xyz/spanbarn/issues/225). Tests: `internal/alert/slo_eval_test.go::TestSLOBurnAlertFiresOnceThenResets`, `internal/slo/slo_test.go::TestBurnRate`.
- **FR-015**: The Go SDK MUST propagate W3C `traceparent` context and offer an HTTP middleware. Issues: [#15](https://github.com/wiebe-xyz/spanbarn/issues/15). Tests: `sdks/go/context_test.go::TestInjectExtractTraceparent`, `sdks/go/middleware_test.go::TestHTTPMiddlewareTraceparent`.
- **FR-016**: The JavaScript and Python SDKs MUST batch spans and propagate `traceparent`. Issues: [#14](https://github.com/wiebe-xyz/spanbarn/issues/14), [#16](https://github.com/wiebe-xyz/spanbarn/issues/16). [inferred]
- **FR-017**: The CLI MUST create users, projects and API keys. Issues: [#12](https://github.com/wiebe-xyz/spanbarn/issues/12). Tests: `cmd/spanbarn/main_test.go::TestAPIKeyCreateAndRevoke`.
- **FR-018**: The setup endpoint MUST guide a first-run operator and keep a branded host. Issues: [#176](https://github.com/wiebe-xyz/spanbarn/issues/176). Tests: `internal/api/brandedhost_test.go::TestHandleSetupKeepsBrandedHost`.
- **FR-019**: The dashboard MUST be protected by username and password login, with passwords hashed. Issues: [#6](https://github.com/wiebe-xyz/spanbarn/issues/6). Tests: `internal/api/login_test.go::TestLoginSuccess`.
- **FR-020**: Web sessions MUST be server-side rows behind an opaque cookie. Issues: [#6](https://github.com/wiebe-xyz/spanbarn/issues/6). Tests: `internal/repository/repo_sessions_test.go::TestWebSessionCreateGetRoundtrip`.
- **FR-021**: API keys MUST be stored hashed, scoped per project, and revocable. Issues: [#6](https://github.com/wiebe-xyz/spanbarn/issues/6). Tests: `internal/repository/repository_test.go::TestCreateAndGetAPIKey`, `::TestRevokeAPIKey`.
- **FR-022**: Login, ingest and API routes MUST be rate limited per client. Issues: [#13](https://github.com/wiebe-xyz/spanbarn/issues/13). Tests: `internal/api/ratelimit_test.go::TestRateLimiterExhausted`, `::TestRateLimiterPerIP`.
- **FR-023**: Responses MUST carry security headers, and the ingest and OTLP endpoints MUST allow cross-origin calls while the live tail MUST NOT use a wildcard origin. Issues: [#13](https://github.com/wiebe-xyz/spanbarn/issues/13). Tests: `internal/api/security_test.go::TestSecurityHeaders`, `internal/api/middleware_test.go::TestCORSIngestEndpoint`, `::TestCORSLiveTailNotWildcard`.
- **FR-024**: The server MUST support a split role: ingest pods spool and forward to a writer through `/internal/v1/ingest`. Issues: none found. [inferred]
- **FR-025**: The system MUST stream live spans over SSE at `GET /api/v1/spans/live` for an authenticated session. Issues: none found. [inferred]
- **FR-026**: The system MUST accept browser error payloads at `POST /api/v1/client-errors`, truncating oversized fields. Issues: none found. Tests: `internal/api/client_errors_test.go::TestHandleClientErrorTruncatesAndAccepts`.
- **FR-027**: The system MUST store, list and delete saved queries per project, including their filters. Issues: [#193](https://github.com/wiebe-xyz/spanbarn/issues/193). Tests: `internal/repository/repo_saved_queries_test.go::TestSavedQueriesCRUD`, `internal/api/query_filter_test.go::TestSavedQueriesCarryFilters`.
- **FR-028**: The system MUST return aggregated web vitals per page and as a time series. Issues: none found. Tests: `internal/service/query_test.go::TestGetWebVitals`, `::TestGetWebVitalsTimeseries`.
- **FR-029**: The system MUST group database queries by fingerprint and return example spans. Issues: none found. Tests: `internal/service/query_database_test.go::TestListDatabaseQueries`.
- **FR-030**: The system MUST group LLM prompts with latency and token counts. Issues: none found. Tests: `internal/service/query_prompts_test.go::TestListPrompts`.
- **FR-031**: The system MUST return a service map of nodes and weighted edges. Issues: none found. Tests: `internal/service/query_service_map_test.go::TestGetServiceMap`.
- **FR-032**: The server MUST report its own error-level logs to BugBarn when `SPANBARN_BUGBARN_ENDPOINT` is set. Issues: [#25](https://github.com/wiebe-xyz/spanbarn/issues/25), [#170](https://github.com/wiebe-xyz/spanbarn/issues/170). Tests: `internal/observability/handler_test.go::TestBugBarnHandler_ForwardsErrorOnly`.
- **FR-033**: Dashboard query results MUST be cached for `SPANBARN_CACHE_TTL_SECONDS`, and rate limits MUST use Redis when `SPANBARN_REDIS_URL` is set. Issues: none found. Tests: `internal/service/query_dashboard_test.go::TestDashboardResultsAreCachedPerFilter`.
- **FR-034**: The server MUST expose `GET /api/v1/version` with version, commit and build time. Issues: [#348](https://github.com/wiebe-xyz/spanbarn/issues/348). Tests: `scripts/test_version_smoke.py`.
- **FR-035** `[gap]`: Span counts MUST NOT be inflated by the sample ratio for traces that sampling did not choose. Issues: [#333](https://github.com/wiebe-xyz/spanbarn/issues/333).
- **FR-036**: The web UI MUST reload once when a lazy chunk from a replaced build fails to load (`vite:preloadError`), guarded so a chunk that stays missing cannot loop, and MUST report a failure that survives the reload under one fixed type and message with the chunk URL as an attribute. Issues: [#383](https://github.com/wiebe-xyz/spanbarn/issues/383). Tests: `web/src/staleChunk.test.ts`.

## Non-Functional Requirements

- **NFR-001**: Ingest answers in under 5 ms at p99 and sustains 10,000 spans per second on one core. Source: this spec's original acceptance criteria. [inferred] No gate measures it.
- **NFR-002**: Dashboard aggregate queries answer in under 100 ms and recent span search in under 500 ms. Source: original acceptance criteria. [inferred]
- **NFR-003**: Memory stays under 100 MB at baseline and 500 MB under sustained load. Source: `internal/config/config.go` sizes the ingest buffers against a 448 MiB `GOMEMLIMIT` and `BenchmarkOTLPIngestMemory`.
- **NFR-004**: A malformed span is logged and skipped and never blocks the pipeline; the spool dead-letters after 3 failed attempts. Source: CLAUDE.md, "Key Patterns". [inferred]
- **NFR-005**: Spool-based ingest survives restarts. Source: `internal/spool/spool_test.go::TestSpoolCursor`.

## Key Entities

- **Span**, **Trace summary**, **Aggregate bucket**, **Error sample**: see [data-model.md](./data-model.md). Owned by `internal/repository`.
- **Project**, **API key**, **Web session**: tenancy and auth, `internal/repository`, `internal/auth`.
- **Alert**, **SLO**: `internal/alert`, `internal/slo`.

## Non-Goals

- Continuous replication of the full database. Disaster recovery is a settings-only snapshot ([deploy/docs/disaster-recovery.md](../../deploy/docs/disaster-recovery.md)).
- Time-sharding of spans. See [002](../002-temporal-sharding/spec.md).

## Open Questions

- [#329](https://github.com/wiebe-xyz/spanbarn/issues/329) The only production alert watches profotograaf in the wrong project.
- [#296](https://github.com/wiebe-xyz/spanbarn/issues/296) How many days of raw logs production should keep.
- [#333](https://github.com/wiebe-xyz/spanbarn/issues/333) Span counts inflated by the sample ratio for unchosen traces.
- Whether `POST /api/v1/client-errors` should forward to BugBarn. The original US-13 said so; see Drift.

## Drift

| Claim | Source | Tree |
|---|---|---|
| Litestream integration backs SQLite up to S3 (US-9). | Original spec, [#19](https://github.com/wiebe-xyz/spanbarn/issues/19) | No Go code uses Litestream. Disaster recovery is an hourly settings-only snapshot (`spanbarn db snapshot-settings`, `internal/repository/dr.go`). `deploy/docs/disaster-recovery.md` says so. |
| Full spans are kept for 72 hours by default. | Original spec, US-4 | `SPANBARN_RETENTION_FULL_HOURS` defaults to 0; interesting spans are kept 48 hours and boring spans 30 minutes (`internal/config/config.go`). |
| Errors are kept for `RETENTION_ERROR_DAYS`. | Original spec, US-4 | The variable is `SPANBARN_RETENTION_ERROR_DAYS` and defaults to 90; an error exemplar per operation lives `SPANBARN_RETENTION_ERROR_EXEMPLAR_DAYS` (30). |
| Session tokens are HMAC-signed. | Original spec, US-10 | Sessions are opaque cookies backed by a `web_sessions` row (`internal/repository/repo_sessions_test.go`); CLAUDE.md says the same. |
| Client errors are forwarded to BugBarn when configured (US-13). | Original spec | `internal/api/client_errors.go` truncates and accepts the payload and does not call BugBarn. The BugBarn handler in `internal/observability` captures SpanBarn's own error logs only. Forwarding of browser errors: not established. |
| Pageview and event spans are forwarded to FunnelBarn (US-18). | Original spec | The `SPANBARN_FUNNELBARN_*` settings feed SpanBarn's own web UI analytics (`web/src/funnelbarn.ts`, `cmd/spanbarn/runtime.go`). No ingest path forwards customer spans to FunnelBarn. |
| `SPANBARN_MODE=ingest` starts an ingest-only pod (US-11). | Original spec | The config marks `ingest` legacy; the modes are `standalone`, `reader`, `writer` and `ingest` (`internal/config/config.go`). Production runs reader and writer pods. |
| Email (SMTP) is a notification channel (US-7). | Original spec | `internal/alert/notifier.go` references SMTP. No test sends email. Whether email ships: not established. |
| Production is deployed manually with a confirmation. | CLAUDE.md before this PR | `.github/workflows/deploy-production.yml` runs on `workflow_run` after "Build and Deploy Testing" succeeds; `workflow_dispatch` stays as the override ([#315](https://github.com/wiebe-xyz/spanbarn/issues/315)). CLAUDE.md corrected in this PR. |
| Browser error attributes reach the error log (FR-036 sends the chunk URL as one). | This PR | `internal/api/client_errors.go` decodes `attributes` and does not log them, so the chunk URL is sent and not yet recorded. Logging them: not established. |

## Proof contract

- [ ] `go test ./...` passes, including the tests named in the FR lines above.
- [ ] Posting a span batch to `POST /api/v1/spans` and to `POST /v1/traces` on staging makes the traces appear in the dashboard (the E2E suite in `e2e/`).
- [ ] `GET /api/v1/version` on each environment returns the commit that `assert-version.sh` checked after the deploy.

## Sources

- The original spec text (git history of this file), [plan.md](./plan.md), [tasks.md](./tasks.md), [data-model.md](./data-model.md), [contracts/](./contracts/)
- `internal/api/routes.go`, `internal/config/config.go`, `internal/retention/`, `internal/alert/`, `internal/observability/`
- `CLAUDE.md`, `deploy/docs/disaster-recovery.md`
- GitHub issues #1 to #25 (scaffold), #50, #166, #169 to #171, #176, #193, #222 to #225, #315, #320, #331, #333, #348
