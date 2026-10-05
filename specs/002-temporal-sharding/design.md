# Design: split and time-shard the SQLite database

Issue: #235. Measurements: the results comment on #235, produced by `cmd/shardspike` (PR #241).

## Decision

Two steps, each shippable on its own:

1. **Functional split.** Spans move to their own file, `spans.db`, next to the main database. Everything span-shaped that is written in the same transaction as spans moves with them: `spans`, `trace_summaries`, `spans_staging`, `aggregates` and `error_samples`.
2. **Time shards for the heavy tables.** `logs`, `metrics` and `prompt_records` move to shard files that rotate by time. Retention for them becomes "delete the oldest file".

Spans are not time-sharded. The spike measured trace list going from 0.2 ms to 10 to 53 ms and analyze running 1.5 to 2.9x slower through `UNION ALL` views, and spans account for 7% of the row-delete cost. They stay in one file under today's row retention (48h default).

Everything else (projects, users, settings, alerts, SLOs, boards, `metric_rollups*`, pinned traces, sessions) stays in the main file, which keeps its path, its migrations and the settings snapshot.

## What the spike measured

| finding | L1 single | L2 split | L3 daily |
|---|---:|---:|---:|
| trace list p50, idle | 0.19 ms | 0.21 ms | 10 ms |
| analyze 24h p95, under write load | 381 ms | 271 ms | n/a |
| heavy insert p99, under write load | 1458 ms | 101 ms | n/a |
| metric series 24h p50, idle | 1028 ms | 548 ms | 352 ms |
| expire one day: wall time | 49 s | n/a | 0.18 s |
| expire one day: bytes freed | 0 | n/a | 506 MiB |
| expire one day: insert p99 meanwhile | 1027 ms | n/a | 6.8 ms |

The run shared the CI host at load 73 to 87, so differences under ~30% are unconfirmed.

## File layout

```
$SPANBARN_DB_PATH                 main: config, rollups, aggregates of record, kept_logs
$SPANBARN_DB_PATH.spans           spans, trace_summaries, spans_staging, aggregates,
                                  error_samples
$SPANBARN_DB_PATH.d/
    logs-20261004.db              daily
    metrics-20261004.db           daily
    prompts-2026w40.db            ISO week
```

Paths derive from `SPANBARN_DB_PATH`, so the Helm chart, the PVC and the backup job need no new settings. One volume holds all files, so the disk ladder keeps measuring one filesystem.

## Design questions

### Which layout

Split first (step 1), then shard the heavy tables (step 2). Step 1 is two fixed files with no rotation and no traces crossing a file boundary. It delivers the read-tail and heavy-insert gains measured under load. Step 2 delivers the retention and reclaim gains. The spike gave no reason to time-shard spans.

### Two writers

Each file gets its own write connection and its own `writescheduler.Scheduler`. The repository holds one write handle per file family and routes each method to its family. `execLow` and `execHigh` take the family as an argument. Admin writes all land in main, so the high-priority path keeps one queue.

Cross-file atomicity is avoided by placement. Every transaction the code runs today touches one family:

- span insert, trace structure refresh, staging promote, aggregate-then-delete: all in `spans.db`. This is why `aggregates` moves with spans: the retention cycle aggregates spans and deletes them in one transaction, and SQLite commits attached WAL databases separately, so a crash between the two files could double-count.
- error-sample copy: `error_samples` lives in `spans.db`. Retention copies error and durable spans into it before deleting them, and the trace-structure refresh reads it.
- log, metric and prompt inserts: one table each, one shard each.
- per-project trace eviction (`EvictProjectTracesOlderThan`) picks victims and deletes the span-family rows in one `spans.db` transaction, then deletes the trace's logs and prompt records with one write each. A crash in between leaves those rows to their own retention.

A rule enforces this going forward: a repository method that writes may touch one family. Every family writer attaches the other files read-only, so a misrouted write fails with "attempt to write a readonly database". CI runs the repository tests a second time with `SPANBARN_TEST_LAYOUT=split`, which puts each family in its own file.

Two places read across families inside a write and need care:

