package main

import (
	"github.com/wiebe-xyz/spanbarn/internal/retention"
	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
)

// registerStorageMetrics exports retention's view of storage as self-metrics:
// the disk-pressure tier, volume usage, database file sizes, rows deleted per
// table and the per-table backlog flags. Until #240 these were log lines only.
//
// The space readings come from the retention cycle's own probe, so they are
// absent until the first cycle has run (one retention interval after start)
// rather than reported as zero.
func registerStorageMetrics(rec *selfmetrics.Recorder, stats func() retention.Stats) {
	measured := func(read func(retention.Stats) float64) func() (float64, bool) {
		return func() (float64, bool) {
			s := stats()
			if !s.Measured {
				return 0, false
			}
			return read(s), true
		}
	}
	rec.RegisterOptionalGauge("spanbarn.disk.tier", nil,
		measured(func(s retention.Stats) float64 { return float64(s.Tier) }))
	rec.RegisterOptionalGauge("spanbarn.disk.used_pct", nil,
		measured(func(s retention.Stats) float64 { return s.VolumeUsedFraction * 100 }))
	rec.RegisterOptionalGauge("spanbarn.db.bytes", nil,
		measured(func(s retention.Stats) float64 { return float64(s.DBFileBytes) }))
	rec.RegisterOptionalGauge("spanbarn.db.wal_bytes", nil,
		measured(func(s retention.Stats) float64 { return float64(s.WALBytes) }))
	rec.RegisterOptionalGauge("spanbarn.db.freelist_bytes", nil,
		measured(func(s retention.Stats) float64 { return float64(s.FreelistBytes) }))

	for _, table := range retention.RetentionTables {
		table := table
		rec.RegisterCounter("spanbarn.retention.deleted", map[string]string{"table": table},
			func() float64 { return float64(stats().Deleted[table]) })
	}
	for _, table := range retention.BacklogTables {
		table := table
		rec.RegisterGauge("spanbarn.retention.backlog", map[string]string{"table": table},
			func() float64 {
				if stats().Backlog[table] {
					return 1
				}
				return 0
			})
	}
}
