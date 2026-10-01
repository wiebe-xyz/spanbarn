# Attribute storage design (issue #195)

Decision record for how SpanBarn stores and queries span attributes on one SQLite file.

**All numbers come from a synthetic database built locally.** Nothing was run on or copied from production. The generator is a SQL script (recursive CTEs with a multiplicative hash) and is not in the repo. Cardinalities and sizes are plausible, not measured from real traffic. Use the numbers for ratios between layouts, not as absolute production timings.

## Decision

**Attribute storage: `json_each` / `json_extract` with sampling.** No `span_attrs` side table for now.

Group-bys and filters from #190 to #194 read `spans.attributes` directly. Group-bys over windows longer than 24h read a deterministic 1-in-N sample (`id % N = 0`). The side table stays on the shelf with a written trigger (below).

| Step | Decision | Reason in one line |
|---|---|---|
| 1. Time-partitioned files | Defer | Query time is unchanged; the win is retention, and spans are the smaller cost at current volume |
| 2. Attributes out of the JSON blob | Defer, with trigger | 7x faster at 5M spans but 2.3x write cost and +42% bytes per span |
| 3. Rollups at ingest | Defer | Depends on step 2 |
| 4. Reader/writer split on sealed files | Drop | Depends on step 1; the Litestream replica half no longer exists |
| 5. DuckDB or ClickHouse | Drop | Worst measured query is 6 s warm at 5M spans, no second service needed |

## Test database

- Schema: the repo migrations, applied with `repository.Migrate`, all 12 span indexes in place (indexes built after the bulk load).
- 5,000,000 spans over 7 days (0.12 s apart), 3 projects, about 600 B of JSON attributes per span: `url.path` (about 1,000 distinct, skewed), `http.request.method`, `http.response.status_code` (5 values, 2% errors), `user_agent.original` (40 values), `client.address` (50,000 distinct), plus 5 constant keys.
- 3,000,000 logs, 1,500,000 metric points, 500,000 `metric_rollups` rows.
- Spans carry flat dotted keys (`"url.path"`). The path in the issue text, `$.url.path`, would address a nested object, so queries use `$."url.path"`. Check this against the attribute shape the #190 work settles on.
- Queries filter on project 1 (a third of the data). Slices are 1h, 24h and 7d ending at the newest row: about 10k, 240k and 1.67M spans.
- Machine: a shared Mac running CI runners, load average 40 to 80 during the runs, so wall times are noisy. Each query ran twice. "Cold" is the first run, "warm" the second. Warm figures are the usable ones; user CPU is listed where wall time is distorted.

## Sizes (`dbstat`, 6.7 GB file)

| Table with its indexes | Size | Share |
|---|---|---|
| spans (table 2,798 MB + 11 indexes 1,684 MB) | 4,482 MB | 67% |
| logs (table 904 MB + 3 indexes 490 MB) | 1,394 MB | 21% |
| metrics (table 235 MB + 2 indexes 165 MB) | 400 MB | 6% |
| metric_rollups | 147 MB | 2% |

About 900 B per span including indexes, 465 B per log, 270 B per metric point. The 12 span indexes add 60% to the span table.

Production holds about 75k spans (issue text), which at 900 B each is roughly 70 MB. Most of the 3.2 GB there is therefore logs, metrics and rollups. Attribute query design matters for spans as volume grows. Today's file size is a logs and metrics problem, and it is covered by the rollup tiers and retention work.

`dbstat` over this 6.7 GB file took 18 s. Do not repeat the scan on the live production pod.

## Group-by on `url.path`, project 1

`select json_extract(attributes,'$."url.path"') p, count(*) from spans where project_id=1 and ingested_at >= ... group by p`

| Layout | 1h cold / warm | 24h cold / warm | 7d cold / warm |
|---|---|---|---|
| Single file, `json_extract` | 0.21 / 0.017 s | 4.3 / 1.1 s | 22.5 / 6.2 s |
| Single file, `json_extract`, 1-in-20 sample | n/a | 2.4 / 0.045 s | 13.5 / 0.63 s |
| Side table `span_attrs`, covering index | 0.003 / 0.003 s | 0.16 / 0.15 s | 0.92 / 0.90 s |
| Per-day files (8 files) | 0.02 / 0.02 s | 0.79 / 0.76 s | 18.0 / 5.7 s |

Notes:

- The sample reads only the 5% of table rows whose id matches. SQLite defers the table seek until a column is needed, so the `id % 20` test runs on the index entry. Top-5 path counts from the sample, scaled by 20, came within 0.4% to 4.4% of exact (resource/0: 14,960 vs 15,100; resource/4: 9,420 vs 9,025; the order of the 4th and 5th swapped).
- The side table is `span_attrs(project_id, key, ts, value, span_id) WITHOUT ROWID` with primary key `(project_id, key, ts, span_id)`, plus a secondary index `(project_id, key, value, ts)`. The literal shape in the issue, `(span_id, key, value)` with an index on `(key, value)`, has no time column, so a time-sliced group-by would have to join `spans`. It was not measured. Putting `ts` into the key makes the group-by a covering range scan.
- Per-day files use a reduced index set (project plus time, trace, span id). Group-by cost matches the single file because the same rows are parsed. The 1h and 24h cases gain only from the smaller index set.
- Key discovery with `json_each`: 1h exact 0.03 s, 24h at 1-in-20 0.065 s, 7d at 1-in-20 0.63 s (all warm).

