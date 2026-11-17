# Feature Specification: One project per product, environment as an attribute

**Spec**: `specs/003-project-per-product/`
**Status**: Draft
**Last verified against the tree**: 2026-11-16 at `310c61b`
**Issues**: [#558](https://github.com/wiebe-xyz/spanbarn/issues/558)
**Principles touched**: II, IV

## Purpose

Production SpanBarn holds 47 projects. Most products appear several times, one project per environment (`main-prod`, `main-staging`) or per spelling (`rr-backend-prod`, `backend-prod`). The frontend and backend spans of one product land in different projects, so the browser-to-backend trace join cannot work. The owner rule of 2026-11-16: one project per product, and testing, staging, production, e2e and dev are an environment on the record. This spec adds the environment dimension, a merge that moves data and keeps old ingest keys valid, and the guard that stops new per-environment projects.

## Users

- **Developer**: sees one project per product, filters it by environment, production by default.
- **Site lane**: keeps its current ingest key and project slug until its own coverage issue switches it; nothing breaks on merge day.
- **Operator**: merges in production in steps that log counts per source, can resume a merge after a restart, and loses no event.

## Decisions (stated assumptions, owner order 2026-11-16 20:28: decide, do not wait)

1. **Environment is the `deployment.environment` resource attribute, stamped at ingest.** Spans, logs and metrics already store resource attributes, and reads group and filter on `deployment.environment` today. Slice 1 therefore adds no column to the sharded tables: it guarantees the attribute is present. A column per table (an index for the filter) is decided in slice 2 from the measured read cost, because the UI reads are already 2 to 4.5 s ([#457](https://github.com/wiebe-xyz/spanbarn/issues/457)).
2. **An ingest key carries an environment.** `api_keys.environment TEXT NOT NULL DEFAULT ''` (migration 047, a metadata-only `ADD COLUMN`). Ingest sets `deployment.environment` on each OTLP resource from, in order: the resource's own `deployment.environment`, its `deployment.environment.name` (the current semantic-convention name, which SpanBarn did not read before), then the key's environment. A resource with none of the three is stored as before and reads as `production` in the default filter. `spanbarn apikey create --env=E` and `spanbarn apikey set-env --id=N --env=E` set it.
3. **A merge repoints; it does not copy.** `spanbarn project merge --source=A --target=B --env=E` updates `api_keys` of A to project B with environment E, then moves A's rows to B in resumable batches (`UPDATE ... SET project_id=B, environment=COALESCE(NULLIF(environment,''),E) WHERE project_id=A LIMIT n`), table by table and shard by shard. Rows keep their ids. The merge stores its cursor in a `project_merges` table, so a restart resumes.
4. **Unique keys on merge.** Tables with a unique key over `project_id` (aggregates, rollups, trace summaries) get `environment` in that key. Where two sources collide on the same bucket, the merge sums counts and merges sketches through the existing rollup merge path rather than dropping a row. The inventory of unique keys is the first task of slice 2.
5. **Sources are archived, never deleted.** After the last batch a source with zero rows gets `archived_at`. Archived projects stay out of the default list and keep their id, so old links resolve to the target through `project_merges`.
6. **Names.** `project create` and the create API refuse a slug ending in `-prod`, `-production`, `-staging`, `-stage`, `-test`, `-testing`, `-e2e` or `-dev`, and a slug starting with `rr-`, with an error that names the environment field. Existing names are untouched until merged.
7. **Target per row.** Per-site projects fold into one project per site, named after the site (`languagetrainer`, `main`, `places`, `scherpstel`, `webwiebe`), with `service.name` as the service dimension. The rapid-root API sources fold into `rapid-root`. A product with several services keeps one project; services use the existing service dimension.

## Slices

| Slice | Content | Ships when |
|---|---|---|
| 1 | Migration 047 (`api_keys.environment`), ingest stamping of `deployment.environment`, CLI flags, name guard | tests prove a key with `environment=staging` stamps spans, logs and metrics, and an own attribute wins |
| 2 | `environment` parameter on the filter-aware read APIs and `GET /api/v1/environments` (PR 1), then the web selector with production as its default and the unique-key inventory (PR 2) | tests prove one project with two environments reads separately and together |
| 2b | `environment` column in each rollup table whose unique key lacks it, one PR per table: aggregates (done), span_rollups (done), span_edges, metric_rollups and metric_rollups_coarse, slo_counts, span_ledger; the services, operations, timeseries, service map and metrics reads honor `environment` as each table lands | tests prove two environments in one bucket stay two rows and read apart, and a second run of the migration succeeds |
| 3 | `project merge`, `project_merges` cursor, archive, counts per table in the log | tests prove resume after a kill, key repoint and collision sums on a real SQLite file |
| 4 | Production merges per the table in #558, counts logged on the issue, one source at a time, smallest first, host idle gate before each | production project list matches the rule |

## Functional Requirements

- **FR-001**: Ingest stores `deployment.environment` on every OTLP resource from the resource's own `deployment.environment`, else its `deployment.environment.name`, else the key's environment. [#558]
- **FR-002**: The filter-aware read APIs (traces, span search, analyze, analyze series, heatmap, attribute compare) take `environment` (a name or `all`); `production` also selects spans with no environment. A request without it reads everything, so existing callers keep their results; the web app sends `production` by default. `GET /api/v1/environments` returns the environments a project holds with span counts. Other read endpoints (services, service map, logs, metrics, dashboard counts) follow once the measured cost of the filter is known. [#558]
- **FR-003**: `spanbarn project merge` moves all of a source's rows and keys into the target without a key change for the site, and is safe to run again after an interruption. [#558]
- **FR-004**: A merge logs, per table, the row count moved, and refuses to start when the host idle gate is not met. [#558]
- **FR-005**: A source project with zero rows after a merge is archived and resolves to its target. [#558]
- **FR-006**: Creating a project (CLI or setup flow) with an environment suffix or an `rr-` prefix is refused; the setup endpoint answers 400. [#558]
- **FR-007**: No event is deleted by a merge or by the archive. [#558]

- **FR-008**: `aggregates` carries `environment TEXT NOT NULL DEFAULT ''` in its unique key `(project_id, environment, service, operation, resource, kind, bucket)`. The accumulator and the retention aggregator take the environment from the span's `deployment.environment`. `GET /api/v1/services`, `/services/{service}/operations` and the timeseries read take `environment`; `production` also selects rows with no environment. The alert evaluator reads every environment. [#558]
- **FR-009**: `span_rollups` carries `environment TEXT NOT NULL DEFAULT ''` in its primary key `(step_seconds, bucket, project_id, environment, service, name, kind, is_root, status, http_status)`. The accumulator takes the environment from the span's `deployment.environment`; compaction merges rows per environment, so tiers never sum two environments. Rows written before the migration carry `''`, which a `production` read also selects. The services and timeseries reads serve a scoped request from `span_rollups` as they serve an unscoped one. [#558]

## Slice 2b inventory: tables keyed on the project without an environment

| Table | Key today | Step |
|---|---|---|
| aggregates | unique `(project_id, service, operation, resource, kind, bucket)` | PR 1 (migration 10 of the spans track) |
| span_rollups | PK `(step_seconds, bucket, project_id, service, name, kind, is_root, status, http_status)` | PR 2 (migration 11 of the spans track) |
| span_rollup_compaction | step and bucket watermark, no project | none needed |
| span_edges | project, caller, callee, bucket | own PR |
| metric_rollups, metric_rollups_coarse | project, metric, labels, bucket | own PR |
| slo_counts, span_ledger | project, bucket | own PR |
| trace_summaries, error_exemplars | PK `(project_id, trace_id)` | a trace belongs to one environment; only a merge collision matters, handled in slice 3 |
| slos, calculated_fields, trace_exclusions, pinned_traces | unique name or id per project | settings; slice 3 collision rule |

Rollback caveat: migration 10 drops the old unique index on `aggregates`. A binary from before it upserts with `ON CONFLICT(project_id, service, ...)`, which SQLite refuses without a matching index, so a rollback past this migration stops aggregate writes. Aggregates back the usage stats and the fallback read, so the path is roll forward.

Rollback caveat, migration 11: it rebuilds `span_rollups` because a WITHOUT ROWID primary key cannot change in place (copy into `span_rollups_env`, drop, rename, one transaction). A binary from before it upserts with the old conflict target, which no longer exists, so span rollup writes fail after a rollback until the roll forward. The accumulator keeps unwritten slots for an hour and compaction waits on its watermark, so the path is roll forward. The cost is one read and one write of the table, which the tiers keep small next to spans.

## Open Questions

- Decision 1 defers the per-table column to slice 2. Whether the filter needs one depends on the read timing from #457. [inferred]

- Collision sums for sketch-bearing rollups need the span_rollups and metric_rollups PRs of slice 2b before the merge of slice 3 is fixed. [inferred]

## Drift

| Item | Document says | Code does |
|---|---|---|
| #558 title | per-product projects exist | 47 projects, one per environment and spelling |
| FR-002 (earlier draft) | every read API defaults to production | the API default stays unfiltered so current callers keep their reads; the web app defaults to production |
| Decision 1 (earlier draft) | `environment` column on every record table | slice 1 stamps the resource attribute instead; columns wait for slice 2 |
| Slice 2b | every rollup table has an environment column | `aggregates` and `span_rollups` have it; the services, operations and timeseries reads take `environment`; the dashboard cards, trace groups, service map and metrics reads stay unscoped until their PR |
