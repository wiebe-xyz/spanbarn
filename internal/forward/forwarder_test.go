package forward

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

type fakePublisher struct {
	got  []model.SpanRecord
	fail error
}

func (p *fakePublisher) Publish(_ context.Context, records []model.SpanRecord) error {
	if p.fail != nil {
		return p.fail
	}
	p.got = append(p.got, records...)
	return nil
}

func spoolWith(t *testing.T, n int) *spool.Spool {
	t.Helper()
	sp, err := spool.NewSpool(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sp.Close() })
	recs := make([]model.SpanRecord, n)
	for i := range recs {
		recs[i] = model.SpanRecord{TraceID: "t", SpanID: string(rune('a' + i%26)), Name: "op"}
	}
	if err := sp.Write(recs); err != nil {
		t.Fatal(err)
	}
	return sp
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Drain publishes everything after the saved cursor, across batches, and
// leaves the cursor at the end so nothing is published twice.
func TestRedisForwarderDrainPublishesTheRestOfTheSpool(t *testing.T) {
	n := 2*defaultBatchSize + 7
	sp := spoolWith(t, n)
	pub := &fakePublisher{}
	f := &RedisForwarder{spool: sp, queue: pub, logger: quiet()}

	got, err := f.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got != n || len(pub.got) != n {
		t.Errorf("Drain forwarded %d, publisher got %d, want %d", got, len(pub.got), n)
	}
	if again, _ := f.Drain(context.Background()); again != 0 {
		t.Errorf("second Drain forwarded %d, want 0", again)
	}
}

// A failed publish stops Drain and keeps the cursor, so the spans are not
// counted as forwarded.
func TestRedisForwarderDrainStopsOnPublishError(t *testing.T) {
	sp := spoolWith(t, 3)
	f := &RedisForwarder{spool: sp, queue: &fakePublisher{fail: errors.New("down")}, logger: quiet()}

	got, err := f.Drain(context.Background())
	if err == nil || got != 0 {
		t.Fatalf("Drain = %d, %v; want 0 and an error", got, err)
	}
	if cur, _ := sp.LoadCursor(); cur != 0 {
		t.Errorf("cursor moved to %d after a failed publish", cur)
	}
}
