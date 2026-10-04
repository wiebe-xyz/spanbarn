# Data Model

## Entity Relationship

```
Project 1──N APIKey
Project 1──N Span
Project 1──N Aggregate
Project 1──N ErrorSample
Project 1──N Alert

Span N──1 Trace (via trace_id)
Span N──1 Span (via parent_span_id, self-referencing)
```

## Tables

### projects

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER | PK, autoincrement |
| slug | TEXT | Unique, URL-safe |
| name | TEXT | Display name |
| created_at | DATETIME | |

### api_keys

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER | PK |
| project_id | INTEGER | FK → projects |
| name | TEXT | Human label |
| key_hash | TEXT | SHA256 hex |
| scope | TEXT | 'ingest' or 'full' |
| last_used_at | DATETIME | Nullable |
| created_at | DATETIME | |

### users

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER | PK |
| username | TEXT | Unique |
| password_hash | TEXT | bcrypt |
| created_at | DATETIME | |

### spans (recent, full fidelity)

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER | PK |
| project_id | INTEGER | FK → projects |
| trace_id | TEXT | 32-char hex |
| span_id | TEXT | 16-char hex |
| parent_span_id | TEXT | Nullable, 16-char hex |
| name | TEXT | Operation name |
| service | TEXT | Service name |
| resource | TEXT | Route/query/target |
| kind | TEXT | server, client, internal, producer, consumer |
| status | TEXT | ok, error, unset |
| start_time_us | INTEGER | Unix microseconds |
| duration_us | INTEGER | Duration in microseconds |
| attributes | TEXT | JSON object with flat dotted keys (`"url.path"`), so queries address them as `$."url.path"`. Attribute discovery (`GET /api/v1/attributes`) reads this column with `json_each`; there is no `span_attrs` side table (see `deploy/docs/attribute-storage-design.md`). |
| events | TEXT | JSON array (logs, exceptions) |
| ingested_at | DATETIME | |
| http_status | INTEGER | VIRTUAL generated from `attributes`: `http.response.status_code`, else `http.status_code`. NULL when absent or when `attributes` is not valid JSON. Never written by ingest. |

**Indexes:**
- `idx_spans_http_status` ON (project_id, ingested_at, http_status)
- `idx_spans_project_ingested` ON (project_id, ingested_at)
- `idx_spans_trace` ON (trace_id)
- `idx_spans_service_name` ON (project_id, service, name, start_time_us)
- `idx_spans_status` ON (project_id, status, ingested_at)

### trace_summaries (one row per trace, serves the trace list)

| Column | Type | Notes |
|--------|------|-------|
| project_id | INTEGER | PK part 1 |
| trace_id | TEXT | PK part 2 |
| root_name | TEXT | Name of the root span. Empty when the trace has no root. |
| root_service | TEXT | Service of the root span. Empty when the trace has no root. |
| root_duration_us | INTEGER | Duration of the root span. 0 when the trace has no root. |
| start_time_us | INTEGER | Earliest span start |
| span_count | INTEGER | Stored spans of the trace, recomputed from `spans` on every write and after eviction |
| has_error | INTEGER | 1 when any span has status error |
| ingested_at | DATETIME | Earliest span ingest time; retention key |
| expires_at | DATETIME | Set for boring-sampled traces, NULL otherwise |
| has_root | INTEGER | Migration 035. 1 when a span with no parent exists, 0 when none does, NULL until computed. |
| orphan_count | INTEGER | Migration 035. Spans whose `parent_span_id` matches no `span_id` in the same trace. NULL until computed. |

**Structure columns.** `has_root`, `orphan_count` and `span_count` are recomputed from `spans` (two seeks of `idx_spans_trace` per trace) inside the transaction that writes spans, so a late parent or a late root corrects the row. Retention does the same for error traces that lose part of their spans (`DeleteSpansByMaxIDRefreshing`); non-error summaries are deleted together with their spans. When no root exists the root fields are cleared.

