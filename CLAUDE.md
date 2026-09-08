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
- **SQLite**, no continuous replication — disaster recovery is a periodic
  settings-only snapshot shipped to S3, not full-DB WAL streaming (see
  `deploy/docs/disaster-recovery.md`)
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

## Quality gate (`make quality-gate`)

`scripts/quality-gate.sh` is a ratchet. It fails in **both** directions: a metric
worse than its baseline is new debt, and a metric better than its baseline is a
win nobody locked in — a baseline with slack in it silently re-opens room for a
regression. Run `make quality-gate-update` to refresh the count baselines, then
commit the diff.

Every threshold lives in **`scripts/quality-thresholds.conf`** and nowhere else.
The gate refuses to run if a threshold name is set in the environment, so
`COMPLEXITY=99 make quality-gate` exits 2 instead of passing. Changing a number
means changing a committed file, which a reviewer sees in the diff. That is the
only escape hatch.

Blocking metrics:
- **Coverage** — `internal/service` and `internal/repository` stay above their package floors, AND every individual source file in them stays above a per-file floor (no fully-uncovered file hiding behind well-covered siblings; migration DDL and test files excluded). Coverage floors are raised by hand from a CI measurement: the workstation and CI measure these packages several points apart, so `--update-baseline` deliberately leaves them alone.
- **Cyclomatic complexity** — the count of functions over gocyclo 15, exactly.
- **File length** — the count of files over 500 lines, exactly, and no file over the hard cap.
- **Duplication** — the count of `dupl` clone groups, exactly.

Soaks — they run, print a count against a committed baseline, and do not block.
Flip `BOUNDARIES_MODE` / `INFRA_MODE` to `enforce` in the thresholds file to make
them blocking; the exit criteria are written next to each one.
- **Boundaries** (`scripts/boundarycheck`) — `internal/api` must reach persistence through `internal/service`, and neither `internal/service` nor `internal/repository` may depend on transport. Baseline: 14 files in `scripts/boundaries-baseline.txt`. Enforce at 5 or fewer; target 0.
- **Infra** (`scripts/infracheck`) — every kustomize overlay renders, and the rendered workloads have resource requests and limits, readiness and liveness probes, non-floating image tags and no reference to a Secret nobody applies. An overlay that fails to render is a hard error, never a pass. Baseline: 6 findings in `scripts/infra-baseline.txt`. Enforce at 0.

The gate scripts have their own tests: `make quality-gate-test` runs the ratchet
semantics (violation present, violation baselined, baseline beatable, clean
tree) against fixtures without touching the real tree.

**Headroom is currently zero** on complexity (23 of 23) and file length (5 of 5),
and `cmd/spanbarn/main.go` is 22 lines under the 1200-line hard cap. The answer
is decomposition, not a bigger number — the gate prints the remaining headroom
on every run so the trend is visible.

## CI/CD (GitHub Actions)

- `ci.yml` — spec, lint/test/build (`code`), and the `quality` gate on every push/PR
- `build-and-test.yml` — Docker build + deploy to k3s testing, then staging + E2E. The
  `quality-backend` job runs the same `scripts/quality-gate.sh`, so the gate is in the
  deploy path and not only in a `quality` job no deploy job depends on
- `deploy-production.yml` — manual production deploy with confirmation
- `binary-release.yml` — semver tags, .deb packages, macOS tarballs, npm/PyPI SDK publish.
  Triggered by `workflow_run` on a **successful** CI run and checks out
  `workflow_run.head_sha`, so a red build cuts no tag and publishes nothing, and the
  release is built from the commit CI actually tested

## Deployment

- K8s on k3s (nijmegen cluster for testing/staging, layer7 for production)
- GHCR for container images
- SOPS + age for secret encryption
