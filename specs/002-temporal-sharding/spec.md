# Feature Specification: Storage split and temporal sharding

**Spec**: `specs/002-temporal-sharding/`
**Status**: Shipped
**Last verified against the tree**: 2026-10-17 at `41a73f7`
**Issues**: [#235](https://github.com/wiebe-xyz/spanbarn/issues/235) (epic), [#242](https://github.com/wiebe-xyz/spanbarn/issues/242) to [#248](https://github.com/wiebe-xyz/spanbarn/issues/248), [#263](https://github.com/wiebe-xyz/spanbarn/issues/263)
**Principles touched**: II, IV

## Purpose

One SQLite file made every heavy write contend with every read, and expiring a day of logs cost a 49 second row delete that freed no disk. The database is split into functional files and the heavy tables are sharded by time, so retention drops a whole file and the span-shaped tables get their own writer. The rationale, the spike measurements and the migration path are in [design.md](./design.md). This spec states what the tree does today.

## Users

- **Operator**: needs retention that returns disk space and a writer that does not stall under ingest load.
- **Developer**: reads logs, metrics, prompts and traces through the same API regardless of file layout.

## User Stories

### US1 - Spans live in their own file (Priority: P1)

**Acceptance Scenarios**:

1. **Given** a new install, **When** the writer starts, **Then** `spans`, `trace_summaries`, `spans_staging`, `aggregates` and `error_samples` live in `$SPANBARN_DB_PATH.spans` and main holds none of them.
2. **Given** an existing single-file database, **When** the writer starts, **Then** it copies the span family into a new file, drops the old tables in one transaction, and keeps the single-file layout if the copy fails.

### US2 - Heavy tables rotate by time (Priority: P1)

**Acceptance Scenarios**:

1. **Given** `SPANBARN_SHARDS` on, **When** a period ends, **Then** logs and metrics (daily) and prompts (ISO week) write to a new file created ahead of the boundary.
2. **Given** a shard whose whole period is older than its retention, **When** retention runs, **Then** the file is retired, and deleted after a delay.

### US3 - Reads span every shard (Priority: P1)

**Acceptance Scenarios**:

1. **Given** logs spread over several daily files, **When** the user lists logs, **Then** one result covers all live shards and the rows of the old main table.

### US4 - Disk pressure drops files first (Priority: P2)

**Acceptance Scenarios**:

1. **Given** disk usage above target, **When** reclaim runs, **Then** it retires the oldest shards before evicting spans.

## Functional Requirements

- **FR-001**: The system MUST place the span family in its own file and decide the layout from main alone at open time. Issues: [#242](https://github.com/wiebe-xyz/spanbarn/issues/242). Tests: `internal/repository/split_test.go::TestSplitLayoutRoutesWrites`, `internal/repository/layout_test.go::TestMainTrackLeavesSpanTablesAlone`.
- **FR-002**: The writer MUST cut an existing database over by copying the span family, committing with the drop of the old tables, and keeping the single-file layout when the copy fails. Issues: [#243](https://github.com/wiebe-xyz/spanbarn/issues/243). Tests: `internal/repository/cutover_test.go::TestCutOverMovesSpansFamily`, `::TestCutOverFinishesAfterDrop`, `::TestCutOverFailureKeepsSingleFile`, `::TestCutOverReplacesUncommittedSpansFile`.
- **FR-003**: Each storage family MUST have one write handle and one write scheduler, and a repository method that writes MUST touch one family. Issues: [#248](https://github.com/wiebe-xyz/spanbarn/issues/248). Tests: `internal/repository/split_test.go::TestSplitLayoutRoutesWrites`.
- **FR-004**: With `SPANBARN_SHARDS` on, the shard manager MUST create each family's file for the current period and for the next one ahead of the boundary, and route inserts to the open shard. Issues: [#244](https://github.com/wiebe-xyz/spanbarn/issues/244). Tests: `internal/repository/shards_test.go::TestShardMaintainCreatesAheadAndClosesEnded`.
- **FR-005**: Reads of a sharded family MUST span every live shard through a per-family read pool and MUST NOT use `INDEXED BY`. Issues: [#245](https://github.com/wiebe-xyz/spanbarn/issues/245), [#269](https://github.com/wiebe-xyz/spanbarn/issues/269). Tests: `internal/repository/shard_read_test.go::TestShardReadsPromptsNewestFirst`, `internal/repository/shard_read_indexed_test.go::TestNoIndexedByOnShardedFamilies`.
- **FR-006**: Retention MUST retire a shard whose whole period is older than its cutoff, never retire the current shard, and delete the file only after a delay. Issues: [#246](https://github.com/wiebe-xyz/spanbarn/issues/246). Tests: `internal/repository/shards_expire_test.go::TestShardExpiryNeverRetiresTheCurrentShard`, `::TestRetireOldestShardAcrossFamilies`.
- **FR-007**: Disk reclaim MUST retire the oldest shards first, count retiring shards as free, and restore the ballast only when usage is under target. Issues: [#240](https://github.com/wiebe-xyz/spanbarn/issues/240). Tests: `internal/retention/shards_test.go::TestReclaimRetiresOldestShardsBeforeEvictingSpans`, `::TestReclaimCountsRetiringShardsAsFree`, `internal/retention/reclaim_test.go::TestReclaimReleasesAndRestoresBallast`.
- **FR-008**: Disk accounting MUST sum main, the spans file and every listed shard. Issues: [#240](https://github.com/wiebe-xyz/spanbarn/issues/240). Tests: `internal/repository/shards_expire_test.go::TestDBSpaceCountsShardFiles`.
- **FR-009**: When a heavy table in main is empty, the writer MUST retire it in two steps, and a writer started with shards off MUST recreate the dropped tables empty. Issues: [#247](https://github.com/wiebe-xyz/spanbarn/issues/247). Tests: `internal/repository/main_retire_test.go::TestMainRestoresHeavyTablesWithShardsOff`.
- **FR-010**: A restored settings-only snapshot MUST start and serve without extra steps. Issues: [#235](https://github.com/wiebe-xyz/spanbarn/issues/235). Tests: `internal/repository/layout_test.go::TestSnapshotRestoreStartsAndServes`, `internal/repository/dr_test.go::TestSnapshotSettingsCopiesSettingsOnly`.
- **FR-011**: A reader that starts before the writer has created the files MUST recover without a restart. Issues: [#297](https://github.com/wiebe-xyz/spanbarn/issues/297). Tests: `internal/repository/layout_test.go::TestReadDBRecoversWhenWriterStartsLater`.
- **FR-012**: The log retention delete MUST work on sharded logs and keep logs of pinned and error-sampled traces. Issues: [#307](https://github.com/wiebe-xyz/spanbarn/issues/307). Tests: `internal/repository/project_log_cap_shards_test.go::TestDeleteProjectLogsOlderThanOnShardedLogs`, `internal/repository/repo_logs_test.go::TestDeleteLogsOlderThan_SkipsErrorSampled`.
- **FR-013**: CI MUST run the repository tests a second time with `SPANBARN_TEST_LAYOUT=split`. Issues: [#248](https://github.com/wiebe-xyz/spanbarn/issues/248). Tests: `.github/workflows/ci.yml` (the `code` job).
- **FR-014**: The shard `kept_logs` table in main holds logs of pinned and error-sampled traces when a logs shard retires. Issues: none found. [inferred]

## Non-Functional Requirements

- **NFR-001**: Heavy insert p99 under write load falls from 1458 ms to 101 ms with the split, and expiring a day of data takes 0.18 s with whole-file deletion. Source: the spike results comment on [#235](https://github.com/wiebe-xyz/spanbarn/issues/235), produced by `cmd/shardspike`. The run shared a loaded host, so differences under 30% are unconfirmed.
- **NFR-002**: A shard family attaches at most 9 files per read pool, because the SQLite driver refuses the 11th `ATTACH`. Source: [design.md](./design.md), "Cross-shard queries". [inferred]
- **NFR-003**: The cut-over needs free space at least the size of main, otherwise it is postponed with a warning. Source: [design.md](./design.md), "Migration path". [inferred]

## Key Entities

- **Storage family**: main, spans, and one per heavy table family. Owned by `internal/repository`.
- **Shard**: one file per family and period, listed in the `shards` table in main with state `active` or `retiring`.
- **Ballast**: a reserved file the reclaim step releases under disk pressure (`internal/repository/ballast.go`).

## Non-Goals

- Time-sharding of spans. The spike measured trace list going from 0.2 ms to 10 to 53 ms through `UNION ALL` views ([design.md](./design.md)).
- Sharding `metric_rollups`. It stays in main ([design.md](./design.md), "Not covered").

## Open Questions

- [#296](https://github.com/wiebe-xyz/spanbarn/issues/296) How many days of raw logs production keeps. It sets the logs shard count.

## Drift

| Claim | Source | Tree |
|---|---|---|
| The writer shards only with `SPANBARN_SHARDS` on, and it stays off in every environment until reads and expiry cover the shards. | [design.md](./design.md), "Migration path" | `deploy/k8s/base/configmap.yaml` sets `SPANBARN_SHARDS: "1"`. #245 and #246 are closed. The Go default is `false` (`internal/config/config.go`). |
| Step 1 (the split) and step 2 (shards) are future work. | [design.md](./design.md), "Decision" | Both are in the tree: `internal/repository/cutover.go`, `shards.go`, `shard_read.go`, `main_retire.go`. |
| The first prod cut-over attempt (v0.3.338) renamed before dropping. | [design.md](./design.md) | The tree commits with the drop and renames afterwards (`cutover.go`, `TestCutOverFinishesAfterDrop`). The version in the incident is from the design text and not checked here. |

## Proof contract

- [ ] `go test ./internal/repository/... ./internal/retention/...` passes, and the same repository tests pass with `SPANBARN_TEST_LAYOUT=split`.
- [ ] On staging, `GET /api/v1/logs` returns rows from more than one daily shard after a midnight boundary.
- [ ] After a retention cycle, the volume's used bytes fall by about the size of the retired shard file.

## Sources

- [design.md](./design.md) and the results comment on [#235](https://github.com/wiebe-xyz/spanbarn/issues/235)
- `internal/repository/{cutover,shards,shard_read,shards_expire,main_retire,space,ballast}.go`, `internal/retention/`
- `deploy/docs/compact-main.md`, `deploy/docs/disaster-recovery.md`, `deploy/k8s/base/configmap.yaml`