## Filter on an attribute value, 7d

`http.response.status_code = 500` (33,331 matching spans in project 1):

| Layout | Cold / warm |
|---|---|
| `json_extract(...) = 500` on `spans` | 3.0 / 2.4 s |
| `span_attrs` with `(project_id, key, value, ts)` index | 0.001 s |

A sample cannot answer this. A filter must return every matching span, so filters have no sampling fallback and read the JSON column. They run under the trace list's limit and pagination, which stops the scan early when matches are common. The slow case is a rare value over a long window.

## Write cost

| Operation | Time |
|---|---|
| Insert 50,000 spans with all 12 indexes | 0.7 s (14 us per span) |
| Insert 200,000 `span_attrs` rows (50,000 spans x 4 keys, two b-trees) | 1.7 s (33 us per span) |

Four promoted keys cost 2.3x the span insert itself, in an in-memory-cached synchronous=OFF microbenchmark. The side table holds 20M rows in 1.8 GB (376 B per span), which is +42% on top of the 900 B per span already stored. The write path already hit throughput limits from index maintenance (see the writer cache incident), so write amplification counts against it.

## Retention

Deleting the oldest 770,000 spans (about 15%) from the single file: 18 s wall (6.7 s user, 4.2 s system), single transaction, no concurrent writer. The file stayed at 6.8 GB. The freelist held 179,148 of 1,662,249 pages (11%). Space comes back only for reuse by new rows. Unlinking a day file of 320,000 spans: 0.03 s, the whole file reclaimed.

This is the real argument for step 1. It saves delete CPU and gives space back without a vacuum. It does nothing for query speed.

## Per-step reasoning

### 1. Time-partitioned files: defer

- Attribute group-by gets no faster (7d: 5.7 s vs 6.2 s warm). The scan parses the same JSON.
- Retention gets cheaper (18 s for 770k rows vs 0.03 s). At current production volume (75k spans) there is nothing to save. The delete churn that hurt production came from logs, metrics and rollups.
- Cost: every reader fans out over files, trace lookup by `trace_id` has to probe up to N files, and `trace_summaries`, `aggregates`, `saved_queries` and the settings tables need a home that is not partitioned. That is a rewrite of the repository layer.
- Revisit when: span retention deletes exceed 60 s per cycle, or the file cannot be shrunk without a multi-hour vacuum. Logs are the better first candidate. They are 21% of the file, append-only, with no joins to other tables, and queried by time range.

### 2. Promoted attributes or side table: defer, with trigger

- Wins: 7d group-by 6.2 s to 0.9 s, 24h 1.1 s to 0.15 s, rare-value filter 2.4 s to 1 ms.
- Costs: 2.3x write time per span for four keys, +42% bytes, a bounded per-project promoted key list to configure, and backfill for old spans.
- The sampled scan reaches 0.63 s at 7d and 0.045 s at 24h for group-bys with no schema change. That covers the dashboard use in #190 to #194 at 5M spans, which is about 70 times the current production span count.
- Trigger to build it: a 24h attribute group-by above 2 s warm on production, or an attribute filter needed on a window where the scan exceeds 5 s. Build `span_attrs` with `ts` inside the key, bounded to a configured key set, written in the same transaction as the span batch.

### 3. Rollups at ingest: defer

Boards (#193) read the sampled scan until step 2 lands. Exact precomputed rows come with the promoted key set, since they group by the same keys.

### 4. Reader/writer split on sealed files: drop

It depends on step 1. Its replica half relied on Litestream, which was removed (see `disaster-recovery.md`). Reader pods already read the same volume read-only. Reopen if step 1 is built.

### 5. Analytics engine: drop for now

Worst measured query at 5M spans is 6 s warm on a loaded machine, and 0.9 s with the side table. A second service (DuckDB over Parquet, ClickHouse) buys nothing at this volume and costs operations work.

## Guidance for #190 to #194

- Read attributes with `json_extract(attributes, '$."<key>"')` (flat dotted keys) and key discovery with `json_each`.
- Group-by endpoints over windows above 24h take a sample rate and filter on `id % N = 0`. Return the rate in the response so the UI can label the figures as estimated.
- Exact filters read the JSON column and rely on the existing `project_id, ingested_at` index for the time bound.
- Keep the attribute access behind one repository function per query shape, so switching to a side table does not touch handlers.

## Caveats

- Synthetic data has uniform arrival, no diurnal pattern, and no wide outlier attribute values. Real attribute blobs vary more in size, which moves the JSON parse cost.
- SQLite page cache and the OS cache were warm for the second run. Production readers use the configured cache and mmap sizes, which were not replicated here.
- Writes were measured without a concurrent reader or checkpointer.
- Nothing here measures the logs and metrics query paths beyond their stored size.