- `DeleteLogsOlderThan` exempts logs whose trace is pinned (main) or error-sampled (`spans.db`). It runs on the logs writer, which attaches the other files read-only, so the exemption still reads both. Step 2 replaces this delete (see "Error-trace logs" below).
- Any future cross-family consistency goes through an idempotent job with a watermark, the pattern `metric_rollup_compaction` already uses.

### Granularity

| family | shard | default retention | files at default |
|---|---|---|---|
| logs | daily | 24h (error-trace logs 30d, see below) | 2 |
| metrics | daily | 7d | 8 |
| prompts | ISO week | 30d | 5 to 6 |

The disk ladder (`retention/pressure.go`, `Tier.Apply`) shortens logs and metrics to hours at "elevated". A daily file cannot express a 4h window, so the ladder keeps row deletes inside the newest shard for windows shorter than a shard, and drops whole files for everything older. Row deletes inside one daily file touch a fraction of today's table, and the freed pages are reused by the same day's inserts.

Hourly shards were rejected: 7 days of hourly metrics is 168 files.

### Cross-shard queries

The modernc build refuses the 11th `ATTACH`. Each family therefore gets its own read pool, and that pool attaches only its family's shards: at most 9 files at default retention (8 metrics days plus the current day). A driver connection hook (`sqlite.RegisterConnectionHook`, available in modernc v1.50.0) attaches the shards and creates the TEMP `UNION ALL` views on every new pooled connection, so the pools keep `MaxOpenConns > 1`. The spike capped its pools at one connection because it set up attachments with `Exec`, which reaches one connection only.

If a configured retention needs more than 9 files in one family (metrics at 14 days, say), the shard manager widens that family's period to two days. The rule is computed at startup: `period = ceil(retention / 9)`.

The spike's plans show index SEARCH per shard through the views, and the heavy reads were equal or faster on L3. The views lose `ORDER BY ... LIMIT` pushdown, which cost trace list its speed. `QueryPromptRecords` therefore asks each shard, and main, for its own newest `offset + limit` rows along its `ingested_at` index and merges them in Go. The logs list keeps the view: it sorts by event time, which no shard boundary follows, and its total count reads every shard anyway.

`INDEXED BY` in `repo_dashboard.go` and `repo_trace_structure.go` names span indexes. Spans stay in one real table in an attached file, where `INDEXED BY` works unchanged. No heavy-table query uses `INDEXED BY` today, and views reject it, so a test asserts none is added for a sharded family.

In step 1, readers attach `spans.db` and rely on unqualified-name resolution (temp, main, then attached in order). Main must not hold a table named `spans` after cut-over, or it shadows the attached one. The cut-over drops them.

### Traces across a boundary

Spans are not sharded, so `GetTraceByID`, `trace_summaries` and `RefreshTraceStructure` keep working on one table. Issue #169 is unaffected.

Logs of one trace can land in two daily files around midnight. Logs by trace reads through the family view, which covers every shard, so the query returns both halves.

### Error-trace logs and pinned traces

Today logs of error-sampled or pinned traces outlive the 24h log window (30 days). A daily logs file is deleted whole, so before the writer retires a logs shard it copies those logs into `kept_logs` in main (migration 043, same columns as `logs`). The copy attaches the shard read-only on main's queue and uses the predicates the row delete used: the trace is pinned (`pinned_traces`, main) or error-sampled after the error-log cutoff (`error_samples`, attached from the spans file). `kept_logs` expires by row with the error-log cutoff in `DeleteLogsOlderThan`. The logs view unions `logs` from the daily files and main with `main.kept_logs`.

`kept_logs` lives in main and `spans.db` holds none of it: the logs read pool opens on main, so the view reads `kept_logs` with no extra attachment, and a reader opens one file fewer.

If the process dies between the copy and marking the shard retiring, the next cycle copies again. `kept_logs` keeps the source id as its primary key (shard ids are seeded per day, so they do not collide) and the copy is `INSERT OR IGNORE`, so the second copy inserts nothing.

### Migrations

Three migration tracks, each with its own goose version table:

- main: the existing track (`goose_db_version`), minus the moved tables after cut-over. Main-track migrations no longer touch span-family tables; `TestMainTrackLeavesSpanTablesAlone` fails if one does.
- spans (`goose_spans_version`, `repository.MigrateSpans`): `spans`, `trace_summaries`, `spans_staging`, `aggregates`, `error_samples` and their indexes. It lives in whichever file holds the family: the spans file in the split layout, main in the single-file layout. Its migration 1 is the baseline: it builds a scratch database to main-track version 41, reads the span-family DDL back and runs it with `IF NOT EXISTS`, so on a single-file database it changes nothing. Spans migrations run inside goose's transaction (`GoFunc.RunTx`), because goose pins the handle's only connection.
- shard: one track per family. A new shard file is created at the current version of its track. On startup the writer migrates every live shard; old shards of a family that only adds an index can be left behind, because they expire within the retention window.

### Layout detection (step 1)

`repository.OpenStorage` decides the layout once, before migrating, from main alone: the split layout when main has no `spans` table (a new install, a restored settings snapshot, or after the cut-over), the single-file layout when main holds `spans`. For a split database it creates and migrates the spans file first (with `auto_vacuum=INCREMENTAL`), then migrates main and drops the empty span tables main's migrations created. Readers (`repository.OpenReadDB`) decide per connection, when it opens: attach the spans file unless main holds `spans`. A reader that starts before the writer has created the files fails to open connections until they exist, and recovers without a restart.

### Writer and readers

The writer owns shard lifecycle: it creates the next period's file ahead of midnight (or the week boundary), and it deletes expired files. A `shards` table in main lists every shard with family, period, path and state (`active`, `retiring`).

Readers (`repository.ShardReaders` in `shard_read.go`, started by `startShardReaders` in `serve.go` for the writer, reader and standalone modes) poll `shards` every 30s. When the set changes they close the family pool and open a new one; the connection hook attaches the new set. Pool connections get `SetConnMaxLifetime(2m)` as a backstop.

Deletion is two-phase so no reader holds a deleted inode, which kept space allocated in an earlier incident:

1. Writer marks the shard `retiring`. Readers drop it from their next pool.
2. After 5 minutes (two poll intervals plus the connection lifetime), the writer deletes the file and its `-wal`/`-shm`, then removes the row.

### Disk ladder and reclaim

`reclaimToTarget` (`retention/reclaim.go`) gains a first step, `retireShardsForReclaim`: retire the oldest shard across all families, oldest period first, until the projected usage is under target. A shard inserts can still reach (the current period or later) is never retired. Retiring shards count as free in that projection, since their files go within `shardDeleteDelay`; span row eviction then runs against the projection, and the ballast is restored only once usage is under target and no retiring shard is pending. The ballast (`repository/ballast.go`) and the size-based target stay.

Retention expires shards by period (`Repository.ExpireShards`, called from the housekeeping step of the cycle): a shard whose whole period is older than its family's cutoff is retired, and a retiring shard older than `shardDeleteDelay` is deleted. When the disk ladder cuts a window below one period (logs to a few hours), the shard that straddles the cutoff also loses its older rows per project in batches, followed by `PRAGMA incremental_vacuum`. Shard files are created with `auto_vacuum=INCREMENTAL` for this.

`DBSpace` (`repository/space.go`) sums `FileBytes` and `WALBytes` over main, `spans.db` and every shard listed in `shards`, and adds each shard's pages in main's page size. Shard freelists are left out, because incremental vacuum returns their freed pages to the volume. The ingest disk-headroom check (`cmd/spanbarn/runtime.go`), the retention worker and the rollup compactor (`internal/rollup/compactor.go`) all read this sum.

### Backups

`SnapshotSettings` (`repository/dr.go`) keeps snapshotting main only; settings never move. Other single-file assumptions to update:

- WAL checkpointing in `db.go` runs per write handle, so each file gets its own PASSIVE/TRUNCATE loop.
- `spanbarn db snapshot-settings` (`cmd/spanbarn/cli_db.go`) writes a main file whose telemetry tables are present and empty. After the split the moved tables are absent from main, and the writer creates an empty `spans.db` and empty current shards on first start, so a restored snapshot still serves with no extra step. A test restores a snapshot into an empty directory and starts the writer against it.

### Migration path

