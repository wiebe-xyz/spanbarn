// Package spanledger counts spans per project and 5-minute bucket at each stage
// between acceptance and storage, in the process that handles them, and reports
// the counts to where they outlive that process (#471).
//
// In-process counters read 0 through two reader rollouts that lost 42 calls of
// one producer: a counter dies with its pod, and nothing compared what was
// accepted with what was stored. The ledger reports deltas every 30 s and once
// at the end of shutdown, so a pod that goes away leaves its accepted spans
// behind with no exit against them.
package spanledger

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// ReportInterval is how often a running process reports its counts. A process
// that is killed loses at most this much of them.
const ReportInterval = 30 * time.Second

// Column names one count of a ledger row.
type Column int

// The columns of a ledger row. See model.SpanLedgerRow.
const (
	Accepted Column = iota
	SampledOut
	ShedUnsampled
	LostInBuffer
	Forwarded
	Processed
	Failed
	columnCount
)

type rowKey struct {
	bucket    int64
	projectID int64
}

// Sink receives the counts of a report. It fails as a whole: the tally keeps
// the rows of a failed report for the next one.
type Sink func(ctx context.Context, rows []model.SpanLedgerRow) error

// Tally holds the counts since the last report. A nil Tally counts nothing, so
// a mode without a ledger needs no checks at its call sites.
type Tally struct {
	mu   sync.Mutex
	rows map[rowKey]*[columnCount]int64
}

// New returns an empty Tally.
func New() *Tally {
	return &Tally{rows: map[rowKey]*[columnCount]int64{}}
}

// Add counts n spans of a project that started at startUs. A negative n takes
// back an earlier count.
func (t *Tally) Add(col Column, projectID, startUs, n int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.add(col, projectID, startUs, n)
}

// AddSpans adds sign once for every span.
func (t *Tally) AddSpans(col Column, spans []model.SpanRecord, sign int64) {
	if t == nil || len(spans) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range spans {
		t.add(col, spans[i].ProjectID, spans[i].StartTimeUs, sign)
	}
}

// add counts under the lock.
func (t *Tally) add(col Column, projectID, startUs, n int64) {
	t.row(rowKey{model.SpanLedgerBucket(startUs), projectID})[col] += n
}

func (t *Tally) row(k rowKey) *[columnCount]int64 {
	r := t.rows[k]
	if r == nil {
		r = &[columnCount]int64{}
		t.rows[k] = r
	}
	return r
}

// Take returns the counts since the last Take and clears them. Rows that net
// out to nothing are left out.
func (t *Tally) Take() []model.SpanLedgerRow {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]model.SpanLedgerRow, 0, len(t.rows))
	for k, r := range t.rows {
		if *r != ([columnCount]int64{}) {
			out = append(out, model.SpanLedgerRow{
				Bucket:        k.bucket,
				ProjectID:     k.projectID,
				Accepted:      r[Accepted],
				SampledOut:    r[SampledOut],
				ShedUnsampled: r[ShedUnsampled],
				LostInBuffer:  r[LostInBuffer],
				Forwarded:     r[Forwarded],
				Processed:     r[Processed],
				Failed:        r[Failed],
			})
		}
		delete(t.rows, k)
	}
	return out
}

// Merge adds rows back, after a report that failed.
func (t *Tally) Merge(rows []model.SpanLedgerRow) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range rows {
		s := &rows[i]
		r := t.row(rowKey{s.Bucket, s.ProjectID})
		r[Accepted] += s.Accepted
		r[SampledOut] += s.SampledOut
		r[ShedUnsampled] += s.ShedUnsampled
		r[LostInBuffer] += s.LostInBuffer
		r[Forwarded] += s.Forwarded
		r[Processed] += s.Processed
		r[Failed] += s.Failed
	}
}

// Report sends the counts since the last report to sink, and keeps them when
// sink fails.
func (t *Tally) Report(ctx context.Context, sink Sink) error {
	rows := t.Take()
	if len(rows) == 0 {
		return nil
	}
	if err := sink(ctx, rows); err != nil {
		t.Merge(rows)
		return err
	}
	return nil
}

// Run reports every interval until ctx ends. A failed report is logged at info
// because the next one carries its counts. The caller makes the last report
// once the work it counts has stopped.
func (t *Tally) Run(ctx context.Context, interval time.Duration, sink Sink, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := t.Report(ctx, sink); err != nil {
				logger.Info("span ledger: report failed, keeping counts for the next one", "error", err)
			}
		}
	}
}
