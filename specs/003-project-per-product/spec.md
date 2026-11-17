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
| 2b | `environment` column in each rollup table whose unique key lacks it, one PR per table: aggregates (done), span_rollups (done), span_edges (done), metric_rollups and metric_rollups_coarse (done, no column needed), slo_counts (no column needed), span_ledger (done); the services, operations, timeseries, service map, dashboard cards and trace groups honor `environment`, and the metrics reads stay on the label filter | tests prove two environments in one bucket stay two rows and read apart, and a second run of the migration succeeds |
| 3 | `project merge`, `project_merges` cursor, archive, counts per table in the log (3a core family, 3b spans family, 3c logs, metrics, prompts and the archive) | tests prove resume after a kill, key repoint and collision sums on a real SQLite file |
| 4 | Production merges per the table in #558, counts logged on the issue, one source at a time, smallest first, host idle gate before each | production project list matches the rule |

## Functional Requirements

- **FR-001**: Ingest stores `deployment.environment` on every OTLP resource from the resource's own `deployment.environment`, else its `deployment.environment.name`, else the key's environment. [#558]
- **FR-002**: The filter-aware read APIs (traces, span search, analyze, analyze series, heatmap, attribute compare) take `environment` (a name or `all`); `production` also selects spans with no environment. A request without it reads everything, so existing callers keep their results; the web app sends `production` by default. `GET /api/v1/environments` returns the environments a project holds with span counts. The dashboard cards (counts, percentiles, heatmap) and the trace groups take it too: from span rollups through the rollup row's environment, from raw spans through the `deployment.environment` attribute, and each environment keeps its own cache entry. Metric reads (series, totals and the other reads through the metric request) take it as well, from the `deployment.environment` label in every tier; `production` also selects series with no such label, on the assumption that unlabeled series come from senders that predate the label and ran in production (decided 2026-11-17, revisit once every estate sender labels its environment); the label filter `label[deployment.environment]=<value>` still works as before. Logs follow once the measured cost of the filter is known. [#558]
- **FR-003**: `spanbarn project merge` moves all of a source's rows and keys into the target without a key change for the site, and is safe to run again after an interruption. [#558]
- **FR-004**: A merge logs, per table, the row count moved, and refuses to start when the host idle gate is not met. [#558]
- **FR-005**: A source project with zero rows after a merge is archived and resolves to its target. [#558]
- **FR-006**: Creating a project (CLI or setup flow) with an environment suffix or an `rr-` prefix is refused; the setup endpoint answers 400. [#558]
- **FR-007**: No event is deleted by a merge or by the archive. [#558]

- **FR-008**: `aggregates` carries `environment TEXT NOT NULL DEFAULT ''` in its unique key `(project_id, environment, service, operation, resource, kind, bucket)`. The accumulator and the retention aggregator take the environment from the span's `deployment.environment`. `GET /api/v1/services`, `/services/{service}/operations` and the timeseries read take `environment`; `production` also selects rows with no environment. The alert evaluator reads every environment. [#558]
- **FR-009**: `span_rollups` carries `environment TEXT NOT NULL DEFAULT ''` in its primary key `(step_seconds, bucket, project_id, environment, service, name, kind, is_root, status, http_status)`. The accumulator takes the environment from the span's `deployment.environment`; compaction merges rows per environment, so tiers never sum two environments. Rows written before the migration carry `''`, which a `production` read also selects. The services and timeseries reads serve a scoped request from `span_rollups` as they serve an unscoped one. [#558]

- **FR-010**: The boring-trace floors (the minute floor of the worker, the hourly floor and the carried drop counts of the trace buffer) key on the operation and the environment of the root span, so one slot belongs to one environment of one operation. A root span with no environment or `production` keeps the plain operation name as its key, so production traffic shares the slot it had before. Ratio lookups keep the plain operation name. A project that sends several environments stores at least one boring trace per environment, operation and minute, where one slot used to serve all of them. [#558]

- **FR-011**: `span_edges` carries `environment TEXT NOT NULL DEFAULT ''` in its primary key `(step_seconds, bucket, project_id, environment, source, target, target_type)`. The accumulator takes the environment from the span that carries the call; compaction merges rows per environment. The bucket marker keeps an empty environment. Rows written before the migration carry `''`, which a `production` read also selects. The service map and the dependency list take `environment` and read it from `span_edges` and `span_rollups`; their raw-span fallback keeps the spans of the environment out of the newest spans it fetched. [#558]

- **FR-012**: `metric_rollups` and `metric_rollups_coarse` gain no `environment` column. OTLP resource attributes merge into each data point's label set at ingest (resource < scope < data point) and stamping writes `deployment.environment` there, so the label set, its fingerprint and the unique key already hold the environment. The default drop policy keeps `deployment.environment` at every tier, so compaction merges builds and pods of one environment and never two environments. A metric read reaches one environment through the label filter `deployment.environment=<value>`; the value is matched as stored, without the lowercasing and production-equals-empty rule that span reads apply. Tests: `TestMetricAccumulatorKeepsEnvironmentsApart`, `TestReduceKeepsEnvironmentsApart`.

- **FR-013**: `span_ledger` carries `environment TEXT NOT NULL DEFAULT ''` in its primary key `(bucket, project_id, environment)`. The tally counts every stage of a span under the environment its attributes name, lowercased, with `production` and no environment sharing the row with `''`, so a process that does not know environments still counts into the same row. `SpanLedgerRow.Environment` is omitted from the queue payload when empty. `GET /api/v1/span-ledger` takes `environment` and returns one entry per environment of each project and bucket. The loss watch adds the environments of a project and bucket before it judges them: an older image counts a span's `accepted` under the production row while the writer counts its exit under the span's own environment, so the rows of one environment do not balance on their own through a rollout, and their sum does. Rows written before the migration carry `''`. Tests: `TestTallyKeepsEnvironmentsApart`, `TestSpanLedgerKeepsEnvironmentsApart`, `TestSpanLedgerEnvironmentMigrationCopiesRows`, `TestSpanLedgerWatchJudgesEnvironmentsTogether`.

- **FR-014**: `slo_counts` gains no `environment` column. Its key is `(slo_id, bucket_start)`, so a count belongs to one SLO row, and an SLO is a settings row of one project whose own filter names what it counts. A merge repoints `slo_id` together with the SLO; two SLOs with one name in the canonical project are a settings collision handled in slice 3, and their counts stay with their own SLO. No two rows of `slo_counts` collide through a project merge.

- **FR-015**: `spanbarn project merge --source=SLUG --target=SLUG --env=NAME [--batch=N] [--min-idle=P]` moves the core family of the source into the target (slice 3a). The ingest keys move first, so new records arrive under the target while the rest moves; a key with an environment of its own keeps it and the others take `--env`. Then alerts, boards, saved queries, releases, SLOs, calculated fields, trace exclusions, pinned traces and the span ledger follow in batches of `--batch` rows, one transaction per batch, and `project_merges` (migration 049) gets the rows moved and the last id in the same transaction, so a stop at any point leaves counts equal to the rows and a second run moves what is left. Name clashes: an SLO or calculated field whose name the target uses moves with ` (<source slug>)` appended; a trace exclusion or pin the target already holds is dropped from the source (configuration, the target keeps the equal row); ledger rows of one bucket and environment add into the target's row. A source cannot merge into two targets or two environments, and a project cannot merge into itself (`ErrMergeConflict`). The command prints one line per batch and a table of moved and collided rows per table, and refuses to start when the host idle share read from `/proc/stat` is under `--min-idle` (default 35; a host that cannot read it is let through with a line saying so). The span, log, metric and prompt families, the archive and the name resolution of old links follow in slices 3b and 3c. Tests: `TestMergeProjectMovesCoreTablesAndRepointsKeys`, `TestMergeProjectSettingsCollisions`, `TestMergeProjectLedgerSumsCollisions`, `TestMergeProjectResumesAfterAStop`, `TestMergeProjectRefusesConflictingMerges`, `TestProjectMergeRepointsKeysOnAFile`, `TestProjectMergeIdleGate`. [#558]

## Slice 2b inventory: tables keyed on the project without an environment

| Table | Key today | Step |
|---|---|---|
| aggregates | unique `(project_id, service, operation, resource, kind, bucket)` | PR 1 (migration 10 of the spans track) |
| span_rollups | PK `(step_seconds, bucket, project_id, service, name, kind, is_root, status, http_status)` | PR 2 (migration 11 of the spans track) |
| span_rollup_compaction | step and bucket watermark, no project | none needed |
| span_edges | PK `(step_seconds, bucket, project_id, source, target, target_type)` | PR 3 (migration 12 of the spans track) |
| metric_rollups, metric_rollups_coarse | unique `(project_id, name, attr_fingerprint, bucket)` and the coarse key with `step_seconds` | PR 4: no migration, `deployment.environment` is a label in the key (FR-012) |
| slo_counts | unique `(slo_id, bucket_start)` | none needed (FR-014) |
| span_ledger | PK `(bucket, project_id)` | PR 5 (migration 048 of the main track, FR-013) |
| trace_summaries, error_exemplars | PK `(project_id, trace_id)` | a trace belongs to one environment; only a merge collision matters, handled in slice 3 |
| slos, calculated_fields, trace_exclusions, pinned_traces | unique name or id per project | settings; slice 3 collision rule |

Rollback caveat: migration 10 drops the old unique index on `aggregates`. A binary from before it upserts with `ON CONFLICT(project_id, service, ...)`, which SQLite refuses without a matching index, so a rollback past this migration stops aggregate writes. Aggregates back the usage stats and the fallback read, so the path is roll forward.

Rollback caveat, migration 11: it rebuilds `span_rollups` because a WITHOUT ROWID primary key cannot change in place (copy into `span_rollups_env`, drop, rename, one transaction). A binary from before it upserts with the old conflict target, which no longer exists, so span rollup writes fail after a rollback until the roll forward. The accumulator keeps unwritten slots for an hour and compaction waits on its watermark, so the path is roll forward. The cost is one read and one write of the table, which the tiers keep small next to spans.

Rollback caveat, migration 12: it rebuilds `span_edges` the same way (copy into `span_edges_env`, drop, rename, one transaction). A binary from before it upserts with the old conflict target, so span edge writes fail after a rollback until the roll forward, with the same one-hour requeue of the accumulator. Production holds 71,034 rows in `span_edges` inside a 1.25 GB spans file, so the copy reads a small table and finishes within the writer's startup probe many times over (`span_rollups` took 34 s).

Rollback caveat, migration 048: it rebuilds `span_ledger` with the environment in the key. A binary from before it upserts with `ON CONFLICT (bucket, project_id)`, which SQLite refuses without a matching unique index, so a rollback past this migration stops ledger writes (the ledger is the only table affected; ingest, storage and reads continue). `down048` folds the environments back into one row per bucket and project. The ledger keeps 14 days of 5-minute buckets, at most 4,032 buckets per project, so the copy is a few hundred thousand rows at most and runs in well under a second; the writer startup probe allows 605 s.

## Open Questions

- Decision 1 defers the per-table column to slice 2. Whether the filter needs one depends on the read timing from #457. [inferred]

- Collision sums for sketch-bearing rollups need the span_rollups and metric_rollups PRs of slice 2b before the merge of slice 3 is fixed. [inferred]

## Drift

| Item | Document says | Code does |
|---|---|---|
| #558 title | per-product projects exist | 47 projects, one per environment and spelling |
| Decision 5 and FR-007 | no event is deleted by a merge | holds for every event table; a merge deletes a duplicate configuration row (trace exclusion, pin) that the target already holds, and sums ledger tallies into one row |
| FR-002 (earlier draft) | every read API defaults to production | the API default stays unfiltered so current callers keep their reads; the web app defaults to production |
| Decision 1 (earlier draft) | `environment` column on every record table | slice 1 stamps the resource attribute instead; columns wait for slice 2 |
| Slice 2b | every rollup table has an environment column | `aggregates`, `span_rollups`, `span_edges` and `span_ledger` have it, the metric rollup tables hold it as a label, `slo_counts` needs none; the services, operations, timeseries, service map and dependency reads take `environment`; the dashboard cards and the trace groups take `environment` (FR-002); the metric reads take `environment` and `production` also selects series without the label (chief, 2026-11-17); the web list includes the dashboard and trace group paths |
| Floor key | one floor slot per (project, operation) | one slot per (project, environment, operation); `production` and no environment share the old key |
