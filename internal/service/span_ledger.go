package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	// spanLedgerSettle is how long after its end a bucket is expected to have
	// all its exits counted. A trace without a root waits 10 minutes in the
	// buffer and a pod reports every 30 s, so spans still on their way after
	// this are stuck or lost.
	spanLedgerSettle = 30 * time.Minute

	// spanLedgerAlertSpans is the loss per project and bucket that logs at
	// error level, which files a BugBarn issue.
	spanLedgerAlertSpans = 50

	// spanLedgerWatchInterval is how often the watch looks for settled buckets.
	spanLedgerWatchInterval = time.Minute

	// spanLedgerMaxBacklog is the write queue depth, in batches, above which the
	// watch waits: spans queued for the writer are late and not lost.
	spanLedgerMaxBacklog = 100
)

// SpanLedgerEntry is a ledger row with what it adds up to. LostSpans is the
// spans the row counts as lost. Before a bucket settles that is the loss its
// counts name; once settled it also holds every accepted span with no exit.
type SpanLedgerEntry struct {
	model.SpanLedgerRow
	LostSpans int64 `json:"lostSpans"`
	Settled   bool  `json:"settled"`
}

// spanLedgerLost adds up a row's loss. The comparison runs from accepted to the
// writer's counts, past forwarded, so a pod killed between its last report and
// its forwarded spans being stored does not read as loss when the writer did
// store them.
func spanLedgerLost(r model.SpanLedgerRow, settled bool) int64 {
	lost := r.LostInBuffer + r.Failed
	if !settled {
		return lost
	}
	noExit := r.Accepted - r.SampledOut - r.ShedUnsampled - r.LostInBuffer - r.Processed - r.Failed
	if noExit > 0 {
		lost += noExit
	}
	return lost
}

func spanLedgerSettled(bucket int64, now time.Time) bool {
	end := time.Unix(bucket+model.SpanLedgerBucketSeconds, 0)
	return !end.Add(spanLedgerSettle).After(now)
}

// SpanLedger returns the ledger of the range, for one project or for all of them
// when projectID is 0.
func (s *QueryService) SpanLedger(ctx context.Context, projectID int64, from, to time.Time) ([]SpanLedgerEntry, error) {
	_, span := tracer.Start(ctx, "query.span_ledger")
	defer span.End()

	rows, err := s.repo.ListSpanLedger(ctx, projectID, from, to)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]SpanLedgerEntry, 0, len(rows))
	for _, r := range rows {
		settled := spanLedgerSettled(r.Bucket, now)
		out = append(out, SpanLedgerEntry{SpanLedgerRow: r, LostSpans: spanLedgerLost(r, settled), Settled: settled})
	}
	return out, nil
}

// attachWindowLoss sets WindowLostSpans on every summary. A prompt summary is
// worked out from stored rows, and a span lost before storage leaves none, so a
// reader of those totals needs the caveat beside them. A failed read leaves the
// field out, because it must not claim zero.
func (s *QueryService) attachWindowLoss(ctx context.Context, summaries []PromptSummary, projectID int64, from, to time.Time) {
	if projectID <= 0 || len(summaries) == 0 {
		return
	}
	entries, err := s.SpanLedger(ctx, projectID, from, to)
	if err != nil {
		s.logger.Warn("span ledger: read for prompt summary failed", "project_id", projectID, "error", err)
		return
	}
	var lost int64
	for i := range entries {
		lost += entries[i].LostSpans
	}
	for i := range summaries {
		summaries[i].WindowLostSpans = &lost
	}
}

// SpanLedgerReader is what the watch reads.
type SpanLedgerReader interface {
	ListSpanLedger(ctx context.Context, projectID int64, from, to time.Time) ([]model.SpanLedgerRow, error)
}

// SpanLedgerWatch logs at error level when a settled bucket shows loss, so
// BugBarn files it. The counts name the loss wherever it happened, and the sites
// that have their own counter only cover part of the route.
type SpanLedgerWatch struct {
	repo    SpanLedgerReader
	backlog func(ctx context.Context) (int64, error)
	logger  *slog.Logger
	now     func() time.Time
	// watermark is the start of the first bucket not judged yet. A process
	// starts it at the buckets settling now, so a restart judges each bucket
	// that was still open and does not repeat the ones before.
	watermark time.Time
	// recheck is how long after first seeing loss in a bucket the watch reads
	// it again. Counts from a pod or the writer can land after the first read,
	// so loss that is gone by then was lateness.
	recheck time.Duration
	pending []pendingLoss
}

// pendingLoss is a bucket the watch saw loss in and has not yet judged.
type pendingLoss struct {
	first  model.SpanLedgerRow
	seenAt time.Time
}

