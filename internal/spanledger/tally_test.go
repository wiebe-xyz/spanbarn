package spanledger

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const startUs = 1_800_000_123 * 1_000_000 // inside the bucket of 1_800_000_000

func TestTallyCountsPerProjectAndBucket(t *testing.T) {
	tally := New()
	tally.Add(Accepted, 1, startUs, 2)
	tally.Add(Accepted, 2, startUs, 1)
	tally.Add(Forwarded, 1, startUs+299*1_000_000, 1)
	tally.Add(Accepted, 1, startUs+300*1_000_000, 1)

	byKey := map[[2]int64]model.SpanLedgerRow{}
	for _, r := range tally.Take() {
		byKey[[2]int64{r.Bucket, r.ProjectID}] = r
	}
	if len(byKey) != 3 {
		t.Fatalf("rows = %v, want 3", byKey)
	}
	if r := byKey[[2]int64{1_800_000_000, 1}]; r.Accepted != 2 {
		t.Errorf("project 1 first bucket = %+v, want accepted 2", r)
	}
	if r := byKey[[2]int64{1_800_000_300, 1}]; r.Accepted != 1 || r.Forwarded != 1 {
		t.Errorf("project 1 second bucket = %+v, want accepted 1 forwarded 1", r)
	}
	if r := byKey[[2]int64{1_800_000_000, 2}]; r.Accepted != 1 {
		t.Errorf("project 2 = %+v, want accepted 1", r)
	}
	if rows := tally.Take(); len(rows) != 0 {
		t.Errorf("second Take = %v, want nothing", rows)
	}
}

func TestTallyAddSpansAndTakeBack(t *testing.T) {
	tally := New()
	spans := []model.SpanRecord{{ProjectID: 1, StartTimeUs: startUs}, {ProjectID: 1, StartTimeUs: startUs}}
	tally.AddSpans(SampledOut, spans, 1)
	tally.AddSpans(SampledOut, spans, -1)
	if rows := tally.Take(); len(rows) != 0 {
		t.Errorf("rows that net out = %v, want none", rows)
	}
}

func TestNilTallyCountsNothing(t *testing.T) {
	var tally *Tally
	tally.Add(Accepted, 1, startUs, 1)
	tally.AddSpans(Accepted, []model.SpanRecord{{ProjectID: 1}}, 1)
	tally.Merge([]model.SpanLedgerRow{{ProjectID: 1}})
	if rows := tally.Take(); rows != nil {
		t.Errorf("Take = %v, want nil", rows)
	}
}

func TestReportKeepsCountsWhenTheSinkFails(t *testing.T) {
	tally := New()
	tally.Add(Accepted, 1, startUs, 3)
	tally.Add(Failed, 1, startUs, 1)

	failing := func(context.Context, []model.SpanLedgerRow) error { return errors.New("redis down") }
	if err := tally.Report(context.Background(), failing); err == nil {
		t.Fatal("Report = nil, want the sink's error")
	}
	tally.Add(Accepted, 1, startUs, 1)

	var got []model.SpanLedgerRow
	sink := func(_ context.Context, rows []model.SpanLedgerRow) error {
		got = rows
		return nil
	}
	if err := tally.Report(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Accepted != 4 || got[0].Failed != 1 {
		t.Errorf("report after failure = %+v, want accepted 4 failed 1", got)
	}
}

func TestReportWithNothingToSayCallsNoSink(t *testing.T) {
	called := false
	err := New().Report(context.Background(), func(context.Context, []model.SpanLedgerRow) error {
		called = true
		return nil
	})
	if err != nil || called {
		t.Errorf("err = %v, called = %v, want no call", err, called)
	}
}

func TestRunReportsUntilCancelled(t *testing.T) {
	tally := New()
	tally.Add(Accepted, 1, startUs, 1)
	reported := make(chan []model.SpanLedgerRow, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tally.Run(ctx, time.Millisecond, func(_ context.Context, rows []model.SpanLedgerRow) error {
			reported <- rows
			return nil
		}, slog.Default())
	}()
	if rows := <-reported; len(rows) != 1 || rows[0].Accepted != 1 {
		t.Errorf("reported %+v, want accepted 1", rows)
	}
	cancel()
	<-done
}
