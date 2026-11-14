package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func recordEndedSpans(t *testing.T) func() []sdktrace.ReadOnlySpan {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return func() []sdktrace.ReadOnlySpan { return exp.GetSpans().Snapshots() }
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
