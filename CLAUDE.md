# CLAUDE.md

## Project Overview

SpanBarn is a self-hosted telemetry aggregator written in Go. It collects distributed traces (OTLP-compatible), stores full-fidelity spans for a short window, then aggregates into long-term performance metrics. Part of the Barn family alongside BugBarn (errors) and FunnelBarn (analytics).

## Build & Development

```bash
make setup    # install all dependencies
make test     # run spec checks + all tests
make lint     # run linters
make build    # compile everything
make dev      # docker compose up --build
```

## Architecture

- **Go binary** (`cmd/spanbarn/main.go`) — single process, no external DB
- **SQLite** with Litestream for optional S3 backup
- **Spool-based ingest** — in-memory queue → NDJSON WAL → background worker → SQLite
- **React + Vite frontend** (`web/`) — served by Caddy/Nginx
- **SDKs** (`sdks/`) — JavaScript, Go, Python

## Key Patterns (mirror BugBarn/FunnelBarn)

- Config via environment variables (`SPANBARN_*`)
- Auth: bcrypt passwords + token-bound server-side sessions (opaque cookie -> web_sessions row; OIDC tokens live server-side with refresh) + SHA256 API keys
- CLI subcommands: `spanbarn user/project/apikey create`
- Spool rotation at 64 MiB
- Dead-letter on 3 failed processing attempts
- Self-reporting to BugBarn for error tracking

## Testing

- Go: `go test ./...` (race detector in CI)
- Frontend: Vitest
- E2E: Playwright

## Quality gate (`make quality-gate`, CI job `quality`)

`scripts/quality-gate.sh` is a ratchet: CI fails if any metric regresses past the
baseline recorded in that script. Baselines only move in the improving direction
— when you legitimately improve a metric, ratchet its baseline DOWN in the script.
Enforced metrics:
- **Coverage** — `internal/service` and `internal/repository` stay above their package floors, AND every individual source file in them stays above a per-file floor (no fully-uncovered file hiding behind well-covered siblings; migration DDL and test files excluded).
- **Cyclomatic complexity** — the count of functions over gocyclo 15 may not grow.
- **File length** — the count of files over 500 lines may not grow, and no file may exceed a hard cap.
- **Duplication** — the count of `dupl` clone groups may not grow.

## CI/CD (GitHub Actions)

- `ci.yml` — spec, lint/test/build (`code`), and the `quality` gate on every push/PR
- `build-and-test.yml` — Docker build + deploy to k3s testing
- `deploy-production.yml` — manual production deploy with confirmation
- `binary-release.yml` — semver tags, .deb packages, macOS tarballs, npm/PyPI SDK publish

## Deployment

- K8s on k3s (nijmegen cluster for testing/staging, layer7 for production)
- GHCR for container images
- SOPS + age for secret encryption
