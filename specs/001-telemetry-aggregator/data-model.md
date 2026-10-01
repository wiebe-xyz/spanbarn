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
