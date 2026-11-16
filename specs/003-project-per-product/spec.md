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

1. **Environment is a column, not an attribute lookup.** `environment TEXT NOT NULL DEFAULT ''` is added to the project-scoped record tables (spans, trace summaries, aggregates, logs, metric points and rollups, prompts, error samples). `ADD COLUMN` with a constant default is a metadata change in SQLite, so no table rewrite. The reason: an attribute JSON filter has no index and the UI reads are already 2 to 4.5 s ([#457](https://github.com/wiebe-xyz/spanbarn/issues/457)).
2. **An ingest key carries an environment.** `api_keys.environment TEXT NOT NULL DEFAULT ''`. Ingest stamps the record from, in order: the record's own `deployment.environment` resource attribute, then the key's environment. An empty result stays empty and reads as `production` in the default filter.
3. **A merge repoints; it does not copy.** `spanbarn project merge --source=A --target=B --env=E` updates `api_keys` of A to project B with environment E, then moves A's rows to B in resumable batches (`UPDATE ... SET project_id=B, environment=COALESCE(NULLIF(environment,''),E) WHERE project_id=A LIMIT n`), table by table and shard by shard. Rows keep their ids. The merge stores its cursor in a `project_merges` table, so a restart resumes.
4. **Unique keys on merge.** Tables with a unique key over `project_id` (aggregates, rollups, trace summaries) get `environment` in that key. Where two sources collide on the same bucket, the merge sums counts and merges sketches through the existing rollup merge path rather than dropping a row. The inventory of unique keys is the first task of slice 2.
5. **Sources are archived, never deleted.** After the last batch a source with zero rows gets `archived_at`. Archived projects stay out of the default list and keep their id, so old links resolve to the target through `project_merges`.
6. **Names.** `project create` and the create API refuse a slug ending in `-prod`, `-production`, `-staging`, `-stage`, `-test`, `-testing`, `-e2e` or `-dev`, and a slug starting with `rr-`, with an error that names the environment field. Existing names are untouched until merged.
7. **Target per row.** Per-site projects fold into one project per site, named after the site (`languagetrainer`, `main`, `places`, `scherpstel`, `webwiebe`), with `service.name` as the service dimension. The rapid-root API sources fold into `rapid-root`. A product with several services keeps one project; services use the existing service dimension.

## Slices

| Slice | Content | Ships when |
|---|---|---|
| 1 | Migration with the `environment` columns, `api_keys.environment`, ingest stamping, name guard | tests prove a key with `environment=staging` stamps spans, logs and metrics |
| 2 | Environment filter on every read API and the UI (default production), environments listed per project, unique-key inventory and the new keys | tests prove one project with two environments reads separately and together |
| 3 | `project merge`, `project_merges` cursor, archive, counts per table in the log | tests prove resume after a kill, key repoint and collision sums on a real SQLite file |
| 4 | Production merges per the table in #558, counts logged on the issue, one source at a time, smallest first, host idle gate before each | production project list matches the rule |

## Functional Requirements

- **FR-001**: Ingest stamps `environment` on every stored record from the record's own `deployment.environment`, else the key's environment. [#558]
- **FR-002**: Every read API takes `environment` (default `production`, `all` for no filter) and returns the environments a project holds. [#558]
- **FR-003**: `spanbarn project merge` moves all of a source's rows and keys into the target without a key change for the site, and is safe to run again after an interruption. [#558]
- **FR-004**: A merge logs, per table, the row count moved, and refuses to start when the host idle gate is not met. [#558]
- **FR-005**: A source project with zero rows after a merge is archived and resolves to its target. [#558]
- **FR-006**: Creating a project with an environment suffix or an `rr-` prefix is refused. [#558]
- **FR-007**: No event is deleted by a merge or by the archive. [#558]

## Open Questions

- Collision sums for sketch-bearing rollups need the inventory of slice 2 before the merge of slice 3 is fixed. [inferred]

## Drift

| Item | Document says | Code does |
|---|---|---|
| #558 title | per-product projects exist | 47 projects, one per environment and spelling |