**Backfill for rows from before migration 035.** The migration only adds nullable columns and runs no statement over `spans`, because a bulk backfill on a multi-GB single-writer database would hold the write connection (the failure that led migration 032 to skip its backfill). Existing rows read NULL. The retention worker calls `BackfillTraceStructure` every cycle: batches of 200 summaries, one low-priority write transaction per batch, at most 20,000 per cycle, reading NULL rows through the partial index `idx_trace_summaries_unchecked`. A summary whose spans are already gone settles from its own `root_name`. NULL rows match neither `has_root = 0` nor `orphan_count > 0`, so filters never report a guess.

**Indexes:**
- `idx_trace_summaries_list` ON (project_id, ingested_at DESC)
- `idx_trace_summaries_expires` ON (expires_at) WHERE expires_at IS NOT NULL
- `idx_trace_summaries_unchecked` ON (project_id, trace_id) WHERE has_root IS NULL

### saved_queries (named trace filters)

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER | PK |
| project_id | INTEGER | FK → projects |
| name | TEXT | |
| service, operation, status, min_duration_us | | Legacy fixed fields from migration 006. Kept for rollback and for clients that still send only these. |
| filters | TEXT | Migration 036. The shared filter model as JSON, `''` when the query has no filter. |
| definition | TEXT | Migration 037. The rest of a board query as JSON (see Boards), `''` for a plain trace filter. |
| created_at | DATETIME | |

Migration 036 adds `filters` and fills it from the four legacy fields: `service` becomes `service = v`, `operation` becomes `name = v`, `status` becomes `status = v` and `min_duration_us` becomes `duration_us >= v`, joined by `and`. The API does the same for a create request that sends only the legacy fields.

**Filter model** (`internal/filter`, one JSON form for the API `filter` query parameter, `saved_queries.filters` and the `filter` parameter of the web URL):

```json
{"match":"and","filters":[
  {"key":"kind","op":"=","value":"server"},
  {"match":"or","filters":[
    {"key":"url.path","op":"starts-with","value":"/api/v1/library"},
    {"key":"http.response.status_code","op":">=","value":500}]}]}
```

- The root is a group. A group holds conditions and at most one level of nested groups. At most 32 conditions, 100 values per `in`.
- Operators: `=`, `!=`, `>`, `<`, `>=`, `<=`, `contains`, `starts-with`, `exists`, `does-not-exist`, `in` and `not-in` (`values` list).
- A key names a span column (`service`, `name` or `operation`, `kind`, `status`, `resource`, `trace_id`, `span_id`, `parent_span_id`, `duration_us`, `start_time_us`, `http_status`) or an attribute. Attributes are flat dotted keys read with `json_extract(attributes, '$."key"')`. The prefix `attributes.` forces an attribute when its name equals a column.
- `>`, `<`, `>=`, `<=` compare numbers. On an attribute they apply to JSON numbers only. `contains` and `starts-with` are case sensitive. `!=` and `not-in` also match spans without the attribute.
- Spans whose `attributes` is not valid JSON never raise an error. They match `does-not-exist`, `!=` and `not-in`.
- A time range (`from`) is required with a filter, so the scan stays inside a window. On the span list a span matches. On the trace list a trace matches when one of its spans satisfies the whole expression, and the trace row still summarises every span of the trace.
- Attribute predicates read the JSON of each span in the window (the `json_extract` storage decision in `deploy/docs/attribute-storage-design.md`). Column predicates and `http_status` can use indexes.

### Group-by queries (no schema change)

`GET /api/v1/analyze` and `GET /api/v1/analyze/series` (`internal/repository/repo_analyze*.go`, `internal/service/query_analyze*.go`) compute calculations per group over the `spans` table. They add no table, column or migration. The filter model selects the spans, and group keys use the same key resolution as filters (span column or attribute, read as text, a missing key groups as `''`).

