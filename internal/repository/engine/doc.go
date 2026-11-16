// Package engine owns SpanBarn's SQLite files: opening them (main, the spans
// file and the period shards), the storage layout and the cut-over to it,
// schema migrations per track, the shard lifecycle (create, close, retire,
// expire, reconcile), the read pools over sharded families, main compaction,
// disaster-recovery snapshots, checkpoints, ballast and disk accounting.
//
// It knows tables and files, not telemetry. Queries over spans, logs, metrics
// and prompts live in the parent package repository, which reaches the files
// through the handles and the ShardManager this package exports.
package engine
