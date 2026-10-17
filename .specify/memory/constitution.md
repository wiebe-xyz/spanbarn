# SpanBarn Constitution

These are the rules the codebase already enforces. Each principle names the
test, gate or issue that backs it. A principle with no source in the tree does
not belong here.

SpanBarn is a self-hosted telemetry aggregator. The applications it observes
must never pay for it, and its single SQLite writer must never be asked to do two
jobs at once, so the first two principles protect those two things.

## Core Principles

### I. Ingest never blocks the instrumented application

Span submission is fire-and-forget. The ingest path MUST accept a batch into the
in-memory queue and the on-disk spool and answer without waiting for the
database. A process stop MUST move buffered traces to the spool instead of
dropping them.

Sources:
- `internal/ingest/handler_test.go::TestHandlerFlushesToSpool`
- `cmd/spanbarn/shutdown_test.go::TestShutdownMovesBufferedTracesToTheSpool`
- `internal/spool/spool_test.go::TestSpoolWriteAndRead`, `::TestSpoolCursor`
- `internal/ingest/trace_buffer_cap_rescue_test.go::TestCapEvictionKeepsSlowTraces`
- Issues #3 (spool-based ingest), #166 (trace buffer at cap), #331 (reader rollouts drop buffered traces)

### II. A write touches one storage family

SQLite has one writer per file. The repository holds one write handle and one
`writescheduler.Scheduler` per storage family (main, spans, and each heavy
shard family). A repository method that writes MUST touch one family, and every
family writer MUST attach the other files read-only so a misrouted write fails.
Main-track migrations MUST NOT touch span-family tables. Heavy-table queries
MUST NOT use `INDEXED BY`, because the shard views reject it.

Sources:
- `internal/repository/layout_test.go::TestMainTrackLeavesSpanTablesAlone`
- `internal/repository/shard_read_indexed_test.go::TestNoIndexedByOnShardedFamilies`
- `internal/repository/cutover_test.go::TestCutOverMovesSpansFamily`, `::TestCutOverFailureKeepsSingleFile`
- CI: the `code` job in `.github/workflows/ci.yml` runs the repository tests a second time with `SPANBARN_TEST_LAYOUT=split`
- [specs/002-temporal-sharding/design.md](../../specs/002-temporal-sharding/design.md); issues #235, #248

### III. Layers import downward only

`api` reaches persistence through `service`. `service` and `repository` MUST NOT
import transport packages (`net/http`, `api`, `auth`, `ingest`). Only
`repository` and `cmd` may import `writescheduler`, `database/sql` or the SQLite
driver. The `depguard` rules MUST NOT gain exclusions, and the boundaries
baseline MUST NOT grow.

Sources:
- `.golangci.yml` (`depguard`), run by `make lint` and the `code` job in `ci.yml`
- `scripts/boundarycheck` against `scripts/boundaries-baseline.txt`, mode in `scripts/quality-thresholds.conf`
- Issues #175, #177

### IV. Quality metrics only move toward better

`scripts/quality-gate.sh` is a ratchet. A metric worse than its baseline fails
the build, and a metric better than its baseline fails it too until the
baseline is refreshed. Every threshold lives in `scripts/quality-thresholds.conf`
and nowhere else. Complexity (gocyclo 15) and file length (500 lines) stand at
zero offenders, so a new offender MUST be decomposed, not baselined.

Sources:
- `scripts/quality-gate.sh`, `scripts/quality-thresholds.conf`
- `scripts/quality-lib.test.sh`, run by `make quality-gate-test` in the `quality` job
- Issue #174

### V. Credentials are hashed and sessions live on the server

Passwords MUST be bcrypt hashed and API keys SHA256 hashed and compared in
constant time. Web sessions are opaque cookies backed by a `web_sessions` row;
tokens MUST NOT reach the browser. Login, ingest and API routes MUST be rate
limited, responses MUST carry the security headers, and every new endpoint MUST
carry an auth middleware and a test.

Sources:
- `internal/repository/repository_test.go::TestCreateAndGetAPIKey`, `::TestRevokeAPIKey`
- `internal/repository/repo_sessions_test.go::TestWebSessionCreateGetRoundtrip`
- `internal/api/login_test.go::TestLoginSuccess`
- `internal/api/ratelimit_test.go::TestRateLimiterExhausted`
- `internal/api/security_test.go::TestSecurityHeaders`
- `internal/api/auth_middleware_test.go`
- Issues #6, #13

### VI. SpanBarn reports its own failures to BugBarn

Error-level log paths MUST use `slog` so the BugBarn handler captures them.
Self-reporting MUST forward only records at error level.

Sources:
- `internal/observability/handler_test.go::TestBugBarnHandler_ForwardsErrorOnly`
- `internal/observability/bugbarn_test.go::TestBugBarnEventsCarryTheEnvironmentAsAttributes`
- Issues #25, #170

### VII. Production runs what staging validated

Images are built once on the merge to `main` and deployed to testing, then to
staging, where the E2E suite runs. Production rolls forward on its own when that
run succeeded. Every deploy MUST assert the running version and commit through
`/api/v1/version` after rollout, and a failed production rollout MUST roll back.

Sources:
- `.github/workflows/build-and-test.yml` (testing, staging and E2E jobs), `.github/workflows/deploy-production.yml` (preflight, rollback, version assertion)
- `deploy/scripts/assert-version.sh`, tested by `scripts/test_version_smoke.py`
- `scripts/test_release_head.py` (the release is built from the commit CI tested)
- Issues #315, #348

## Not ratified

Candidates checked against the tree and left out because nothing enforces them:

- Ingest p99 under 5 ms and 10,000 spans per second per core
  ([specs/001-telemetry-aggregator/spec.md](../../specs/001-telemetry-aggregator/spec.md), NFR-001 and NFR-002). A benchmark exists
  (`BenchmarkOTLPIngestMemory` for memory) but no gate fails on a latency or
  throughput number.
- Continuous backup of the full database. Disaster recovery is a settings-only
  snapshot by design ([deploy/docs/disaster-recovery.md](../../deploy/docs/disaster-recovery.md)).

## Governance

- This file changes through a pull request that edits the principle and names
  the gate, test, ADR or issue that now backs the change. A principle whose
  source is removed from the tree is removed or amended in the same PR.
- A principle is tightened by adding a gate or test. It is weakened only by an
  ADR that says why.
- Every spec under `specs/` lists the principles it touches in its
  "Principles touched" header line, and every plan answers the Constitution
  Check in `.specify/templates/plan-template.md`.
- Where this file and the agent rule (`AGENTS.md`, `CLAUDE.md`) disagree, fix
  whichever one is wrong against the tree in the same PR.

**Version**: 1.0.0 | **Ratified**: 2026-10-17 | **Last Amended**: 2026-10-17
