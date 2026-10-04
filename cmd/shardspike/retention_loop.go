package main

import (
	"context"
	"fmt"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// retInsertLoop inserts one batch of spans per tick until stopped. The ticker drops ticks
// while an insert is blocked, so a stalled writer shows as fewer inserts and one long one.
type retInsertLoop struct {
	started time.Time
	stopCh  chan struct{}
	done    chan struct{}
	lat     timings
	err     error
}

func startInsertLoop(ctx context.Context, repo *repository.Repository) *retInsertLoop {
	l := &retInsertLoop{started: time.Now(), stopCh: make(chan struct{}), done: make(chan struct{})}
	go l.run(ctx, repo)
	return l
}

func (l *retInsertLoop) run(ctx context.Context, repo *repository.Repository) {
	defer close(l.done)
	tick := time.NewTicker(retInsertEvery)
	defer tick.Stop()
	for seq := 0; ; seq++ {
		select {
		case <-l.stopCh:
			return
		case <-ctx.Done():
			l.err = ctx.Err()
			return
		case at := <-tick.C:
			if err := repo.InsertSpansContext(ctx, loopSpans(seq, time.Now())); err != nil {
				l.err = fmt.Errorf("insert batch %d: %w", seq, err)
				return
			}
			l.lat = append(l.lat, time.Since(at))
		}
	}
}

// stop ends the loop once it has run for at least retMinInsertRun and returns
// the latency from each tick to its commit.
func (l *retInsertLoop) stop() (timings, error) {
	if rest := retMinInsertRun - time.Since(l.started); rest > 0 {
		time.Sleep(rest)
	}
	close(l.stopCh)
	<-l.done
	return l.lat, l.err
}

// loopSpans builds one insert batch: a root and its children of one trace.
func loopSpans(seq int, now time.Time) []repository.Span {
	traceID := fmt.Sprintf("7e7e%028x", seq)
	root := fmt.Sprintf("%016x", seq*retInsertSpans)
	out := make([]repository.Span, retInsertSpans)
	for i := range out {
		s := repository.Span{ProjectID: 1, TraceID: traceID, SpanID: fmt.Sprintf("%016x", seq*retInsertSpans+i),
			Name: "GET /v1/items", Service: "api", Kind: "server", Status: "ok",
			StartTimeUs: now.UnixMicro(), DurationUs: 1_000, Attributes: "{}", Events: "[]"}
		if i > 0 {
			s.ParentSpanID, s.Name = root, "db.query"
		}
		out[i] = s
	}
	return out
}

// retSizeWatch samples a file's size on a ticker and keeps the peak.
type retSizeWatch struct {
	stopCh chan struct{}
	done   chan struct{}
	peak   int64
}

func watchSize(path string) *retSizeWatch {
	w := &retSizeWatch{stopCh: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		tick := time.NewTicker(retWALPollEvery)
		defer tick.Stop()
		for {
			w.peak = max(w.peak, retFileSize(path))
			select {
			case <-w.stopCh:
				return
			case <-tick.C:
			}
		}
	}()
	return w
}

func (w *retSizeWatch) stop() int64 {
	close(w.stopCh)
	<-w.done
	return w.peak
}
