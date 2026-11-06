package ingest

import (
	"log/slog"
	"time"
)

// flushFailureReportInterval caps a handler's failed-flush ERROR at one line per
// minute. A handler flushes every 100 ms, so a Redis at maxmemory fails every
// flush and each ERROR files a BugBarn event.
const flushFailureReportInterval = time.Minute

// flushFailures totals a handler's failed flushes between ERROR lines. The handler's
// Run goroutine owns it.
type flushFailures struct {
	logger     *slog.Logger
	msg        string
	flushes    int64
	records    int64
	cause      error
	lastReport time.Time
}

// add counts a failed flush of count records.
func (f *flushFailures) add(count int, cause error, now time.Time) {
	f.flushes++
	f.records += int64(count)
	f.cause = cause
	f.report(now, false)
}

// report logs the failures since the last line, at most once per
// flushFailureReportInterval unless force is set.
func (f *flushFailures) report(now time.Time, force bool) {
	if f.flushes == 0 || (!force && now.Sub(f.lastReport) < flushFailureReportInterval) {
		return
	}
	f.logger.Error(f.msg, "error", f.cause, "flushes", f.flushes, "count", f.records)
	f.flushes, f.records, f.cause = 0, 0, nil
	f.lastReport = now
}