- Calculations: `count`, `error_rate` (fraction of spans with status `error`), `sum_duration`, `avg_duration`, `max_duration`, `p50`, `p95`, `p99` (nearest rank over `duration_us`, computed with window functions per group) and `count_distinct:<attribute>`. Durations are microseconds.
- Group by 0 to 4 keys. A table query returns the top `limit` groups (default 20, at most 100) ordered by one calculation, plus one other row that aggregates every group beyond the cap. Each group row carries a `drill` filter (the request filter AND one `=` or `does-not-exist` per key) that selects exactly that group on the trace and span lists. The other row has none.
- A series query takes one calculation and returns it per bucket for the top groups (default 5, at most 10) and an other line. It reuses the sample ratio of the table query so both views agree. The bucket defaults to the smallest of 1m, 5m, 15m, 30m, 1h, 3h, 6h, 12h, 1d that gives 48 buckets or fewer, at most 500 buckets.
- `project_id`, `from` and `to` are required and the range is at most 30 days. The scan reads at most `max_spans` spans (default 200,000, at most 500,000), newest first. `sample` is 1 in N (1 to 1000). With `sample` unset the repository counts matching spans up to the cap. When they exceed it, it estimates the match from a sampled count and picks the smallest ratio that fits (`id % N = 0`). Counts and sums are multiplied by N in the response. Percentiles, maxima and distinct counts are not scaled and describe the sample. The response reports `scanned`, `sampleEvery`, `truncated` and `maxSpans`.
- Cost: each query reads the attribute JSON of up to `max_spans` spans through `idx_spans_project_ingested`, the same cost class as attribute discovery.

### boards, board_panels and releases

Migration 037 adds the tables behind boards: a project's ordered grid of query panels, each drawn as a chart or a table.

**boards**

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER | PK |
| project_id | INTEGER | FK → projects |
| name | TEXT | At most 120 characters |
| time_range | TEXT | The shared range of every panel: `1h`, `24h` (default), `7d` or `30d`, all inside the 30 day limit of a group-by query |
| refresh_seconds | INTEGER | 0 (off), 30, 60, 300 or 900 |
| created_at, updated_at | DATETIME | |

**board_panels**

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER | PK |
| board_id | INTEGER | FK → boards |
| saved_query_id | INTEGER | FK → saved_queries. The query this panel runs |
| title | TEXT | At most 200 characters |
| view | TEXT | `table` or `chart` |
| position | INTEGER | Order in the grid, 0 first |
| created_at | DATETIME | |

**releases**

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER | PK |
| project_id | INTEGER | FK → projects |
| version | TEXT | At most 120 characters |
| released_at | DATETIME | Drawn as a dashed vertical line on chart panels in range |
| created_at | DATETIME | |

Indexes: `idx_boards_project` ON (project_id), `idx_board_panels_board` ON (board_id, position), `idx_board_panels_query` ON (saved_query_id), `idx_releases_project_time` ON (project_id, released_at).

**Query definition.** A panel's query is a `saved_queries` row. `filters` (migration 036) holds the filter model and `definition` (migration 037) holds the rest as JSON, with the names of the group-by endpoint parameters:

```json
{"groupBy":["url.path"],"calcs":["count","p95"],"orderBy":"p95","asc":false,"limit":20,"sample":0,"chartCalc":"p95"}
```

`calcs` has 1 to 9 entries and `groupBy` at most 4 distinct keys, both validated like a `/api/v1/analyze` request. `orderBy` and `chartCalc` name one of `calcs`. `chartCalc` is the calculation a chart panel draws and defaults to the first. The time range is not part of the definition. It belongs to the board, so every panel of a board shares one range and one refresh interval. A panel runs its definition against `/api/v1/analyze` (table) or `/api/v1/analyze/series` (chart) in the window `[now - range, now]`.

- "Save to board" (`POST /api/v1/boards/{id}/panels`) inserts the `saved_queries` row and the `board_panels` row in one transaction, and the query takes the board's project. Rows from before migration 037 keep `definition = ''` and are plain trace filters.
- Deleting a panel or a board also deletes the board queries (`definition != ''`) that no panel uses any more. A plain trace filter is never deleted this way. Deleting a saved query deletes the panels that point at it. Deleting a project deletes its panels, boards, releases and saved queries.
- A board holds at most 50 panels. Reordering takes the full list of panel ids.
- Releases are recorded with `POST /api/v1/releases` (a version and an optional time, default now) from the board page or a script with a session. Nothing derives them from `service.version` on spans.
- The tables are small (a handful of rows per project), so no backfill or special retention applies.

### calculated_fields (named expressions)

Migration 040. A calculated field is a named expression over span columns and attributes that a filter or group-by uses like a normal key.

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER | PK |
| project_id | INTEGER | FK → projects |
| name | TEXT | 1 to 64 characters of `[A-Za-z0-9_.]`, not starting with a digit. Unique per project. Not a span column (`duration_us`, `name`, `operation` ...) and not starting with `attributes.` |
| expression | TEXT | At most 500 characters, parsed by `internal/calcfield` |
| created_at, updated_at | DATETIME | |

