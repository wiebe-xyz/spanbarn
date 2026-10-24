package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

func TestChunkTraceKeysBoundsEachChunk(t *testing.T) {
	keys := make([]TraceKey, 11)
	chunks := chunkTraceKeys(keys, refreshKeysPerSlot)
	if want := (len(keys) + refreshKeysPerSlot - 1) / refreshKeysPerSlot; len(chunks) != want {
		t.Fatalf("%d chunks, want %d", len(chunks), want)
	}
	total := 0
	for _, c := range chunks {
		if len(c) == 0 || len(c) > refreshKeysPerSlot {
			t.Errorf("chunk of %d keys, want 1..%d", len(c), refreshKeysPerSlot)
		}
		total += len(c)
	}
	if total != len(keys) {
		t.Errorf("chunks hold %d keys, want %d", total, len(keys))
	}
	if got := chunkTraceKeys(nil, refreshKeysPerSlot); len(got) != 0 {
		t.Errorf("nil keys gave %d chunks", len(got))
	}
}

// A refresh of many traces runs through several write slots and leaves every
// summary correct.
func TestRefreshTraceStructureManyTracesAcrossSlots(t *testing.T) {
	repo := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	repo.SetWriteScheduler(s)
	go s.Run(ctx)
	repo.SetDeleteBatchYield(time.Millisecond)

	n := refreshKeysPerSlot*3 + 1
	keys := make([]TraceKey, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("many-%d", i)
		if err := repo.InsertSpans([]Span{
			tsSpan(id, id+"-r", "", "GET /x", "web", "ok", 1, 10),
			tsSpan(id, id+"-c", id+"-gone", "SELECT", "db", "ok", 2, 3),
		}); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, TraceKey{ProjectID: 1, TraceID: id})
	}
	if err := repo.RefreshTraceStructure(ctx, keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		hr, orphans, spans, _ := summaryRow(t, repo, k.TraceID)
		if hr == nil || !*hr || orphans != 1 || spans != 2 {
			t.Errorf("%s: hasRoot=%v orphans=%d spans=%d", k.TraceID, hr, orphans, spans)
		}
	}

	stopped, stop := context.WithCancel(ctx)
	stop()
	if err := repo.RefreshTraceStructure(stopped, keys); err == nil {
		t.Error("a cancelled context should stop the refresh")
	}
}
