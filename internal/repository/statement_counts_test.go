package repository

import (
	"context"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

// TestStatementCountsReplaceSpans pins that a call under WithStatementCounts
// creates no sqlite span and is counted under its span name instead.
func TestStatementCountsReplaceSpans(t *testing.T) {
	spanNames := recordSpans(t)
	repo := setupTestDB(t)
	before := len(sqliteSpanNames(spanNames()))

	ctx, calls := engine.WithStatementCounts(context.Background())
	for range 3 {
		var n int
		if err := repo.db.QueryRowContext(ctx, "SELECT\n\tCOUNT(*) FROM spans").Scan(&n); err != nil {
			t.Fatalf("count spans: %v", err)
		}
	}
	if _, err := repo.writer(engine.FamilySpans).ExecContext(ctx, "DELETE FROM spans WHERE id < 0"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if got := sqliteSpanNames(spanNames()); len(got) != before {
		t.Fatalf("counted calls emitted %d sqlite span(s): %v", len(got)-before, got[before:])
	}
	attrs := map[string]int64{}
	for _, kv := range calls.Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsInt64()
	}
	if attrs["db.calls.sqlite.select"] != 3 || attrs["db.calls.sqlite.delete"] != 1 {
		t.Fatalf("counts = %v, want 3 selects and 1 delete", attrs)
	}
	var sum int64
	for k, v := range attrs {
		if k != "db.calls" {
			sum += v
		}
	}
	if attrs["db.calls"] != sum {
		t.Fatalf("db.calls = %d, want the sum of the names %d (%v)", attrs["db.calls"], sum, attrs)
	}
}