Index: unique `idx_calculated_fields_project_name` ON (project_id, name). A project holds at most 50 fields.

**Grammar.** Closed. Literals (`12`, `1.5`, `'text'` with `''` for a quote), identifiers (`http.route`, or `` `app.user-id` `` in backticks), `+ - * / %`, `= == != <> < <= > >=`, `and or not`, parentheses and four functions: `coalesce(a, b, ...)`, `if(cond, a, b)`, `concat(a, b, ...)`, `lower(a)`. `/` is real division and a zero divisor gives NULL. A missing attribute is NULL, `concat` reads it as an empty string. Caps: 500 characters, 16 levels of nesting, 200 terms, 8 arguments per call, and 2000 terms after other fields are expanded into it.

**Resolution.** A key in a filter, group-by or `count_distinct` resolves in this order: the `attributes.` prefix (an attribute), a span column, a calculated field of the query's project, an attribute of that name. A field may use other fields. A cycle is rejected on write and, for a row stored by other means, fails the queries that use it with a 400, never a NULL. A field named like an attribute replaces it for filters and group-bys. The expression reads the attribute with `attributes.<name>`. Fields are read once per query for the query's project, so a field never applies to another project.

**No user text reaches SQL.** Literals and attribute paths are bound parameters. Operators and functions come from a fixed table. The lexer refuses every character outside the grammar (double quotes, semicolons, backslashes, NUL). The compiler emits `(0 - x)` for negation so generated SQL never contains `--`.

**Values.** A calculated number compares as a number (`= 2` matches 2 and 2.0), reads as text for `contains`, `starts-with`, `in` and group labels, with a whole number written without a fraction (`3`, not `3.0`).

### aggregates (long-term metrics)

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER | PK |
| project_id | INTEGER | FK → projects |
| service | TEXT | |
| operation | TEXT | Span name |
| resource | TEXT | Route, query, target |
| kind | TEXT | server, client, etc. |
| bucket | DATETIME | Truncated to interval |
| count | INTEGER | Total span count |
| error_count | INTEGER | Spans with status=error |
| p50_us | INTEGER | 50th percentile duration |
| p95_us | INTEGER | 95th percentile duration |
| p99_us | INTEGER | 99th percentile duration |
| max_us | INTEGER | Maximum duration |
| sum_duration_us | INTEGER | Sum for mean calculation |

**Indexes:**
- `idx_agg_lookup` ON (project_id, service, operation, bucket)
- `idx_agg_bucket` ON (project_id, bucket)

### error_samples (medium-term error/slow spans)

Same schema as `spans` table, but:
- Only contains spans where status=error OR duration > slow threshold
- Retained for `RETENTION_ERROR_DAYS` instead of `RETENTION_FULL_HOURS`
- Separate table to avoid complicating retention queries

### alerts

| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER | PK |
| project_id | INTEGER | FK → projects |
| service | TEXT | |
| operation | TEXT | |
| type | TEXT | 'latency' or 'error_rate' |
| threshold | REAL | ms for latency, fraction for error_rate |
| comparison_window | INTEGER | Number of previous buckets to average |
| cooldown_minutes | INTEGER | Min time between alerts |
| webhook_url | TEXT | Nullable |
| email | TEXT | Nullable |
| enabled | INTEGER | Boolean |
| last_triggered_at | DATETIME | Nullable |
| created_at | DATETIME | |

## Aggregation Algorithm

1. Retention worker runs every 5 minutes
2. Select spans older than `RETENTION_FULL_HOURS` not yet aggregated
3. Group by (project_id, service, name, resource, kind, bucket)
4. For each group:
   a. Compute count, error_count
   b. Compute percentiles using sorted duration list (exact for small groups, t-digest for large)
   c. INSERT OR UPDATE aggregate row
5. Copy error/slow spans to error_samples table
6. DELETE aggregated spans from spans table
7. DELETE error_samples older than `RETENTION_ERROR_DAYS`
8. DELETE aggregates older than `RETENTION_AGGREGATED_DAYS`