// SetRecheck sets the wait between the first read of a bucket with loss and
// the read that decides whether it is logged. Zero logs at the first read.
func (w *SpanLedgerWatch) SetRecheck(d time.Duration) { w.recheck = d }

// NewSpanLedgerWatch returns a watch. backlog reports the write queue's depth.
func NewSpanLedgerWatch(repo SpanLedgerReader, backlog func(ctx context.Context) (int64, error), logger *slog.Logger) *SpanLedgerWatch {
	w := &SpanLedgerWatch{repo: repo, backlog: backlog, logger: logger, now: time.Now}
	w.watermark = w.settledUpTo()
	return w
}

// settledUpTo returns the end of the settled buckets.
func (w *SpanLedgerWatch) settledUpTo() time.Time {
	t := w.now().Add(-spanLedgerSettle)
	return time.Unix(model.SpanLedgerBucket(t.UnixMicro()), 0)
}

// Run checks every interval until ctx ends.
func (w *SpanLedgerWatch) Run(ctx context.Context) {
	ticker := time.NewTicker(spanLedgerWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Check(ctx)
		}
	}
}

// Check judges the buckets that settled since the last check. When the writer
// is behind, or the read fails, it leaves them for the next check.
func (w *SpanLedgerWatch) Check(ctx context.Context) {
	w.judgePending(ctx)
	upTo := w.settledUpTo()
	if !upTo.After(w.watermark) {
		return
	}
	depth, err := w.backlog(ctx)
	if err != nil {
		w.logger.Info("span ledger: write queue depth unavailable, checking later", "error", err)
		return
	}
	if depth > spanLedgerMaxBacklog {
		return
	}
	rows, err := w.repo.ListSpanLedger(ctx, 0, w.watermark, upTo)
	if err != nil {
		w.logger.Warn("span ledger: read failed, checking later", "error", err)
		return
	}
	now := w.now()
	for _, r := range rows {
		if spanLedgerLost(r, true) < spanLedgerAlertSpans {
			continue
		}
		if w.recheck <= 0 {
			w.logLoss(r, r)
			continue
		}
		w.pending = append(w.pending, pendingLoss{first: r, seenAt: now})
	}
	w.watermark = upTo
}

// judgePending reads the buckets whose wait is over again. Loss still above
// the threshold is logged with both counts; a bucket whose count dropped below
// it was late and is noted at info level. A failed read keeps the bucket for
// the next check.
func (w *SpanLedgerWatch) judgePending(ctx context.Context) {
	now := w.now()
	var keep []pendingLoss
	for _, p := range w.pending {
		if now.Sub(p.seenAt) < w.recheck {
			keep = append(keep, p)
			continue
		}
		start := time.Unix(p.first.Bucket, 0)
		rows, err := w.repo.ListSpanLedger(ctx, p.first.ProjectID, start, start.Add(time.Second))
		if err != nil {
			w.logger.Warn("span ledger: recheck read failed, checking later", "error", err)
			keep = append(keep, p)
			continue
		}
		second := p.first
		second.Accepted, second.LostInBuffer, second.Forwarded = 0, 0, 0
		second.Processed, second.Failed, second.SampledOut, second.ShedUnsampled = 0, 0, 0, 0
		for _, r := range rows {
			if r.ProjectID == p.first.ProjectID && r.Bucket == p.first.Bucket {
				second = r
			}
		}
		if spanLedgerLost(second, true) >= spanLedgerAlertSpans {
			w.logLoss(p.first, second)
			continue
		}
		w.logger.Info("span ledger: counts arrived late, no loss",
			"project_id", p.first.ProjectID,
			"bucket", time.Unix(p.first.Bucket, 0).UTC().Format(time.RFC3339),
			"first_lost_spans", spanLedgerLost(p.first, true),
			"second_lost_spans", spanLedgerLost(second, true))
	}
	w.pending = keep
}

// logLoss logs a bucket's loss at error level, which files a BugBarn issue,
// with the counts of the first read and of the read that confirmed it.
func (w *SpanLedgerWatch) logLoss(first, second model.SpanLedgerRow) {
	w.logger.Error("span ledger: accepted spans lost before storage",
		"project_id", second.ProjectID,
		"bucket", time.Unix(second.Bucket, 0).UTC().Format(time.RFC3339),
		"lost_spans", spanLedgerLost(second, true),
		"accepted", second.Accepted,
		"lost_in_buffer", second.LostInBuffer,
		"forwarded", second.Forwarded,
		"processed", second.Processed,
		"failed", second.Failed,
		"first_lost_spans", spanLedgerLost(first, true),
		"first_accepted", first.Accepted,
		"first_forwarded", first.Forwarded,
		"first_processed", first.Processed)
}
