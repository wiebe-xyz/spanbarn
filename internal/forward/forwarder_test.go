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

// levelRecorder remembers the highest level it was asked to log.
type levelRecorder struct {
	slog.Handler
	max slog.Level
}

func (r *levelRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *levelRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level > r.max {
		r.max = rec.Level
	}
	return nil
}

// ctxPublisher fails the way a Redis client does when its context ends.
type ctxPublisher struct{}

func (ctxPublisher) Publish(ctx context.Context, _ []model.SpanRecord) error {
	<-ctx.Done()
	return errors.New("queue: lpush: " + ctx.Err().Error())
}

// Shutdown cancelling a publish loses nothing: the cursor stays and Drain sends
// the batch. selflog turns every ERROR into a BugBarn issue, so it must stay
// below ERROR (SPA-79).
func TestRedisForwarderShutdownInterruptedPublishIsNotAnError(t *testing.T) {
	sp := spoolWith(t, 3)
	rec := &levelRecorder{max: slog.LevelDebug}
	f := &RedisForwarder{spool: sp, queue: ctxPublisher{}, logger: slog.New(rec)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := f.forwardBatch(ctx, 0); err == nil {
		t.Fatal("forwardBatch returned no error for an interrupted publish")
	}
	if rec.max >= slog.LevelError {
		t.Errorf("logged at %v, want below ERROR", rec.max)
	}
	if cur, _ := sp.LoadCursor(); cur != 0 {
		t.Errorf("cursor moved to %d after an interrupted publish", cur)
	}
}

// A publish that fails while the process is running still logs ERROR.
func TestRedisForwarderPublishFailureWhileRunningIsAnError(t *testing.T) {
	sp := spoolWith(t, 3)
	rec := &levelRecorder{max: slog.LevelDebug}
	f := &RedisForwarder{spool: sp, queue: &fakePublisher{fail: errors.New("down")}, logger: slog.New(rec)}

	if _, _, err := f.forwardBatch(context.Background(), 0); err == nil {
		t.Fatal("forwardBatch returned no error")
	}
	if rec.max < slog.LevelError {
		t.Errorf("logged at %v, want ERROR", rec.max)
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

// Every span reaches the queue once across a rotation, including the batch that
// triggered it and what the forwarder had not reached yet.
func TestRedisForwarderPublishesAcrossARotation(t *testing.T) {
	sp, err := spool.NewSpool(t.TempDir(), 500)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	pub := &fakePublisher{}
	f := &RedisForwarder{spool: sp, queue: pub, logger: quiet()}

	write := func(ids ...string) {
		t.Helper()
		recs := make([]model.SpanRecord, len(ids))
		for i, id := range ids {
			recs[i] = model.SpanRecord{TraceID: "t", SpanID: id, Name: "operation", Service: "service"}
		}
		if err := sp.Write(recs); err != nil {
			t.Fatal(err)
		}
	}
	write("a")
	if _, err := f.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	write("b")
	write("c", "d", "e", "f", "g", "h", "i", "j")
	if sp.Size() != 0 {
		t.Fatalf("size = %d, want a rotation to have happened", sp.Size())
	}
	write("k")
	if _, err := f.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := ""
	for _, r := range pub.got {
		got += r.SpanID
	}
	if got != "abcdefghijk" {
		t.Errorf("published %q, want every span once, in order", got)
	}
}
