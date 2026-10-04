package retention

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// RunOnce copies a durable clean span to error_samples and leaves a plain
// clean span out; both raw spans are deleted.
func TestRunOnceSamplesDurableSpans(t *testing.T) {
	cfg := Config{
		InterestingRetentionHours: 1,
		ErrorRetentionDays:        30,
		AggregateRetentionDays:    365,
		SlowThresholdUS:           1_000_000,
	}
	worker, repo := setupTestWorker(t, cfg)

	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	oldTime := time.Now().UTC().Add(-2 * time.Hour)
	mk := func(trace string, durable bool) repository.Span {
		return repository.Span{
			ProjectID: 1, TraceID: trace, SpanID: trace + "-root",
			Name: "nightly-sweep", Service: "cron", Kind: "server", Status: "ok",
			StartTimeUs: oldTime.UnixMicro(), DurationUs: 500,
			Attributes: "{}", Events: "[]", IngestedAt: oldTime, Durable: durable,
		}
	}
	if err := repo.InsertSpans([]repository.Span{mk("durable-trace", true), mk("plain-trace", false)}); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}
	// InsertSpans stamps spans.ingested_at with CURRENT_TIMESTAMP; age them.
	if _, err := repo.DB().Exec("UPDATE spans SET ingested_at = ?", oldTime); err != nil {
		t.Fatalf("age spans: %v", err)
	}

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	samples, err := repo.QueryErrorSamples(repository.SpanFilter{ProjectID: 1, Limit: 100})
	if err != nil {
		t.Fatalf("QueryErrorSamples: %v", err)
	}
	if len(samples) != 1 || samples[0].TraceID != "durable-trace" {
		t.Fatalf("want exactly the durable span sampled, got %+v", samples)
	}

	var remaining int
	if err := repo.DB().QueryRow("SELECT COUNT(*) FROM spans").Scan(&remaining); err != nil {
		t.Fatalf("count spans: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("want both raw spans deleted, %d remain", remaining)
	}
}