Prod cannot hold two copies of the heavy tables (logs, metrics, prompts), so no step copies them. Step 1 copies only the spans family.

- **Step 1 (split).** The spans family is a fraction of prod's file (about 1M rows, mostly `aggregates`). The writer copies it at startup (`repository/cutover.go`), after migrations and before any worker runs, so nothing writes to the span tables during the copy; ingest waits in the Redis write queue. Every writer deployment uses the Recreate strategy, so no second writer runs. The copy needs free space at least the size of main, otherwise it is postponed to the next start with a warning.
  1. Create and migrate `$SPANBARN_DB_PATH.spans-cutover` (without main attached: goose names its version table unqualified, and main's `goose_spans_version` would answer for the new file).
  2. Attach main read-only and copy each table in rowid batches of 20k with an explicit column list (generated columns excluded), checking row counts per table. Copy the `sqlite_sequence` counters so ids keep growing from main's. Write a `spans_cutover` marker row and checkpoint.
  3. Drop the span tables and `goose_spans_version` from main in one transaction. This is the commit point: from here the copy is the only one, and an older image can no longer write spans into main.
  4. Rename the copy to `$SPANBARN_DB_PATH.spans` and open the split layout.

  A failure before the drop commits is logged at error level, removes the copy and keeps the single-file layout. A stop between the drop and the rename leaves main without span tables and a finished copy; the next start renames it. A spans file next to a main that still has span tables was never committed and is replaced by a new copy, since a writer may have written to main after it was made.

  The first prod attempt renamed before dropping (v0.3.338). The rollout timed out during the drop, the deploy rolled back to the previous image, and that image kept writing spans to main next to a renamed spans file. Deciding the layout from main alone, and committing with the drop, makes that state harmless. On a 3.7 GB copy of prod's data the copy took 49 s for 984k rows on a loaded host; prod's writer, limited to 500m CPU, took about 5 minutes, so the production rollout waits up to 20 minutes for the writer. Readers attach the spans file only on connections where main has no `spans` table, and recycle connections every 2 minutes (`SetConnMaxLifetime`).
- **Step 2 (shards).** The writer shards only with `SPANBARN_SHARDS` on. It stays off in every environment until reads (#245) and file expiry (#246) cover the shards, because a row in a shard is invisible to the dashboard before #245 and never expires before #246. With it on, `repository.ShardManager` (`shards.go`) creates each family's file for the current period and, an hour ahead, for the next one, records it in `shards`, and takes the family's inserts on its own write queue; the shard is picked when the queued insert runs. Retention deletes of the sharded families keep running on main. On startup the manager records shard files that have no row, drops rows whose file is gone, and migrates every shard. Shards start empty on deploy. Each shard's AUTOINCREMENT ids start at the period's day number shifted left 32 bits, so ids stay unique across the union and below 2^53. Reads union the old main table with the shard views (the view definition includes `main.logs` while main still holds rows). Existing row retention keeps deleting from main. When a family's table in main is empty, the writer retires it in two steps, like a shard file (`repository/main_retire.go`, run from `ExpireShards`): it lists the table in `retired_tables` (main migration 044), readers leave `main.<table>` out of the view on their next refresh, and after 5 minutes the writer drops the table and its row. Row retention and the trace-eviction cascade skip a table main no longer has. A writer started with `SPANBARN_SHARDS` off recreates the dropped tables, empty, from the shard baseline DDL, so turning shards off keeps ingest working. Main-track migrations must not touch `logs`, `metrics` or `prompt_records`; changes to them go in the shard track. Main has `auto_vacuum=INCREMENTAL` (migration 018) and its checkpoint loop runs `incremental_vacuum`, so the pages of emptied and dropped tables go back to the volume by themselves. A main with `auto_vacuum=NONE` (rebuilt from a schema dump) needs one `VACUUM INTO` plus a swap after the 30-day families empty (`deploy/docs/compact-main.md`).

## Not covered

- `metric_rollups` (76% of prod in September, before PR #178's compaction tiers) stays in main. If it grows again, the same family-shard machinery applies to the 5-minute tier.
- Reader query cache keys need no change: query results do not depend on layout.

## Build plan

The epic #235 lists the sub-issues and their order.
