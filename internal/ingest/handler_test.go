package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

func TestHandlerFlushesToSpool(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.NewSpool(dir, spool.DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	q := NewQueue(1000)
	h := NewHandler(q, sp, 5*time.Millisecond, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)

	// Enqueue some records.
	for i := 0; i < 20; i++ {
		ok := h.Enqueue(model.SpanRecord{
			ProjectID: 1,
			SpanID:    fmt.Sprintf("handler-span-%d", i),
			Name:      "test-op",
			Service:   "test-svc",
			Kind:      "SERVER",
			Status:    "OK",
		})
		if !ok {
			t.Fatalf("Enqueue(%d) returned false", i)
		}
	}

	// Wait for flush to happen (flush interval is 5ms, give it 100ms).
	time.Sleep(100 * time.Millisecond)

	h.Stop()

	// Verify spool has the records.
	records, _, err := sp.Read(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 20 {
		t.Fatalf("spool has %d records, want 20", len(records))
	}
}

func TestHandlerStopFlushesRemaining(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.NewSpool(dir, spool.DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	q := NewQueue(1000)
	// Use a very long flush interval so the ticker won't fire.
	h := NewHandler(q, sp, 10*time.Second, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)

	for i := 0; i < 5; i++ {
		h.Enqueue(model.SpanRecord{
			ProjectID: 1,
			SpanID:    fmt.Sprintf("stop-span-%d", i),
			Name:      "op",
			Service:   "svc",
			Kind:      "SERVER",
			Status:    "OK",
		})
	}

	// Stop should flush remaining items.
	h.Stop()

	records, _, err := sp.Read(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 5 {
		t.Fatalf("after Stop(), spool has %d records, want 5", len(records))
	}
}
