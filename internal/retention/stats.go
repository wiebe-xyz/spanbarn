package retention

import (
	"sync"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// Stats is what retention has measured and done since the process started. It
// backs the storage self-metrics: before them, the disk tier and the per-table
// deletes were only in the logs, so nothing could alert on the ladder leaving
// normal or on a backlog that keeps draining cycle after cycle. That is how
// prompt_records grew to the largest table in production unnoticed.
type Stats struct {
	// Measured is false until a cycle has sized the volume, and stays false on
	// a database that cannot be sized (in-memory). The space fields are zero
	// then and should not be exported as real readings.
	Measured           bool
	Tier               Tier
	VolumeUsedFraction float64
	DBFileBytes        int64
	WALBytes           int64
	FreelistBytes      int64

	// Deleted is the cumulative rows deleted per table, keyed by RetentionTables.
	Deleted map[string]int64
	// Backlog reports, for the tables whose deletes are capped per cycle,
	// whether the last cycle stopped at the cap with rows still to go.
	Backlog map[string]bool
}

// RetentionTables are the keys of Stats.Deleted. The set is fixed so metric
// labels stay bounded.
var RetentionTables = []string{
	"spans", "boring_spans", "error_samples", "aggregates", "metrics",
	"metric_rollups", "logs", "prompt_records", "project_traces",
	"web_sessions", "e2e_users",
}

// BacklogTables are the keys of Stats.Backlog.
var BacklogTables = []string{"spans", "metric_rollups", "prompt_records"}

// statsState is the worker's running copy of Stats.
type statsState struct {
	mu       sync.Mutex
	measured bool
	tier     Tier
	space    repository.Space
	deleted  map[string]int64
	backlog  map[string]bool
}

// Stats returns a copy of what retention has observed so far. Safe to call
// from any goroutine.
func (w *RetentionWorker) Stats() Stats {
	s := &w.stats
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Stats{
		Measured:           s.measured,
		Tier:               s.tier,
		VolumeUsedFraction: s.space.UsedFraction(),
		DBFileBytes:        s.space.FileBytes,
		WALBytes:           s.space.WALBytes,
		FreelistBytes:      s.space.ReusableBytes(),
		Deleted:            make(map[string]int64, len(RetentionTables)),
		Backlog:            make(map[string]bool, len(BacklogTables)),
	}
	for _, t := range RetentionTables {
		out.Deleted[t] = s.deleted[t]
	}
	for _, t := range BacklogTables {
		out.Backlog[t] = s.backlog[t]
	}
	return out
}

// recordSpace stores the latest volume measurement and the tier it maps to.
func (w *RetentionWorker) recordSpace(space repository.Space, tier Tier) {
	s := &w.stats
	s.mu.Lock()
	s.measured, s.space, s.tier = true, space, tier
	s.mu.Unlock()
}

// recordCycle adds one completed cycle's deletes to the running totals and
// replaces the backlog flags.
func (w *RetentionWorker) recordCycle(st *cycleStats) {
	s := &w.stats
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleted == nil {
		s.deleted = make(map[string]int64, len(RetentionTables))
	}
	for table, n := range st.deletedByTable() {
		s.deleted[table] += n
	}
	s.backlog = map[string]bool{
		"spans":          st.backlogRemains,
		"metric_rollups": st.rollupBacklogRemains,
		"prompt_records": st.promptBacklogRemains,
	}
}

func (c *cycleStats) deletedByTable() map[string]int64 {
	return map[string]int64{
		"spans":          c.spansDeleted,
		"boring_spans":   c.boringDeleted,
		"error_samples":  c.errorSamplesDeleted,
		"aggregates":     c.aggregatesDeleted,
		"metrics":        c.metricsDeleted,
		"metric_rollups": c.rollupRowsDeleted,
		"logs":           c.logsDeleted,
		"prompt_records": c.promptsDeleted,
		"project_traces": c.projectTracesEvicted,
		"web_sessions":   c.webSessionsDeleted,
		"e2e_users":      c.e2eUsersDeleted,
	}
}
