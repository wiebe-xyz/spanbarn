package main

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

// The shutdown order the serving modes use: a trace the buffer still holds
// when the signal arrives ends up in the spool.
func TestShutdownMovesBufferedTracesToTheSpool(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sp, err := spool.NewSpool(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	handler := ingest.NewHandler(ingest.NewQueue(16), sp, time.Millisecond, logger)
	handler.Start(t.Context())
	buf := ingest.NewTraceBuffer(time.Hour, ingest.NewStaticRatioLookup(1), logger)
	var wg sync.WaitGroup
	drained := drainTraceBuffer(&wg, buf, handler)

	buf.Add(model.SpanRecord{ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "GET /", Status: "OK"})

	closeTraceBuffer(buf, drained, logger)
	handler.Stop()
	wg.Wait()

	recs, _, err := sp.Read(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].TraceID != "t1" {
		t.Fatalf("spool holds %d records, want the buffered trace", len(recs))
	}
}
