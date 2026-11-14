// Package tracebuffer holds the tail-sampling trace buffer. It collects spans
// per trace until the trace can be decided (root arrived and grace passed, or
// the TTL ran out), keeps or drops the whole trace by the configured sample
// ratio and rescue rules (errors, slow traces, floor traces), carries the
// totals of dropped traces forward, sheds sacrificial traces at the cap and
// drains on shutdown. CachedRatioLookup and StaticRatioLookup supply the
// per-project and per-operation ratios the decision reads.
//
// The package depends on model, observability, sampling and spanledger only.
// internal/ingest, internal/api and cmd/spanbarn import it, never the other
// way round.
package tracebuffer
