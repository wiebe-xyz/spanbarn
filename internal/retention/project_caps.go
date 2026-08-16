package retention

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// evictPerProjectCaps enforces the per-project retention caps configured via the
// settings table: retention.max_hours.project.{id} (delete non-error traces older
// than that many hours) and retention.max_traces.project.{id} (keep only the
// newest N non-error traces). Both only ever shorten retention; absent/≤0 keys
// are skipped. Returns the total traces evicted across all projects.
func (w *RetentionWorker) evictPerProjectCaps(ctx context.Context, now time.Time) (int64, error) {
	pids, err := w.repo.ListProjectIDs()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if h := w.settingInt(fmt.Sprintf("retention.max_hours.project.%d", pid)); h > 0 {
			n, err := w.repo.EvictProjectTracesOlderThan(ctx, pid, now.Add(-time.Duration(h)*time.Hour))
			if err != nil {
				return total, err
			}
			total += n
		}
		if maxN := w.settingInt(fmt.Sprintf("retention.max_traces.project.%d", pid)); maxN > 0 {
			cutoff, ok, err := w.repo.ProjectNonErrorTraceCountCutoff(ctx, pid, maxN)
			if err != nil {
				return total, err
			}
			if ok {
				n, err := w.repo.EvictProjectTracesOlderThan(ctx, pid, cutoff)
				if err != nil {
					return total, err
				}
				total += n
			}
		}
	}
	return total, nil
}

// settingInt reads a positive int from the settings table, returning 0 when the
// key is absent, empty, unparseable, or non-positive.
func (w *RetentionWorker) settingInt(key string) int {
	v, err := w.repo.GetSetting(key)
	if err != nil || v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
