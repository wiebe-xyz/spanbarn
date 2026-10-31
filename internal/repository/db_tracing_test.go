package repository

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordSpans installs an in-memory tracer provider as the global one (which is
// what otelsql uses when no provider is passed) and returns a func listing the
// names of spans recorded so far.
func recordSpans(t *testing.T) func() []string {
	t.Helper()
	ended := recordEndedSpans(t)
	return func() []string {
		var names []string
		for _, s := range ended() {
			names = append(names, s.Name())
		}
		return names
	}
}

func recordEndedSpans(t *testing.T) func() []sdktrace.ReadOnlySpan {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})

	return func() []sdktrace.ReadOnlySpan {
		_ = tp.ForceFlush(context.Background())
		return sr.Ended()
	}
}

// A statement cancelled by its caller, as a checkpoint is at every rollout, is
// no error span. A deadline and a failed statement stay error spans (#382).
func TestOnlyACancelledStatementEscapesTheErrorStatus(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name, query string
		ctx         context.Context
		want        codes.Code
	}{
		{"cancelled", "PRAGMA main.wal_checkpoint(TRUNCATE)", cancelled, codes.Unset},
		{"deadline", "PRAGMA main.wal_checkpoint(TRUNCATE)", expired, codes.Error},
		{"failed", "SELECT missing FROM nowhere", context.Background(), codes.Error},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ended := recordEndedSpans(t)
			db, err := NewDB(filepath.Join(t.TempDir(), "statuses.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			// A pinned connection takes the statement to the driver, where the
			// span starts, whatever state the context is in.
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()

			rows, err := conn.QueryContext(tc.ctx, tc.query)
			if err == nil {
				defer rows.Close()
				t.Fatal("statement succeeded, want it to fail")
			}

			want := sqliteSpanName(tc.ctx, "", tc.query)
			var seen []string
			for _, s := range ended() {
				if s.Name() != want {
					continue
				}
				seen = append(seen, s.Name())
				if got := s.Status().Code; got != tc.want {
					t.Errorf("status = %v, want %v (err %v)", got, tc.want, err)
				}
			}
			if len(seen) == 0 {
				t.Fatalf("no %s span recorded, so this checks nothing", want)
			}
		})
	}
}

// A new shard's version table probe fails by design and must not read as an
// error span.
func TestMigrateShardEmitsNoSpans(t *testing.T) {
	spanNames := recordSpans(t)
	h, err := NewDB(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	before := len(sqliteSpanNames(spanNames()))

	if err := MigrateShard(context.Background(), h.DB, FamilyLogs); err != nil {
		t.Fatal(err)
	}

	if got := sqliteSpanNames(spanNames()); len(got) != before {
		t.Fatalf("migrating a shard emitted %v", got[before:])
	}
}

func sqliteSpanNames(names []string) []string {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, "sqlite.") {
			out = append(out, n)
		}
	}
	return out
}

// TestInsertSpansEmitsNoSpans is the regression test for SpanBarn spamming
// itself: otelsql instruments every statement, so inserting a span used to emit
// a sqlite.insert span, which had to be inserted, emitting another. In
// production that recursion produced 876k sqlite.insert spans — 96% of
// SpanBarn's self-telemetry. Writing telemetry must not produce telemetry.
func TestInsertSpansEmitsNoSpans(t *testing.T) {
	spanNames := recordSpans(t)
	repo := setupTestDB(t)
	if _, err := repo.CreateProject("app", "App"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	before := len(sqliteSpanNames(spanNames()))

	if err := repo.InsertSpansContext(context.Background(), []Span{{
		ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "op",
		Service: "svc", Kind: "internal", Status: "ok",
		StartTimeUs: 1, DurationUs: 1, Attributes: "{}", Events: "[]",
	}}); err != nil {
		t.Fatalf("InsertSpansContext: %v", err)
	}

	got := sqliteSpanNames(spanNames())
	if len(got) != before {
		t.Fatalf("writing a span emitted %d sqlite span(s) %v — the feedback loop is back", len(got)-before, got[before:])
	}
}

// TestInsertLogsAndMetricsEmitNoSpans covers the other telemetry write paths on
// the same loop.
func TestInsertLogsAndMetricsEmitNoSpans(t *testing.T) {
	spanNames := recordSpans(t)
	repo := setupTestDB(t)
	if _, err := repo.CreateProject("app", "App"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	before := len(sqliteSpanNames(spanNames()))

	if err := repo.UpsertAggregates([]Aggregate{{
		ProjectID: 1, Service: "svc", Operation: "op", Kind: "internal",
		Bucket: time.Now().UTC().Truncate(time.Minute), Count: 1,
	}}); err != nil {
		t.Fatalf("UpsertAggregates: %v", err)
	}

	got := sqliteSpanNames(spanNames())
	if len(got) != before {
		t.Fatalf("writing aggregates emitted %d sqlite span(s) %v", len(got)-before, got[before:])
	}
}

// TestReadsAreStillTraced pins that the suppression is scoped to the telemetry
// write paths — user-facing queries must stay instrumented, since a read cannot
// feed itself.
func TestReadsAreStillTraced(t *testing.T) {
	spanNames := recordSpans(t)
	repo := setupTestDB(t)

	if _, err := repo.CreateProject("app", "App"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := repo.QuerySpans(SpanFilter{ProjectID: 1, Limit: 10}); err != nil {
		t.Fatalf("QuerySpans: %v", err)
	}

	if got := sqliteSpanNames(spanNames()); len(got) == 0 {
		t.Fatal("no sqlite spans recorded for reads — suppression is too broad, DB observability is lost")
	}
}
